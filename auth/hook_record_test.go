package auth

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	fberrors "github.com/werewolfboy13/filebrowser/v2/errors"
	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// updatingStore is mockUserStore with updates that store the user as the real
// store does: the fields that are named, or the whole record, and only when
// the stored record still has the mark the caller read.
type updatingStore struct {
	*mockUserStore
	updates int

	// fields is what the last update named.
	fields []string

	// before, when set, is called before each compare-and-swap, with the
	// number of the attempt.
	before   func(attempt int)
	attempts int
}

func (s *updatingStore) Update(user *users.User, _ ...string) error {
	s.updates++
	s.users[user.Username] = user
	return nil
}

func (s *updatingStore) UpdateIfMark(user *users.User, mark int64, revoke bool, fields ...string) (*users.User, error) {
	s.attempts++
	if s.before != nil {
		s.before(s.attempts)
	}

	stored, ok := s.users[user.Username]
	if !ok {
		return nil, fberrors.ErrNotExist
	}
	if stored.TokensNotBefore != mark {
		return nil, fberrors.ErrUserChanged
	}

	next := *stored
	if len(fields) == 0 {
		next = *user
	}
	for _, field := range fields {
		reflect.ValueOf(&next).Elem().FieldByName(field).Set(reflect.ValueOf(user).Elem().FieldByName(field))
	}
	next.TokensNotBefore = stored.TokensNotBefore
	if revoke {
		next.RevokeTokens(time.Now())
	}
	user.TokensNotBefore = next.TokensNotBefore

	s.updates++
	s.fields = fields
	s.users[user.Username] = &next
	return &next, nil
}

// fillValue gives every field that can be set a value that is not its zero
// value, so that a field that gets lost on the way shows.
func fillValue(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("stored-" + name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillValue(s.Index(0), name)
		v.Set(s)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillValue(p.Elem(), name)
		v.Set(p)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				fillValue(f, name+"."+v.Type().Field(i).Name)
			}
		}
	}
}

// storedHookUser returns a user as it could be stored for a hook login with
// password, with every field set.
func storedHookUser(t *testing.T, password string) *users.User {
	t.Helper()
	u := &users.User{}
	fillValue(reflect.ValueOf(u).Elem(), "User")
	u.Username = "bob"
	u.Fs = nil

	hash, err := users.HashPwd(password)
	if err != nil {
		t.Fatal(err)
	}
	u.Password = hash

	// Guard the test itself: a field it could not fill would go unnoticed.
	v := reflect.ValueOf(u).Elem()
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name != "Fs" && v.Field(i).IsZero() {
			t.Fatalf("the test does not fill User.%s", name)
		}
	}
	return u
}

// A login through the hook rewrote the whole user record from the fields a
// hook can supply, and what a hook has no field for was lost: the rules an
// administrator had set for the user were gone after the user's next login,
// and with them the denial of whatever they denied.
//
// Every field the hook does not supply is kept as it is stored. The test
// fills every field of the record, so one that is added later is covered.
func TestHookLoginKeepsWhatTheHookDoesNotSupply(t *testing.T) {
	const password = "a-strong-password"
	stored := storedHookUser(t, password)
	want := *stored
	want.Locale = "from-the-hook"

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{
		"user.locale": "from-the-hook",
	})
	a.Users = store

	got, err := a.SaveUser()
	if err != nil {
		t.Fatal(err)
	}
	if store.updates != 1 {
		t.Fatalf("the record was stored %d times, want 1", store.updates)
	}
	if got != store.users["bob"] {
		t.Fatal("the user that was returned is not the one that was stored")
	}
	// Only what the hook changed is written: a field it says nothing about
	// is not stored back as it was read, over what was stored since.
	if !reflect.DeepEqual(store.fields, []string{"Locale"}) {
		t.Errorf("the login stored the fields %v, want only the locale", store.fields)
	}

	if len(got.Rules) != 1 {
		t.Errorf("the rules of the user are %+v after a hook login, want the one that was stored", got.Rules)
	}

	got.Fs = nil
	gv, wv := reflect.ValueOf(got).Elem(), reflect.ValueOf(&want).Elem()
	for i := 0; i < gv.NumField(); i++ {
		if name := gv.Type().Field(i).Name; !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
			t.Errorf("User.%s = %+v after a hook login that only supplied the locale, want %+v", name, gv.Field(i).Interface(), wv.Field(i).Interface())
		}
	}
}

