package fbhttp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/werewolfboy13/filebrowser/v2/users"
)

// useFakeClock puts the limiters of the environment on a clock the test moves.
func (e *fullEnv) useFakeClock() *fakeClock {
	clock := newFakeClock()
	e.limits.login.now = clock.Now
	e.limits.share.now = clock.Now
	e.limits.password.now = clock.Now
	return clock
}

func (e *fullEnv) loginFrom(addr, user, password string) (int, http.Header) {
	e.t.Helper()
	body := `{"username":"` + user + `","password":"` + password + `"}`
	var hdr map[string]string
	if addr != "" {
		hdr = map[string]string{"X-Forwarded-For": addr}
	}
	code, h, _ := e.do(http.MethodPost, "/api/login", "", strings.NewReader(body), hdr)
	return code, h
}

// The login accepted any number of guesses, each of them answered as fast as
// the password hash can be computed. Failed attempts for a user from an
// address now have to wait, longer every time.
func TestLoginGuessesAreSlowedDown(t *testing.T) {
	e := newFullEnv(t)
	clock := e.useFakeClock()
	const attacker, elsewhere = "198.51.100.7", "203.0.113.9"

	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.loginFrom(attacker, "admin", "wrong-guess-"+strconv.Itoa(i)); code != http.StatusForbidden {
			t.Fatalf("wrong password %d = %d, want 403", i, code)
		}
	}

	// The next guess has to wait, and so does the right password: while the
	// key waits, nothing is checked.
	code, h := e.loginFrom(attacker, "admin", "wrong-guess-6")
	if code != http.StatusTooManyRequests {
		t.Fatalf("guess after %d failures = %d, want 429", limiterFreeFailures, code)
	}
	if retry, err := strconv.Atoi(h.Get("Retry-After")); err != nil || retry < 1 {
		t.Errorf("Retry-After = %q, want the seconds to wait", h.Get("Retry-After"))
	}
	if code, _ := e.loginFrom(attacker, "admin", fullEnvAdminPassword); code != http.StatusTooManyRequests {
		t.Errorf("the right password while waiting = %d, want 429", code)
	}

	// Nobody else is held up: not the same user from another address, and not
	// another user from the same address.
	if code, _ := e.loginFrom(elsewhere, "admin", fullEnvAdminPassword); code != http.StatusOK {
		t.Errorf("the same user from another address = %d, want 200", code)
	}
	if code, _ := e.loginFrom(attacker, "bob", fullEnvUserPassword); code != http.StatusOK {
		t.Errorf("another user from the same address = %d, want 200", code)
	}

	// The wait doubles with every further failure.
	wait := limiterBaseDelay
	for i := 0; i < 4; i++ {
		clock.Advance(wait - time.Millisecond)
		if code, _ := e.loginFrom(attacker, "admin", "early"); code != http.StatusTooManyRequests {
			t.Fatalf("guess %s into a wait of %s = %d, want 429", wait-time.Millisecond, wait, code)
		}
		clock.Advance(time.Millisecond)
		if code, _ := e.loginFrom(attacker, "admin", "wrong-again"); code != http.StatusForbidden {
			t.Fatalf("guess after waiting %s = %d, want 403", wait, code)
		}
		wait *= 2
	}

	// After the wait the right password works, and clears the count.
	clock.Advance(wait)
	if code, _ := e.loginFrom(attacker, "admin", fullEnvAdminPassword); code != http.StatusOK {
		t.Fatalf("the right password after the wait = %d, want 200", code)
	}
	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.loginFrom(attacker, "admin", "wrong-guess-"+strconv.Itoa(i)); code != http.StatusForbidden {
			t.Fatalf("wrong password %d after a successful login = %d, want 403", i, code)
		}
	}
}

