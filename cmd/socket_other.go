//go:build !unix

package cmd

// removeStaleSocket does nothing where a socket that a server left behind
// cannot be told from one that is in use the way it is on unix: what is at
// path stays, and listening fails when the name is taken.
func removeStaleSocket(string) error {
	return nil
}
