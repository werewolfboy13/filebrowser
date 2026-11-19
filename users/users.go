package users

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/files"
	"github.com/werewolfboy13/filebrowser/v3/rules"
)

// ViewMode describes a view mode.
type ViewMode string

const (
	ListViewMode   ViewMode = "list"
	MosaicViewMode ViewMode = "mosaic"
)

// User describes a user.
type User struct {
	ID                    uint          `storm:"id,increment" json:"id"`
	Username              string        `storm:"unique" json:"username"`
	Password              string        `json:"password"`
	Scope                 string        `json:"scope"`
	Locale                string        `json:"locale"`
	LockPassword          bool          `json:"lockPassword"`
	ViewMode              ViewMode      `json:"viewMode"`
	SingleClick           bool          `json:"singleClick"`
	RedirectAfterCopyMove bool          `json:"redirectAfterCopyMove"`
	Perm                  Permissions   `json:"perm"`
	Commands              []string      `json:"commands"`
	Sorting               files.Sorting `json:"sorting"`
	Fs                    afero.Fs      `json:"-" yaml:"-"`
	Rules                 []rules.Rule  `json:"rules"`
	HideDotfiles          bool          `json:"hideDotfiles"`
	DateFormat            bool          `json:"dateFormat"`
	AceEditorTheme        string        `json:"aceEditorTheme"`

	// TokensNotBefore is the time, in Unix seconds, before which no session
	// token of the user is valid any more. Zero, as in every record written
	// before the field existed, means no token was ever revoked. It is set by
	// RevokeTokens and is not something a client can change.
	TokensNotBefore int64 `json:"tokensNotBefore,omitempty"`
}

// RevokeTokens ends every session of the user: no token issued up to now is
// valid afterwards. Tokens are dated to the second, so the mark is put one
// second ahead, and moved on when it is already there, which makes a second
// revocation within the same second end the tokens issued after the first.
func (u *User) RevokeTokens(now time.Time) {
	mark := now.Unix() + 1
	if mark <= u.TokensNotBefore {
		mark = u.TokensNotBefore + 1
	}
	u.TokensNotBefore = mark
}

// TokenRevoked reports whether a token issued at issuedAt was revoked.
func (u *User) TokenRevoked(issuedAt time.Time) bool {
	return u.TokensNotBefore != 0 && issuedAt.Unix() < u.TokensNotBefore
}

// IssueTime returns the time to date a token issued now with. It is now,
// unless tokens were revoked within the last second or so: the token is then
// dated at the revocation mark, so that it is not taken for a revoked one.
func (u *User) IssueTime(now time.Time) time.Time {
	if now.Unix() < u.TokensNotBefore {
		return time.Unix(u.TokensNotBefore, 0)
	}
	return now
}

// GetRules implements rules.Provider.
func (u *User) GetRules() []rules.Rule {
	return u.Rules
}

var checkableFields = []string{
	"Username",
	"Password",
	"Scope",
	"ViewMode",
	"Commands",
	"Sorting",
	"Rules",
}

// Clean cleans up a user and verifies if all its fields
// are alright to be saved.
func (u *User) Clean(baseScope string, followExternalSymlinks bool, fields ...string) error {
	if len(fields) == 0 {
		fields = checkableFields
	}

	for _, field := range fields {
		switch field {
		case "Username":
			if u.Username == "" {
				return fberrors.ErrEmptyUsername
			}
		case "Password":
			if u.Password == "" {
				return fberrors.ErrEmptyPassword
			}
		case "ViewMode":
			if u.ViewMode == "" {
				u.ViewMode = ListViewMode
			}
		case "Commands":
			if u.Commands == nil {
				u.Commands = []string{}
			}
		case "Sorting":
			if u.Sorting.By == "" {
				u.Sorting.By = "name"
			}
		case "Rules":
			if u.Rules == nil {
				u.Rules = []rules.Rule{}
			}
		}
	}

	if u.Fs == nil {
		scope := u.Scope
		scope = filepath.Join(baseScope, filepath.Join("/", scope))
		u.Fs = files.NewFs(afero.NewOsFs(), scope, followExternalSymlinks)
	}

	return nil
}

// Length limits, in bytes, for the text fields of a user. Locale, ViewMode,
// AceEditorTheme and Sorting.By can be set by users on their own account, and
// together with Username they are copied into every token and user listing,
// so they are kept short.
const (
	MaxUsernameLength   = 256
	MaxPreferenceLength = 64
	MaxScopeLength      = 4096
)

// validatableFields are the fields ValidateLengths checks by default.
var validatableFields = []string{
	"Username",
	"Scope",
	"Locale",
	"ViewMode",
	"AceEditorTheme",
	"Sorting",
}

// ValidateLengths reports whether the given fields (all of them, when none is
// given) are within their length limits. It is meant for input that is about
// to be stored, and is deliberately separate from Clean: Clean also runs when
// a user is read, where a record written before these limits existed must
// keep loading.
func (u *User) ValidateLengths(fields ...string) error {
	if len(fields) == 0 {
		fields = validatableFields
	}

	for _, field := range fields {
		var value string
		limit := MaxPreferenceLength

		switch field {
		case "Username":
			value, limit = u.Username, MaxUsernameLength
		case "Scope":
			value, limit = u.Scope, MaxScopeLength
		case "Locale":
			value = u.Locale
		case "ViewMode":
			value = string(u.ViewMode)
		case "AceEditorTheme":
			value = u.AceEditorTheme
		case "Sorting":
			value = u.Sorting.By
		default:
			continue
		}

		if len(value) > limit {
			return fmt.Errorf("%s: %w (at most %d bytes)", field, fberrors.ErrFieldTooLong, limit)
		}
	}

	return nil
}

// FullPath gets the full path for a user's relative path.
func (u *User) FullPath(path string) string {
	return afero.FullBaseFsPath(files.BasePath(u.Fs), path)
}
