package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolvedHealthTarget returns where the health command, run with args, looks
// for the server.
func resolvedHealthTarget(t *testing.T, args ...string) healthTarget {
	t.Helper()

	cmd := newHealthTestCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	target, err := resolveHealthTarget(cmd)
	if err != nil {
		t.Fatal(err)
	}

	return target
}

// The record tells the health command where to send its request, and the
// command believed whatever was in the file. Where the server cannot replace
// a file that is already there (a directory with the sticky bit, such as
// /dev/shm, shared with another container), a planted record stayed, and
// sent every health check to a place of its author's choosing: the container
// was reported as unhealthy while the server was fine.
func TestHealthIgnoresARecordItCannotTrust(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	// Where the environment says the server is. A record that is not
	// trusted leaves the command with that.
	t.Setenv("FB_ADDRESS", "127.0.0.1")
	t.Setenv("FB_PORT", "8080")
	const fromEnvironment = "http://127.0.0.1:8080/health"

	// A record of this process, which is as good as a record gets.
	trusted := listenRecord{
		PID:       os.Getpid(),
		StartTime: processStartTime(os.Getpid()),
		Address:   "127.0.0.1",
		Port:      "9000",
	}
	write := func(t *testing.T, record listenRecord) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		writeRecordFile(t, path, record)
		return path
	}

	t.Run("control: a record of the running server is used", func(t *testing.T) {
		write(t, trusted)
		if got := resolvedHealthTarget(t); got.url != "http://127.0.0.1:9000/health" {
			t.Errorf("target = %+v, want the one in the record", got)
		}
	})

	t.Run("a record that does not say when its process started", func(t *testing.T) {
		if trusted.StartTime == "" {
			t.Skip("the start time of a process cannot be read on this system")
		}
		planted := trusted
		planted.PID, planted.StartTime = 1, ""
		write(t, planted)
		if got := resolvedHealthTarget(t); got.url != fromEnvironment {
			t.Errorf("target = %+v, want %s: the record names a process without saying which", got, fromEnvironment)
		}
	})

	t.Run("a record that belongs to another user", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("giving a file to another user takes root")
		}
		path := write(t, trusted)
		if err := os.Chown(path, 12345, 12345); err != nil {
			t.Skipf("chown: %v", err)
		}
		if got := resolvedHealthTarget(t); got.url != fromEnvironment {
			t.Errorf("target = %+v, want %s: the file is neither this user's nor the recorded process's", got, fromEnvironment)
		}
	})

	t.Run("a record that points at another machine", func(t *testing.T) {
		for _, address := range []string{"192.0.2.1", "[2001:db8::1]", "files.example.net"} {
			elsewhere := trusted
			elsewhere.Address = address
			write(t, elsewhere)
			if got := resolvedHealthTarget(t); got.url != fromEnvironment {
				t.Errorf("address %s: target = %+v, want %s", address, got, fromEnvironment)
			}
		}
	})

	t.Run("unless that is where the server is configured to listen", func(t *testing.T) {
		t.Setenv("FB_ADDRESS", "files.example.net")
		elsewhere := trusted
		elsewhere.Address = "files.example.net"
		write(t, elsewhere)
		if got := resolvedHealthTarget(t); got.url != "http://files.example.net:9000/health" {
			t.Errorf("target = %+v, want the address of the configuration with the port of the record", got)
		}
	})

	t.Run("a record whose port is not a port", func(t *testing.T) {
		for _, port := range []string{"80@files.example.net", "http", "99999", "-1", " 80"} {
			odd := trusted
			odd.Port = port
			write(t, odd)
			got := resolvedHealthTarget(t)
			if got.url != fromEnvironment || strings.Contains(got.url, "example") {
				t.Errorf("port %q: target = %+v, want %s", port, got, fromEnvironment)
			}
		}
	})

	t.Run("an address of this machine is fine", func(t *testing.T) {
		for address, want := range map[string]string{
			"":          "http://localhost:9000/health",
			"0.0.0.0":   "http://127.0.0.1:9000/health",
			"[::]":      "http://[::1]:9000/health",
			"localhost": "http://localhost:9000/health",
			"127.0.0.2": "http://127.0.0.2:9000/health",
			"[::1]":     "http://[::1]:9000/health",
		} {
			local := trusted
			local.Address = address
			write(t, local)
			if got := resolvedHealthTarget(t); got.url != want {
				t.Errorf("address %q: target = %+v, want %s", address, got, want)
			}
		}
	})
}
