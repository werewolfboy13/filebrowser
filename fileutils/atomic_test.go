package fileutils

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
)

// brokenReader yields some data and then fails, like a request body whose
// client went away.
type brokenReader struct {
	data string
	done bool
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		return copy(p, b.data), nil
	}
	return 0, errors.New("simulated client disconnect")
}

func newAtomicFs(t *testing.T) (afero.Fs, string) {
	t.Helper()
	root := t.TempDir()
	return afero.NewBasePathFs(afero.NewOsFs(), root), root
}

func tempsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var temps []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), TempPrefix) {
			temps = append(temps, entry.Name())
		}
	}
	return temps
}

func TestWriteFileAtomicKeepsTheOriginalOnFailure(t *testing.T) {
	afs, root := newAtomicFs(t)
	target := filepath.Join(root, "report.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteFileAtomic(afs, "/report.txt", &brokenReader{data: "new-partial"}, 0o640, true); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "ORIGINAL" {
		t.Errorf("after an interrupted write the file holds %q (%v), want the original", data, err)
	}
	if temps := tempsIn(t, root); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}

	// A failed write of a new file leaves nothing.
	if _, err := WriteFileAtomic(afs, "/new.txt", &brokenReader{data: "partial"}, 0o640, true); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if _, err := os.Lstat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("a partial new file was left behind (%v)", err)
	}
}

