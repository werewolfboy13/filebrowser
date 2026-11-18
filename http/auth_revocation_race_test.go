package fbhttp

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/filebrowser/filebrowser/v2/users"
)

// A session token used to be placed against a revocation by its date alone,
// and it was dated when it was signed. A request that had read the account
// before a password change was stored, and signed after it, got a token dated
// after the change: one that the change did not end, and that could be
// renewed for ever. A login has the time of a password hash between the two,
// a renewal and the answer to the user's own change a moment.
//
// A token now says which state of the account it was issued from (the
// revocation mark of the record that was read), and is refused when the
// account has moved on. That does not depend on any clock, so it holds
// wherever the read and the change fall.

// loginDuration is how long a login takes, which is the time of one password
// hash.
func loginDuration(e *fullEnv) time.Duration {
	e.t.Helper()
	start := time.Now()
	e.login("bob", fullEnvUserPassword)
	return time.Since(start)
}

// secondTurn returns the next turn of a second that is at least lead away.
func secondTurn(lead time.Duration) time.Time {
	next := time.Now().Truncate(time.Second).Add(time.Second)
	for time.Until(next) < lead+10*time.Millisecond {
		next = next.Add(time.Second)
	}
	return next
}

// Every way of issuing a token (a login, a renewal, the answer to the user's
// own change) signs it from an account record. Whatever the clock says when
// it does, a token signed from a record that a revocation has replaced is
// refused.
func TestTokenFromARecordReadBeforeARevocationIsRefused(t *testing.T) {
	e := newFullEnv(t)
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	d := &data{settings: set}

	for _, name := range []string{"first revocation of the account", "a later revocation"} {
		t.Run(name, func(t *testing.T) {
			// What the request read.
			stale := e.user(2)

			// The password change, stored as userPut stores it.
			u := e.user(2)
			u.RevokeTokens(time.Now())
			if err := e.st.Users.Update(u, "TokensNotBefore"); err != nil {
				t.Fatal(err)
			}

			// The request signs once the revocation mark has passed: the
			// token is dated after it.
			time.Sleep(time.Until(time.Unix(u.TokensNotBefore, 0).Add(50 * time.Millisecond)))
			for _, passwordLogin := range []bool{true, false} {
				token, err := signSessionToken(d, stale, time.Hour, passwordLogin)
				if err != nil {
					t.Fatal(err)
				}
				if e.works(token) {
					t.Errorf("a token (password login: %v) signed from the account as it was before the revocation works", passwordLogin)
				}
				if code, _, _ := e.do(http.MethodPost, "/api/renew", token, nil, nil); code != http.StatusUnauthorized {
					t.Errorf("renewal of that token = %d, want 401", code)
				}
			}

			// One signed from the account as it is now works, and renews.
			token, err := signSessionToken(d, e.user(2), time.Hour, false)
			if err != nil {
				t.Fatal(err)
			}
			if !e.works(token) {
				t.Fatal("a token signed from the current record does not work")
			}
			code, _, renewed := e.do(http.MethodPost, "/api/renew", token, nil, nil)
			if code != http.StatusOK || !e.works(renewed) {
				t.Errorf("renewal of a token of the current record = %d, renewed token works: %v", code, e.works(renewed))
			}
		})
	}
}

