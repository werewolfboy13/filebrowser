package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/files"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// MethodHookAuth is used to identify hook auth.
const MethodHookAuth settings.AuthMethod = "hook"

type hookCred struct {
	Password string `json:"password"`
	Username string `json:"username"`
}

// ErrHookRefused is returned by hook authentication when the hook was run
// with the credentials and did not answer with an action it knows: it ended
// with an error status, or printed no action, or another one. Whatever the
// hook meant by it, the password was put to the test and was not accepted,
// so it counts as a failed attempt at guessing it. It is not reported as a
// wrong password (os.ErrPermission), because the hook did not say that
// either: a hook that fails for a reason of its own answers the same way.
//
// A hook that could not be run at all is not reported with it: nothing was
// tested. Neither is one that was stopped because its time was over (see
// RunCommand): a hook that is slow, or that hangs, said nothing about the
// password.
//
// A hook that was stopped because the client of its login went away is
// reported with it. That hook said nothing either, but the client did: it is
// the client that chooses which of its logins it gives up on, and one that
// gives up on every login that is not accepted within the time the hook
// takes to accept the right password is told what a refusal would have told
// it. Were these logins not counted, it could guess without ever being
// slowed down, against any hook that takes longer to refuse than to accept.
// The price is that a user who leaves the login page while the hook is still
// running has used one of the failed attempts that cost nothing.
var ErrHookRefused = errors.New("the authentication hook did not accept the login")

// hookTimeout is how long the hook command gets to answer, unless
// HookAuth.Timeout says otherwise.
//
// A login waits for its hook, and holds one of the few turns its client
// address has for as long (see the login handler). A hook that never answers
// (its directory server took the connection and went silent) used to hold
// that turn for as long as it hung, and a handful of them kept the address
// out of the login. Real hooks answer in a second or two.
const hookTimeout = 30 * time.Second

// hookWaitDelay is how long the output of the hook is waited for once the
// hook itself has ended, by itself or because it was stopped. Only the
// command that was started is stopped, not what it started in turn, and a
// process that is left behind with the output of the hook still open would
// otherwise be waited for as if it were the hook. It is a variable so that
// tests can shorten it.
var hookWaitDelay = 5 * time.Second

// HookAuth is a hook implementation of an Auther.
type HookAuth struct {
	Users    users.Store        `json:"-"`
	Settings *settings.Settings `json:"-"`
	Server   *settings.Server   `json:"-"`
	Cred     hookCred           `json:"-"`
	Fields   hookFields         `json:"-"`
	Command  string             `json:"command"`

	// Timeout is how long the command gets to answer. Zero stands for
	// hookTimeout. It is not part of the stored configuration.
	Timeout time.Duration `json:"-" yaml:"-"`
}

// Auth authenticates the user via a json in content body.
func (a *HookAuth) Auth(r *http.Request, usr users.Store, stg *settings.Settings, srv *settings.Server) (*users.User, error) {
	var cred hookCred

	if r.Body == nil {
		return nil, ErrNoAttempt
	}

	err := json.NewDecoder(r.Body).Decode(&cred)
	if err != nil {
		return nil, ErrNoAttempt
	}

	a.Users = usr
	a.Settings = stg
	a.Server = srv
	a.Cred = cred

	action, err := a.RunCommand(r.Context())
	if err != nil {
		// An exit error is the hook's own doing: it ran, with the
		// credentials, and ended with an error status.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%w: %w", ErrHookRefused, err)
		}
		return nil, err
	}

	switch action {
	case "auth":
		u, err := a.SaveUser()
		if err != nil {
			return nil, err
		}
		return u, nil
	case "block":
		return nil, os.ErrPermission
	case "pass":
		u, err := a.Users.Get(a.Server.Root, a.Server.FollowExternalSymlinks, a.Cred.Username)
		if err != nil || !users.CheckPwd(a.Cred.Password, u.Password) {
			return nil, os.ErrPermission
		}
		return u, nil
	default:
		return nil, fmt.Errorf("%w: invalid hook action: %s", ErrHookRefused, action)
	}
}

// LoginPage tells that hook auth requires a login page.
func (a *HookAuth) LoginPage() bool {
	return true
}