// A client cannot get itself a fresh count by claiming another address: only
// the entry a proxy adds to X-Forwarded-For counts, and none of it does for a
// connection that does not come from a proxy.
func TestLoginThrottleIsNotResetByForgedAddresses(t *testing.T) {
	e := newFullEnv(t)
	e.useFakeClock()

	guess := func(i int) int {
		// What a client behind a proxy can do: put anything it likes in front
		// of the entry the proxy appends.
		forged := fmt.Sprintf("10.9.%d.%d, 198.51.100.7", i/250, i%250)
		code, _ := e.loginFrom(forged, "admin", "wrong-guess-"+strconv.Itoa(i))
		return code
	}
	for i := 0; i < limiterFreeFailures; i++ {
		if code := guess(i); code != http.StatusForbidden {
			t.Fatalf("guess %d = %d, want 403", i, code)
		}
	}
	for i := limiterFreeFailures; i < limiterFreeFailures+20; i++ {
		if code := guess(i); code != http.StatusTooManyRequests {
			t.Fatalf("guess %d with a forged address = %d, want 429", i, code)
		}
	}
}

// The password of a share could be guessed without limit as well.
func TestSharePasswordGuessesAreSlowedDown(t *testing.T) {
	e := newFullEnv(t)
	clock := e.useFakeClock()
	e.writeBobFile("a.txt", "first")
	e.writeBobFile("b.txt", "second")
	tok := e.login("bob", fullEnvUserPassword)
	share := e.createShare(tok, "/a.txt", `{"password":"sharepw"}`)
	other := e.createShare(tok, "/b.txt", `{"password":"sharepw"}`)

	get := func(hash, password string) (int, http.Header, string) {
		return e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": password})
	}

	// Opening the share without a password is how its page asks whether one
	// is needed: that is not a guess.
	for i := 0; i < 3*limiterFreeFailures; i++ {
		if code, _, _ := e.do(http.MethodGet, "/api/public/share/"+share, "", nil, nil); code != http.StatusUnauthorized {
			t.Fatalf("request without a password = %d, want 401", code)
		}
	}

	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _, _ := get(share, "guess-"+strconv.Itoa(i)); code != http.StatusUnauthorized {
			t.Fatalf("wrong share password %d = %d, want 401", i, code)
		}
	}
	code, h, _ := get(share, "guess-6")
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" {
		t.Fatalf("guess after %d failures = %d (Retry-After %q), want 429", limiterFreeFailures, code, h.Get("Retry-After"))
	}
	if code, _, _ := get(share, "sharepw"); code != http.StatusTooManyRequests {
		t.Errorf("the right password while waiting = %d, want 429", code)
	}

	// Another share is not affected, and neither is someone who already has
	// the share's token.
	if code, _, body := get(other, "sharepw"); code != http.StatusOK || body != "second" {
		t.Errorf("another share = %d %q", code, body)
	}
	link, err := e.st.Share.GetByHash(share)
	if err != nil {
		t.Fatal(err)
	}
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	token := shareToken(set.Key, link, time.Now().Add(time.Hour))
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/"+share+"?token="+token, "", nil, nil); code != http.StatusOK || body != "first" {
		t.Errorf("download with the share token while the password waits = %d %q", code, body)
	}

	clock.Advance(limiterBaseDelay)
	if code, _, body := get(share, "sharepw"); code != http.StatusOK || body != "first" {
		t.Errorf("the right password after the wait = %d %q", code, body)
	}
}

// shortBodyTimeouts makes the body timeouts short for the length of a test.
func shortBodyTimeouts(t *testing.T) {
	t.Helper()
	body, leftover := requestBodyTimeout, leftoverBodyTimeout
	requestBodyTimeout, leftoverBodyTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { requestBodyTimeout, leftoverBodyTimeout = body, leftover })
}

