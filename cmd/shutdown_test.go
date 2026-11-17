package cmd

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

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
// Docker's own limit is the same 10 seconds, so more often the server was
// killed instead. It has to cut the transfer off and return.
func TestShutdownServerCutsOffARequestThatDoesNotFinish(t *testing.T) {
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

// The server must give up before Docker does: `docker stop` kills a
// container after 10 seconds unless it is told otherwise.
func TestShutdownTimeoutIsBelowDockersDefault(t *testing.T) {
	const dockerStopTimeout = 10 * time.Second
	if shutdownTimeout >= dockerStopTimeout {
		t.Errorf("shutdownTimeout = %s, want less than Docker's %s", shutdownTimeout, dockerStopTimeout)
	}
}
