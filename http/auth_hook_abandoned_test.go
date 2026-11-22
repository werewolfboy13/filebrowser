package fbhttp

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A hook is stopped when the client of its login goes away, and the login of
// a hook that was stopped was not counted as a failed attempt: the hook had
// said nothing about the password. But a client chooses which of its logins
// it gives up on. Against a hook that takes longer to refuse than to accept
// (one that sleeps after a wrong password, or that asks a service which
// does), a client that closed its connection a moment after the right
// password would have been answered had every wrong guess stopped, and none
// counted, while the right password still got its token. Guessing a password
// that way was never slowed down.
//
// The clients of these tests give up on a login by closing its connection
// without having read an answer. They do it at a moment the test picks out
// for them (the hook notes what it is doing in a directory), which stands
// for the fixed time such a client would wait and does not depend on how
// fast the machine is.

// impatientLogin sends a login over a connection of its own and waits for
// the answer, until giveUp says to give up: the connection is then closed,
// and the code is 0.
func (e *fullEnv) impatientLogin(addr, user, password string, giveUp func() bool) (code int, body string) {
	e.t.Helper()
	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	login := `{"username":"` + user + `","password":"` + password + `"}`
	if _, err := io.WriteString(conn, "POST /api/login HTTP/1.1\r\nHost: filebrowser.test\r\nContent-Type: application/json\r\nX-Forwarded-For: "+addr+
		"\r\nContent-Length: "+strconv.Itoa(len(login))+"\r\n\r\n"+login); err != nil {
		e.t.Fatal(err)
	}

	type answer struct {
		code int
		body []byte
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			answers <- answer{err: err}
			return
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		answers <- answer{res.StatusCode, b, err}
	}()

	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case a := <-answers:
			if a.err != nil {
				e.t.Fatalf("no answer to the login: %v", a.err)
			}
			return a.code, string(a.body)
		case <-poll.C:
			if giveUp() {
				return 0, ""
			}
		}
	}
}

// loginsSettled waits until the server is done with every login that it has
// begun: no attempt is in flight in the limiter, and no turn of addr is
// taken. A login is only waited for once the server has begun it, which the
// caller has to know (its hook was running, say).
func (e *fullEnv) loginsSettled(addr string) {
	e.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		inFlight := 0
		e.limits.login.mu.Lock()
		for _, state := range e.limits.login.keys {
			inFlight += state.inFlight
		}
		e.limits.login.mu.Unlock()
		if inFlight == 0 && e.limits.loginWork.atWork(addr) == 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the logins of %s are still under way", addr)
		}
	}
}

// loginFailures is the number of failed attempts the limiter has counted for
// the logins of user from addr.
func (e *fullEnv) loginFailures(addr, user string) int {
	e.limits.login.mu.Lock()
	defer e.limits.login.mu.Unlock()
	if state := e.limits.login.keys[limiterKey("login", addr, user)]; state != nil {
		return state.failures
	}
	return 0
}

// slowRefusalHook is a hook that accepts bob with his password at once, and
// refuses every other login after it has slept for the given time (as
// sleep(1) takes it). It notes in dir that it is about to (refusing-*) and,
// when it was let sleep to the end, that it has refused (refused-*). What it
// sleeps in does not hold the output of the hook.
type slowRefusalHook struct{ dir string }

func (e *fullEnv) useSlowRefusalHook(sleep string) *slowRefusalHook {
	e.t.Helper()
	h := &slowRefusalHook{dir: e.t.TempDir()}
	e.useHookAuth(`if [ "$USERNAME" = bob ] && [ "$PASSWORD" = '` + fullEnvUserPassword + `' ]; then
  echo hook.action=auth
  exit 0
fi
: > '` + h.dir + `'/refusing-$$
sleep ` + sleep + ` >/dev/null 2>&1
: > '` + h.dir + `'/refused-$$
exit 1
`)
	return h
}

func (h *slowRefusalHook) noted(what string) int {
	noted, _ := filepath.Glob(filepath.Join(h.dir, what+"-*"))
	return len(noted)
}

// aboutToRefuse returns what a client gives up on: the next login whose hook
// has not accepted it at once, and is sleeping before it refuses.
func (h *slowRefusalHook) aboutToRefuse() func() bool {
	before := h.noted("refusing")
	return func() bool { return h.noted("refusing") > before }
}

// The control: wrong guesses whose answer the client waits for are counted,
// however long the hook takes to refuse them.
func TestSlowRefusalsOfAHookThatAreWaitedForAreCounted(t *testing.T) {
	e := newFullEnv(t)
	e.useFakeClock()
	hook := e.useSlowRefusalHook("0.3")

	const addr = "198.51.100.71"
	for i := 1; i <= limiterFreeFailures; i++ {
		if code, _ := e.loginFrom(addr, "bob", "wrong-guess-"+strconv.Itoa(i)); code != http.StatusInternalServerError {
			t.Fatalf("wrong guess %d = %d, want 500", i, code)
		}
	}
	if n := hook.noted("refused"); n != limiterFreeFailures {
		t.Errorf("%d hooks refused by themselves, want %d", n, limiterFreeFailures)
	}
	if n := e.loginFailures(addr, "bob"); n != limiterFreeFailures {
		t.Errorf("the limiter counted %d of %d wrong guesses that were waited for", n, limiterFreeFailures)
	}
	if code, h := e.loginFrom(addr, "bob", "wrong-guess-more"); code != http.StatusTooManyRequests {
		t.Errorf("guess %d = %d (Retry-After %q), want 429", limiterFreeFailures+1, code, h.Get("Retry-After"))
	}
}

