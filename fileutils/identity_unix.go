//go:build unix

package fileutils

import (
	"os"
	"syscall"

	"github.com/spf13/afero"
)

// keepIdentity reports whether the file at tmp can take the place of the file
// described by old without changing who owns it or which other names refer to
// it. It gives tmp the owner and group of old when they differ, and reports
// false when that is not permitted, or when old has other hard links (a rename
// would detach them from the new content).
func keepIdentity(afs afero.Fs, tmp string, old os.FileInfo) bool {
	oldStat, ok := old.Sys().(*syscall.Stat_t)
	if !ok {
		// Not a filesystem with owners or links.
		return true
	}
	if oldStat.Nlink > 1 {
		return false
	}

	tmpInfo, err := afs.Stat(tmp)
	if err != nil {
		return false
	}
	tmpStat, ok := tmpInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if tmpStat.Uid == oldStat.Uid && tmpStat.Gid == oldStat.Gid {
		return true
	}

	return afs.Chown(tmp, int(oldStat.Uid), int(oldStat.Gid)) == nil
}
