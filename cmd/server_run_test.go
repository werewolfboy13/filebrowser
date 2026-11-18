//go:build unix

package cmd

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/pflag"
	bolt "go.etcd.io/bbolt"
)

// waitFor polls until done reports true, and fails the test when it does not
// within a few seconds.
func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServerRunRecordsWhereItListensAndStopsCleanly runs the server command
// itself, the way the Docker image does: started with flags that the health
// check cannot see, checked with `health`, and stopped with SIGTERM.
func TestServerRunRecordsWhereItListensAndStopsCleanly(t *testing.T) {
	// A server command that ends the process instead of returning would end
	// the tests with it.
	if !inOwnProcess(t) {
		return
	}

	dir := t.TempDir()
	root := filepath.Join(dir, "srv")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dir, "filebrowser.db")
	record := filepath.Join(dir, "health.json")

	// Not the machine's own configuration.
	t.Setenv("HOME", dir)
	t.Setenv(envConfig, "")
	t.Setenv(envHealthFile, record)

	// The server command is the package's own, so what this test changes on
	// it is put back: its flags, and the signals it asked for.
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.Flags().VisitAll(func(flag *pflag.Flag) {
			_ = flag.Value.Set(flag.DefValue)
			flag.Changed = false
		})
		signal.Reset(os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	})

	// The server asks for SIGTERM a moment after it has written down where it
	// listens. A SIGTERM that arrives in between would end this whole test
	// run, so the test asks for the signal itself first, and discards it.
	discard := make(chan os.Signal, 1)
	signal.Notify(discard, syscall.SIGTERM)

	// Port 0 is any free port: only the server knows which one it got.
	rootCmd.SetArgs([]string{"--database", database, "--root", root, "--address", "127.0.0.1", "--port", "0"})
	stopped := make(chan error, 1)
	go func() { stopped <- rootCmd.Execute() }()

	waitFor(t, "the server to write down where it listens", func() bool {
		select {
		case err := <-stopped:
			t.Fatalf("the server stopped by itself: %v", err)
		default:
		}
		_, ok := readListenRecord(record)
		return ok
	})

	// With no flags of its own, and nothing in the environment or in a
	// configuration file that says where the server is.
	if err := runHealthTestCmd(); err != nil {
		t.Errorf("health failed against the running server: %v", err)
	}

	// Again and again until the server stops, in case it was not listening
	// for the signal yet the first time.
	deadline := time.After(shutdownTimeout + 10*time.Second)
	for running := true; running; {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-stopped:
			if err != nil {
				t.Errorf("the server stopped with an error: %v", err)
			}
			running = false
		case <-deadline:
			t.Fatal("the server did not stop")
		case <-time.After(50 * time.Millisecond):
		}
	}

	assertNoFile(t, record)
	if err := runHealthTestCmd(); err == nil {
		t.Error("health succeeded against a server that has stopped")
	}

	// The server holds the database locked for as long as it has it open.
	db, err := bolt.Open(database, databasePermissions, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("the database is still held after the server stopped: %v", err)
	}
	db.Close()
}
