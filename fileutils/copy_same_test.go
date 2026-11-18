package fileutils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// A copy opens its destination for writing, which empties it, and then reads
// the source. When both are the same file under two names (through a link to
// a directory, or a hard link), the file was empty before it was read, and
// the copy "succeeded" with nothing left of it. Only the two paths were
// compared, as text.
func TestCopyRefusesToCopyAFileOntoItself(t *testing.T) {
	const content = "precious content"

	// Each case builds a tree and names a copy in it whose source and
	// destination are one file. kept is the file that must still hold its
	// content afterwards.
	cases := map[string]struct {
		build          func(t *testing.T, root string)
		src, dst, kept string
		nested         bool // the file is met inside a directory that is copied
	}{
		"through a link to its directory": {
			build: func(t *testing.T, root string) {
				mustSymlink(t, "docs", filepath.Join(root, "alias"))
			},
			src: "/docs/a.txt", dst: "/alias/a.txt", kept: "docs/a.txt",
		},
		"onto a link to itself": {
			build: func(t *testing.T, root string) {
				mustSymlink(t, filepath.Join("docs", "a.txt"), filepath.Join(root, "a-link.txt"))
			},
			src: "/docs/a.txt", dst: "/a-link.txt", kept: "docs/a.txt",
		},
		"onto a hard link to itself": {
			build: func(t *testing.T, root string) {
				if err := os.Link(filepath.Join(root, "docs", "a.txt"), filepath.Join(root, "b.txt")); err != nil {
					t.Skip(err)
				}
			},
			src: "/docs/a.txt", dst: "/b.txt", kept: "docs/a.txt",
		},
		"a directory onto a link to itself": {
			build: func(t *testing.T, root string) {
				mustSymlink(t, "docs", filepath.Join(root, "alias"))
			},
			src: "/docs", dst: "/alias", kept: "docs/a.txt",
		},
		"a directory onto one that holds a hard link to a file in it": {
			build: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "copy"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(root, "docs", "a.txt"), filepath.Join(root, "copy", "a.txt")); err != nil {
					t.Skip(err)
				}
			},
			src: "/docs", dst: "/copy", kept: "docs/a.txt", nested: true,
		},
		"a directory onto one that holds a link to a directory in it": {
			build: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "docs", "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "docs", "sub", "deep.txt"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "copy"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, filepath.Join("..", "docs", "sub"), filepath.Join(root, "copy", "sub"))
			},
			src: "/docs", dst: "/copy", kept: "docs/sub/deep.txt", nested: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "docs", "a.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "docs", "other.txt"), []byte("other"), 0o644); err != nil {
				t.Fatal(err)
			}
			tc.build(t, root)

			err := Copy(afero.NewBasePathFs(afero.NewOsFs(), root), tc.src, tc.dst, 0o644, 0o755)
			if err == nil {
				t.Error("the copy of a file onto itself reported success")
			} else if !tc.nested && !errors.Is(err, ErrSameFile) {
				t.Errorf("error %v, want ErrSameFile", err)
			}

			if data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(tc.kept))); readErr != nil || string(data) != content {
				t.Errorf("%s holds %q (%v) after the copy, want %q", tc.kept, data, readErr, content)
			}
			if data, _ := os.ReadFile(filepath.Join(root, "docs", "other.txt")); string(data) != "other" {
				t.Errorf("docs/other.txt holds %q after the copy", data)
			}
			// What is not the same file is still copied, in a directory.
			if tc.nested {
				if data, _ := os.ReadFile(filepath.Join(root, "copy", "other.txt")); string(data) != "other" {
					t.Errorf("copy/other.txt holds %q: the rest of the directory was not copied", data)
				}
			}
		})
	}
}

// Two files that merely have the same content are two files: one is copied
// over the other as before.
func TestCopyStillOverwritesAnotherFile(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"a.txt": "same", "b.txt": "same", "c.txt": "different"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	afs := afero.NewBasePathFs(afero.NewOsFs(), root)

	for _, dst := range []string{"/b.txt", "/c.txt", "/new.txt"} {
		if err := Copy(afs, "/a.txt", dst, 0o644, 0o755); err != nil {
			t.Errorf("copy to %s: %v", dst, err)
		}
		if data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(dst))); string(data) != "same" {
			t.Errorf("%s holds %q after the copy", dst, data)
		}
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
}
