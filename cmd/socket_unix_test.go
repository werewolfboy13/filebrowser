//go:build unix

package cmd

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socketTestDir returns a directory for a socket. The path of a socket is
// limited to about a hundred characters, which the directory of a test with
// a long name, on a system with a long temporary directory, goes over.
func socketTestDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "fb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// staleSocket leaves a socket at path the way a server that was killed
// does: nothing listens on it, and the file is still there.
func staleSocket(t *testing.T, path string) {
	t.Helper()

	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != fs.ModeSocket {
		t.Fatalf("no socket was left at %s (%v)", path, err)
	}
}

// assertAnswers fails the test unless a server takes a connection on the
// socket at path.
func assertAnswers(t *testing.T, path string) {
	t.Helper()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("nothing answers on %s: %v", path, err)
	}
	conn.Close()
}

// A server that is killed leaves its socket behind. The next start ended in
// "bind: address already in use", and so did every one after it: in the
// Docker image there is no shell to remove the file with.
func TestListenOnSocket(t *testing.T) {
	t.Run("listens where nothing is, with the permissions asked for", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")

		listener, err := listenOnSocket(path, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()

		assertAnswers(t, path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("mode = %o, want 600", got)
		}
	})

	t.Run("replaces the socket of a server that no longer runs", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")
		staleSocket(t, path)

		listener, err := listenOnSocket(path, 0666)
		if err != nil {
			t.Fatalf("a socket that nothing listens on stopped the start: %v", err)
		}
		defer listener.Close()

		assertAnswers(t, path)
	})

	t.Run("leaves the socket of a server that is running", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")
		running, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer running.Close()

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("a second server listens on the socket of a running one")
		}
		assertErrorNames(t, err, path, "another server")

		// The running server still has its socket, and still answers on it.
		accepted := make(chan error, 1)
		go func() {
			conn, err := running.Accept()
			if err == nil {
				conn.Close()
			}
			accepted <- err
		}()
		assertAnswers(t, path)
		// The first connection it gets is the one that asked whether it is
		// there, or this one: either way, it is the one that takes them.
		if err := <-accepted; err != nil {
			t.Errorf("the running server no longer takes connections: %v", err)
		}
	})

	t.Run("leaves a file that is not a socket", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")
		writeTestFile(t, path, "not a socket")

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("expected an error")
		}
		assertErrorNames(t, err, path, "not a socket")

		if got := readTestFile(t, path); got != "not a socket" {
			t.Errorf("the file holds %q, want it untouched", got)
		}
	})

	t.Run("leaves a directory", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("expected an error")
		}

		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Errorf("the directory is gone (%v)", err)
		}
	})

	// A link is somebody's arrangement, and what it points to is not this
	// server's to remove, a socket that nothing listens on included.
	t.Run("leaves a link, and the socket it points to", func(t *testing.T) {
		dir := socketTestDir(t)
		target := filepath.Join(dir, "target.sock")
		staleSocket(t, target)
		path := filepath.Join(dir, "fb.sock")
		if err := os.Symlink(target, path); err != nil {
			t.Skip("symbolic links are not available")
		}

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("expected an error")
		}

		if info, err := os.Lstat(path); err != nil || info.Mode().Type() != fs.ModeSymlink {
			t.Errorf("the link is gone (%v)", err)
		}
		if info, err := os.Lstat(target); err != nil || info.Mode().Type() != fs.ModeSocket {
			t.Errorf("the socket the link points to is gone (%v)", err)
		}
	})

	// Not every socket that turns a stream connection down is one that
	// nobody holds: a datagram socket in use does not take one either.
	t.Run("leaves a socket in use that takes no connections", func(t *testing.T) {
		path := filepath.Join(socketTestDir(t), "fb.sock")
		inUse, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
		if err != nil {
			t.Fatal(err)
		}
		defer inUse.Close()
		defer os.Remove(path)

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name %s", err, path)
		}

		if info, err := os.Lstat(path); err != nil || info.Mode().Type() != fs.ModeSocket {
			t.Errorf("the socket is gone (%v)", err)
		}
	})

	t.Run("leaves a socket that this user may not connect to", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root may connect to any socket")
		}

		path := filepath.Join(socketTestDir(t), "fb.sock")
		staleSocket(t, path)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}

		listener, err := listenOnSocket(path, 0666)
		if err == nil {
			listener.Close()
			t.Fatal("expected an error")
		}

		if info, err := os.Lstat(path); err != nil || info.Mode().Type() != fs.ModeSocket {
			t.Errorf("the socket is gone (%v)", err)
		}
	})
}