// RunCommand starts the hook command and returns the action.
//
// The command is stopped when ctx ends (the request it runs for is over: its
// client went away) or when it has not answered within the time it is given
// (see hookTimeout), whichever comes first. The error it then returns is not
// an *exec.ExitError, although that is what running a command that was
// stopped gives: the hook did not end with an error status of its own.
//
// Which of the two stopped it decides whether its login is a failed attempt.
// One that ran out of time is not: the error is not ErrHookRefused. One
// whose client went away is, and the error is ErrHookRefused: see there for
// why. A command that was never started, because ctx had ended already, is
// neither: the error is that of ctx.
func (a *HookAuth) RunCommand(ctx context.Context) (string, error) {
	command := strings.Split(a.Command, " ")

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = hookTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	// Whether the end of ctx stopped the hook. A hook that ended by itself,
	// and only then had ctx end (its client went away while a process it
	// left behind still held its output), answered: its refusal counts.
	var stopped atomic.Bool
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if err == nil {
			stopped.Store(true)
		}
		return err
	}
	cmd.WaitDelay = hookWaitDelay
	cmd.Env = append(os.Environ(), fmt.Sprintf("USERNAME=%s", a.Cred.Username))
	cmd.Env = append(cmd.Env, fmt.Sprintf("PASSWORD=%s", a.Cred.Password))
	out, err := cmd.Output()
	switch {
	case err != nil && stopped.Load() && errors.Is(ctx.Err(), context.Canceled):
		// The request ended first, and not the time of the hook, which ends
		// ctx with context.DeadlineExceeded: the client gave up on the hook.
		return "", fmt.Errorf("%w: its client went away before it answered: %w", ErrHookRefused, ctx.Err())
	case err != nil && stopped.Load():
		return "", fmt.Errorf("the authentication hook was stopped before it answered: %w", ctx.Err())
	case errors.Is(err, exec.ErrWaitDelay):
		// The hook answered and ended, and something it started still has
		// its output open: what the hook itself printed is its answer.
	case err != nil:
		return "", err
	}

	a.GetValues(string(out))

	return a.Fields.Values["hook.action"], nil
}

// GetValues creates a map with values from the key-value format string
func (a *HookAuth) GetValues(s string) {
	m := map[string]string{}

	// make line breaks consistent on Windows platform
	s = strings.ReplaceAll(s, "\r\n", "\n")

	// iterate input lines
	for val := range strings.Lines(s) {
		v := strings.SplitN(val, "=", 2)

		// skips non key and value format
		if len(v) != 2 {
			continue
		}

		fieldKey := strings.TrimSpace(v[0])
		fieldValue := strings.TrimSpace(v[1])

		if a.Fields.IsValid(fieldKey) {
			m[fieldKey] = fieldValue
		}
	}

	a.Fields.Values = m
}

// hookSaveAttempts is how often a login tries to store what the hook said
// about a user whose record keeps being changed under it. See SaveUser.
const hookSaveAttempts = 3

// SaveUser updates the existing user or creates a new one when not found.
//
// The record is read, the password is compared with it (which takes a
// while), and what the hook changed is stored. When the account was changed
// in between in a way that ends its sessions (an administrator took a
// permission away, or reset the password), nothing is stored: the login
// would be storing what it made of a record that is no longer the account.
// It starts over instead, from the record as it is now, without running the
// hook again. That it has to more than once in a row takes someone changing
// the account without pause; the login then gives up with
// fberrors.ErrUserChanged.
func (a *HookAuth) SaveUser() (*users.User, error) {
	for attempt := 1; ; attempt++ {
		u, err := a.saveUser()
		if !errors.Is(err, fberrors.ErrUserChanged) || attempt >= hookSaveAttempts {
			return u, err
		}
	}
}

// hookUserFields are the fields of a user that GetUser can change, by their
// Go names.
var hookUserFields = []string{
	"Scope",
	"Locale",
	"ViewMode",
	"SingleClick",
	"RedirectAfterCopyMove",
	"Sorting",
	"Commands",
	"DateFormat",
	"HideDotfiles",
	"Perm",
	"LockPassword",
}

