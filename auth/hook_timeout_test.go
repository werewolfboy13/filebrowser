package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// A hook that never answers was waited for without end, by a login that
// holds one of the few turns of its client address. The tests below run such
// hooks. Each waits on a file, so that a hook the code under test fails to
// stop ends with the test all the same.

// hungHook returns the path of a hook script, and the function that lets
// every hook started from it go. waiting is what the script does until then;
// it is given the path of the file to wait for as $release.
func hungHook(t *testing.T, waiting string) (script string, release func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the hook")
	}
	file := filepath.Join(t.TempDir(), "release")
	release = func() { _ = os.WriteFile(file, nil, 0o600) }
	t.Cleanup(release)
	return writeHookScript(t, "release='"+file+"'\n"+waiting), release
}

// within runs fn and fails the test when it has not returned in time: the
// test is over then, and does not wait for what it was to show does not
// hang.
func within(t *testing.T, limit time.Duration, what string, fn func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s had not returned after %v", what, limit)
	}
	return time.Since(start)
}

func hookLogin(ctx context.Context, t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/login", strings.NewReader(`{"username":"bob","password":"whatever-it-is"}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stopped checks the error of a login whose hook was stopped: it is an
// error, it says why, and it is neither a wrong password nor a refusal by
// the hook, which the login counts as failed guesses.
func stopped(t *testing.T, err, cause error) {
	t.Helper()
	var exit *exec.ExitError
	switch {
	case err == nil:
		t.Error("a login whose hook was stopped was let in")
	case errors.Is(err, ErrHookRefused), errors.Is(err, os.ErrPermission), errors.As(err, &exit):
		t.Errorf("a hook that was stopped is reported as one that refused the login: %v", err)
	case !errors.Is(err, cause):
		t.Errorf("the error of a hook that was stopped is %q, want it to say %q", err, cause)
	}
}

func TestHookThatDoesNotAnswerIsStoppedAfterItsTime(t *testing.T) {
	script, _ := hungHook(t, `while [ ! -e "$release" ]; do sleep 0.05; done
echo hook.action=auth
`)
	a := &HookAuth{Command: script, Timeout: 200 * time.Millisecond}

	var err error
	took := within(t, 20*time.Second, "the login", func() {
		_, err = a.Auth(hookLogin(context.Background(), t), &mockUserStore{users: map[string]*users.User{}}, &settings.Settings{}, &settings.Server{Root: t.TempDir()})
	})
	stopped(t, err, context.DeadlineExceeded)
	if took < a.Timeout {
		t.Errorf("the hook was stopped after %v, before its %v were over", took, a.Timeout)
	}
}

func TestHookOfARequestThatIsOverIsStopped(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	script, _ := hungHook(t, `: > '`+started+`'
while [ ! -e "$release" ]; do sleep 0.05; done
echo hook.action=auth
`)
	// With the time a hook is given by default: it is the end of the
	// request that stops this one.
	a := &HookAuth{Command: script}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				break
			}
		}
		cancel() // the client went away
	}()

	var err error
	within(t, 20*time.Second, "the login", func() {
		_, err = a.Auth(hookLogin(ctx, t), &mockUserStore{users: map[string]*users.User{}}, &settings.Settings{}, &settings.Server{Root: t.TempDir()})
	})
	if _, statErr := os.Stat(started); statErr != nil {
		t.Fatal("the hook was not running when the request ended")
	}
	stopped(t, err, context.Canceled)
}

// Stopping the hook stops the command that was started, and not what it
// started in turn. A process the hook leaves behind keeps the output of the
// hook open, and the login waited for that process as if it were the hook.
func TestHookIsNotWaitedForLongerThanItsChildren(t *testing.T) {
	delay := hookWaitDelay
	hookWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { hookWaitDelay = delay })

	// The process that is left behind, in both cases: it has the output of
	// the hook, and goes on until the test lets it go.
	const child = `( while [ ! -e "$release" ]; do sleep 0.05; done ) &
`

	t.Run("a hook that hangs", func(t *testing.T) {
		script, _ := hungHook(t, child+"wait\necho hook.action=auth\n")
		a := &HookAuth{Command: script, Timeout: 200 * time.Millisecond}

		var err error
		within(t, 20*time.Second, "the hook", func() { _, err = a.RunCommand(context.Background()) })
		stopped(t, err, context.DeadlineExceeded)
	})

	// The hook has answered and ended by itself: its answer stands.
	t.Run("a hook that answers", func(t *testing.T) {
		script, _ := hungHook(t, child+"echo hook.action=block\n")
		a := &HookAuth{Command: script}

		var (
			action string
			err    error
		)
		within(t, 20*time.Second, "the hook", func() { action, err = a.RunCommand(context.Background()) })
		if err != nil || action != "block" {
			t.Errorf("RunCommand = %q, %v; want the answer of the hook, block", action, err)
		}
	})
}

