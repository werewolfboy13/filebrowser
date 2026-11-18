package cmd

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// ownProcessEnv names the test a process was started for by inOwnProcess.
const ownProcessEnv = "FB_TEST_IN_OWN_PROCESS"

// inOwnProcess runs the calling test in a process of its own, and reports
// whether this is that process: a test starts with
//
//	if !inOwnProcess(t) {
//		return
//	}
//
// It is for tests of code that is not to end the process it runs in. When it
// does all the same (log.Fatal, os.Exit), a test that runs it directly takes
// the whole test run down with it, and nothing says which test it was or
// what it expected. This way the test fails, with what the process printed.
func inOwnProcess(t *testing.T) bool {
	t.Helper()

	if os.Getenv(ownProcessEnv) == t.Name() {
		return true
	}

	child := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	child.Env = append(os.Environ(), ownProcessEnv+"="+t.Name())
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("run in a process of its own, the test ended with %v. Its output:\n%s", err, output)
	}

	return false
}

// serveForShutdownTest starts a server with handler on a loopback port and
// returns it with its URL.
func serveForShutdownTest(t *testing.T, handler http.Handler) (srv *http.Server, url string) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv = newServer(handler)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return srv, "http://" + listener.Addr().String()
}

// A transfer that was still running when the time to stop ran out made the
// server exit on the spot, with an error and without closing the database.
// Docker's own limit is 10 seconds at most, so more often the server was
// killed instead. It has to cut the transfer off and return.
func TestShutdownServerCutsOffARequestThatDoesNotFinish(t *testing.T) {
	// It made the server exit, and would do the same to the tests.
	if !inOwnProcess(t) {
		return
	}

	started := make(chan struct{})
	cutOff := make(chan struct{})
	srv, url := serveForShutdownTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A download that never ends, such as a large file on a slow line.
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cutOff)
	}))

	clientDone := make(chan error, 1)
	go func() {
		res, err := http.Get(url) //nolint:gosec,noctx
		if err != nil {
			clientDone <- err
			return
		}
		defer res.Body.Close()
		_, err = io.Copy(io.Discard, res.Body)
		clientDone <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not start")
	}

	const timeout = 200 * time.Millisecond
	begin := time.Now()
	graceful := shutdownServer(srv, timeout)
	elapsed := time.Since(begin)

	if graceful {
		t.Error("the shutdown is reported as graceful, with a request still in progress")
	}
	if elapsed < timeout {
		t.Errorf("returned after %s: the request was not given its %s", elapsed, timeout)
	}
	if elapsed > timeout+3*time.Second {
		t.Errorf("returned after %s: the request was waited for beyond its %s", elapsed, timeout)
	}

	select {
	case <-cutOff:
	case <-time.After(5 * time.Second):
		t.Error("the request is still in progress after the shutdown")
	}
	select {
	case err := <-clientDone:
		if err == nil {
			t.Error("the client got a complete answer from a download that never ends")
		}
	case <-time.After(5 * time.Second):
		t.Error("the client's connection is still open after the shutdown")
	}
}

func TestShutdownServerWaitsForARequestThatFinishes(t *testing.T) {
	started := make(chan struct{})
	srv, url := serveForShutdownTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	}))

	body := make(chan string, 1)
	go func() {
		res, err := http.Get(url) //nolint:gosec,noctx
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		body <- string(data)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not start")
	}

	if graceful := shutdownServer(srv, 10*time.Second); !graceful {
		t.Error("the shutdown is not reported as graceful")
	}
	if got := <-body; got != "done" {
		t.Errorf("the client got %q, want the whole answer", got)
	}
}

// The server must give up before Docker does. How long Docker waits is not
// the same everywhere (10 seconds according to its documentation, about 3
// were measured with Docker Desktop), so compose.yaml says how long: more
// than the server takes when it is not told otherwise.
func TestShutdownTimeoutIsBelowTheStopGracePeriodOfCompose(t *testing.T) {
	compose, err := os.ReadFile(filepath.Join("..", "compose.yaml"))
	if err != nil {
		// compose.yaml is not among the files that the image is built from,
		// where these tests run too.
		t.Skipf("no compose.yaml to compare with: %v", err)
	}

	match := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\S+)`).FindSubmatch(compose)
	if match == nil {
		t.Fatal("compose.yaml has no stop_grace_period")
	}
	gracePeriod, err := time.ParseDuration(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}

	if shutdownTimeout >= gracePeriod {
		t.Errorf("shutdownTimeout = %s, want less than the stop_grace_period of %s", shutdownTimeout, gracePeriod)
	}
}

// The time can be set, for where the server is given less to stop than it
// takes by default, or more.
func TestShutdownTimeoutFrom(t *testing.T) {
	// Not the configuration of the machine that runs the tests.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	if rootCmd.Flags().Lookup("shutdownTimeout") == nil {
		t.Fatal("the server has no --shutdownTimeout flag")
	}

	for value, want := range map[string]time.Duration{
		"":      shutdownTimeout,
		"2s":    2 * time.Second,
		"1m30s": 90 * time.Second,
		"0s":    0,
	} {
		t.Run("FB_SHUTDOWN_TIMEOUT="+value, func(t *testing.T) {
			t.Setenv("FB_SHUTDOWN_TIMEOUT", value)

			v, err := newViper(rootCmd)
			if err != nil {
				t.Fatal(err)
			}
			got, err := shutdownTimeoutFrom(v)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("timeout = %s, want %s", got, want)
			}
		})
	}

	// A number without a unit would be nanoseconds, which nobody means.
	for _, value := range []string{"15", "-1s", "soon"} {
		t.Run("FB_SHUTDOWN_TIMEOUT="+value+" is refused", func(t *testing.T) {
			t.Setenv("FB_SHUTDOWN_TIMEOUT", value)

			v, err := newViper(rootCmd)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := shutdownTimeoutFrom(v); err == nil {
				t.Errorf("timeout = %s, want an error", got)
			}
		})
	}
}