// The same guesses, by a client that gives up on each as soon as its hook
// has not accepted it: they are counted like the refusals they were about to
// be, and the client is turned away after as many of them. The right
// password, sent the same way, logs in.
func TestWrongGuessesAClientGivesUpOnBeforeASlowHookRefusesAreCounted(t *testing.T) {
	e := newFullEnv(t)
	clock := e.useFakeClock()
	// Long enough that no hook gets to refuse by itself before its client
	// is gone. None is waited for.
	hook := e.useSlowRefusalHook("20")

	const addr = "198.51.100.72"
	for i := 1; i <= limiterFreeFailures; i++ {
		code, body := e.impatientLogin(addr, "bob", "wrong-guess-"+strconv.Itoa(i), hook.aboutToRefuse())
		if code != 0 {
			t.Fatalf("wrong guess %d = %d %q, want no answer before its client gave up", i, code, body)
		}
		e.loginsSettled(addr)
	}
	if n := hook.noted("refused"); n != 0 {
		t.Fatalf("%d hooks refused by themselves: their clients did not give up on them in time", n)
	}
	if n := e.loginFailures(addr, "bob"); n != limiterFreeFailures {
		t.Errorf("the limiter counted %d of %d wrong guesses whose client gave up before the hook refused them", n, limiterFreeFailures)
	}

	code, body := e.impatientLogin(addr, "bob", "wrong-guess-more", hook.aboutToRefuse())
	if code != http.StatusTooManyRequests {
		t.Errorf("guess %d, after %d that were given up on = %d %q, want 429", limiterFreeFailures+1, limiterFreeFailures, code, body)
	}
	e.loginsSettled(addr)

	// Once the wait is over, the right password logs in, sent the way the
	// guesses were: its hook answers before the client would give up.
	clock.Advance(limiterBaseDelay)
	code, token := e.impatientLogin(addr, "bob", fullEnvUserPassword, hook.aboutToRefuse())
	if code != http.StatusOK || !e.works(token) {
		t.Errorf("the right password, sent the same way = %d %q, want a token that works", code, token)
	}
}

// With the passwords File Browser checks itself there is nothing to stop
// when the client goes away: the hash is computed to the end, and a wrong
// password is counted whether or not anyone is left to be told.
func TestWrongPasswordsAClientGivesUpOnWhileTheyAreHashedAreCounted(t *testing.T) {
	e := newFullEnv(t)
	e.useFakeClock()

	const addr = "198.51.100.74"
	counted := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); e.loginFailures(addr, "bob") != want; time.Sleep(2 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the limiter counted %d wrong passwords whose client gave up, want %d", e.loginFailures(addr, "bob"), want)
			}
		}
	}
	for i := 1; i <= limiterFreeFailures; i++ {
		// Given up on at once: before the hash, or in the middle of it.
		code, body := e.impatientLogin(addr, "bob", "wrong-guess-"+strconv.Itoa(i), func() bool { return true })
		if code != 0 {
			t.Fatalf("wrong password %d = %d %q, want no answer before its client gave up", i, code, body)
		}
		counted(i)
	}
	if code, h := e.loginFrom(addr, "bob", fullEnvUserPassword); code != http.StatusTooManyRequests {
		t.Errorf("login after %d wrong passwords whose client gave up = %d (Retry-After %q), want 429", limiterFreeFailures, code, h.Get("Retry-After"))
	}
}

// A request that was over before its hook was started has not run it: no
// password was put to any test, and nothing is counted.
func TestLoginsThatWereOverBeforeTheirHookWasStartedAreNotCounted(t *testing.T) {
	e := newFullEnv(t)
	ran := filepath.Join(t.TempDir(), "ran")
	e.useHookAuth(": > '" + ran + "'\nexit 1\n")

	handler := e.srv.Config.Handler
	for i := 1; i <= 3*limiterFreeFailures; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the client is gone already
		req := httptest.NewRequest(http.MethodPost, "/api/login",
			strings.NewReader(`{"username":"bob","password":"wrong-guess-`+strconv.Itoa(i)+`"}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("login %d of a request that was over = %d, want 500", i, rec.Code)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the hook of a request that was over was run")
	}
	e.limits.login.mu.Lock()
	kept := len(e.limits.login.keys)
	e.limits.login.mu.Unlock()
	if kept != 0 {
		t.Errorf("the limiter kept %d keys of logins whose hook was never run", kept)
	}
}