func TestWriteFileAtomicReplacesOnlyTheContent(t *testing.T) {
	afs, root := newAtomicFs(t)
	target := filepath.Join(root, "report.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := WriteFileAtomic(afs, "/report.txt", strings.NewReader("replaced"), 0o644, true)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("replaced")) {
		t.Errorf("reported size %d", info.Size())
	}
	if data, _ := os.ReadFile(target); string(data) != "replaced" {
		t.Errorf("content = %q", data)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o600 {
		t.Errorf("mode changed from 600 to %o", after.Mode().Perm())
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Errorf("reported modification time %v, file has %v", info.ModTime(), after.ModTime())
	}

	// A new file gets the requested mode.
	if _, err := WriteFileAtomic(afs, "/new.txt", strings.NewReader("x"), 0o640, true); err != nil {
		t.Fatal(err)
	}
	created, err := os.Stat(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && created.Mode().Perm()&^0o640 != 0 {
		t.Errorf("new file has mode %o, want at most 640", created.Mode().Perm())
	}
	if temps := tempsIn(t, root); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}

// What a rename would silently change is written in place instead.
func TestWriteFileAtomicKeepsLinks(t *testing.T) {
	afs, root := newAtomicFs(t)
	target := filepath.Join(root, "real.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Link(target, filepath.Join(root, "hard.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	if _, err := WriteFileAtomic(afs, "/link.txt", strings.NewReader("via symlink"), 0o644, true); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symbolic link was replaced by a file (%v)", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "via symlink" {
		t.Errorf("the link target holds %q", data)
	}

	if _, err := WriteFileAtomic(afs, "/real.txt", strings.NewReader("via name"), 0o644, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "hard.txt")); string(data) != "via name" {
		t.Errorf("the other hard link holds %q: the write detached it", data)
	}

	// A failure while writing through a link still leaves the content alone.
	if _, err := WriteFileAtomic(afs, "/real.txt", &brokenReader{data: "partial"}, 0o644, true); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if runtime.GOOS != "windows" {
		if data, _ := os.ReadFile(target); string(data) != "via name" {
			t.Errorf("an interrupted write to a hard-linked file left %q", data)
		}
	}
	if temps := tempsIn(t, root); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}

// faultyFs fails the operations the atomic write depends on, to stand in for a
// directory that takes no new files or a destination that cannot be renamed
// over (a bind-mounted file).
type faultyFs struct {
	afero.Fs
	noTemp   bool
	noRename bool
}

func (f *faultyFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if f.noTemp && flag&os.O_EXCL != 0 {
		return nil, os.ErrPermission
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f *faultyFs) Rename(oldname, newname string) error {
	if f.noRename {
		return errors.New("device or resource busy")
	}
	return f.Fs.Rename(oldname, newname)
}

func TestWriteFileAtomicFallsBackToWritingInPlace(t *testing.T) {
	for name, faults := range map[string]faultyFs{
		"directory takes no new file":        {noTemp: true},
		"destination cannot be renamed over": {noRename: true},
	} {
		t.Run(name, func(t *testing.T) {
			base, root := newAtomicFs(t)
			faults.Fs = base
			target := filepath.Join(root, "config.json")
			if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := WriteFileAtomic(&faults, "/config.json", strings.NewReader("updated"), 0o644, true); err != nil {
				t.Fatalf("the write failed instead of falling back: %v", err)
			}
			if data, _ := os.ReadFile(target); string(data) != "updated" {
				t.Errorf("content = %q", data)
			}
			if temps := tempsIn(t, root); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}

			// When the content went to a temporary file first, an interrupted
			// write still cannot damage the destination.
			if faults.noRename {
				if _, err := WriteFileAtomic(&faults, "/config.json", &brokenReader{data: "partial"}, 0o644, true); err == nil {
					t.Fatal("an interrupted write reported success")
				}
				if data, _ := os.ReadFile(target); string(data) != "updated" {
					t.Errorf("an interrupted write left %q", data)
				}
			}
		})
	}
}

// Where the directory takes no temporary file, a new file is written in
// place. When that write fails, what it created is removed again here: no
// caller can tell a partial file of its own from a file that was put there by
// someone else.
func TestWriteFileAtomicRemovesItsOwnPartialFile(t *testing.T) {
	base, root := newAtomicFs(t)
	// Refuses the temporary file, but not the destination.
	noTemp := &tempRefusingFs{Fs: base}

	if _, err := WriteFileAtomic(noTemp, "/new.txt", &brokenReader{data: "partial"}, 0o644, true); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if _, err := os.Lstat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("the partial new file was left behind (%v)", err)
	}

	// A complete write of a new file still works that way.
	if _, err := WriteFileAtomic(noTemp, "/new.txt", strings.NewReader("complete"), 0o644, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(data) != "complete" {
		t.Errorf("content = %q", data)
	}

	// And a failed overwrite in place does not remove the file that was
	// there before it.
	if _, err := WriteFileAtomic(noTemp, "/new.txt", &brokenReader{data: "partial"}, 0o644, true); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if _, err := os.Lstat(filepath.Join(root, "new.txt")); err != nil {
		t.Errorf("a failed overwrite removed the destination: %v", err)
	}
}

// tempRefusingFs refuses to create temporary files, like a directory in which
// only the names that are already there can be written.
type tempRefusingFs struct {
	afero.Fs
}

func (f *tempRefusingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if IsTempName(filepath.Base(name)) {
		return nil, os.ErrPermission
	}
	return f.Fs.OpenFile(name, flag, perm)
}

// duringReader yields its data, runs during, and ends: a write during which
// something else happens.
type duringReader struct {
	data   string
	during func()
	step   int
}

func (r *duringReader) Read(p []byte) (int, error) {
	r.step++
	switch r.step {
	case 1:
		return copy(p, r.data), nil
	case 2:
		r.during()
	}
	return 0, io.EOF
}

// noLinkFs hides what the filesystem under it is, so that no hard link can be
// made through it: a filesystem without hard links (FAT, exFAT, some network
// and FUSE mounts).
type noLinkFs struct {
	afero.Fs
}

func (f noLinkFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return f.Fs.(afero.Lstater).LstatIfPossible(name)
}

// A write that may not replace a file must not replace one that appears while
// the content is on its way either. The check made when the write starts says
// nothing about the moment the content is put in place.
func TestWriteFileAtomicWithoutReplacing(t *testing.T) {
	// With "no temporary file" the content is written in place: the
	// destination is created, exclusively, before the content arrives, so
	// nothing can appear there meanwhile.
	const inPlace = "no temporary file"
	filesystems := map[string]func(afero.Fs) afero.Fs{
		"hard link":                    func(afs afero.Fs) afero.Fs { return afs },
		"no hard links: check, rename": func(afs afero.Fs) afero.Fs { return noLinkFs{afs} },
		"no hard links, no rename":     func(afs afero.Fs) afero.Fs { return &faultyFs{Fs: afs, noRename: true} },
		inPlace:                        func(afs afero.Fs) afero.Fs { return &tempRefusingFs{Fs: afs} },
	}
	for name, wrap := range filesystems {
		t.Run(name, func(t *testing.T) {
			base, root := newAtomicFs(t)
			afs := wrap(base)
			target := filepath.Join(root, "report.txt")

			// Nothing in the way: the file is created.
			info, err := WriteFileAtomic(afs, "/report.txt", strings.NewReader("first"), 0o640, false)
			if err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(target); string(data) != "first" || info.Size() != 5 {
				t.Errorf("content = %q, reported size %d", data, info.Size())
			}
			created, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && created.Mode().Perm()&^0o640 != 0 {
				t.Errorf("new file has mode %o, want at most 640", created.Mode().Perm())
			}

			// It is there now, and is not replaced.
			if _, err := WriteFileAtomic(afs, "/report.txt", strings.NewReader("second"), 0o640, false); !errors.Is(err, fberrors.ErrExist) {
				t.Errorf("writing onto an existing file without replacing = %v, want ErrExist", err)
			}
			if data, _ := os.ReadFile(target); string(data) != "first" {
				t.Errorf("the existing file was replaced: %q", data)
			}

			// Not even a dangling link: the write would go through it.
			if err := os.Symlink("elsewhere.txt", filepath.Join(root, "dangling.txt")); err == nil {
				if _, err := WriteFileAtomic(afs, "/dangling.txt", strings.NewReader("mine"), 0o640, false); !errors.Is(err, fberrors.ErrExist) {
					t.Errorf("writing onto a dangling link without replacing = %v, want ErrExist", err)
				}
				if _, err := os.Lstat(filepath.Join(root, "elsewhere.txt")); err == nil {
					t.Error("the write went through the dangling link")
				}
			}

			if name != inPlace {
				// A file that appears while the content is on its way is not
				// replaced either.
				appears := filepath.Join(root, "appears.txt")
				during := &duringReader{data: "mine", during: func() {
					if err := os.WriteFile(appears, []byte("theirs"), 0o644); err != nil {
						t.Fatal(err)
					}
				}}
				if _, err := WriteFileAtomic(afs, "/appears.txt", during, 0o640, false); !errors.Is(err, fberrors.ErrExist) {
					t.Errorf("writing over a file created meanwhile = %v, want ErrExist", err)
				}
				if data, _ := os.ReadFile(appears); string(data) != "theirs" {
					t.Errorf("the file created meanwhile was replaced: %q", data)
				}

				// With permission to replace, it is.
				during = &duringReader{data: "mine", during: func() {
					if err := os.WriteFile(filepath.Join(root, "replace.txt"), []byte("theirs"), 0o644); err != nil {
						t.Fatal(err)
					}
				}}
				if _, err := WriteFileAtomic(afs, "/replace.txt", during, 0o640, true); err != nil {
					t.Errorf("writing over a file created meanwhile, with permission = %v", err)
				}
				if data, _ := os.ReadFile(filepath.Join(root, "replace.txt")); string(data) != "mine" {
					t.Errorf("replace.txt = %q", data)
				}
			}

			if temps := tempsIn(t, root); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		})
	}
}

// CommitTemp without permission to replace puts the complete temporary file
// in place only where nothing is, and removes it otherwise.
func TestCommitTempWithoutReplacing(t *testing.T) {
	for name, wrap := range map[string]func(afero.Fs) afero.Fs{
		"hard link":     func(afs afero.Fs) afero.Fs { return afs },
		"no hard links": func(afs afero.Fs) afero.Fs { return noLinkFs{afs} },
	} {
		t.Run(name, func(t *testing.T) {
			base, root := newAtomicFs(t)
			afs := wrap(base)
			tmp := "/" + TempPrefix + "tus-" + strings.Repeat("a", 32)
			writeTemp := func() {
				if err := os.WriteFile(filepath.Join(root, tmp), []byte("uploaded"), 0o640); err != nil {
					t.Fatal(err)
				}
			}

			writeTemp()
			if err := CommitTemp(afs, tmp, "/free.bin", 0o640, false); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(filepath.Join(root, "free.bin")); string(data) != "uploaded" {
				t.Errorf("free.bin = %q", data)
			}

			writeTemp()
			if err := CommitTemp(afs, tmp, "/free.bin", 0o640, false); !errors.Is(err, fberrors.ErrExist) {
				t.Errorf("CommitTemp onto an existing file without replacing = %v, want ErrExist", err)
			}
			if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeTemp()
			if err := CommitTemp(afs, tmp, "/dir", 0o640, false); err == nil {
				t.Error("CommitTemp onto a directory succeeded")
			}
			if temps := tempsIn(t, root); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		})
	}
}

// fullDiskFs is a filesystem on which the destination cannot be renamed over
// (so the content has to be copied into it) and writes to it start to fail
// after room more bytes, like a disk that fills up.
type fullDiskFs struct {
	afero.Fs
	room int
}

func (f *fullDiskFs) Rename(string, string) error {
	return errors.New("device or resource busy")
}

func (f *fullDiskFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil || IsTempName(filepath.Base(name)) || flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return file, err
	}
	return &fullDiskFile{File: file, fs: f}, nil
}

