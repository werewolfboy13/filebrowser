package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/files"
)

// racedFs is a filesystem on which somebody else's file appears at the
// destination of every rename in the instant before it: the instant that
// looking first and renaming then leaves open, and in which a rename
// replaces what it finds. It counts the renames, and can do the same before
// a hard link is made.
type racedFs struct {
	afero.Fs
	root string

	renames    int
	beforeLink func()
}

func (f *racedFs) Rename(oldname, newname string) error {
	f.renames++
	if err := os.WriteFile(filepath.Join(f.root, newname), []byte("theirs"), 0o644); err != nil {
		return err
	}
	return f.Fs.Rename(oldname, newname)
}

// Link makes a hard link the way the filesystem underneath does.
func (f *racedFs) Link(oldname, newname string) error {
	if f.beforeLink != nil {
		f.beforeLink()
	}
	return link(f.Fs, oldname, newname)
}

func (f *racedFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return f.Fs.(afero.Lstater).LstatIfPossible(name)
}

// A file that may not replace anything is put in place with a hard link,
// which the kernel refuses when the name is taken: there is no moment
// between looking and creating. Looking first and renaming then, which is
// all a filesystem without hard links leaves, has such a moment, and nothing
// told when it was used where a hard link could have been made: every test
// passed with the hard link taken out.
//
// On each of the filesystems a user's files are reached through, a file is
// put in place while another one appears at the destination of every rename.
// With the hard link there is no rename, and nothing is replaced.
func TestNewFileIsPutInPlaceWithAHardLink(t *testing.T) {
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, "a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(probe, "a"), filepath.Join(probe, "b")); err != nil {
		t.Skipf("no hard links here: %v", err)
	}

	filesystems := map[string]func(root string) afero.Fs{
		"the scoped filesystem of a user":    func(root string) afero.Fs { return files.NewScopedFs(afero.NewOsFs(), root) },
		"a filesystem that follows any link": func(root string) afero.Fs { return afero.NewBasePathFs(afero.NewOsFs(), root) },
	}
	for name, newFs := range filesystems {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			afs := &racedFs{Fs: newFs(root), root: root}
			tmp := "/" + TempPrefix + "tus-" + strings.Repeat("a", 32)
			writeTemp := func() {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, tmp), []byte("uploaded"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			content := func(name string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}

			// A complete upload.
			writeTemp()
			if err := CommitTemp(afs, tmp, "/upload.bin", 0o640, false); err != nil {
				t.Fatalf("CommitTemp = %v", err)
			}
			if afs.renames != 0 {
				t.Errorf("the upload was put in place with %d renames: a rename replaces a file that appears before it", afs.renames)
			}
			if got := content("upload.bin"); got != "uploaded" {
				t.Errorf("upload.bin = %q", got)
			}

			// A file that is written in one request.
			if _, err := WriteFileAtomic(afs, "/new.txt", strings.NewReader("mine"), 0o640, false); err != nil {
				t.Fatalf("WriteFileAtomic = %v", err)
			}
			if afs.renames != 0 {
				t.Errorf("the file was put in place with %d renames", afs.renames)
			}
			if got := content("new.txt"); got != "mine" {
				t.Errorf("new.txt = %q", got)
			}

			// And when the other file is there first, by however little, it
			// stays.
			afs.beforeLink = func() {
				if err := os.WriteFile(filepath.Join(root, "taken.bin"), []byte("theirs"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			writeTemp()
			if err := CommitTemp(afs, tmp, "/taken.bin", 0o640, false); !errors.Is(err, fberrors.ErrExist) {
				t.Errorf("CommitTemp onto a file that appeared a moment before = %v, want ErrExist", err)
			}
			if got := content("taken.bin"); got != "theirs" {
				t.Errorf("the file that appeared a moment before was replaced: %q", got)
			}
			if afs.renames != 0 {
				t.Errorf("%d renames", afs.renames)
			}
			if temps := tempsIn(t, root); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		})
	}
}
