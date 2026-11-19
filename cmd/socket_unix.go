//go:build unix

package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"syscall"
	"time"
)

// staleSocketDialTimeout is how long a server that holds the socket gets to
// take the connection that asks whether it is there.
const staleSocketDialTimeout = 2 * time.Second

// removeStaleSocket removes the socket at path when no server listens on it
// any more, so that this one can.
//
// A server that stops removes its socket. One that is killed cannot, and
// the socket it left behind stopped every later start with "bind: address
// already in use". In the Docker image, which has no shell and no rm, there
// was nothing to remove it with.
//
// Only a socket that refuses the connection is removed: that is the answer
// of a socket nobody listens on. Anything else at path stays as it is and
// stops the start, with an error that says what it is: a file that is not a
// socket, a link, a socket that a server answers on, and a socket that
// cannot be asked, because it does not let this user connect or does not
// answer in time.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		// Nothing is there, which is as it should be, or it cannot be looked
		// at, and listening says why.
		return nil
	}
	if info.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("can't listen on the socket %s: something that is not a socket has that name, and is left as it is", path)
	}

	conn, err := net.DialTimeout("unix", path, staleSocketDialTimeout)
	if err == nil {
		conn.Close()
		return fmt.Errorf("can't listen on the socket %s: another server is listening on it", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("can't listen on the socket %s: a socket of that name is there, and whether a server still listens on it can't be told: %w", path, err)
	}

	log.Printf("NOTICE: %s is the socket of a server that no longer runs. It is removed.", path)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("can't remove the socket %s, which a server that no longer runs left behind: %w", path, err)
	}

	return nil
}