// stalledRequest sends the head of a request that announces a body, sends the
// first few bytes of it, and then nothing. It returns the status line the
// server answers with, and whether the server went on to close the
// connection, both within the given time.
func stalledRequest(t *testing.T, e *fullEnv, head, firstBytes string, patience time.Duration) (status string, closed bool) {
	t.Helper()
	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(head + firstBytes)); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(patience))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", false
	}
	status = strings.TrimSpace(line)

	_, err = io.Copy(io.Discard, reader)
	return status, err == nil // nil means EOF: the server closed the connection
}

// Through the API: failed logins under throw-away user names do not push out
// the key of an account that is being guessed.
func TestLoginThrottleIsNotResetByAFlood(t *testing.T) {
	e := newFullEnv(t)
	clock := e.useFakeClock()
	e.limits.login.maxKeys = 30 // the production value is 10000; the same code runs

	for i := 0; i <= limiterFreeFailures; i++ {
		_, _, _ = e.tryLogin("admin", "wrong-"+strconv.Itoa(i))
	}
	code, h, _ := e.tryLogin("admin", "wrong-again")
	if code != http.StatusTooManyRequests {
		t.Fatalf("control: the admin key is not waiting: %d", code)
	}
	wait, _ := strconv.Atoi(h.Get("Retry-After"))

	// A flood of logins under names that do not exist, from the same address.
	for i := 0; i < 60; i++ {
		code, h, _ := e.tryLogin("nobody-"+strconv.Itoa(i), "x")
		if code == http.StatusTooManyRequests && h.Get("Retry-After") == "" {
			t.Fatal("a refused login carries no Retry-After")
		}
	}
	e.limits.login.mu.Lock()
	kept := len(e.limits.login.keys)
	e.limits.login.mu.Unlock()
	if kept > 30 {
		t.Errorf("the limiter holds %d keys after the flood, limit 30", kept)
	}

	if code, _, _ := e.tryLogin("admin", "wrong-after-flood"); code != http.StatusTooManyRequests {
		t.Errorf("guess at the waiting account right after the flood = %d, want 429: its key was dropped", code)
	}

	// Once the wait is over the next guess is checked, and the one after it
	// waits longer than before, not as if nothing had happened.
	clock.Advance(time.Duration(wait+1) * time.Second)
	if code, _, _ := e.tryLogin("admin", "wrong-later"); code != http.StatusForbidden {
		t.Fatalf("guess after the wait = %d, want 403", code)
	}
	code, h, _ = e.tryLogin("admin", "wrong-later-still")
	if next, _ := strconv.Atoi(h.Get("Retry-After")); code != http.StatusTooManyRequests || next < 2*wait {
		t.Errorf("guess after that = %d with Retry-After %d, want 429 and at least %d", code, next, 2*wait)
	}

	// The account itself is fine, and so is anyone at another address.
	clock.Advance(limiterMaxDelay)
	if code, _, _ := e.tryLogin("admin", fullEnvAdminPassword); code != http.StatusOK {
		t.Errorf("the right password after the wait = %d, want 200", code)
	}
}

// renew trades a token for a renewed one.
func (e *fullEnv) renew(token string) string {
	e.t.Helper()
	code, _, renewed := e.do(http.MethodPost, "/api/renew", token, nil, nil)
	if code != http.StatusOK {
		e.t.Fatalf("renewal = %d %q", code, renewed)
	}
	return renewed
}

// changeOwnPassword sends a password change for the user id with the given
// guess at the current password.
func (e *fullEnv) changeOwnPassword(token string, id uint, guess, addr string) (int, http.Header) {
	e.t.Helper()
	payload := `{"what":"user","which":["password"],"current_password":"` + guess + `","data":{"id":` +
		strconv.FormatUint(uint64(id), 10) + `,"password":"the-new-password-123"}}`
	var hdr map[string]string
	if addr != "" {
		hdr = map[string]string{"X-Forwarded-For": addr}
	}
	code, h, _ := e.do(http.MethodPut, "/api/users/"+strconv.FormatUint(uint64(id), 10), token, strings.NewReader(payload), hdr)
	return code, h
}