// The filesystem of the record that was read is for the scope it had: it
// must not come along when the hook moves the user elsewhere.
func TestHookLoginDoesNotKeepTheFilesystemOfTheOldScope(t *testing.T) {
	const password = "a-strong-password"
	stored := storedHookUser(t, password)
	if err := stored.Clean(t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	if stored.Fs == nil {
		t.Fatal("the stored user has no filesystem")
	}

	a := newHookAuth(&mockUserStore{users: map[string]*users.User{"bob": stored}}, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{
		"user.scope": "/elsewhere",
	})
	if got := a.GetUser(stored); got.Scope != "/elsewhere" || got.Fs != nil {
		t.Errorf("scope %q, filesystem %v; want /elsewhere and none", got.Scope, got.Fs)
	}
}

// A password that the hook accepts and that is not the stored one is a
// password that changed at the source. It ends the sessions that were opened
// with the old one, like every other change of password; a login with the
// password that is stored ends nothing.
func TestHookPasswordChangeRevokesTokens(t *testing.T) {
	stored := storedHookUser(t, "the-old-password")
	stored.TokensNotBefore = 0
	before := *stored

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	login := func(password string) *users.User {
		t.Helper()
		a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{})
		a.Users = store
		a.Cred.Password = password
		u, err := a.SaveUser()
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	if u := login("the-old-password"); u.TokensNotBefore != 0 || u.Password != before.Password {
		t.Errorf("a login with the stored password changed the record: mark %d", u.TokensNotBefore)
	}

	u := login("the-new-password")
	if !users.CheckPwd("the-new-password", u.Password) {
		t.Fatal("the new password was not stored")
	}
	if u.TokensNotBefore == 0 {
		t.Error("a password change through the hook did not revoke the tokens of the user")
	}
	mark := u.TokensNotBefore

	if u := login("the-new-password"); u.TokensNotBefore != mark {
		t.Errorf("a login with the password that is now stored moved the mark from %d to %d", mark, u.TokensNotBefore)
	}
}

// How a hook that does not answer with an action is told apart from a hook
// that could not be run: only the former was given the credentials.
func TestHookThatDoesNotVouchIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}

	body := func() io.Reader { return strings.NewReader(`{"username":"bob","password":"guess"}`) }
	auth := func(command string) error {
		r, err := http.NewRequest(http.MethodPost, "/api/login", body())
		if err != nil {
			t.Fatal(err)
		}
		a := &HookAuth{Command: command}
		_, err = a.Auth(r, &mockUserStore{users: map[string]*users.User{}}, &settings.Settings{}, &settings.Server{Root: t.TempDir()})
		return err
	}

	for name, script := range map[string]string{
		"error status":   "exit 1\n",
		"no action":      "exit 0\n",
		"unknown action": "echo hook.action=maybe\n",
	} {
		err := auth(writeHookScript(t, script))
		if !errors.Is(err, ErrHookRefused) {
			t.Errorf("%s: error %v, want ErrHookRefused", name, err)
		}
		if errors.Is(err, os.ErrPermission) {
			t.Errorf("%s: the error says that the password is wrong, which the hook did not say", name)
		}
	}

	if err := auth(writeHookScript(t, "echo hook.action=block\n")); !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrHookRefused) {
		t.Errorf("a hook that blocks: error %v, want os.ErrPermission", err)
	}
	if err := auth(filepath.Join(t.TempDir(), "no-such-hook")); err == nil || errors.Is(err, ErrHookRefused) || errors.Is(err, os.ErrPermission) {
		t.Errorf("a hook that cannot be run: error %v, want an error that is neither a refusal nor a wrong password", err)
	}
}

// A login that only brings what is stored already stores nothing.
func TestHookLoginThatChangesNothingStoresNothing(t *testing.T) {
	const password = "a-strong-password"
	stored := storedHookUser(t, password)

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{
		"user.locale":       stored.Locale,
		"user.perm.delete":  "true",
		"user.hideDotfiles": "true",
	})
	a.Users = store

	got, err := a.SaveUser()
	if err != nil {
		t.Fatal(err)
	}
	if store.updates != 0 || got != stored {
		t.Errorf("the record was stored %d times (fields %v)", store.updates, store.fields)
	}
}

