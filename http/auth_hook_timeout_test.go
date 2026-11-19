package fbhttp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/werewolfboy13/filebrowser/v3/auth"
	"github.com/werewolfboy13/filebrowser/v3/settings"
)

// A login holds one of the few turns of its client address while its hook
// runs (see workGate), and the hook was run without a time limit and without
// regard for the request. A hook that never answered, because the server it
// asks took the connection and went silent, held the turn for good: after
// four such logins the address was turned away with 429 until the server was
// restarted, also once the hook would have answered again, and also when the
// clients had long gone.
//
// The hooks of these tests hang while a file exists, as if on such a server,
// and note that they run. All of them are let go when the test ends.

// hookTimeouts hands out the hook auther with the time limit of a test.
type hookTimeouts struct {
	auth.StorageBackend
	timeout time.Duration
}

func (b hookTimeouts) Get(method settings.AuthMethod) (auth.Auther, error) {
	auther, err := b.StorageBackend.Get(method)
	if hook, ok := auther.(*auth.HookAuth); ok {
		hook.Timeout = b.timeout
	}
	return auther, err
}

// hangingHook switches the instance to a hook that accepts bob with his
// password, and that hangs before it answers for as long as the upstream it
// stands for is down. timeout is the time the hook is given; zero leaves it
// at what it is in production.
type hangingHook struct {
	e   *fullEnv
	dir string
}