// The users API confirms the current password before it changes anything
// that matters, and answered every wrong one as fast as the hash can be
// computed. Whoever holds a session token (a stolen one) could guess the
// password of the account that way, past the limiter on the login, and then
// change it. The guesses are now counted per account.
func TestCurrentPasswordGuessesAreSlowedDown(t *testing.T) {
	e := newFullEnv(t)
	clock := e.useFakeClock()
	const attacker, elsewhere = "198.51.100.7", "203.0.113.9"

	// The token an attacker holds: renewed since the login, as a token is
	// after at most two hours.
	stolen := e.renew(e.login("bob", fullEnvUserPassword))

	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.changeOwnPassword(stolen, 2, "wrong-guess-"+strconv.Itoa(i), attacker); code != http.StatusBadRequest {
			t.Fatalf("wrong password %d = %d, want 400", i, code)
		}
	}

	// The next guess has to wait, and so does the right password.
	code, h := e.changeOwnPassword(stolen, 2, "wrong-guess-6", attacker)
	if code != http.StatusTooManyRequests {
		t.Fatalf("guess after %d failures = %d, want 429", limiterFreeFailures, code)
	}
	if retry, err := strconv.Atoi(h.Get("Retry-After")); err != nil || retry < 1 {
		t.Errorf("Retry-After = %q, want the seconds to wait", h.Get("Retry-After"))
	}
	if code, _ := e.changeOwnPassword(stolen, 2, fullEnvUserPassword, attacker); code != http.StatusTooManyRequests {
		t.Errorf("the right password while waiting = %d, want 429", code)
	}

	// Neither another address nor another renewal of the token gets around
	// it: the count belongs to the account.
	if code, _ := e.changeOwnPassword(stolen, 2, "wrong-guess-7", elsewhere); code != http.StatusTooManyRequests {
		t.Errorf("a guess from another address = %d, want 429", code)
	}
	time.Sleep(1100 * time.Millisecond) // a renewal in the same second is the same token
	if code, _ := e.changeOwnPassword(e.renew(stolen), 2, "wrong-guess-8", elsewhere); code != http.StatusTooManyRequests {
		t.Errorf("a guess with the token renewed once more = %d, want 429", code)
	}

	// The other routes that confirm the password count on the same account.
	if code, _, _ := e.do(http.MethodDelete, "/api/users/2", stolen, strings.NewReader(`{"current_password":"wrong-guess-9"}`), nil); code != http.StatusTooManyRequests {
		t.Errorf("a guess through the deletion of the account = %d, want 429", code)
	}
	if u := e.user(2); u == nil || !users.CheckPwd(fullEnvUserPassword, u.Password) {
		t.Fatal("the account was changed by a guess")
	}

	// Other accounts are not affected.
	admin := e.login("admin", fullEnvAdminPassword)
	if code, body := e.putUser(admin, 2, `{"what":"user","which":["locale"],"current_password":"`+fullEnvAdminPassword+`","data":{"id":2,"locale":"fr"}}`); code != http.StatusOK {
		t.Errorf("an administrator confirming their own password = %d %q", code, body)
	}

	// The wait doubles with every further failure.
	clock.Advance(limiterBaseDelay + time.Second)
	if code, _ := e.changeOwnPassword(stolen, 2, "wrong-guess-10", attacker); code != http.StatusBadRequest {
		t.Errorf("a guess after the wait = %d, want 400", code)
	}
	clock.Advance(limiterBaseDelay + time.Second/2)
	if code, _ := e.changeOwnPassword(stolen, 2, "wrong-guess-11", attacker); code != http.StatusTooManyRequests {
		t.Errorf("a guess after the first wait only = %d, want 429", code)
	}

	// The user is not locked out by this: a session that comes straight
	// from a login with the password is counted on its own, and such a
	// session is what the attacker cannot get. The user logs in again and
	// changes the password, which ends the stolen session.
	own := e.login("bob", fullEnvUserPassword)
	code, h = e.changeOwnPassword(own, 2, fullEnvUserPassword, elsewhere)
	if code != http.StatusOK || h.Get("X-Auth-Token") == "" {
		t.Fatalf("the user's own password change from a fresh login = %d, want 200 and a new token", code)
	}
	if e.works(stolen) {
		t.Error("the stolen session survived the password change")
	}
}

