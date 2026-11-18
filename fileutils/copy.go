package fileutils

import (
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/spf13/afero"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
)

// ErrSameFile is returned for a copy whose source and destination are the
// same file, under two names. Copying it would empty it: the destination is
// opened for writing, and with that truncated, before the source is read. It
// is an invalid request rather than a failure of the server.
var ErrSameFile = fmt.Errorf("%w: source and destination are the same file", fberrors.ErrInvalidRequestParams)

// sameFile reports whether name is the file or directory that src describes,
// with symbolic links followed, as a copy follows them. A name that does not
// exist is not, and neither is anything on a filesystem that cannot tell.
func sameFile(afs afero.Fs, src os.FileInfo, name string) bool {
	info, err := afs.Stat(name)
	return err == nil && os.SameFile(src, info)
}

// Copy copies a file or folder from one place to another.
func Copy(afs afero.Fs, src, dst string, fileMode, dirMode fs.FileMode) error {
	if src = path.Clean("/" + src); src == "" {
		return os.ErrNotExist
	}

	if dst = path.Clean("/" + dst); dst == "" {
		return os.ErrNotExist
	}

	if src == "/" || dst == "/" {
		// Prohibit copying from or to the virtual root directory.
		return os.ErrInvalid
	}

	if dst == src {
		return os.ErrInvalid
	}

	info, err := afs.Stat(src)
	if err != nil {
		return err
	}

	// The two paths can differ and still name the same file or directory:
	// through a link to a directory, or a hard link. See sameFile.
	if sameFile(afs, info, dst) {
		return &os.PathError{Op: "copy", Path: dst, Err: ErrSameFile}
	}

	if info.IsDir() {
		return CopyDir(afs, src, dst, fileMode, dirMode)
	}

	return CopyFile(afs, src, dst, fileMode, dirMode)
}
