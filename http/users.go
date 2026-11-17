package fbhttp

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/filebrowser/filebrowser/v2/auth"
	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/users"
)

var (
	NonModifiableFieldsForNonAdmin = []string{"Username", "Scope", "LockPassword", "Perm", "Commands", "Rules"}
)

type modifyUserRequest struct {
	modifyRequest
	Data *users.User `json:"data"`
}

func getUserID(r *http.Request) (uint, error) {
	vars := mux.Vars(r)
	i, err := strconv.ParseUint(vars["id"], 10, 0)
	if err != nil {
		return 0, err
	}
	return uint(i), err
}

// getUser decodes a user create or update request. On failure it returns the
// status to answer with.
func getUser(w http.ResponseWriter, r *http.Request) (*modifyUserRequest, int, error) {
	req := &modifyUserRequest{}
	if status, err := decodeJSONBody(w, r, req); err != nil {
		return nil, status, err
	}

	if req.What != "user" {
		return nil, http.StatusBadRequest, fberrors.ErrInvalidDataType
	}

	// Every caller goes on to read the user object, so a request without one
	// is refused here instead of being dereferenced there.
	if req.Data == nil {
		return nil, http.StatusBadRequest, fberrors.ErrEmptyRequest
	}

	return req, 0, nil
}

// confirmPassword checks that password is the password of the user making
// the request, which the users API asks for before it changes an account. It
// returns status 0 when it is, and the answer to give otherwise.
//
// Whoever holds a session token can ask this question, so it is a way to
// guess the password of the account without going through the login, and the
// login's limiter. A stolen token that finds the password can change it and
// take the account over. Wrong answers are therefore counted, by the same
// limiter the login uses: a few are free, then every further one has to wait,
// twice as long each time, and while it waits nothing is checked.
//
// They are counted per account, and not per address as well, as the login
// does: a token can be used from any number of addresses.
//
// That alone would let the holder of a stolen token keep the count of the
// account blocked for as long as the token lives, and with it the one thing
// that ends the stolen session: the user changing their password. So a
// session that comes straight from a login with the password
// (authToken.PasswordLogin) is counted on its own, by its token. Such a token
// cannot be made without the password, and a renewal does not carry the mark
// on, so an attacker cannot get more of these counts than they have stolen
// login tokens. The user can always log in again and confirm the password
// from that session, whatever the stolen one is doing.
//
// The price: someone who stole a token that came straight from a login gets
// that token's own allowance of guesses, on top of the account's, until the
// token expires (two hours by default). And as with the login, the limiter
// lives in memory: a restart forgets the counts.
//
// A request without a password is refused without being counted and without
// computing a hash: it is not a guess.
func confirmPassword(w http.ResponseWriter, d *data, password string) (int, error) {
	if password == "" {
		return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
	}

	done := func(attemptOutcome) {}
	if d.limits != nil {
		account := strconv.FormatUint(uint64(d.user.ID), 10)
		key := limiterKey("password", account)
		if d.loginToken {
			key = limiterKey("password", account, "login", d.token)
		}

		attempt, retryAfter := d.limits.password.begin(account, key)
		if attempt == nil {
			return tooManyAttempts(w, retryAfter)
		}
		done = attempt
	}

	if !users.CheckPwd(password, d.user.Password) {
		done(attemptFailed)
		return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
	}
	done(attemptSucceeded)

	return 0, nil
}

func withSelfOrAdmin(fn handleFunc) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		id, err := getUserID(r)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		if d.user.ID != id && !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}

		d.raw = id
		return fn(w, r, d)
	})
}

var usersGetHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	users, err := d.store.Users.Gets(d.server.Root, d.server.FollowExternalSymlinks)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	for _, u := range users {
		u.Password = ""
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})

	return renderJSON(w, r, users)
})

var userGetHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	u, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.raw.(uint))
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusNotFound, err
	}

	if err != nil {
		return http.StatusInternalServerError, err
	}

	u.Password = ""
	if !d.user.Perm.Admin {
		u.Scope = ""
	}
	return renderJSON(w, r, u)
})

var userDeleteHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	var body struct {
		CurrentPassword string `json:"current_password"`
	}

	if status, err := decodeJSONBody(w, r, &body); err != nil {
		return status, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if status, err := confirmPassword(w, d, body.CurrentPassword); status != 0 {
			return status, err
		}
	}

	err := d.store.Users.Delete(d.raw.(uint))
	if err != nil {
		return errToStatus(err), err
	}

	return http.StatusOK, nil
})

var userPostHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	req, status, err := getUser(w, r)
	if err != nil {
		return status, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if status, err := confirmPassword(w, d, req.CurrentPassword); status != 0 {
			return status, err
		}
	}

	if len(req.Which) != 0 {
		return http.StatusBadRequest, nil
	}

	if req.Data.Password == "" {
		return http.StatusBadRequest, fberrors.ErrEmptyPassword
	}

	req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if req.Data.Perm.Share && !req.Data.Perm.Download {
		return http.StatusBadRequest, fberrors.ErrShareRequiresDownload
	}

	if err := rules.Validate(req.Data.Rules); err != nil {
		return http.StatusBadRequest, err
	}

	if err := req.Data.ValidateLengths(); err != nil {
		return http.StatusBadRequest, err
	}

	// A new account has no sessions to revoke, whatever the request says.
	req.Data.TokensNotBefore = 0

	userHome, err := d.settings.MakeUserDir(req.Data.Username, req.Data.Scope, d.server.Root)
	if err != nil {
		log.Printf("create user: failed to mkdir user home dir: [%s]", userHome)
		return http.StatusInternalServerError, err
	}
	req.Data.Scope = userHome
	log.Printf("user: %s, home dir: [%s].", req.Data.Username, userHome)

	err = d.store.Users.Save(req.Data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Location", "/settings/users/"+strconv.FormatUint(uint64(req.Data.ID), 10))
	return http.StatusCreated, nil
})

