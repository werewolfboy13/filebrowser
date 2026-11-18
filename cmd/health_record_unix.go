//go:build unix

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// recordOwnerTrusted reports whether the file a record was read from belongs
// to a user whose word on where the server listens can be taken: the one
// running the health command, or the one the recorded process runs as.
//
// The server writes its record as itself. A file that somebody else put
// there, and that the server could not replace (in a directory with the
// sticky bit, such as /dev/shm, a file is its owner's to replace), belongs
// to them.
func recordOwnerTrusted(info os.FileInfo, pid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}

	processUID, known := processOwner(pid)
	return ownerTrusted(stat.Uid, uint32(os.Geteuid()), processUID, known) //nolint:gosec // a user ID fits
}

// ownerTrusted is the rule of recordOwnerTrusted, on the user IDs.
func ownerTrusted(fileUID, selfUID, processUID uint32, processKnown bool) bool {
	return fileUID == selfUID || (processKnown && fileUID == processUID)
}

// processOwner returns the user the process runs as, where /proc tells.
func processOwner(pid int) (uid uint32, ok bool) {
	info, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}
