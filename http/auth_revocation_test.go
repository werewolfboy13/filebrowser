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
// the change is handed a fresh token with the answer to it.
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

	// The answer carries the token the session goes on with. It is a
	// credential, so the answer may not be stored, and the token is not in
	// the body, where a client that only shows it would put it on the page.
	fresh := h.Get("X-Auth-Token")
	if strings.Count(fresh, ".") != 2 || fresh == mine {
		t.Fatalf("the password change did not hand out a new token (X-Auth-Token %q)", fresh)
	}
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "no-store") || len(h.Values("Cache-Control")) != 1 {
		t.Errorf("the answer carrying a token may be stored: Cache-Control %q", h.Values("Cache-Control"))
	}
	if strings.Contains(body, fresh) {
		t.Error("the new token is in the response body")
	}
	// The old token must not be used for a renewal: it would be refused.
	if h.Get("X-Renew-Token") == "true" {
		t.Error("the password change asked the client to renew the token it just revoked")
	}
	if !e.works(fresh) {
		t.Error("the token handed out with the password change does not work")
	}

	// Every token from before the change is over, the one that made the
	// change included: no request, and no renewal, not even once.
	for name, token := range map[string]string{"another session": stolen, "the session that made the change": mine} {
		if e.works(token) {
			t.Errorf("the token of %s still works after the password change", name)
		}
		for i := 0; i < 2; i++ {
			if code, _, _ := e.do(http.MethodPost, "/api/renew", token, nil, nil); code != http.StatusUnauthorized {
				t.Errorf("renewal of the token of %s = %d, want 401", name, code)
			}
		}
	}

	// New sessions are unaffected, also one opened within the same second.
	if !e.works(e.login("bob", "brand-new-password-77")) {
		t.Error("a login after the password change does not work")
	}
	code, _, again := e.do(http.MethodPost, "/api/renew", fresh, nil, nil)
	if code != http.StatusOK || !e.works(again) {
		t.Errorf("an ordinary renewal after the change = %d", code)
	}
}

// The session that changed its own password used to be let through one
// renewal of its revoked token. That permission was tied to the token, not to
// the client: whoever else held the same token and renewed first got a
// session that outlived the password change, and the user's own renewal was
// the one refused. A user who changes the password because the session was
// stolen ended up locked out, with the thief logged in.
func TestRevokedTokenCannotBeRenewedByAnyHolder(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword) // held by the user and by someone else
	time.Sleep(1100 * time.Millisecond)

	change := `{"what":"user","which":["password"],"current_password":"` + fullEnvUserPassword + `","data":{"id":2,"password":"brand-new-password-77"}}`
	code, h, _ := e.do(http.MethodPut, "/api/users/2", tok, strings.NewReader(change), nil)
	if code != http.StatusOK {
		t.Fatalf("own password change = %d", code)
	}

	// The other holder is first to ask for a renewal.
	code, _, renewed := e.do(http.MethodPost, "/api/renew", tok, nil, nil)
	if code != http.StatusUnauthorized {
		t.Errorf("renewal of the revoked token by another holder = %d, want 401", code)
	}
	if code == http.StatusOK && e.works(renewed) {
		t.Error("a second holder of the session token survives the password change")
	}

	// The user goes on with the token from the answer, whatever the other
	// holder did in between.
	if fresh := h.Get("X-Auth-Token"); !e.works(fresh) {
		t.Errorf("the user's own session ended with the password change (X-Auth-Token %q)", fresh)
	}
}