// A session straight from a login is counted on its own, but not without
// limit, and not for longer than the token it came with: a renewal puts it
// back on the count of the account.
func TestCurrentPasswordGuessesWithALoginToken(t *testing.T) {
	e := newFullEnv(t)
	e.useFakeClock()

	fresh := e.login("bob", fullEnvUserPassword)
	var claims authToken
	if _, _, err := jwt.NewParser().ParseUnverified(fresh, &claims); err != nil || !claims.PasswordLogin {
		t.Fatalf("a token from a login is not marked as one: %+v, %v", claims, err)
	}
	renewed := e.renew(fresh)
	claims = authToken{}
	if _, _, err := jwt.NewParser().ParseUnverified(renewed, &claims); err != nil || claims.PasswordLogin {
		t.Fatalf("a renewed token is marked as coming from a login: %+v, %v", claims, err)
	}

	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.changeOwnPassword(fresh, 2, "wrong-guess-"+strconv.Itoa(i), ""); code != http.StatusBadRequest {
			t.Fatalf("wrong password %d = %d, want 400", i, code)
		}
	}
	if code, _ := e.changeOwnPassword(fresh, 2, "wrong-guess-6", ""); code != http.StatusTooManyRequests {
		t.Errorf("guess %d with a login token = %d, want 429", limiterFreeFailures+1, code)
	}

	// The renewed token is on the count of the account, which is untouched
	// so far, and which every further renewal shares.
	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.changeOwnPassword(renewed, 2, "other-guess-"+strconv.Itoa(i), ""); code != http.StatusBadRequest {
			t.Fatalf("wrong password %d with the renewed token = %d, want 400", i, code)
		}
	}
	time.Sleep(1100 * time.Millisecond)
	for _, token := range []string{renewed, e.renew(renewed), e.renew(fresh)} {
		if code, _ := e.changeOwnPassword(token, 2, "other-guess-6", ""); code != http.StatusTooManyRequests {
			t.Errorf("a further guess with a renewed token = %d, want 429", code)
		}
	}

	// A request without a password is not a guess: it is refused without
	// being counted, and without computing a hash.
	admin := e.login("admin", fullEnvAdminPassword)
	for i := 0; i < 3*limiterFreeFailures; i++ {
		if code, _ := e.changeOwnPassword(admin, 1, "", ""); code != http.StatusBadRequest {
			t.Fatalf("request %d without a password = %d, want 400", i, code)
		}
	}
	if code, _ := e.changeOwnPassword(admin, 1, fullEnvAdminPassword, ""); code != http.StatusOK {
		t.Errorf("the right password after requests without one = %d, want 200", code)
	}
}

