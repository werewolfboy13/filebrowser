package auth

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// loginRequest is a login request with the given body, or without one.
func loginRequest(t *testing.T, body string, hasBody bool) *http.Request {
	t.Helper()
	var reader io.Reader
	if hasBody {
		reader = strings.NewReader(body)
	}
	r, err := http.NewRequest(http.MethodPost, "/api/login", reader)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBody {
		r.Body = nil
	}
	return r
}

// The login counts failed guesses at a password, and keeps a record of each.
// An auther refuses some requests before it looks at any password, and those
// are not guesses: it has to say which is which, or requests that cost
// nothing fill the limiter.
func TestAuthersTellARefusalFromAGuess(t *testing.T) {
	hash, err := users.HashPwd("the-right-password")
	if err != nil {
		t.Fatal(err)
	}
	store := &mockUserStore{users: map[string]*users.User{"admin": {Username: "admin", Password: hash}}}
	srv := &settings.Server{Root: t.TempDir()}

	// A captcha service that turns every answer down.
	captcha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer captcha.Close()

	plain := JSONAuth{}
	withCaptcha := JSONAuth{ReCaptcha: &ReCaptcha{Host: captcha.URL, Secret: "secret"}}
	hook := &HookAuth{Command: "/nonexistent-hook-command"}

	cases := map[string]struct {
		auther  Auther
		body    string
		noBody  bool
		attempt bool
	}{
		"json: no body":                     {auther: plain, noBody: true},
		"json: an empty body":               {auther: plain, body: ``},
		"json: not JSON":                    {auther: plain, body: `username=admin`},
		"json: a password that is a number": {auther: plain, body: `{"username":"admin","password":0}`},
		"json: a user name that is a list":  {auther: plain, body: `{"username":["admin"],"password":"x"}`},
		"json: a captcha that is a number":  {auther: plain, body: `{"username":"admin","password":"x","recaptcha":0}`},
		"json: no user name":                {auther: plain, body: `{"password":"x"}`},
		"json: an empty user name":          {auther: plain, body: `{"username":"","password":"x"}`},
		"json: a failed captcha":            {auther: withCaptcha, body: `{"username":"admin","password":"the-right-password","recaptcha":"nope"}`},
		"json: a wrong password":            {auther: plain, body: `{"username":"admin","password":"wrong"}`, attempt: true},
		"json: an empty password":           {auther: plain, body: `{"username":"admin","password":""}`, attempt: true},
		"json: a user that does not exist":  {auther: plain, body: `{"username":"nobody","password":"wrong"}`, attempt: true},
		"hook: no body":                     {auther: hook, noBody: true},
		"hook: not JSON":                    {auther: hook, body: `username=admin`},
		"hook: a password that is a number": {auther: hook, body: `{"username":"admin","password":0}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			user, err := tc.auther.Auth(loginRequest(t, tc.body, !tc.noBody), store, &settings.Settings{}, srv)
			if user != nil || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("Auth = %v, %v; want a refusal", user, err)
			}
			if got := !errors.Is(err, ErrNoAttempt); got != tc.attempt {
				t.Errorf("counts as a guess at a password: %v, want %v (%v)", got, tc.attempt, err)
			}
		})
	}

	// The right password is still the right password.
	if user, err := plain.Auth(loginRequest(t, `{"username":"admin","password":"the-right-password"}`, true), store, &settings.Settings{}, srv); err != nil || user == nil {
		t.Errorf("the right password = %v, %v", user, err)
	}
}
