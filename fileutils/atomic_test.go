package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
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

	if _, err := WriteFileAtomic(afs, "/report.txt", &brokenReader{data: "new-partial"}, 0o640); err == nil {
		t.Fatal("an interrupted write reported success")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "ORIGINAL" {
		t.Errorf("after an interrupted write the file holds %q (%v), want the original", data, err)
	}
	if temps := tempsIn(t, root); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}

	// A failed write of a new file leaves nothing.
	if _, err := WriteFileAtomic(afs, "/new.txt", &brokenReader{data: "partial"}, 0o640); err == nil {
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

	info, err := WriteFileAtomic(afs, "/report.txt", strings.NewReader("replaced"), 0o644)
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
	if _, err := WriteFileAtomic(afs, "/new.txt", strings.NewReader("x"), 0o640); err != nil {
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

	if _, err := WriteFileAtomic(afs, "/link.txt", strings.NewReader("via symlink"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symbolic link was replaced by a file (%v)", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "via symlink" {
		t.Errorf("the link target holds %q", data)
	}

	if _, err := WriteFileAtomic(afs, "/real.txt", strings.NewReader("via name"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "hard.txt")); string(data) != "via name" {
		t.Errorf("the other hard link holds %q: the write detached it", data)
	}

	// A failure while writing through a link still leaves the content alone.
	if _, err := WriteFileAtomic(afs, "/real.txt", &brokenReader{data: "partial"}, 0o644); err == nil {
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

			if _, err := WriteFileAtomic(&faults, "/config.json", strings.NewReader("updated"), 0o644); err != nil {
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
				if _, err := WriteFileAtomic(&faults, "/config.json", &brokenReader{data: "partial"}, 0o644); err == nil {
					t.Fatal("an interrupted write reported success")
				}
				if data, _ := os.ReadFile(target); string(data) != "updated" {
					t.Errorf("an interrupted write left %q", data)
				}
			}
		})
	}
}

func TestWriteFileAtomicRefusesADirectory(t *testing.T) {
	afs, root := newAtomicFs(t)
	if err := os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFileAtomic(afs, "/dir", strings.NewReader("x"), 0o644); err == nil {
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
		".fbtmp-" + strings.Repeat("A", 32):           true,  // not our naming
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
	p, err := NewTempPath("/a/b/file.txt")
	if err != nil || filepath.ToSlash(filepath.Dir(p)) != "/a/b" || !IsTempName(filepath.Base(p)) {
		t.Errorf("NewTempPath = %q, %v", p, err)
	}
}
