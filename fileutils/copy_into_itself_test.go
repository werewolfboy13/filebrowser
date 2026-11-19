package fileutils

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/werewolfboy13/filebrowser/v2/files"
)

// deepestDirectory returns how many directories deep the tree below root
// goes.
func deepestDirectory(root string) int {
	depth := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			if n := strings.Count(strings.TrimPrefix(p, root), string(os.PathSeparator)); n > depth {
				depth = n
			}
		}
		return nil
	})
	return depth
}

// A directory copied to a place inside itself finds what it has just created
// among what it is to copy: the copy went one level deeper each time, 800
// directories and more than half a minute, until a path was too long. The
// request handler compares the two paths, which does not show it when a
// symbolic link is on the way: the source is a link to a directory the
// destination is in, or the destination is reached through a link that leads
// into the source.
func TestCopyRefusesToCopyADirectoryIntoItself(t *testing.T) {
	cases := map[string]struct {
		build    func(t *testing.T, root string)
		src, dst string
		// created is the path, below the root, the copy would have made.
		created string
	}{
		"a link to the directory it is in": {
			build:   func(t *testing.T, root string) { mustSymlink(t, ".", filepath.Join(root, "a", "up")) },
			src:     "/a/up",
			dst:     "/a/copy",
			created: "a/copy",
		},
		"a link to a directory further up": {
			build: func(t *testing.T, root string) {
				mustSymlink(t, filepath.Join("..", ".."), filepath.Join(root, "a", "sub", "top"))
			},
			src:     "/a/sub/top",
			dst:     "/a/sub/copy",
			created: "a/sub/copy",
		},
		"through a link that leads into the source": {
			build: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, filepath.Join("..", "a", "sub"), filepath.Join(root, "x", "inside"))
			},
			src:     "/a",
			dst:     "/x/inside/copy",
			created: "a/sub/copy",
		},
		"through a link that leads into the source, to directories that do not exist yet": {
			build: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, filepath.Join("..", "a", "sub"), filepath.Join(root, "x", "inside"))
			},
			src:     "/a",
			dst:     "/x/inside/new/deeper/copy",
			created: "a/sub/new",
		},
		"onto a link that leads into the source": {
			build: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, filepath.Join("..", "a", "sub"), filepath.Join(root, "x", "inside"))
			},
			src:     "/a",
			dst:     "/x/inside",
			created: "a/sub/sub",
		},
		"into itself, in so many words": {
			build:   func(*testing.T, string) {},
			src:     "/a",
			dst:     "/a/sub/copy",
			created: "a/sub/copy",
		},
	}

	filesystems := map[string]func(root string) afero.Fs{
		"scoped":        func(root string) afero.Fs { return files.NewFs(afero.NewOsFs(), root, false) },
		"follows links": func(root string) afero.Fs { return files.NewFs(afero.NewOsFs(), root, true) },
	}

	for name, tc := range cases {
		for fsName, newFs := range filesystems {
			t.Run(name+", "+fsName, func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, "a", "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "a", "file.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				tc.build(t, root)

				for action, run := range map[string]func(afs afero.Fs) error{
					"copy": func(afs afero.Fs) error { return Copy(afs, tc.src, tc.dst, 0o644, 0o755) },
					// A move that cannot rename copies.
					"move": func(afs afero.Fs) error { return MoveFile(afs, tc.src, tc.dst, 0o644, 0o755) },
				} {
					// A move of a link is a rename of the link, which works.
					if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(tc.src))); action == "move" && err == nil && info.Mode()&os.ModeSymlink != 0 {
						continue
					}

					start := time.Now()
					err = run(newFs(root))
					if !errors.Is(err, ErrIntoItself) {
						t.Errorf("%s: error %v, want ErrIntoItself", action, err)
					}
					if took := time.Since(start); took > 5*time.Second {
						t.Errorf("%s: took %v", action, took)
					}
					if depth := deepestDirectory(root); depth > 4 {
						t.Errorf("%s: went into its own output: directories %d deep", action, depth)
					}
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(tc.created))); err == nil {
						t.Errorf("%s: %s was created", action, tc.created)
					}
					if data, _ := os.ReadFile(filepath.Join(root, "a", "file.txt")); string(data) != "x" {
						t.Errorf("%s: a/file.txt holds %q", action, data)
					}
				}
			})
		}
	}
}

// What is not a copy into itself still works: a copy next to the source,
// one of a link to a directory somewhere else, one from inside a directory
// to further up, and one whose destination is yet to be created.
func TestCopyOfADirectoryToAnotherPlaceStillWorks(t *testing.T) {
	cases := map[string]struct {
		build    func(t *testing.T, root string)
		src, dst string
		want     string // a file the copy has to produce, below the root
	}{
		"next to the source": {src: "/a", dst: "/b", want: "b/sub/deep.txt"},
		"next to the source, by a name that begins like it": {src: "/a", dst: "/a (1)", want: "a (1)/sub/deep.txt"},
		"to directories that do not exist yet":              {src: "/a", dst: "/new/deeper/copy", want: "new/deeper/copy/sub/deep.txt"},
		"from inside a directory to further up":             {src: "/a/sub", dst: "/sub-copy", want: "sub-copy/deep.txt"},
		"from inside a directory to next to itself":         {src: "/a/sub", dst: "/a/sub2", want: "a/sub2/deep.txt"},
		"a link to another directory, into the directory the link is in": {
			build: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "other"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "other", "o.txt"), []byte("content"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, filepath.Join("..", "other"), filepath.Join(root, "a", "elsewhere"))
			},
			src: "/a/elsewhere", dst: "/a/copy", want: "a/copy/o.txt",
		},
		"through a link that leads next to the source": {
			build: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "other"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustSymlink(t, "other", filepath.Join(root, "alias"))
			},
			src: "/a", dst: "/alias/copy", want: "other/copy/sub/deep.txt",
		},
		"a file, into the directory it is in": {src: "/a/file.txt", dst: "/a/sub/file.txt", want: "a/sub/file.txt"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "a", "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"file.txt", filepath.Join("sub", "deep.txt")} {
				if err := os.WriteFile(filepath.Join(root, "a", name), []byte("content"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.build != nil {
				tc.build(t, root)
			}

			if err := Copy(files.NewFs(afero.NewOsFs(), root, false), tc.src, tc.dst, 0o644, 0o755); err != nil {
				t.Fatalf("copy: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tc.want))); err != nil || string(data) != "content" {
				t.Errorf("%s holds %q (%v) after the copy", tc.want, data, err)
			}
		})
	}
}

// A filesystem that is not on disk has no links, and nothing to look up: the
// paths alone decide.
func TestCopyIntoItselfOnAFilesystemInMemory(t *testing.T) {
	afs := afero.NewMemMapFs()
	if err := afero.WriteFile(afs, "/a/sub/deep.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Copy(afs, "/a", "/a/sub/copy", 0o644, 0o755); !errors.Is(err, ErrIntoItself) {
		t.Errorf("copy into itself: error %v, want ErrIntoItself", err)
	}
	if err := Copy(afs, "/a", "/ab", 0o644, 0o755); err != nil {
		t.Errorf("copy next to itself: %v", err)
	}
	if data, _ := afero.ReadFile(afs, "/ab/sub/deep.txt"); string(data) != "content" {
		t.Errorf("the copy holds %q", data)
	}
}