// userFields lists the fields a user update may name in "which", by their Go
// field name. The value tells whether the field is a display preference: those
// can be changed without confirming the current password. Every other field
// decides who the user is or what they may reach, so changing it requires the
// password when passwords are in use.
var userFields = map[string]bool{
	"Locale":                true,
	"ViewMode":              true,
	"SingleClick":           true,
	"RedirectAfterCopyMove": true,
	"HideDotfiles":          true,
	"DateFormat":            true,
	"AceEditorTheme":        true,
	"Sorting":               true,

	"Username":     false,
	"Password":     false,
	"Scope":        false,
	"LockPassword": false,
	"Perm":         false,
	"Commands":     false,
	"Rules":        false,
}

// userUpdateFields turns the "which" list of a user update into the Go field
// names it stands for. An empty list and the single entry "all" both mean the
// whole user, reported as all. Names are canonicalised exactly once, here, and
// every later decision is made on the result: deciding on the raw spelling and
// then updating by the canonical one is what let "ſcope" (which title-cases to
// "Scope") past earlier checks.
func userUpdateFields(which []string) (fields []string, all bool, err error) {
	if len(which) == 0 {
		return nil, true, nil
	}

	title := cases.Title(language.English, cases.NoLower)
	for _, name := range which {
		field := title.String(name)
		if field == "All" && len(which) == 1 {
			return nil, true, nil
		}
		if _, ok := userFields[field]; !ok {
			return nil, false, fmt.Errorf("%w: unknown user field %q", fberrors.ErrInvalidRequestParams, name)
		}
		fields = append(fields, field)
	}

	return fields, false, nil
}

// userPutHandler updates a user. tokenExpireTime is the lifetime of the token
// handed to a session whose change ended its own token.
func userPutHandler(tokenExpireTime time.Duration) handleFunc {
	return withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		return userPut(w, r, d, tokenExpireTime)
	})
}

func userPut(w http.ResponseWriter, r *http.Request, d *data, tokenExpireTime time.Duration) (int, error) {
	req, status, err := getUser(w, r)
	if err != nil {
		return status, err
	}

	fields, all, err := userUpdateFields(req.Which)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		// Allow-list: only an update that is limited to display preferences
		// goes through without the current password.
		needsPassword := all
		for _, field := range fields {
			if !userFields[field] {
				needsPassword = true
			}
		}

		if needsPassword {
			if status, err := confirmPassword(w, d, req.CurrentPassword); status != 0 {
				return status, err
			}
		}
	}

	if req.Data.ID != d.raw.(uint) {
		return http.StatusBadRequest, nil
	}

	if all || slices.Contains(fields, "Perm") {
		if req.Data.Perm.Share && !req.Data.Perm.Download {
			return http.StatusBadRequest, fberrors.ErrShareRequiresDownload
		}
	}

	// Rules are only checked when this request stores them: a partial update
	// of other fields must not fail over rules it does not touch.
	if all || slices.Contains(fields, "Rules") {
		if err := rules.Validate(req.Data.Rules); err != nil {
			return http.StatusBadRequest, err
		}
	}

	// With no fields named, every field is stored and so every one is checked.
	if err := req.Data.ValidateLengths(fields...); err != nil {
		return http.StatusBadRequest, err
	}

	if all && !d.user.Perm.Admin {
		return http.StatusForbidden, nil
	}

	stored, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.raw.(uint))
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusNotFound, err
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}

	// A change of the password, the permissions or the scope ends the
	// sessions of the account: whoever holds a token from before must not
	// keep it past the change.
	newPassword := false

	if all {
		if req.Data.Password != "" {
			req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
			if err != nil {
				return http.StatusBadRequest, err
			}
			newPassword = true
		} else {
			req.Data.Password = stored.Password
		}
	}

	for _, field := range fields {
		if field == "Password" {
			if !d.user.Perm.Admin && d.user.LockPassword {
				return http.StatusForbidden, nil
			}

			req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
			if err != nil {
				return http.StatusBadRequest, err
			}
			newPassword = true
		}

		if !d.user.Perm.Admin && slices.Contains(NonModifiableFieldsForNonAdmin, field) {
			return http.StatusForbidden, nil
		}
	}

	revoke := newPassword ||
		((all || slices.Contains(fields, "Perm")) && req.Data.Perm != stored.Perm) ||
		((all || slices.Contains(fields, "Scope")) && req.Data.Scope != stored.Scope)

	// The mark is the server's to set, whatever the request carries.
	req.Data.TokensNotBefore = stored.TokensNotBefore
	if revoke {
		req.Data.RevokeTokens(time.Now())
		if !all {
			fields = append(fields, "TokensNotBefore")
		}
	}

	// With no fields, Update stores the whole user.
	err = d.store.Users.Update(req.Data, fields...)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	// The session that made the change was revoked with the others. It is
	// handed a new token with this answer, issued for the account as it is
	// stored now.
	if revoke && req.Data.ID == d.user.ID {
		updated, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.user.ID)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		if err := replaceSessionToken(w, d, updated, tokenExpireTime); err != nil {
			return http.StatusInternalServerError, err
		}
	}

	return http.StatusOK, nil
}
