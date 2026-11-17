package fbhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"

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

func getUser(_ http.ResponseWriter, r *http.Request) (*modifyUserRequest, error) {
	if r.Body == nil {
		return nil, fberrors.ErrEmptyRequest
	}

	req := &modifyUserRequest{}
	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		return nil, err
	}

	if req.What != "user" {
		return nil, fberrors.ErrInvalidDataType
	}

	// Every caller goes on to read the user object, so a request without one
	// is refused here instead of being dereferenced there.
	if req.Data == nil {
		return nil, fberrors.ErrEmptyRequest
	}

	return req, nil
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

var userDeleteHandler = withSelfOrAdmin(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if r.Body == nil {
		return http.StatusBadRequest, fberrors.ErrEmptyRequest
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return http.StatusBadRequest, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if !users.CheckPwd(body.CurrentPassword, d.user.Password) {
			return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
		}
	}

	err := d.store.Users.Delete(d.raw.(uint))
	if err != nil {
		return errToStatus(err), err
	}

	return http.StatusOK, nil
})

var userPostHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	req, err := getUser(w, r)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if !users.CheckPwd(req.CurrentPassword, d.user.Password) {
			return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
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

var userPutHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	req, err := getUser(w, r)
	if err != nil {
		return http.StatusBadRequest, err
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

		if needsPassword && !users.CheckPwd(req.CurrentPassword, d.user.Password) {
			return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
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

	if all {
		if !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}

		if req.Data.Password != "" {
			req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
			if err != nil {
				return http.StatusBadRequest, err
			}
		} else {
			var suser *users.User
			suser, err = d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.raw.(uint))
			if err != nil {
				return http.StatusInternalServerError, err
			}
			req.Data.Password = suser.Password
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
		}

		if !d.user.Perm.Admin && slices.Contains(NonModifiableFieldsForNonAdmin, field) {
			return http.StatusForbidden, nil
		}
	}

	// With no fields, Update stores the whole user.
	err = d.store.Users.Update(req.Data, fields...)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	return http.StatusOK, nil
})