// A hook that takes its time, within the time it has, and one that refuses
// by ending with an error status: neither is stopped, and both are what they
// were.
func TestHookThatAnswersInTimeIsNotStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the hook")
	}
	slow := &HookAuth{Command: writeHookScript(t, "sleep 0.3\necho hook.action=block\n"), Timeout: 20 * time.Second}
	if _, err := slow.Auth(hookLogin(context.Background(), t), &mockUserStore{}, &settings.Settings{}, &settings.Server{}); !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrNoAttempt) {
		t.Errorf("a hook that blocks after a while: %v, want a wrong password", err)
	}

	failing := &HookAuth{Command: writeHookScript(t, "exit 3\n"), Timeout: 20 * time.Second}
	if _, err := failing.Auth(hookLogin(context.Background(), t), &mockUserStore{}, &settings.Settings{}, &settings.Server{}); !errors.Is(err, ErrHookRefused) {
		t.Errorf("a hook that ends with an error status: %v, want ErrHookRefused", err)
	}
}

// A hook that had refused the login by itself was taken for one that was
// stopped when its request ended before RunCommand returned: the hook has
// ended with an error status, a process it left behind still has its output,
// and the client goes away while that output is waited for. The login of a
// stopped hook is not counted as a failed guess, and the right password was
// let in all the same, so a client that left at that moment could guess
// without ever being slowed down.
func TestRefusalAHookMadeBeforeItsClientWentAwayIsCounted(t *testing.T) {
	login := func(password string) (*users.User, error) {
		// The hook decides by its exit status, and notes that it has, just
		// before it ends. The process it leaves behind (a call sent off in
		// the background, say) has its output until the test lets it go.
		answered := filepath.Join(t.TempDir(), "answered")
		script, release := hungHook(t, `( while [ ! -e "$release" ]; do sleep 0.05; done ) &
if [ "$PASSWORD" = 'the-right-password' ]; then
  echo hook.action=auth
  : > '`+answered+`'
  exit 0
fi
: > '`+answered+`'
exit 1
`)
		a := &HookAuth{Command: script}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if _, err := os.Stat(answered); err == nil {
					break
				}
			}
			time.Sleep(300 * time.Millisecond) // the hook has ended
			cancel()                           // the client went away
			release()
		}()

		r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/login", strings.NewReader(`{"username":"bob","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var u *users.User
		within(t, 40*time.Second, "the login", func() {
			u, err = a.Auth(r, &mockUserStore{users: map[string]*users.User{}}, &settings.Settings{MinimumPasswordLength: 1}, &settings.Server{Root: t.TempDir()})
		})
		if ctx.Err() == nil {
			t.Fatal("the request had not ended when the login returned")
		}
		return u, err
	}

	if u, err := login("a-wrong-guess"); u != nil || !errors.Is(err, ErrHookRefused) {
		t.Errorf("a login the hook refused before its client went away: user %v, error %v; want ErrHookRefused, which is counted", u != nil, err)
	}
	if u, err := login("the-right-password"); u == nil || err != nil {
		t.Errorf("a login the hook accepted before its client went away: user %v, error %v; want the user", u != nil, err)
	}
}

// The captcha service was asked without a time limit, and without regard
// for the request: a service that took the connection and never answered
// held the login, and its turn, for good.
func TestCaptchaServiceThatDoesNotAnswerIsNotWaitedFor(t *testing.T) {
	release := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer silent.Close()
	defer close(release)

	captcha := &ReCaptcha{Host: silent.URL, Secret: "secret"}
	auther := JSONAuth{ReCaptcha: captcha}
	login := func(ctx context.Context) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"whatever-it-is","recaptcha":"answer"}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = auther.Auth(r, &mockUserStore{users: map[string]*users.User{}}, &settings.Settings{}, &settings.Server{})
		return err
	}
	// Not a wrong password, and not a login that was refused: the server
	// could not find out.
	unanswered := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || errors.Is(err, os.ErrPermission) {
			t.Errorf("a login whose captcha was not checked: %v, want an error that is not a refusal", err)
		}
	}

	t.Run("after its time", func(t *testing.T) {
		timeout := reCaptchaTimeout
		reCaptchaTimeout = 200 * time.Millisecond
		t.Cleanup(func() { reCaptchaTimeout = timeout })

		var err error
		within(t, 20*time.Second, "the login", func() { err = login(context.Background()) })
		unanswered(t, err)
	})

	t.Run("when the request is over", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(200*time.Millisecond, cancel)

		var err error
		within(t, 20*time.Second, "the login", func() { err = login(ctx) })
		unanswered(t, err)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the error of a captcha check that was given up: %v, want it to say that the request ended", err)
		}
	})
}
