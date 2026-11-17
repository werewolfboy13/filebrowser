package users

import (
	"fmt"
	"path/filepath"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/spf13/afero"
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
