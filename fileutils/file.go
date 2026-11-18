package fileutils

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/afero"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
)

// MoveFile moves file from src to dst.
// By default the rename filesystem system call is used. If src and dst point to different volumes
// the file copy is used as a fallback
func MoveFile(afs afero.Fs, src, dst string, fileMode, dirMode fs.FileMode) error {
	if afs.Rename(src, dst) == nil {
		return nil
	}

	// Whatever is at dst before the fallback runs is not this move's to
	// remove: the cleanup below is only for a partial copy it created.
	_, statErr := lstat(afs, dst)
	created := errors.Is(statErr, fs.ErrNotExist)

	// fallback
	err := Copy(afs, src, dst, fileMode, dirMode)
	if err != nil {
		if created {
			_ = afs.Remove(dst)
		}
		return err
	}
	if err := afs.RemoveAll(src); err != nil {
		return err
	}
	return nil
}

// lstat stats name without following a final symlink where the filesystem
// allows it.
func lstat(afs afero.Fs, name string) (os.FileInfo, error) {
	if lstater, ok := afs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(name)
		return info, err
	}
	return afs.Stat(name)
}

// ErrNotRegular is returned for a path whose content cannot be read or
// written like that of a file: a named pipe, a socket or a device. It is an
// invalid request rather than a failure of the server.
var ErrNotRegular = fmt.Errorf("%w: not a regular file", fberrors.ErrInvalidRequestParams)

// requireRegular reports an error unless name is a regular file, following
// symlinks. With mayBeMissing, a name that does not exist passes.
func requireRegular(afs afero.Fs, name string, mayBeMissing bool) error {
	info, err := afs.Stat(name)
	switch {
	case err != nil && mayBeMissing && errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return &os.PathError{Op: "open", Path: name, Err: ErrNotRegular}
	}
	return nil
}

// CopyFile copies a file from source to dest and returns
// an error if any.
func CopyFile(afs afero.Fs, source, dest string, fileMode, dirMode fs.FileMode) error {
	// Only regular files are copied: opening a named pipe, to read from it or
	// to write to it, blocks until something opens its other end. Stat
	// follows symlinks, as the copy itself does.
	if err := requireRegular(afs, source, false); err != nil {
		return err
	}
	if err := requireRegular(afs, dest, true); err != nil {
		return err
	}

	// Creating the destination empties it. When it is the source under
	// another name (a hard link, or a path through a link to a directory),
	// that would leave nothing to copy, and nothing of the file. This is
	// checked here, for every file, because a directory that is copied can
	// meet such a file anywhere below it.
	if srcInfo, err := afs.Stat(source); err == nil && sameFile(afs, srcInfo, dest) {
		return &os.PathError{Op: "copy", Path: dest, Err: ErrSameFile}
	}

	// Open the source file.
	src, err := afs.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	// Makes the directory needed to create the dst
	// file.
	err = afs.MkdirAll(filepath.Dir(dest), dirMode)
	if err != nil {
		return err
	}

	// Create the destination file.
	dst, err := afs.OpenFile(dest, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}
	defer dst.Close()

	// Copy the contents of the file.
	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}

	// Copy the mode
	info, err := afs.Stat(source)
	if err != nil {
		return err
	}
	err = afs.Chmod(dest, info.Mode())
	if err != nil {
		return err
	}

	return nil
}

// CommonPrefix returns common directory path of provided files
func CommonPrefix(sep byte, paths ...string) string {
	// Handle special cases.
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return path.Clean(paths[0])
	}

	// Note, we treat string as []byte, not []rune as is often
	// done in Go. (And sep as byte, not rune). This is because
	// most/all supported OS' treat paths as string of non-zero
	// bytes. A filename may be displayed as a sequence of Unicode
	// runes (typically encoded as UTF-8) but paths are
	// not required to be valid UTF-8 or in any normalized form
	// (e.g. "é" (U+00C9) and "é" (U+0065,U+0301) are different
	// file names.
	c := []byte(path.Clean(paths[0]))

	// We add a trailing sep to handle the case where the
	// common prefix directory is included in the path list
	// (e.g. /home/user1, /home/user1/foo, /home/user1/bar).
	// path.Clean will have cleaned off trailing / separators with
	// the exception of the root directory, "/" (in which case we
	// make it "//", but this will get fixed up to "/" below).
	c = append(c, sep)

	// Ignore the first path since it's already in c
	for _, v := range paths[1:] {
		// Clean up each path before testing it
		v = path.Clean(v) + string(sep)

		// Find the first non-common byte and truncate c
		if len(v) < len(c) {
			c = c[:len(v)]
		}
		for i := 0; i < len(c); i++ {
			if v[i] != c[i] {
				c = c[:i]
				break
			}
		}
	}

	// Remove trailing non-separator characters and the final separator
	for i := len(c) - 1; i >= 0; i-- {
		if c[i] == sep {
			c = c[:i]
			break
		}
	}

	return string(c)
}
