package fbhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/filebrowser/filebrowser/v2/users"
)

// distinctLogin logs in after the clock has moved to another second, so the
// token differs from one issued just before: tokens of the same user issued
// within one second are byte for byte the same.
func (e *fullEnv) distinctLogin(user, password string) string {
	e.t.Helper()
	time.Sleep(1100 * time.Millisecond)
	return e.login(user, password)
}

func (e *fullEnv) works(token string) bool {
	e.t.Helper()
	code, _, _ := e.do(http.MethodGet, "/api/resources/", token, nil, nil)
	return code == http.StatusOK
}

// A token stayed valid until it expired whatever happened to the account, and
// could be renewed for ever: changing a password did not end the sessions of
// whoever had the old one (GHSA-v7vv-5wj2-gfcj, GHSA-7xwp-2cpp-p8r7). A change
// of password now ends every session of the user, and the session that made
// the change is handed a fresh token.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	e := newFullEnv(t)
	stolen := e.login("bob", fullEnvUserPassword)
	mine := e.distinctLogin("bob", fullEnvUserPassword)
	if stolen == mine {
		t.Fatal("the two sessions share one token")
	}

	change := `{"what":"user","which":["password"],"current_password":"` + fullEnvUserPassword + `","data":{"id":2,"password":"brand-new-password-77"}}`
	code, h, body := e.do(http.MethodPut, "/api/users/2", mine, strings.NewReader(change), nil)
	if code != http.StatusOK {
		t.Fatalf("password change = %d %q", code, body)
	}
	// This is what makes the web interface renew its token right away.
	if h.Get("X-Renew-Token") != "true" {
		t.Errorf("the password change did not ask the client to renew its token (X-Renew-Token %q)", h.Get("X-Renew-Token"))
	}

	// The other session is over: no request, and no renewal.
	if e.works(stolen) {
		t.Error("a token issued before the password change still works")
	}
	if code, _, _ := e.do(http.MethodPost, "/api/renew", stolen, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("renewal of a token issued before the password change = %d, want 401", code)
	}

	// The session that changed the password renews once with its old token...
	code, _, renewed := e.do(http.MethodPost, "/api/renew", mine, nil, nil)
	if code != http.StatusOK || strings.Count(renewed, ".") != 2 {
		t.Fatalf("renewal by the session that changed the password = %d %q", code, renewed)
	}
	if !e.works(renewed) {
		t.Error("the renewed token does not work")
	}
	// ... and only once, and for nothing else.
	if code, _, _ := e.do(http.MethodPost, "/api/renew", mine, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("second renewal with the old token = %d, want 401", code)
	}
	if e.works(mine) {
		t.Error("the old token of the session that changed the password still works for requests")
	}

	// New sessions are unaffected, also one opened within the same second.
	if !e.works(e.login("bob", "brand-new-password-77")) {
		t.Error("a login after the password change does not work")
	}
	code, _, again := e.do(http.MethodPost, "/api/renew", renewed, nil, nil)
	if code != http.StatusOK || !e.works(again) {
		t.Errorf("an ordinary renewal after the change = %d", code)
	}
}

// What an administrator does to an account ends its sessions when it changes
// who the user is or what they may reach, and leaves them alone otherwise.
func TestAdminChangesEndSessionsOfTheAccount(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)

	fullUpdate := func(mutate func(u map[string]interface{})) string {
		stored, _ := json.Marshal(e.user(2))
		var u map[string]interface{}
		_ = json.Unmarshal(stored, &u)
		u["password"] = ""
		mutate(u)
		data, _ := json.Marshal(u)
		return `{"what":"user","which":["all"],"current_password":"` + fullEnvAdminPassword + `","data":` + string(data) + `}`
	}

	cases := []struct {
		name    string
		payload func() string
		ends    bool
	}{
		{"locale only", func() string {
			return `{"what":"user","which":["locale"],"data":{"id":2,"locale":"fr"}}`
		}, false},
		{"whole user, nothing sensitive changed", func() string {
			return fullUpdate(func(u map[string]interface{}) { u["locale"] = "de" })
		}, false},
		{"a client cannot lift the revocation", func() string {
			return fullUpdate(func(u map[string]interface{}) { u["tokensNotBefore"] = 0 })
		}, false},
		{"permissions", func() string {
			return `{"what":"user","which":["perm"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"perm":{"download":true}}}`
		}, true},
		{"scope", func() string {
			return `{"what":"user","which":["scope"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"scope":"/users"}}`
		}, true},
		{"password", func() string {
			return `{"what":"user","which":["password"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"password":"set-by-the-admin-123"}}`
		}, true},
		{"whole user with a new password", func() string {
			return fullUpdate(func(u map[string]interface{}) { u["password"] = "set-by-the-admin-456" })
		}, true},
		{"whole user with other permissions", func() string {
			return fullUpdate(func(u map[string]interface{}) { u["perm"] = map[string]bool{"download": true, "create": true} })
		}, true},
		{"unchanged permissions named explicitly", func() string {
			perm, _ := json.Marshal(e.user(2).Perm)
			return `{"what":"user","which":["perm"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"perm":` + string(perm) + `}}`
		}, false},
	}

	password := fullEnvUserPassword
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := e.login("bob", password)
			if !e.works(session) {
				t.Fatal("control: the session does not work")
			}

			code, h, body := e.do(http.MethodPut, "/api/users/2", admin, strings.NewReader(tc.payload()), nil)
			if code != http.StatusOK {
				t.Fatalf("update = %d %q", code, body)
			}
			if h.Get("X-Renew-Token") == "true" {
				t.Error("the admin's own session was asked to renew for a change to another account")
			}

			if ended := !e.works(session); ended != tc.ends {
				t.Errorf("the session of the account ended = %v, want %v", ended, tc.ends)
			}
			if tc.ends {
				if code, _, _ := e.do(http.MethodPost, "/api/renew", session, nil, nil); code != http.StatusUnauthorized {
					t.Errorf("renewal of the ended session = %d, want 401", code)
				}
			}
			if !e.works(admin) {
				t.Fatal("the admin's own session ended")
			}

			switch tc.name {
			case "password":
				password = "set-by-the-admin-123"
			case "whole user with a new password":
				password = "set-by-the-admin-456"
			}
		})
	}

	// Revocation is not something a request can name.
	if code, body := e.putUser(admin, 2, `{"what":"user","which":["tokensNotBefore"],"current_password":"`+fullEnvAdminPassword+`","data":{"id":2,"tokensNotBefore":0}}`); code != http.StatusBadRequest {
		t.Errorf("update of tokensNotBefore = %d %q, want 400", code, body)
	}
}