func (e *fullEnv) useHangingHook(timeout time.Duration) *hangingHook {
	e.t.Helper()
	h := &hangingHook{e: e, dir: e.t.TempDir()}
	if err := os.WriteFile(filepath.Join(h.dir, "down"), nil, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if timeout != 0 {
		e.st.Auth = auth.NewStorage(hookTimeouts{e.st.Auth, timeout}, nil)
	}
	e.useHookAuth(`if [ -e '` + h.dir + `/down' ]; then
  : > '` + h.dir + `'/running-$$
  while [ ! -e '` + h.dir + `/release' ]; do sleep 0.05; done
fi
if [ "$USERNAME" = bob ] && [ "$PASSWORD" = '` + fullEnvUserPassword + `' ]; then
  echo hook.action=auth
else
  echo hook.action=block
fi
`)
	return h
}

// release lets go of every hook that hangs. The server waits for its
// requests when it is closed, so every test ends with it.
func (h *hangingHook) release() {
	_ = os.WriteFile(filepath.Join(h.dir, "release"), nil, 0o600)
}

// recover is the upstream answering again: hooks started from now on do not
// hang. Those that already do, still do.
func (h *hangingHook) recover() {
	h.e.t.Helper()
	if err := os.Remove(filepath.Join(h.dir, "down")); err != nil {
		h.e.t.Fatal(err)
	}
}

// waitForHung waits until n hooks have been started that hang.
func (h *hangingHook) waitForHung(n int) {
	h.e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		running, _ := filepath.Glob(filepath.Join(h.dir, "running-*"))
		if len(running) >= n {
			return
		}
		if time.Now().After(deadline) {
			h.e.t.Fatalf("%d hooks were started, want %d", len(running), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// atWork is the number of turns that are taken in group.
func (g *workGate) atWork(group string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr := g.groups[group]; gr != nil {
		return len(gr.slots)
	}
	return 0
}

func TestHungHooksDoNotKeepAnAddressOutOfTheLogin(t *testing.T) {
	e := newFullEnv(t)
	hook := e.useHangingHook(500 * time.Millisecond)
	defer hook.release()

	// As many logins as the address has turns, each with a hook that hangs.
	const addr = "198.51.100.20"
	answers := make(chan int, loginWorkPerAddress)
	for i := 0; i < loginWorkPerAddress; i++ {
		go func() {
			code, _ := e.loginFrom(addr, "bob", fullEnvUserPassword)
			answers <- code
		}()
	}
	hook.waitForHung(loginWorkPerAddress)
	hook.recover()

	// Their hooks are stopped when their time is over, and the logins are
	// answered: the server could not find out, which is its error.
	for i := 0; i < loginWorkPerAddress; i++ {
		select {
		case code := <-answers:
			if code != http.StatusInternalServerError {
				t.Errorf("a login whose hook was stopped = %d, want 500", code)
			}
		case <-time.After(15 * time.Second):
			t.Errorf("%d of the %d logins are still waiting for a hook that had 500ms to answer", loginWorkPerAddress-i, loginWorkPerAddress)
			i = loginWorkPerAddress
		}
	}

	// The turns are free again, and a login from the address is served. It
	// does not wait long for its turn here: there is nothing to wait for.
	e.limits.loginWork.setWait(300 * time.Millisecond)
	if code, h := e.loginFrom(addr, "bob", fullEnvUserPassword); code != http.StatusOK {
		t.Errorf("a login from the address whose hooks hung, after their time was over = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
	}

	hook.release()
	if n := e.limits.loginWork.atWork(addr); n != 0 {
		t.Errorf("%d turns of the address are still taken", n)
	}
}

func TestLoginWhoseClientWentAwayGivesItsTurnBack(t *testing.T) {
	e := newFullEnv(t)
	// With the time a hook has in production: it is the clients going away
	// that ends these.
	hook := e.useHangingHook(0)
	defer hook.release()

	const addr = "198.51.100.30"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gone := make(chan struct{}, loginWorkPerAddress)
	for i := 0; i < loginWorkPerAddress; i++ {
		go func() {
			defer func() { gone <- struct{}{} }()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+"/api/login",
				strings.NewReader(`{"username":"bob","password":"`+fullEnvUserPassword+`"}`))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("X-Forwarded-For", addr)
			if res, err := e.srv.Client().Do(req); err == nil {
				res.Body.Close()
				t.Errorf("a login whose hook hangs was answered: %d", res.StatusCode)
			}
		}()
	}
	hook.waitForHung(loginWorkPerAddress)
	if n := e.limits.loginWork.atWork(addr); n != loginWorkPerAddress {
		t.Fatalf("%d turns of the address are taken while its hooks hang, want %d", n, loginWorkPerAddress)
	}

	cancel() // the clients give up, and close their connections
	for i := 0; i < loginWorkPerAddress; i++ {
		<-gone
	}
	for deadline := time.Now().Add(15 * time.Second); e.limits.loginWork.atWork(addr) != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Errorf("%d turns of the address are still taken after its clients went away", e.limits.loginWork.atWork(addr))
			break
		}
	}

	hook.recover()
	e.limits.loginWork.setWait(300 * time.Millisecond)
	if code, h := e.loginFrom(addr, "bob", fullEnvUserPassword); code != http.StatusOK {
		t.Errorf("a login from the address after its clients went away = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
	}
}

// A hook that was stopped said nothing about the password. Its login is not
// a failed guess, and the user does not have to wait, once the hook answers
// again, for the logins that failed because it did not.
func TestLoginsWhoseHookWasStoppedAreNotCountedAsWrongPasswords(t *testing.T) {
	e := newFullEnv(t)
	hook := e.useHangingHook(100 * time.Millisecond)
	defer hook.release()

	const addr = "198.51.100.40"
	for i := 0; i < limiterFreeFailures+2; i++ {
		answer := make(chan int, 1)
		go func() {
			code, _ := e.loginFrom(addr, "bob", fullEnvUserPassword)
			answer <- code
		}()
		select {
		case code := <-answer:
			if code != http.StatusInternalServerError {
				t.Fatalf("login %d with a hook that does not answer = %d, want 500", i+1, code)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("login %d is still waiting for a hook that had 100ms to answer", i+1)
		}
	}

	e.limits.login.mu.Lock()
	state := e.limits.login.keys[limiterKey("login", addr, "bob")]
	e.limits.login.mu.Unlock()
	if state != nil {
		t.Errorf("the limiter kept something of logins that were never checked: %+v", state)
	}

	hook.recover()
	if code, h := e.loginFrom(addr, "bob", fullEnvUserPassword); code != http.StatusOK {
		t.Errorf("login once the hook answers again = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
	}
}

// A hook that takes a while, well within its time, is waited for as before.
func TestHookThatTakesAWhileStillLogsIn(t *testing.T) {
	e := newFullEnv(t)
	e.useHookAuth(`sleep 0.3
if [ "$USERNAME" = bob ] && [ "$PASSWORD" = '` + fullEnvUserPassword + `' ]; then
  echo hook.action=auth
else
  echo hook.action=block
fi
`)
	if tok := e.login("bob", fullEnvUserPassword); !e.works(tok) {
		t.Error("the token of a login whose hook took a while does not work")
	}
	if code, _, _ := e.tryLogin("bob", "not-the-password"); code != http.StatusForbidden {
		t.Errorf("a login the hook blocks after a while = %d, want 403", code)
	}
}
