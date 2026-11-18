package auth

import (
	"reflect"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// updatingStore is mockUserStore with an Update that stores the user, like
// the real one does when it is given no field names.
type updatingStore struct {
	*mockUserStore
	updates int
}

func (s *updatingStore) Update(user *users.User, _ ...string) error {
	s.updates++
	s.users[user.Username] = user
	return nil
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
