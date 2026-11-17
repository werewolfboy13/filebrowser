package fileutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// MoveFile falls back to copy-then-delete when the rename fails. When the copy
// failed too, it removed the destination, whatever it was: moving a directory
// onto an existing file deleted that file, and moving a file onto an empty
// directory deleted that directory, with the move itself failing. A failed
// move must leave a destination that was already there untouched.
func TestMoveFileFailureKeepsExistingDestination(t *testing.T) {
	newFs := func(t *testing.T) (afero.Fs, string) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"dir/x": "x", "file.txt": "precious"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		return afero.NewBasePathFs(afero.NewOsFs(), root), root
	}

	t.Run("directory onto a file", func(t *testing.T) {
		afs, root := newFs(t)
		if err := MoveFile(afs, "/dir", "/file.txt", 0o644, 0o755); err == nil {
			t.Fatal("moving a directory onto a file succeeded")
		}
		if data, err := os.ReadFile(filepath.Join(root, "file.txt")); err != nil || string(data) != "precious" {
			t.Errorf("destination file was lost: %q, %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(root, "dir", "x")); err != nil {
			t.Errorf("source was lost: %v", err)
		}
	})

	t.Run("file onto an empty directory", func(t *testing.T) {
		afs, root := newFs(t)
		if err := MoveFile(afs, "/file.txt", "/empty", 0o644, 0o755); err == nil {
			t.Fatal("moving a file onto a directory succeeded")
		}
		if fi, err := os.Stat(filepath.Join(root, "empty")); err != nil || !fi.IsDir() {
			t.Errorf("destination directory was lost: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
			t.Errorf("source was lost: %v", err)
		}
	})

	t.Run("plain moves still work", func(t *testing.T) {
		afs, root := newFs(t)
		if err := MoveFile(afs, "/file.txt", "/moved.txt", 0o644, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := MoveFile(afs, "/dir", "/moved-dir", 0o644, 0o755); err != nil {
			t.Fatal(err)
		}
		// Replacing an existing file is what override is for.
		if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MoveFile(afs, "/moved.txt", "/other.txt", 0o644, 0o755); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(filepath.Join(root, "other.txt")); string(data) != "precious" {
			t.Errorf("other.txt = %q, want the moved content", data)
		}
		if _, err := os.Stat(filepath.Join(root, "moved-dir", "x")); err != nil {
			t.Errorf("moved directory content missing: %v", err)
		}
	})
}
