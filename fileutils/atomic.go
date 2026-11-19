package fileutils

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"

	"github.com/spf13/afero"

	fberrors "github.com/werewolfboy13/filebrowser/v2/errors"
	"github.com/werewolfboy13/filebrowser/v2/rules"
)

// TempPrefix starts the name of every temporary file written next to the file
// it will replace. See WriteFileAtomic.
const TempPrefix = ".fbtmp-"

var (
	// ownTempName matches the names this package gives its temporary files.
	ownTempName = regexp.MustCompile(`^\.fbtmp-(tus-)?[0-9a-f]{32}$`)

	// tempName matches them in any ASCII spelling.
	tempName = regexp.MustCompile(`(?i)^\.fbtmp-(tus-)?[0-9a-f]{32}$`)
)

// IsTempName reports whether name, one element of a path, names one of the
// temporary files this package creates, or would on a filesystem that takes
// more than one spelling for the same name. One that does not tell upper from
// lower case (the default on macOS and Windows) opens the file that was
// created as ".fbtmp-..." under ".FBTMP-..." too, and one that folds case in
// full (APFS) under a name in which "ff" is written as the ligature, or that
// is composed differently: see rules.CanonicalName.
//
// The spelling is ignored everywhere rather than only where the filesystem
// is known to fold names, so that nothing depends on that being found out
// right.
func IsTempName(name string) bool {
	// Every element of every path that is checked comes through here, so
	// the names that cannot be one are told first. The canonical form of a
	// name that is all ASCII is the name in lower case, which the first
	// expression covers, and such a name is as long as the ones this
	// package gives or it is not one.
	if isASCII(name) {
		return (len(name) == tempNameLength || len(name) == tusTempNameLength) && tempName.MatchString(name)
	}
	return tempName.MatchString(name) || ownTempName.MatchString(rules.CanonicalName(name, true))
}

// The lengths of the names this package gives: the prefix, 32 hexadecimal
// digits, and "tus-" between them for a resumable upload.
const (
	tempNameLength    = len(TempPrefix) + 32
	tusTempNameLength = tempNameLength + len("tus-")
)

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// IsOwnTempName reports whether name is exactly a name this package gives
// its temporary files. It is what to ask before removing one: a file under
// another spelling of such a name was not created here.
func IsOwnTempName(name string) bool {
	return ownTempName.MatchString(name)
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
		// Whoever sent the content still has it, so the temporary file is of
		// no use, unless dst was left damaged: then it is the only complete
		// copy of either version there is.
		if !errors.Is(err, ErrDestinationDamaged) {
			_ = afs.Remove(tmp)
		}
		return nil, err
	}

	return info, nil
}

// ErrDestinationDamaged is returned when the content could not be put in
// place and the destination is no longer what it was either: writing into it
// failed after part of it had been overwritten. The complete content is still
// in the temporary file, which is kept.
var ErrDestinationDamaged = errors.New("the destination was left incomplete")

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
//
// tmp is removed when it is in place, and when dst refuses it for good
// (something is there that may not be replaced, or cannot take content).
// When putting it in place fails for another reason (a full disk), tmp is
// kept: it is complete, and the caller can try again or discard it. The error
// is ErrDestinationDamaged if dst was changed on the way.
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

	damaged, err := copyInPlace(afs, tmp, dst, fileMode)
	switch {
	case err == nil:
		_ = afs.Remove(tmp)
		return nil
	case damaged:
		return fmt.Errorf("%w: %w", ErrDestinationDamaged, err)
	}
	return err
}

// placeNew puts the complete temporary file tmp at dst if, and only if,
// nothing is there. It removes tmp when it is in place and when something is
// there, and keeps it when putting it there failed: see CommitTemp.
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
	// it, which fails as well when the name is taken, and which is removed
	// again when the copy fails.
	in, err := afs.Open(tmp)
	if err != nil {
		return err
	}
	_, err = writeNewInPlace(afs, dst, in, fileMode, false)
	_ = in.Close()
	if err == nil || errors.Is(err, fberrors.ErrExist) {
		_ = afs.Remove(tmp)
	}
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

// copyInPlace makes the content of the file at src the content of dst, by
// writing into dst. It reports whether a failure left dst changed.
//
// dst is not emptied first. What the new content is longer by is written
// first, behind the old content: that is the part that needs room on the
// disk, and when there is none, cutting it off again leaves dst exactly as
// it was. Only then is the old content overwritten, which needs no room on
// the filesystems this is likely to meet, and what is left of it cut off.
// Emptying dst first, as this did, lost the old content to a disk that was
// full before the new content was written.
//
// A failure while the old content is being overwritten cannot be undone: dst
// is then a mix of both, and the caller is told.
func copyInPlace(afs afero.Fs, src, dst string, fileMode fs.FileMode) (damaged bool, err error) {
	in, err := afs.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()

	srcInfo, err := in.Stat()
	if err != nil {
		return false, err
	}
	newSize := srcInfo.Size()

	// Created here if it is not there (it was a moment ago, or the rename
	// that was tried first failed): then it is ours to remove again.
	created := true
	out, err := afs.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
	if errors.Is(err, fs.ErrExist) {
		// Also a symbolic link whose target does not exist yet: the content
		// goes through the link, which creates the target.
		created = false
		out, err = afs.OpenFile(dst, os.O_RDWR|os.O_CREATE, fileMode)
	}
	if err != nil {
		return false, err
	}
	defer func() {
		_ = out.Close()
		if err != nil && created {
			_ = afs.Remove(dst)
			damaged = false
		}
	}()

	outInfo, err := out.Stat()
	if err != nil {
		return false, err
	}
	oldSize := outInfo.Size()

	if newSize > oldSize {
		if err := copyRange(out, in, oldSize, newSize-oldSize); err != nil {
			// Put it back the way it was.
			if truncErr := out.Truncate(oldSize); truncErr != nil {
				return true, err
			}
			return false, err
		}
	}

	if err := copyRange(out, in, 0, min(oldSize, newSize)); err != nil {
		return true, err
	}
	if newSize < oldSize {
		if err := out.Truncate(newSize); err != nil {
			return true, err
		}
	}

	// Sync the file to ensure all data is written to storage.
	if err := out.Sync(); err != nil {
		return true, err
	}
	return false, nil
}

// copyRange copies n bytes at offset from in to the same place in out.
func copyRange(out, in afero.File, offset, n int64) error {
	if n == 0 {
		return nil
	}
	if _, err := in.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(out, in, n)
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
