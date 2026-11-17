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

var (
	// ownTempName matches the names this package gives its temporary files.
	ownTempName = regexp.MustCompile(`^\.fbtmp-(tus-)?[0-9a-f]{32}$`)

	// tempName matches them in any spelling.
	tempName = regexp.MustCompile(`(?i)^\.fbtmp-(tus-)?[0-9a-f]{32}$`)
)

// IsTempName reports whether name, the last element of a path, names one of
// the temporary files this package creates, or would on a filesystem that
// does not tell upper from lower case (the default on macOS and Windows):
// there, ".FBTMP-..." opens the file that was created as ".fbtmp-...". The
// spelling is ignored everywhere rather than only where the filesystem is
// known to fold case, so that nothing depends on that being found out right.
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
// which takes the place of dst only once it is complete and synced.
//
// replace says whether the content may take the place of a file that is at
// dst. Without it, dst is only ever created: ErrExist is returned when
// something is there, whether it was there from the start or appeared while
// the content was on its way. See CommitTemp for how that is made sure of.
//
// Replacing a file with another one must not change anything but its content,
// so dst is written in place instead, as it was before, when that cannot be
// guaranteed: when dst is a symbolic link (the link would be replaced, not its
// target), has other hard links, belongs to an owner
// the temporary file cannot be given, or when the directory does not allow
// creating or renaming a file (a read-only directory holding a writable file,
// or a bind-mounted file). In the cases that are only discovered after the
// content was received, dst is rewritten from the complete temporary file, so
// an interrupted upload still cannot damage it.
//
// A destination that is neither a regular file nor a link to one (a named
// pipe, a socket, a device) is refused with ErrNotRegular.
//
// It returns the FileInfo of the written file.
func WriteFileAtomic(afs afero.Fs, dst string, in io.Reader, fileMode fs.FileMode, replace bool) (os.FileInfo, error) {
	old, err := lstat(afs, dst)
	switch {
	case err == nil && old.IsDir():
		return nil, fberrors.ErrIsDirectory
	case err == nil && !replace:
		// Also a symbolic link whose target does not exist: the write would
		// go through it.
		return nil, fberrors.ErrExist
	case err == nil && !old.Mode().IsRegular():
		// A symbolic link is written through, to keep the link; anything
		// else that is not a regular file cannot take content.
		if err := requireRegular(afs, dst, old.Mode()&os.ModeSymlink != 0); err != nil {
			return nil, err
		}
		return writeInPlace(afs, dst, in, fileMode)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		// dst cannot be examined (it is out of bounds, for instance). Let the
		// write itself report why.
		if !replace {
			return writeNewInPlace(afs, dst, in, fileMode, false)
		}
		return writeInPlace(afs, dst, in, fileMode)
	}

	// Past the switch, dst is a regular file or does not exist.
	exists := err == nil

	tmp, err := NewTempPath(dst)
	if err != nil {
		return nil, err
	}

	file, err := afs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		// The directory does not take a new file. Writing in place either
		// works (an existing, writable file) or reports the real problem.
		if !exists {
			return writeNewInPlace(afs, dst, in, fileMode, replace)
		}
		return writeInPlace(afs, dst, in, fileMode)
	}

	info, err := fillFile(file, in)
	if err != nil {
		_ = afs.Remove(tmp)
		return nil, err
	}

	if err := CommitTemp(afs, tmp, dst, fileMode, replace); err != nil {
		return nil, err
	}

	return info, nil
}