// Only the request header had a time limit. A client could send the first
// byte of a login and then keep the connection, and the goroutine serving it,
// for as long as it liked, without ever authenticating.
func TestStalledRequestBodiesAreCutOff(t *testing.T) {
	shortBodyTimeouts(t)
	e := newFullEnv(t)
	e.writeBobFile("f.txt", "shared")
	share := e.createShare(e.login("bob", fullEnvUserPassword), "/f.txt", "{}")

	requests := map[string]string{
		"login":                             "POST /api/login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n",
		"signup":                            "POST /api/signup HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n",
		"public share with a body":          "GET /api/public/share/" + share + " HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"public download with a body":       "GET /api/public/dl/" + share + " HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"application page with a body":      "GET / HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"health check with a body":          "GET /health HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"upload without a session":          "POST /api/resources/x.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"chunk without a session":           "PATCH /api/tus/x.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"user update without a session":     "PUT /api/users/1 HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"method that has no route":          "OPTIONS /api/resources/x HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"wrong method for a route":          "POST /api/shares HTTP/1.1\r\nHost: x\r\nContent-Length: 64\r\n\r\n",
		"chunked body that never continues": "POST /api/login HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n",
	}
	for name, head := range requests {
		t.Run(name, func(t *testing.T) {
			first := `{"u`
			if strings.Contains(head, "chunked") {
				first = "3\r\n" + first + "\r\n"
			}
			start := time.Now()
			status, closed := stalledRequest(t, e, head, first, 5*time.Second)
			if status == "" {
				t.Fatalf("no answer within %s", time.Since(start).Round(time.Millisecond))
			}
			if !closed {
				t.Errorf("answered %q but kept the stalled connection open", status)
			}
			if strings.Contains(status, " 200 ") && (name == "login" || name == "signup") {
				t.Errorf("answered %q to a request whose body never arrived", status)
			}
		})
	}
}

// The limits on request bodies must not reach what takes long for good
// reasons: a download slower than the limit, and an upload slower than it.
func TestSlowTransfersAreNotCutOff(t *testing.T) {
	shortBodyTimeouts(t)
	e := newFullEnv(t)
	payload := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB
	e.writeBobFile("big.bin", payload)
	tok := e.login("bob", fullEnvUserPassword)
	share := e.createShare(tok, "/big.bin", "{}")

	t.Run("download read slowly", func(t *testing.T) {
		for _, target := range []struct{ path, token string }{{"/api/public/dl/" + share, ""}, {"/api/raw/big.bin", tok}, {"/api/raw/?algo=tar", tok}} {
			req, _ := http.NewRequest(http.MethodGet, e.srv.URL+target.path, http.NoBody)
			if target.token != "" {
				req.Header.Set("X-Auth", target.token)
			}
			res, err := e.srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var got int
			buf := make([]byte, 64<<10)
			for {
				n, err := res.Body.Read(buf)
				got += n
				if err != nil {
					break
				}
				time.Sleep(60 * time.Millisecond) // about a second in all
			}
			res.Body.Close()
			if got < len(payload) {
				t.Errorf("%s: received %d of %d bytes", target.path, got, len(payload))
			}
		}
	})

	t.Run("upload sent slowly", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			pr, pw := io.Pipe()
			go func() {
				for i := 0; i < 10; i++ {
					_, _ = pw.Write([]byte("0123456789"))
					time.Sleep(100 * time.Millisecond) // a second in all
				}
				pw.Close()
			}()
			code, _, body := e.do(method, "/api/resources/slow.bin?override=true", tok, pr, nil)
			if code != http.StatusOK {
				t.Fatalf("slow %s = %d %q", method, code, body)
			}
			if data, _ := os.ReadFile(filepath.Join(e.bobDir, "slow.bin")); len(data) != 100 {
				t.Errorf("slow %s stored %d bytes, want 100", method, len(data))
			}
		}
	})

	t.Run("chunk sent slowly", func(t *testing.T) {
		code, h, _ := e.do(http.MethodPost, "/api/tus/slow-tus.bin", tok, nil, map[string]string{"Upload-Length": "100"})
		if code != http.StatusCreated {
			t.Fatalf("create = %d", code)
		}
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 10; i++ {
				_, _ = pw.Write([]byte("0123456789"))
				time.Sleep(100 * time.Millisecond)
			}
			pw.Close()
		}()
		code, _, body := e.do(http.MethodPatch, h.Get("Location"), tok, pr, map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
		if code != http.StatusNoContent {
			t.Fatalf("slow PATCH = %d %q", code, body)
		}
	})

	t.Run("login with its body", func(t *testing.T) {
		if code, _, _ := e.tryLogin("bob", fullEnvUserPassword); code != http.StatusOK {
			t.Errorf("login = %d", code)
		}
	})
}
