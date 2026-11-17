package fileutils

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"

	"github.com/spf13/afero"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
)

// TempPrefix starts the name of every temporary file written next to the file
// it will replace. See WriteFileAtomic.
const TempPrefix = ".fbtmp-"

var tempName = regexp.MustCompile(`^\.fbtmp-(tus-)?[0-9a-f]{32}$`)

// IsTempName reports whether name, the last element of a path, is the name of
// one of the temporary files this package creates.
func IsTempName(name string) bool {
	return tempName.MatchString(name)
}

// NewTempPath returns a fresh temporary path in the directory of dst. The name
// does not depend on the name of dst, so it cannot exceed the length limit of
// the filesystem when that name is already close to it.
func NewTempPath(dst string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	dir, _ := path.Split(dst)
	return dir + TempPrefix + hex.EncodeToString(b), nil
}

// WriteFileAtomic writes the content read from in to dst so that a failure on
// the way (the client going away, a full disk, a crash) leaves whatever was at
// dst untouched: the content goes to a temporary file in the same directory,
// which replaces dst only once it is complete and synced.
//
// Replacing a file with another one must not change anything but its content,
// so dst is written in place instead, as it was before, when that cannot be
// guaranteed: when dst is a symbolic link (the link would be replaced, not its
// target), has other hard links, is not a regular file, belongs to an owner
// the temporary file cannot be given, or when the directory does not allow
// creating or renaming a file (a read-only directory holding a writable file,
// or a bind-mounted file). In the cases that are only discovered after the
// content was received, dst is rewritten from the complete temporary file, so
// an interrupted upload still cannot damage it.
//
// It returns the FileInfo of the written file.
func WriteFileAtomic(afs afero.Fs, dst string, in io.Reader, fileMode fs.FileMode) (os.FileInfo, error) {
	old, err := lstat(afs, dst)
	switch {
	case err == nil && old.IsDir():
		return nil, fberrors.ErrIsDirectory
	case err == nil && !old.Mode().IsRegular():
		return writeInPlace(afs, dst, in, fileMode)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		// dst cannot be examined (it is out of bounds, for instance). Let the
		// write itself report why.
		return writeInPlace(afs, dst, in, fileMode)
	}

	tmp, err := NewTempPath(dst)
	if err != nil {
		return nil, err
	}

	file, err := afs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		// The directory does not take a new file. Writing in place either
		// works (an existing, writable file) or reports the real problem.
		return writeInPlace(afs, dst, in, fileMode)
	}

	info, err := fillTemp(file, in)
	if err != nil {
		_ = afs.Remove(tmp)
		return nil, err
	}

	if err := ReplaceWithTemp(afs, tmp, dst, fileMode); err != nil {
		return nil, err
	}

	return info, nil
}

// fillTemp copies in to the temporary file, syncs it and closes it.
func fillTemp(file afero.File, in io.Reader) (os.FileInfo, error) {
	_, err := io.Copy(file, in)
	if err == nil {
		// Sync the file to ensure all data is written to storage before it
		// takes the place of the previous content.
		err = file.Sync()
	}

	var info os.FileInfo
	if err == nil {
		info, err = file.Stat()
	}

	// The file has to be closed before it can be renamed on Windows.
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	return info, nil
}

// ReplaceWithTemp makes the complete temporary file tmp the content of dst,
// and removes tmp. It renames tmp over dst when that changes nothing but the
// content, and otherwise copies the content into dst in place; see
// WriteFileAtomic. fileMode is used when dst does not exist yet.
func ReplaceWithTemp(afs afero.Fs, tmp, dst string, fileMode fs.FileMode) error {
	old, err := lstat(afs, dst)
	exists := err == nil

	rename := true
	switch {
	case exists && old.IsDir():
		_ = afs.Remove(tmp)
		return fberrors.ErrIsDirectory
	case exists && !old.Mode().IsRegular():
		rename = false
	case exists:
		// Take over the permissions and the ownership of the file being
		// replaced. Without them the rename would silently reset both.
		rename = afs.Chmod(tmp, old.Mode().Perm()) == nil && keepIdentity(afs, tmp, old)
	}

	if rename {
		if err := afs.Rename(tmp, dst); err == nil {
			syncDir(afs, dst)
			return nil
		}
		// The rename can fail where writing still works: dst is a mount point
		// (a bind-mounted file), or is open elsewhere on Windows.
	}

	err = copyInPlace(afs, tmp, dst, fileMode)
	_ = afs.Remove(tmp)
	return err
}

// copyInPlace overwrites dst with the content of the file at src.
func copyInPlace(afs afero.Fs, src, dst string, fileMode fs.FileMode) error {
	in, err := afs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	_, err = writeInPlace(afs, dst, in, fileMode)
	return err
}

// writeInPlace truncates dst and writes the content read from in to it.
func writeInPlace(afs afero.Fs, dst string, in io.Reader, fileMode fs.FileMode) (os.FileInfo, error) {
	file, err := afs.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	_, err = io.Copy(file, in)
	if err != nil {
		return nil, err
	}

	// Sync the file to ensure all data is written to storage.
	// to prevent file corruption.
	if err := file.Sync(); err != nil {
		return nil, err
	}

	return file.Stat()
}

// syncDir flushes the directory entry of name, so that a rename survives a
// crash. Not every platform can sync a directory, and the content is already
// safe either way, so errors are ignored.
func syncDir(afs afero.Fs, name string) {
	dir, _ := path.Split(name)
	if dir == "" {
		dir = "/"
	}

	d, err := afs.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// RemoveStaleTemps removes the temporary files in dir that stale reports as
// abandoned: leftovers of a write that a crash or a restart interrupted. It
// returns the error met while listing dir; a file that cannot be removed is
// skipped.
func RemoveStaleTemps(afs afero.Fs, dir string, stale func(os.FileInfo) bool) error {
	d, err := afs.Open(dir)
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return err
	}

	for _, name := range names {
		if !IsTempName(name) {
			continue
		}

		p := path.Join(dir, name)
		info, err := lstat(afs, p)
		if err != nil || !info.Mode().IsRegular() || !stale(info) {
			continue
		}
		_ = afs.Remove(p)
	}

	return nil
}
