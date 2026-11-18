//go:build !unix

package cmd

import "os"

// recordOwnerTrusted reports whether the file a record was read from belongs
// to a user whose word on where the server listens can be taken. Where files
// have no owner that can be compared, there is nothing to tell them apart by.
func recordOwnerTrusted(os.FileInfo, int) bool {
	return true
}