// A token from before tokens said which record they were issued from is
// still placed by its date: it works until the account is revoked, and one
// dated after the last revocation keeps working.
func TestTokensWithoutARecordMarkArePlacedByTheirDate(t *testing.T) {
	e := newFullEnv(t)
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	sign := func(issuedAt time.Time) string {
		claims := &authToken{
			User:             userInfo{ID: 2},
			RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issuedAt), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(set.Key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}

	before := sign(time.Now().Add(-time.Hour))
	if !e.works(before) {
		t.Fatal("a token without a record mark is refused by an account that was never revoked")
	}

	u := e.user(2)
	u.RevokeTokens(time.Now())
	if err := e.st.Users.Update(u, "TokensNotBefore"); err != nil {
		t.Fatal(err)
	}
	if e.works(before) {
		t.Error("a token without a record mark, dated before the revocation, works after it")
	}
	if after := sign(time.Unix(u.TokensNotBefore, 0)); !e.works(after) {
		t.Error("a token without a record mark, dated after the revocation, is refused")
	}
}

// A login with the old password that is still hashing when the password is
// changed. The change is stored as userPut stores it (the password and the
// mark in one update), a quarter of a login before a second turns; the login
// began half a login earlier, and signs after the turn.
func TestLoginStraddlingAPasswordChangeDoesNotSurvive(t *testing.T) {
	e := newFullEnv(t)
	b := loginDuration(e)

	newHash, err := users.HashPwd("brand-new-password-77")
	if err != nil {
		t.Fatal(err)
	}

	turn := secondTurn(b)
	time.Sleep(time.Until(turn.Add(-b * 3 / 4)))

	var (
		wg   sync.WaitGroup
		code int
		tok  string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		code, _, tok = e.tryLogin("bob", fullEnvUserPassword) // the old password
	}()

	time.Sleep(time.Until(turn.Add(-b / 4)))
	u := e.user(2)
	u.Password = newHash
	u.RevokeTokens(time.Now())
	if err := e.st.Users.Update(u, "Password", "TokensNotBefore"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if code != http.StatusOK {
		t.Skipf("the login did not straddle the change this time (%d)", code)
	}
	assertSessionIsOver(t, e, tok)
}

// The same, with the login reading the account in the moment between the
// change working out its revocation mark and the change being stored. The
// mark is the second after the one it is worked out in; when that second has
// begun by the time the login reads the account, the login's own reading of
// the clock is not before the mark either, and dating the token by when the
// account was read (instead of when the token was signed) does not help.
func TestLoginReadingTheAccountWhileARevocationIsStoredDoesNotSurvive(t *testing.T) {
	e := newFullEnv(t)
	b := loginDuration(e)

	newHash, err := users.HashPwd("brand-new-password-77")
	if err != nil {
		t.Fatal(err)
	}

	// The change works out its mark just before the second turns (the time
	// is handed in, so that the test does not depend on waking up on the
	// millisecond)...
	turn := secondTurn(50 * time.Millisecond)
	u := e.user(2)
	u.Password = newHash
	u.RevokeTokens(turn.Add(-5 * time.Millisecond))

	// ...the login reads the account just after it...
	time.Sleep(time.Until(turn.Add(5 * time.Millisecond)))
	var (
		wg   sync.WaitGroup
		code int
		tok  string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		code, _, tok = e.tryLogin("bob", fullEnvUserPassword) // the old password
	}()

	// ...and only then is the change stored, while the login is hashing.
	time.Sleep(b / 3)
	if err := e.st.Users.Update(u, "Password", "TokensNotBefore"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if code != http.StatusOK {
		t.Skipf("the login did not read the account before the change was stored (%d)", code)
	}
	assertSessionIsOver(t, e, tok)
}

// assertSessionIsOver fails the test when the token still works, or can be
// renewed.
func assertSessionIsOver(t *testing.T, e *fullEnv, token string) {
	t.Helper()
	works := e.works(token)
	code, _, renewed := e.do(http.MethodPost, "/api/renew", token, nil, nil)
	if works || code != http.StatusUnauthorized {
		t.Errorf("a token from a login with the old password, which began before the password change was stored, outlives it: "+
			"works = %v, renewal = %d (the renewed token works: %v)", works, code, code == http.StatusOK && e.works(renewed))
	}
}

// Through the API alone: the owner changes the password with PUT
// /api/users/2 while someone who knows the old one keeps logging in with it.
// The change is timed to be stored shortly before a second turns, which is
// when a login in flight signs after the mark. No login with the old password
// may leave a session that works after the change.
func TestLoginsRacingAPasswordChangeDoNotSurvive(t *testing.T) {
	e := newFullEnv(t)
	b := loginDuration(e)

	passwords := []string{fullEnvUserPassword, "second-long-password-88", "third-long-password-999", "fourth-long-password-0000"}
	mine := e.login("bob", passwords[0])

	survived, raced := 0, 0
	for round := 0; round+1 < len(passwords); round++ {
		oldPw, newPw := passwords[round], passwords[round+1]

		// The change costs about two password hashes (the current password
		// is checked, the new one hashed) before it is stored.
		lead := 2*b + 10*time.Millisecond
		turn := secondTurn(lead + b/4 + 10*time.Millisecond)

		stop := make(chan struct{})
		var (
			mu     sync.Mutex
			tokens []string
			wg     sync.WaitGroup
		)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					// The address is the round's own: the failed logins that
					// follow a change must not have the limiter hold the next
					// round back.
					cred := `{"username":"bob","password":"` + oldPw + `"}`
					xff := map[string]string{"X-Forwarded-For": "10.9.0." + string(rune('1'+round))}
					if code, _, tok := e.do(http.MethodPost, "/api/login", "", strings.NewReader(cred), xff); code == http.StatusOK {
						mu.Lock()
						tokens = append(tokens, tok)
						mu.Unlock()
					}
				}
			}()
		}

		time.Sleep(time.Until(turn.Add(-lead - b/4)))
		change := `{"what":"user","which":["password"],"current_password":"` + oldPw + `","data":{"id":2,"password":"` + newPw + `"}}`
		code, h, body := e.do(http.MethodPut, "/api/users/2", mine, strings.NewReader(change), nil)
		if code != http.StatusOK {
			close(stop)
			wg.Wait()
			t.Fatalf("password change = %d %q", code, body)
		}
		mine = h.Get("X-Auth-Token")
		if !e.works(mine) {
			t.Errorf("round %d: the token handed out with the password change does not work", round)
		}

		// Let the logins in flight finish.
		time.Sleep(2 * b)
		close(stop)
		wg.Wait()

		raced += len(tokens)
		for _, tok := range tokens {
			if e.works(tok) {
				survived++
				break
			}
		}
	}
	t.Logf("a login takes %v; %d logins with the old password were answered around %d changes", b, raced, len(passwords)-1)

	if survived > 0 {
		t.Errorf("after %d of %d password changes, a session opened with the old password still works", survived, len(passwords)-1)
	}
}
