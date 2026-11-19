package fileutils

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
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

// ErrIntoItself is returned for a copy of a directory to a place inside that
// directory. The copy would find what it has just created among what it is
// to copy, and copy that as well, one level deeper each time, until a path
// gets too long for the system. It is an invalid request rather than a
// failure of the server.
var ErrIntoItself = fmt.Errorf("%w: the destination is inside the source", fberrors.ErrInvalidRequestParams)

// insideSource reports whether dst, the destination of a copy, is or would
// be created inside the directory that src describes.
//
// The two paths do not show it. The source can be a symbolic link to a
// directory that the destination is in ("a/up" leading to "a", copied to
// "a/copy"), and the destination can be reached through a link that leads
// into the source. So the directories the destination really is in are
// looked at, from where it is on disk upwards, and each is compared with the
// source by identity, as the filesystem tells two directories apart: that
// holds whatever the names are, and however a filesystem folds them.
func insideSource(afs afero.Fs, src os.FileInfo, srcPath, dst string) bool {
	if !src.IsDir() {
		return false
	}

	// What the paths do show.
	if strings.HasPrefix(dst, strings.TrimSuffix(srcPath, "/")+"/") {
		return true
	}

	located, ok := afs.(interface {
		RealPath(name string) (string, error)
	})
	if !ok {
		// Not a filesystem on disk, or not one that says where: it has no
		// links to follow either.
		return false
	}
	onDisk, err := located.RealPath(dst)
	if err != nil {
		return false
	}

	// Where the destination is, with the links on the way followed. What
	// does not exist yet is created in the nearest directory that does.
	dir := filepath.Clean(onDisk)
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			dir = resolved
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}

	for {
		if info, err := os.Stat(dir); err == nil && os.SameFile(src, info) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
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

	// Neither can a directory be copied into itself. See insideSource.
	if insideSource(afs, info, src, dst) {
		return &os.PathError{Op: "copy", Path: dst, Err: ErrIntoItself}
	}

	if info.IsDir() {
		return CopyDir(afs, src, dst, fileMode, dirMode)
	}

	return CopyFile(afs, src, dst, fileMode, dirMode)
}