// An administrator who changes their own password through the user editor
// keeps their session the same way.
func TestOwnPasswordChangeThroughFullUpdateKeepsTheSession(t *testing.T) {
	e := newFullEnv(t)
	other := e.login("admin", fullEnvAdminPassword)
	admin := e.distinctLogin("admin", fullEnvAdminPassword)

	stored, _ := json.Marshal(e.user(1))
	var u map[string]interface{}
	_ = json.Unmarshal(stored, &u)
	u["password"] = "the-admins-new-password-1"
	data, _ := json.Marshal(u)
	payload := `{"what":"user","which":["all"],"current_password":"` + fullEnvAdminPassword + `","data":` + string(data) + `}`

	code, h, body := e.do(http.MethodPut, "/api/users/1", admin, strings.NewReader(payload), nil)
	if code != http.StatusOK || h.Get("X-Renew-Token") != "true" {
		t.Fatalf("own password change = %d %q, X-Renew-Token %q", code, body, h.Get("X-Renew-Token"))
	}
	code, _, renewed := e.do(http.MethodPost, "/api/renew", admin, nil, nil)
	if code != http.StatusOK || !e.works(renewed) {
		t.Errorf("renewal after the own password change = %d", code)
	}
	if e.works(other) {
		t.Error("the admin's other session survived the password change")
	}
}

// Accounts that were never revoked (every account of an existing database)
// accept their tokens as before, and a revoked account refuses a token that
// carries no issue time at all.
func TestRevocationLeavesExistingAccountsAlone(t *testing.T) {
	e := newFullEnv(t)
	if e.user(2).TokensNotBefore != 0 {
		t.Fatal("a new account starts out revoked")
	}
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	sign := func(issuedAt *jwt.NumericDate) string {
		claims := &authToken{
			User:             userInfo{ID: 2},
			RegisteredClaims: jwt.RegisteredClaims{IssuedAt: issuedAt, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(set.Key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	old := sign(jwt.NewNumericDate(time.Now().Add(-24 * time.Hour)))
	undated := sign(nil)
	if !e.works(old) || !e.works(undated) {
		t.Fatal("tokens of an account that was never revoked are refused")
	}

	u := e.user(2)
	u.RevokeTokens(time.Now())
	if err := e.st.Users.Update(u, "TokensNotBefore"); err != nil {
		t.Fatal(err)
	}
	if e.works(old) || e.works(undated) {
		t.Error("a revoked account accepts a token issued before the revocation, or one without an issue time")
	}
	if !e.works(e.login("bob", fullEnvUserPassword)) {
		t.Error("a login right after the revocation does not work")
	}
}

// A user record is rewritten as a whole from many places (the CLI, hook
// authentication, imports) with a value that knows nothing of the revocation.
// None of them may bring revoked sessions back.
func TestRevocationSurvivesARewriteOfTheRecord(t *testing.T) {
	e := newFullEnv(t)
	session := e.login("bob", fullEnvUserPassword)

	u := e.user(2)
	u.RevokeTokens(time.Now())
	mark := u.TokensNotBefore
	if err := e.st.Users.Update(u, "TokensNotBefore"); err != nil {
		t.Fatal(err)
	}

	for name, rewrite := range map[string]func(fresh *users.User) error{
		"Save":   func(fresh *users.User) error { return e.st.Users.Save(fresh) },
		"Update": func(fresh *users.User) error { return e.st.Users.Update(fresh) },
	} {
		fresh := e.user(2)
		fresh.TokensNotBefore = 0
		fresh.Locale = name
		if err := rewrite(fresh); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := e.user(2); got.TokensNotBefore != mark || got.Locale != name {
			t.Errorf("%s: mark = %d (want %d), locale = %q", name, got.TokensNotBefore, mark, got.Locale)
		}
	}
	if e.works(session) {
		t.Error("a revoked session works again after the record was rewritten")
	}
}
