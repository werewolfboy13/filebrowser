package cmd

import (
	"net"
	"os"
)

// listenOnSocket listens on the unix socket at path, and gives the socket
// the permissions in perm.
//
// A socket that a server left behind is removed first: see removeStaleSocket.
func listenOnSocket(path string, perm os.FileMode) (net.Listener, error) {
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, perm); err != nil {
		listener.Close()
		return nil, err
	}

	return listener, nil
}
