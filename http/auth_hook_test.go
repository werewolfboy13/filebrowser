package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/filebrowser/filebrowser/v2/auth"
	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/users"
)

// useHookAuth switches the instance to hook authentication, with a POSIX
// shell script as the hook. The script sees the credentials as $USERNAME and
// $PASSWORD.
func (e *fullEnv) useHookAuth(script string) {
	e.t.Helper()
	if runtime.GOOS == "windows" {
		e.t.Skip("uses a POSIX shell script as the hook")
	}

	path := filepath.Join(e.t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		e.t.Fatal(err)
	}
	s, err := e.st.Settings.Get()
	if err != nil {
		e.t.Fatal(err)
	}
	s.AuthMethod = auth.MethodHookAuth
	if err := e.st.Settings.Save(s); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.Auth.Save(&auth.HookAuth{Command: path}); err != nil {
		e.t.Fatal(err)
	}
}

// With hook authentication, a login of an existing user rewrote the whole
// user record from what a hook can supply, which does not include the rules
// of the user. The rules an administrator had set were erased by the user's
// next login, and the files they denied were served.
func TestHookLoginKeepsTheRulesOfTheUser(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("secret/plans.txt", "do not read")
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{{Allow: false, Path: "/secret"}}
		u.AceEditorTheme = "chaos"
	})

	// The rule holds to begin with.
	tok := e.login("bob", fullEnvUserPassword)
	if code, _, _ := e.do(http.MethodGet, "/api/raw/secret/plans.txt", tok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("the rule is not in force to begin with: %d", code)
	}

	// The hook accepts bob and supplies a field besides the action, as hooks
	// do, which is what makes the login store the user.
	e.useHookAuth(`if [ "$USERNAME" = bob ] && [ "$PASSWORD" = '` + fullEnvUserPassword + `' ]; then
  echo hook.action=auth
  echo user.locale=en
else
  echo hook.action=block
fi
`)
	tok = e.login("bob", fullEnvUserPassword)

	stored := e.user(2)
	if len(stored.Rules) != 1 || stored.Rules[0].Path != "/secret" || stored.Rules[0].Allow {
		t.Errorf("the rules of the user after a hook login: %+v", stored.Rules)
	}
	if stored.AceEditorTheme != "chaos" || stored.Locale != "en" {
		t.Errorf("after a hook login: editor theme %q (want chaos), locale %q (want en, from the hook)", stored.AceEditorTheme, stored.Locale)
	}
	if code, _, body := e.do(http.MethodGet, "/api/raw/secret/plans.txt", tok, nil, nil); code != http.StatusForbidden {
		t.Errorf("GET of the denied file after a hook login = %d %q, want 403", code, body)
	}
	if !e.works(tok) {
		t.Error("the token of the hook login does not work")
	}
}

// A password that changed at the source the hook asks reaches the server
// with the user's next login. It ends the sessions that were opened with the
// old password, like a change through the API does, and the login that
// brought it gets a session that works.
func TestHookPasswordChangeEndsOlderSessions(t *testing.T) {
	e := newFullEnv(t)

	// The source knows one password at a time: it is kept in a file.
	current := filepath.Join(t.TempDir(), "password")
	setPassword := func(p string) {
		t.Helper()
		if err := os.WriteFile(current, []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setPassword(fullEnvUserPassword)
	e.useHookAuth(`if [ "$USERNAME" = bob ] && [ "$PASSWORD" = "$(cat '` + current + `')" ]; then
  echo hook.action=auth
else
  echo hook.action=block
fi
`)

	older := e.login("bob", fullEnvUserPassword)
	again := e.distinctLogin("bob", fullEnvUserPassword)
	if !e.works(older) || !e.works(again) {
		t.Fatal("a second login with the same password ended a session")
	}

	setPassword("brand-new-password-77")
	if code, _, _ := e.tryLogin("bob", fullEnvUserPassword); code != http.StatusForbidden {
		t.Fatalf("login with the old password = %d, want 403", code)
	}
	if !e.works(older) {
		t.Fatal("a refused login ended a session")
	}

	fresh := e.login("bob", "brand-new-password-77")
	if !e.works(fresh) {
		t.Error("the token of the login that brought the new password does not work")
	}
	for name, token := range map[string]string{"the first session": older, "the second session": again} {
		if e.works(token) {
			t.Errorf("%s, opened with the old password, still works after the password changed", name)
		}
		if code, _, _ := e.do(http.MethodPost, "/api/renew", token, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("renewal of %s = %d, want 401", name, code)
		}
	}

	// Logging in again with the password that is stored now ends nothing.
	if next := e.distinctLogin("bob", "brand-new-password-77"); !e.works(next) || !e.works(fresh) {
		t.Error("a login with the stored password ended a session")
	}
}