// changedHookFields names the fields in which the user the hook made differs
// from the record it was made from.
func changedHookFields(read, made *users.User) []string {
	var fields []string
	rv, mv := reflect.ValueOf(read).Elem(), reflect.ValueOf(made).Elem()
	for _, field := range hookUserFields {
		if !reflect.DeepEqual(rv.FieldByName(field).Interface(), mv.FieldByName(field).Interface()) {
			fields = append(fields, field)
		}
	}
	return fields
}

func (a *HookAuth) saveUser() (*users.User, error) {
	u, err := a.Users.Get(a.Server.Root, a.Server.FollowExternalSymlinks, a.Cred.Username)
	if err != nil && !errors.Is(err, fberrors.ErrNotExist) {
		return nil, err
	}

	if u == nil {
		pass, err := users.ValidateAndHashPwd(a.Cred.Password, a.Settings.MinimumPasswordLength)
		if err != nil {
			return nil, err
		}

		// create user with the provided credentials
		d := &users.User{
			Username:              a.Cred.Username,
			Password:              pass,
			Scope:                 a.Settings.Defaults.Scope,
			Locale:                a.Settings.Defaults.Locale,
			ViewMode:              a.Settings.Defaults.ViewMode,
			SingleClick:           a.Settings.Defaults.SingleClick,
			RedirectAfterCopyMove: a.Settings.Defaults.RedirectAfterCopyMove,
			Sorting:               a.Settings.Defaults.Sorting,
			Perm:                  a.Settings.Defaults.Perm,
			Commands:              a.Settings.Defaults.Commands,
			DateFormat:            a.Settings.Defaults.DateFormat,
			HideDotfiles:          a.Settings.Defaults.HideDotfiles,
		}
		u = a.GetUser(d)

		// A scope explicitly returned by the hook takes precedence over the
		// automatic per-user home directory derivation.
		_, explicitScope := a.Fields.Values["user.scope"]
		derivedScope, err := a.Settings.CreateUserHome(u, a.Server.Root, explicitScope)
		if err != nil {
			return nil, err
		}
		log.Printf("user: %s, home dir: [%s].", u.Username, u.Scope)

		if err := a.Users.SaveProvisioned(u, derivedScope); err != nil {
			return nil, err
		}
	} else if p := !users.CheckPwd(a.Cred.Password, u.Password); len(a.Fields.Values) > 1 || p {
		read := u
		u = a.GetUser(read)

		// Only what the hook changed is stored, and not the record as it
		// was read with those changes in it: what an administrator stored
		// in the meantime in a field the hook says nothing about (a rule
		// that denies a directory, the commands the user may run) stays as
		// the administrator left it. The whole record used to be written
		// back, and a login that was under way undid such a change.
		fields := changedHookFields(read, u)

		// update the password when it doesn't match the current
		revoke := false
		if p {
			pass, err := users.ValidateAndHashPwd(a.Cred.Password, a.Settings.MinimumPasswordLength)
			if err != nil {
				return nil, err
			}
			u.Password = pass
			fields = append(fields, "Password")

			// The password changed where the hook asks: the sessions that
			// were opened with the old one end here, as they do when a
			// password is changed through the API.
			//
			// Unless the hook says that this is not what happened. A hook
			// that accepts more than one password for a user (an app
			// password next to the account's, a password with a one-time
			// code) sees a password that is not the stored one at almost
			// every login, with nothing having changed, and each login
			// would end the sessions of the user's other devices. Such a
			// hook answers hook.keepSessions=true, for the logins it knows
			// to be of that kind.
			revoke = !a.Fields.GetBoolean(hookKeepSessions, false)
		}

		if len(fields) == 0 {
			// The hook said nothing new.
			return read, nil
		}

		// Stored only if the account is still the one that was read: see
		// SaveUser. The user that is returned is the record as it is
		// stored with this login's changes in it, and the token of the
		// login is issued from it.
		return a.Users.UpdateIfMark(u, read.TokensNotBefore, revoke, fields...)
	}

	return u, nil
}