// fillFile copies in to the file, syncs it and closes it.
func fillFile(file afero.File, in io.Reader) (os.FileInfo, error) {
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

// CommitTemp makes the complete temporary file tmp the content of dst, and
// removes tmp. fileMode is used when dst does not exist yet.
//
// With replace, it renames tmp over dst when that changes nothing but the
// content, and otherwise copies the content into dst in place; see
// WriteFileAtomic.
//
// Without replace, dst is created and never replaced: when something is at
// dst, tmp is removed and ErrExist returned. See placeNew.
func CommitTemp(afs afero.Fs, tmp, dst string, fileMode fs.FileMode, replace bool) error {
	old, err := lstat(afs, dst)
	exists := err == nil

	rename := true
	switch {
	case exists && old.IsDir():
		_ = afs.Remove(tmp)
		return fberrors.ErrIsDirectory
	case exists && !replace:
		_ = afs.Remove(tmp)
		return fberrors.ErrExist
	case !replace:
		return placeNew(afs, tmp, dst, fileMode)
	case exists && !old.Mode().IsRegular():
		if err := requireRegular(afs, dst, old.Mode()&os.ModeSymlink != 0); err != nil {
			_ = afs.Remove(tmp)
			return err
		}
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

// placeNew puts the complete temporary file tmp at dst if, and only if,
// nothing is there, and removes tmp either way.
//
// Looking first and renaming then would leave a moment in which a file can
// appear at dst and be replaced, and a rename replaces without asking. A hard
// link does not: creating one fails when the name is taken, and the check and
// the creation are one step in the kernel. So dst is made a second name of
// tmp, and the first name is removed.
//
// A filesystem without hard links (FAT, exFAT, some network and FUSE mounts)
// leaves only the look and the rename, immediately after one another. On
// those, a file created at dst in the instant between the two is still
// replaced.
func placeNew(afs afero.Fs, tmp, dst string, fileMode fs.FileMode) error {
	err := link(afs, tmp, dst)
	switch {
	case err == nil:
		// Should removing the first name fail, it stays behind as a stale
		// temporary file and is swept up later.
		_ = afs.Remove(tmp)
		syncDir(afs, dst)
		return nil
	case errors.Is(err, fs.ErrExist):
		_ = afs.Remove(tmp)
		return fberrors.ErrExist
	}

	if _, statErr := lstat(afs, dst); statErr == nil {
		_ = afs.Remove(tmp)
		return fberrors.ErrExist
	}
	if err := afs.Rename(tmp, dst); err == nil {
		syncDir(afs, dst)
		return nil
	}

	// Neither a link nor a rename: copy the content into a file created for
	// it, which fails as well when the name is taken.
	in, err := afs.Open(tmp)
	if err == nil {
		_, err = writeNewInPlace(afs, dst, in, fileMode, false)
		_ = in.Close()
	}
	_ = afs.Remove(tmp)
	return err
}

// hardLinker is implemented by the filesystems that can give a file a second
// name. afero has no such operation, so the ones in use here are known by
// type: see link.
type hardLinker interface {
	Link(oldname, newname string) error
}

// errNoHardLinks is returned by link for a filesystem it cannot make a hard
// link on.
var errNoHardLinks = errors.New("hard links are not available on this filesystem")

// link makes newname a hard link to oldname. It fails, with an error that is
// fs.ErrExist, when newname exists, and never replaces it.
func link(afs afero.Fs, oldname, newname string) error {
	switch f := afs.(type) {
	case hardLinker:
		// The scoped filesystem of a user.
		return f.Link(oldname, newname)
	case *afero.BasePathFs:
		oldReal, err := f.RealPath(oldname)
		if err != nil {
			return err
		}
		newReal, err := f.RealPath(newname)
		if err != nil {
			return err
		}
		return os.Link(oldReal, newReal)
	case *afero.OsFs:
		return os.Link(oldname, newname)
	}
	return errNoHardLinks
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

// writeNewInPlace is writeInPlace for a destination that did not exist a
// moment ago. It creates dst itself, which is how it knows that a file it
// could not complete is its own to remove: a caller cannot tell such a file
// from one that someone else put there in the meantime.
//
// When dst was created in the meantime, it is overwritten with replace, and
// left alone, with ErrExist, without.
func writeNewInPlace(afs afero.Fs, dst string, in io.Reader, fileMode fs.FileMode, replace bool) (os.FileInfo, error) {
	file, err := afs.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
	switch {
	case errors.Is(err, fs.ErrExist) && replace:
		return writeInPlace(afs, dst, in, fileMode)
	case errors.Is(err, fs.ErrExist):
		return nil, fberrors.ErrExist
	case err != nil:
		return nil, err
	}

	info, err := fillFile(file, in)
	if err != nil {
		_ = afs.Remove(dst)
		return nil, err
	}
	return info, nil
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
		// Only the names as this package writes them: a file under another
		// spelling was not created here, and is not ours to remove.
		if !ownTempName.MatchString(name) {
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