// The account is changed, in a way that ends its sessions, between the
// login's reading of it and its storing what the hook said: an administrator
// takes a permission away. The login must not store what it made of the
// record it read, which has the permission. It starts over from the record
// as it is now, and its own changes go on top of the administrator's.
func TestHookLoginStartsOverWhenTheAccountWasRevokedMeanwhile(t *testing.T) {
	const password = "a-strong-password"
	stored := storedHookUser(t, password)
	stored.Perm.Admin = false
	stored.TokensNotBefore = 100

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	store.before = func(attempt int) {
		if attempt != 1 {
			return
		}
		changed := *store.users["bob"]
		changed.Perm.Delete = false
		changed.Rules = nil
		changed.TokensNotBefore = 200
		store.users["bob"] = &changed
	}
	a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{
		"user.locale": "from-the-hook",
	})
	a.Users = store

	got, err := a.SaveUser()
	if err != nil {
		t.Fatal(err)
	}
	if store.attempts != 2 || store.updates != 1 {
		t.Fatalf("%d attempts, %d of them stored; want 2 and 1", store.attempts, store.updates)
	}
	if got.Perm.Delete || got.Rules != nil {
		t.Errorf("the login brought back what the administrator had taken away: %+v, rules %+v", got.Perm, got.Rules)
	}
	if got.Locale != "from-the-hook" {
		t.Errorf("the locale the hook supplied was not stored: %q", got.Locale)
	}
	if got.TokensNotBefore != 200 {
		t.Errorf("the user that is returned has mark %d, want that of the record as it is stored, 200", got.TokensNotBefore)
	}
}

// When that happens every time, the login gives up: it does not go on for
// ever, and it stores nothing.
func TestHookLoginGivesUpWhenTheAccountKeepsChanging(t *testing.T) {
	const password = "a-strong-password"
	stored := storedHookUser(t, password)
	stored.TokensNotBefore = 100

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	store.before = func(int) {
		changed := *store.users["bob"]
		changed.TokensNotBefore++
		store.users["bob"] = &changed
	}
	a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", map[string]string{
		"user.locale": "from-the-hook",
	})
	a.Users = store

	got, err := a.SaveUser()
	if !errors.Is(err, fberrors.ErrUserChanged) || got != nil {
		t.Fatalf("user %v, error %v; want ErrUserChanged", got, err)
	}
	if store.attempts != hookSaveAttempts || store.updates != 0 {
		t.Errorf("%d attempts, %d of them stored; want %d and none", store.attempts, store.updates, hookSaveAttempts)
	}
	if store.users["bob"].Locale == "from-the-hook" {
		t.Error("the login that gave up stored its change")
	}
}

// A hook that accepts several passwords for a user says so, and a login
// with another one than the last then ends no session. The password is
// still stored, as it always was. The answer is given per login: without
// it, and with anything but "true", a password that is not the stored one
// ends the sessions as before.
func TestHookThatKeepsSessionsDoesNotRevokeTokens(t *testing.T) {
	stored := storedHookUser(t, "the-account-password")
	stored.TokensNotBefore = 100

	store := &updatingStore{mockUserStore: &mockUserStore{users: map[string]*users.User{"bob": stored}}}
	login := func(password string, fields map[string]string) *users.User {
		t.Helper()
		a := newHookAuth(store.mockUserStore, &settings.Settings{}, &settings.Server{Root: t.TempDir()}, "bob", fields)
		a.Users = store
		a.Cred.Password = password
		u, err := a.SaveUser()
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	u := login("an-app-password-42", map[string]string{"hook.keepSessions": "true"})
	if u.TokensNotBefore != 100 {
		t.Errorf("a login the hook said to keep the sessions for moved the mark from 100 to %d", u.TokensNotBefore)
	}
	if !users.CheckPwd("an-app-password-42", u.Password) || !reflect.DeepEqual(store.fields, []string{"Password"}) {
		t.Errorf("the accepted password was not stored, or more than it was: fields %v", store.fields)
	}

	for _, value := range []string{"false", "yes", "1", "TRUE", ""} {
		before := store.users["bob"].TokensNotBefore
		u := login("another-password-"+value, map[string]string{"hook.keepSessions": value})
		if u.TokensNotBefore <= before {
			t.Errorf("hook.keepSessions=%q: a password that is not the stored one left the mark at %d", value, u.TokensNotBefore)
		}
	}

	before := store.users["bob"].TokensNotBefore
	if u := login("yet-another-password", map[string]string{}); u.TokensNotBefore <= before {
		t.Errorf("without the field, a password that is not the stored one left the mark at %d", u.TokensNotBefore)
	}
}

// The field is one the hook's output is read for.
func TestHookKeepSessionsIsAFieldOfTheOutput(t *testing.T) {
	a := &HookAuth{}
	a.GetValues("hook.action=auth\nhook.keepSessions=true\nhook.unknown=1\n")
	if a.Fields.Values["hook.keepSessions"] != "true" || len(a.Fields.Values) != 2 {
		t.Errorf("values read from the output: %v", a.Fields.Values)
	}
}