// GetUser returns a User filled with hook values or provided defaults
func (a *HookAuth) GetUser(d *users.User) *users.User {
	// adds all permissions when user is admin
	isAdmin := a.Fields.GetBoolean("user.perm.admin", d.Perm.Admin)
	perms := users.Permissions{
		Admin:    isAdmin,
		Execute:  isAdmin || a.Fields.GetBoolean("user.perm.execute", d.Perm.Execute),
		Create:   isAdmin || a.Fields.GetBoolean("user.perm.create", d.Perm.Create),
		Rename:   isAdmin || a.Fields.GetBoolean("user.perm.rename", d.Perm.Rename),
		Modify:   isAdmin || a.Fields.GetBoolean("user.perm.modify", d.Perm.Modify),
		Delete:   isAdmin || a.Fields.GetBoolean("user.perm.delete", d.Perm.Delete),
		Share:    isAdmin || a.Fields.GetBoolean("user.perm.share", d.Perm.Share),
		Download: isAdmin || a.Fields.GetBoolean("user.perm.download", d.Perm.Download),
	}

	// The user starts out as a copy of d, so that everything a hook has no
	// field for stays as it is: the rules an administrator set for the user,
	// the revocation mark, and whatever field is added to a user later. The
	// user used to be built from the fields listed here, and a login then
	// erased the others from the stored record.
	user := *d

	// Built for the scope d has, which the hook may be about to change. It
	// is set up again, for the scope the user ends up with, where the user
	// is stored or read.
	user.Fs = nil

	user.Scope = a.Fields.GetString("user.scope", d.Scope)
	user.Locale = a.Fields.GetString("user.locale", d.Locale)
	user.ViewMode = users.ViewMode(a.Fields.GetString("user.viewMode", string(d.ViewMode)))
	user.SingleClick = a.Fields.GetBoolean("user.singleClick", d.SingleClick)
	user.RedirectAfterCopyMove = a.Fields.GetBoolean("user.redirectAfterCopyMove", d.RedirectAfterCopyMove)
	user.Sorting = files.Sorting{
		Asc: a.Fields.GetBoolean("user.sorting.asc", d.Sorting.Asc),
		By:  a.Fields.GetString("user.sorting.by", d.Sorting.By),
	}
	user.Commands = a.Fields.GetArray("user.commands", d.Commands)
	user.DateFormat = a.Fields.GetBoolean("user.dateFormat", d.DateFormat)
	user.HideDotfiles = a.Fields.GetBoolean("user.hideDotfiles", d.HideDotfiles)
	user.Perm = perms
	user.LockPassword = true

	return &user
}

// hookKeepSessions is the field a hook sets to "true" to say that the
// password of this login, when it is not the one that is stored, is one of
// several it accepts for the user and not a password that was changed: the
// login must not end the other sessions of the user. See saveUser.
const hookKeepSessions = "hook.keepSessions"

// hookFields is used to access fields from the hook
type hookFields struct {
	Values map[string]string
}

// validHookFields contains names of the fields that can be used
var validHookFields = []string{
	"hook.action",
	hookKeepSessions,
	"user.scope",
	"user.locale",
	"user.viewMode",
	"user.singleClick",
	"user.redirectAfterCopyMove",
	"user.sorting.by",
	"user.sorting.asc",
	"user.commands",
	"user.hideDotfiles",
	"user.perm.admin",
	"user.perm.execute",
	"user.perm.create",
	"user.perm.rename",
	"user.perm.modify",
	"user.perm.delete",
	"user.perm.share",
	"user.perm.download",
}

// IsValid checks if the provided field is on the valid fields list
func (hf *hookFields) IsValid(field string) bool {
	return slices.Contains(validHookFields, field)
}

// GetString returns the string value or provided default
func (hf *hookFields) GetString(k, dv string) string {
	val, ok := hf.Values[k]
	if ok {
		return val
	}
	return dv
}

// GetBoolean returns the bool value or provided default
func (hf *hookFields) GetBoolean(k string, dv bool) bool {
	val, ok := hf.Values[k]
	if ok {
		return val == "true"
	}
	return dv
}

// GetArray returns the array value or provided default
func (hf *hookFields) GetArray(k string, dv []string) []string {
	val, ok := hf.Values[k]
	if ok && strings.TrimSpace(val) != "" {
		return strings.Split(val, " ")
	}
	return dv
}