// Only the session that ends itself is handed a token, and only when it does.
func TestNewTokenIsOnlyHandedToTheSessionThatEndedItself(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)
	bob := e.login("bob", fullEnvUserPassword)

	requests := map[string]struct {
		token, path, payload string
	}{
		"an administrator changes another account's password": {admin, "/api/users/2",
			`{"what":"user","which":["password"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"password":"set-by-the-admin-123"}}`},
		"an administrator changes another account's permissions": {admin, "/api/users/2",
			`{"what":"user","which":["perm"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"perm":{"download":true}}}`},
		"a change of one's own that ends no session": {admin, "/api/users/1",
			`{"what":"user","which":["locale"],"data":{"id":1,"locale":"fr"}}`},
		"a change that is refused": {admin, "/api/users/1",
			`{"what":"user","which":["password"],"current_password":"wrong","data":{"id":1,"password":"never-stored-password-1"}}`},
	}
	for name, rq := range requests {
		_, h, body := e.do(http.MethodPut, rq.path, rq.token, strings.NewReader(rq.payload), nil)
		if got := h.Get("X-Auth-Token"); got != "" || strings.Count(body, ".") == 2 {
			t.Errorf("%s: a token was handed out (X-Auth-Token %q, body %q)", name, got, body)
		}
	}
	if e.works(bob) {
		t.Fatal("control: bob's session survived the change of his password")
	}
	if !e.works(admin) {
		t.Fatal("the administrator's session ended")
	}

	// An administrator who takes a permission away from their own account is
	// handed a token that says so.
	perm := e.user(1).Perm
	perm.Share = false
	data, _ := json.Marshal(perm)
	payload := `{"what":"user","which":["perm"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":1,"perm":` + string(data) + `}}`
	code, h, body := e.do(http.MethodPut, "/api/users/1", admin, strings.NewReader(payload), nil)
	fresh := h.Get("X-Auth-Token")
	if code != http.StatusOK || fresh == "" {
		t.Fatalf("own permission change = %d %q, X-Auth-Token %q", code, body, fresh)
	}
	var claims authToken
	if _, _, err := jwt.NewParser().ParseUnverified(fresh, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.User.ID != 1 || claims.User.Perm.Share || !claims.User.Perm.Admin {
		t.Errorf("the new token does not describe the account as it is now: %+v", claims.User)
	}
	if e.works(admin) || !e.works(fresh) {
		t.Errorf("after the own permission change: old token works = %v, new token works = %v", e.works(admin), e.works(fresh))
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
			if h.Get("X-Renew-Token") == "true" || h.Get("X-Auth-Token") != "" {
				t.Error("the admin's own session was asked to renew, or handed a token, for a change to another account")
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
	fresh := h.Get("X-Auth-Token")
	if code != http.StatusOK || fresh == "" {
		t.Fatalf("own password change = %d %q, X-Auth-Token %q", code, body, fresh)
	}
	if !e.works(fresh) {
		t.Error("the token handed out with the own password change does not work")
	}
	if code, _, _ := e.do(http.MethodGet, "/api/users", fresh, nil, nil); code != http.StatusOK {
		t.Errorf("the new token is not an administrator's: user list = %d", code)
	}
	if e.works(other) || e.works(admin) {
		t.Error("a session of the admin from before the password change survived it")
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

// The answer to a token that has been revoked, or whose account is gone, used
// to carry X-Renew-Token: true: the header was set before either was checked.
// The web interface renews when it sees the header, before it looks at the
// status; the renewal of such a token is refused, and the page was left with
// an error instead of being sent to the login.
func TestEndedSessionsAreNotToldToRenew(t *testing.T) {
	e := newFullEnv(t)
	session := e.login("bob", fullEnvUserPassword)
	admin := e.distinctLogin("admin", fullEnvAdminPassword)

	// Control: a session whose account was updated without being revoked is
	// told to renew, and can.
	if code, body := e.putUser(admin, 2, `{"what":"user","which":["locale"],"data":{"id":2,"locale":"fr"}}`); code != http.StatusOK {
		t.Fatalf("locale change = %d %q", code, body)
	}
	time.Sleep(1100 * time.Millisecond) // the update is dated to the second
	code, h, _ := e.do(http.MethodGet, "/api/resources/", session, nil, nil)
	if code != http.StatusOK || h.Get("X-Renew-Token") != "true" {
		t.Fatalf("control: request after an update of the account = %d, X-Renew-Token %q; want 200 and true", code, h.Get("X-Renew-Token"))
	}
	code, h, renewed := e.do(http.MethodPost, "/api/renew", session, nil, nil)
	if code != http.StatusOK || h.Get("X-Renew-Token") == "true" {
		t.Fatalf("control: renewal = %d, X-Renew-Token %q", code, h.Get("X-Renew-Token"))
	}

	// The account's sessions are ended.
	change := `{"what":"user","which":["password"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"password":"brand-new-password-77"}}`
	if code, body := e.putUser(admin, 2, change); code != http.StatusOK {
		t.Fatalf("password change = %d %q", code, body)
	}
	for _, token := range []string{session, renewed} {
		for _, rq := range []struct{ method, path string }{
			{http.MethodGet, "/api/resources/"},
			{http.MethodPost, "/api/renew"},
			{http.MethodGet, "/api/users/2"},
		} {
			code, h, _ := e.do(rq.method, rq.path, token, nil, nil)
			if code != http.StatusUnauthorized {
				t.Errorf("%s %s with a revoked token = %d, want 401", rq.method, rq.path, code)
			}
			if got := h.Get("X-Renew-Token"); got == "true" {
				t.Errorf("%s %s with a revoked token: X-Renew-Token = %q on a 401", rq.method, rq.path, got)
			}
		}
	}

	// The account is deleted.
	fresh := e.login("bob", "brand-new-password-77")
	if code, _, body := e.do(http.MethodDelete, "/api/users/2", admin, strings.NewReader(`{"current_password":"`+fullEnvAdminPassword+`"}`), nil); code != http.StatusOK {
		t.Fatalf("deleting the account = %d %q", code, body)
	}
	code, h, _ = e.do(http.MethodGet, "/api/resources/", fresh, nil, nil)
	if code != http.StatusUnauthorized || h.Get("X-Renew-Token") == "true" {
		t.Errorf("request for a deleted account = %d, X-Renew-Token %q; want 401 without the header", code, h.Get("X-Renew-Token"))
	}
}
