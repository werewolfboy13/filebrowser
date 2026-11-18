//go:build unix

package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// serverProcessEnv names the directory TestServerProcess runs a server
	// in. It is only set for the process TestServerStopsWithARequestInFlight
	// starts.
	serverProcessEnv = "FB_TEST_SERVER_PROCESS_DIR"

	// serverReturnedMarker is what that process prints once the server
	// command has returned, followed by whether it could then open the
	// database.
	serverReturnedMarker = "THE SERVER COMMAND RETURNED; DATABASE RELEASED: "
)

// TestServerProcess is not a test of its own. It is the server, run in a
// process of its own by TestServerStopsWithARequestInFlight: a server that
// ends its process on the way out (which it once did, with an error, when a
// request was still in progress after the time it was given) cannot be told
// from a test binary that crashed when it runs inside the test.
func TestServerProcess(t *testing.T) {
	dir := os.Getenv(serverProcessEnv)
	if dir == "" {
		return
	}
	database := filepath.Join(dir, "filebrowser.db")

	// The server asks for SIGTERM a moment after it has written down where
	// it listens. One that arrives in between must not end the process.
	discard := make(chan os.Signal, 1)
	signal.Notify(discard, syscall.SIGTERM)

	rootCmd.SetArgs([]string{"--database", database, "--root", filepath.Join(dir, "srv"), "--address", "127.0.0.1", "--port", "0"})
	if err := rootCmd.Execute(); err != nil {
		t.Errorf("the server command returned an error: %v", err)
	}

	// The server holds the database locked for as long as it has it open,
	// also against this very process.
	db, err := bolt.Open(database, databasePermissions, &bolt.Options{Timeout: time.Second})
	if err == nil {
		_ = db.Close()
	}
	fmt.Printf("\n%s%v\n", serverReturnedMarker, err == nil)
}

// A request that is still in progress when the server is told to stop is
// given shutdownTimeout, or the time that FB_SHUTDOWN_TIMEOUT names, and is
// then cut off. After that the server command has to return, so that the
// database is closed and the record of where the server listened is removed:
// `docker stop` kills what is still running when its own time is up, and a
// database that was not closed is what is found the next time.
func TestServerStopsWithARequestInFlight(t *testing.T) {
	t.Run("after the default time", func(t *testing.T) {
		testServerStopsWithARequestInFlight(t, "", shutdownTimeout)
	})
	// Far enough below the default to tell the two apart.
	t.Run("after the time FB_SHUTDOWN_TIMEOUT names", func(t *testing.T) {
		testServerStopsWithARequestInFlight(t, "2s", 2*time.Second)
	})
}

func testServerStopsWithARequestInFlight(t *testing.T, setting string, timeout time.Duration) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "srv"), 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "health.json")

	var output bytes.Buffer
	server := exec.Command(os.Args[0], "-test.run=^TestServerProcess$", "-test.count=1")
	server.Env = append(os.Environ(),
		serverProcessEnv+"="+dir,
		// Not the machine's own configuration.
		"HOME="+dir,
		envConfig+"=",
		envHealthFile+"="+record,
		"FB_SHUTDOWN_TIMEOUT="+setting,
	)
	server.Stdout, server.Stderr = &output, &output
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- server.Wait() }()
	t.Cleanup(func() {
		_ = server.Process.Kill()
		if t.Failed() {
			t.Logf("output of the server process:\n%s", output.String())
		}
	})

	var listens *listenRecord
	waitFor(t, "the server to write down where it listens", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("the server process ended by itself: %v\n%s", err, output.String())
		default:
		}
		var ok bool
		listens, ok = readListenRecord(record)
		return ok
	})

	// A request that does not finish: a login whose body never arrives in
	// full. The handler waits for the rest of it for longer than the server
	// waits for the handler.
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", listens.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST /api/login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"username\":"); err != nil {
		t.Fatal(err)
	}
	// What the client sees of it: nothing, until its connection is closed.
	answered := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(conn).ReadString('\n')
		answered <- fmt.Sprintf("%q, %v", line, err)
	}()
	select {
	case got := <-answered:
		t.Fatalf("the request was answered before the server was told to stop: %s", got)
	case <-time.After(300 * time.Millisecond):
	}

	// Again and again until the process ends, in case the server was not
	// listening for the signal yet the first time.
	begin := time.Now()
	deadline := time.After(timeout + 15*time.Second)
	var exitErr error
	for running := true; running; {
		if err := server.Process.Signal(syscall.SIGTERM); err != nil && running {
			t.Logf("SIGTERM: %v", err)
		}
		select {
		case exitErr = <-exited:
			running = false
		case <-deadline:
			t.Fatal("the server process did not end")
		case <-time.After(50 * time.Millisecond):
		}
	}
	elapsed := time.Since(begin)

	if exitErr != nil {
		t.Errorf("the server process ended with %v, want a clean exit", exitErr)
	}
	switch {
	case strings.Contains(output.String(), serverReturnedMarker+"true"):
	case strings.Contains(output.String(), serverReturnedMarker+"false"):
		t.Error("the server command returned without closing the database")
	default:
		t.Error("the process ended before the server command returned: nothing closed the database")
	}
	if elapsed < timeout-time.Second {
		t.Errorf("the server stopped after %s: the request in progress was not given its %s", elapsed.Round(time.Millisecond), timeout)
	}
	if elapsed > timeout+5*time.Second {
		t.Errorf("the server stopped after %s: the request in progress was waited for beyond its %s", elapsed.Round(time.Millisecond), timeout)
	}

	select {
	case got := <-answered:
		if strings.Contains(got, "200") {
			t.Errorf("the request that never finished was answered: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("the client's connection is still open after the server has stopped")
	}

	assertNoFile(t, record)
	db, err := bolt.Open(filepath.Join(dir, "filebrowser.db"), databasePermissions, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("the database is still held after the server stopped: %v", err)
	}
	db.Close()
}