type fullDiskFile struct {
	afero.File
	fs *fullDiskFs
}

func (f *fullDiskFile) Write(p []byte) (int, error) {
	if len(p) <= f.fs.room {
		f.fs.room -= len(p)
		return f.File.Write(p)
	}
	n, _ := f.File.Write(p[:f.fs.room])
	f.fs.room -= n
	return n, errors.New("no space left on device")
}

// Where the content cannot be renamed into place it is copied into the
// destination. That copy used to truncate the destination first and remove
// the temporary file whatever happened: when it failed part way (a disk that
// fills up), the old content was gone, the new content was incomplete, and
// the only complete copy of it had just been deleted.
func TestInPlaceCopyDoesNotLoseBothVersions(t *testing.T) {
	const original = "ORIGINAL-CONTENT"
	longer := strings.Repeat("N", 40)
	tmp := "/" + TempPrefix + strings.Repeat("a", 32)

	setup := func(t *testing.T, room int) (afs *fullDiskFs, root, target string) {
		t.Helper()
		base, root := newAtomicFs(t)
		target = filepath.Join(root, "data.bin")
		if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		return &fullDiskFs{Fs: base, room: room}, root, target
	}
	content := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Run("no room for what the new content is longer by", func(t *testing.T) {
		afs, root, target := setup(t, 10)

		// A one-off write: the destination is as it was, and nothing is left
		// behind (the client still has what it sent).
		if _, err := WriteFileAtomic(afs, "/data.bin", strings.NewReader(longer), 0o644, true); err == nil {
			t.Fatal("the write reported success")
		}
		if got := content(t, target); got != original {
			t.Errorf("after the failed write the destination holds %q, want the original", got)
		}
		if temps := tempsIn(t, root); len(temps) != 0 {
			t.Errorf("temporary files left behind: %v", temps)
		}

		// An upload that is put in place at its end keeps its data, so that
		// it can be put in place again once there is room.
		afs.room = 10
		if err := os.WriteFile(filepath.Join(root, tmp), []byte(longer), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := CommitTemp(afs, tmp, "/data.bin", 0o644, true); err == nil {
			t.Fatal("CommitTemp reported success")
		}
		if got := content(t, target); got != original {
			t.Errorf("after the failed CommitTemp the destination holds %q, want the original", got)
		}
		if got := content(t, filepath.Join(root, tmp)); got != longer {
			t.Errorf("the complete temporary file holds %q after the failure", got)
		}

		afs.room = 1000
		if err := CommitTemp(afs, tmp, "/data.bin", 0o644, true); err != nil {
			t.Fatalf("CommitTemp with room again: %v", err)
		}
		if got := content(t, target); got != longer {
			t.Errorf("the destination holds %q", got)
		}
		if temps := tempsIn(t, root); len(temps) != 0 {
			t.Errorf("temporary files left behind: %v", temps)
		}
	})

	t.Run("a failure while the old content is being overwritten", func(t *testing.T) {
		// Nothing can put the old content back then. The complete new
		// content must not be thrown away on top of that.
		shorter := strings.Repeat("S", 12)
		for name, write := range map[string]func(afs *fullDiskFs, root string) error{
			"WriteFileAtomic": func(afs *fullDiskFs, _ string) error {
				_, err := WriteFileAtomic(afs, "/data.bin", strings.NewReader(shorter), 0o644, true)
				return err
			},
			"CommitTemp": func(afs *fullDiskFs, root string) error {
				if err := os.WriteFile(filepath.Join(root, tmp), []byte(shorter), 0o644); err != nil {
					t.Fatal(err)
				}
				return CommitTemp(afs, tmp, "/data.bin", 0o644, true)
			},
		} {
			afs, root, target := setup(t, 5)
			if err := write(afs, root); err == nil {
				t.Fatalf("%s reported success", name)
			}
			temps := tempsIn(t, root)
			if len(temps) != 1 || content(t, filepath.Join(root, temps[0])) != shorter {
				t.Errorf("%s: the destination holds %q and the complete new content is gone (temporary files: %v)", name, content(t, target), temps)
			}
		}
	})

	t.Run("through a link whose target does not exist yet", func(t *testing.T) {
		afs, root, _ := setup(t, 1000)
		if err := os.Symlink("target.bin", filepath.Join(root, "link.bin")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, tmp), []byte(longer), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := CommitTemp(afs, tmp, "/link.bin", 0o644, true); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Lstat(filepath.Join(root, "link.bin")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced (%v)", err)
		}
		if got := content(t, filepath.Join(root, "target.bin")); got != longer {
			t.Errorf("the target of the link holds %q", got)
		}
	})

	t.Run("it still works", func(t *testing.T) {
		for _, replacement := range []string{longer, "short", original, ""} {
			afs, root, target := setup(t, 1000)
			if _, err := WriteFileAtomic(afs, "/data.bin", strings.NewReader(replacement), 0o644, true); err != nil {
				t.Fatal(err)
			}
			if got := content(t, target); got != replacement {
				t.Errorf("the destination holds %q, want %q", got, replacement)
			}
			if temps := tempsIn(t, root); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		}
	})
}

func TestWriteFileAtomicRefusesADirectory(t *testing.T) {
	afs, root := newAtomicFs(t)
	if err := os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFileAtomic(afs, "/dir", strings.NewReader("x"), 0o644, true); err == nil {
		t.Fatal("writing onto a directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "sub")); err != nil {
		t.Errorf("the directory was damaged: %v", err)
	}
	if temps := tempsIn(t, root); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}

func TestRemoveStaleTemps(t *testing.T) {
	afs, root := newAtomicFs(t)
	old := time.Now().Add(-48 * time.Hour)

	files := map[string]bool{ // name -> expected to remain
		TempPrefix + strings.Repeat("a", 32):          false, // stale
		TempPrefix + "tus-" + strings.Repeat("b", 32): false, // stale
		TempPrefix + strings.Repeat("c", 32):          true,  // recent
		TempPrefix + "notes":                          true,  // a user's own file
		".fbtmp-" + strings.Repeat("g", 32):           true,  // not our naming
		".fbtmp-" + strings.Repeat("A", 32):           true,  // refused by name, but not written by us
		"regular.txt":                                 true,
	}
	for name := range files {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if name != TempPrefix+strings.Repeat("c", 32) {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A directory with a temporary name is not ours either.
	dirName := TempPrefix + strings.Repeat("d", 32)
	if err := os.Mkdir(filepath.Join(root, dirName), 0o755); err != nil {
		t.Fatal(err)
	}
	files[dirName] = true

	stale := func(info os.FileInfo) bool { return time.Since(info.ModTime()) > 24*time.Hour }
	if err := RemoveStaleTemps(afs, "/", stale); err != nil {
		t.Fatal(err)
	}
	for name, remains := range files {
		_, err := os.Lstat(filepath.Join(root, name))
		if remains && err != nil {
			t.Errorf("%s was removed", name)
		}
		if !remains && err == nil {
			t.Errorf("%s was kept", name)
		}
	}

	if IsTempName("report.txt") || !IsTempName(TempPrefix+strings.Repeat("0", 32)) {
		t.Error("IsTempName misjudges a name")
	}

	// On a filesystem that does not tell upper from lower case (the default
	// on macOS and Windows) a temporary file answers to every spelling of its
	// name, so every spelling is one.
	for _, name := range []string{
		".FBTMP-" + strings.Repeat("a", 32),
		".fbtmp-" + strings.Repeat("A", 32),
		".FbTmp-TUS-" + strings.Repeat("aB", 16),
		".fbtmp-Tus-" + strings.Repeat("0", 32),
	} {
		if !IsTempName(name) {
			t.Errorf("IsTempName(%q) = false", name)
		}
	}
	for _, name := range []string{".fbtmp-" + strings.Repeat("a", 31), ".fbtmp-" + strings.Repeat("a", 33), "fbtmp-" + strings.Repeat("a", 32), ".FBTMP-NOTES"} {
		if IsTempName(name) {
			t.Errorf("IsTempName(%q) = true", name)
		}
	}
	p, err := NewTempPath("/a/b/file.txt")
	if err != nil || filepath.ToSlash(filepath.Dir(p)) != "/a/b" || !IsTempName(filepath.Base(p)) {
		t.Errorf("NewTempPath = %q, %v", p, err)
	}
}
