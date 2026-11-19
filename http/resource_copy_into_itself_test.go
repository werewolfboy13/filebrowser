package fbhttp

import (
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deepestDirectoryBelow returns how many directories deep the tree below
// root goes.
func deepestDirectoryBelow(root string) int {
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

// A copy of a directory into itself is refused by comparing the two paths.
// With a symbolic link on the way they do not show it: "a/up" leads to "a",
// and copied to "a/copy" it copied "a", with the "copy" it had just made in
// it, and that again, 800 directories deep and for more than half a minute,
// until the path was too long and the request ended in a 500. The same went
// for a destination reached through a link that leads into the source, and
// for a move that has to copy.
func TestCopyOfADirectoryIntoItselfThroughALinkIsRefused(t *testing.T) {
	cases := map[string]struct {
		build            func(t *testing.T, e *fullEnv)
		action, src, dst string
	}{
		"a link to the directory it is in": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Symlink(".", filepath.Join(e.bobDir, "a", "up")); err != nil {
					t.Skip(err)
				}
			},
			action: "copy", src: "/a/up", dst: "/a/copy",
		},
		"a link to the directory it is in, kept next to it under a new name": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Symlink(".", filepath.Join(e.bobDir, "a", "up")); err != nil {
					t.Skip(err)
				}
			},
			action: "copy&rename=true", src: "/a/up", dst: "/a/up",
		},
		"through a link that leads into the source": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Mkdir(filepath.Join(e.bobDir, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("..", "a", "sub"), filepath.Join(e.bobDir, "x", "inside")); err != nil {
					t.Skip(err)
				}
			},
			action: "copy", src: "/a", dst: "/x/inside/copy",
		},
		"a move through a link that leads into the source": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Mkdir(filepath.Join(e.bobDir, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("..", "a", "sub"), filepath.Join(e.bobDir, "x", "inside")); err != nil {
					t.Skip(err)
				}
			},
			action: "rename", src: "/a", dst: "/x/inside/moved",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newFullEnv(t)
			tok := e.login("bob", fullEnvUserPassword)
			e.writeBobFile("a/file.txt", "x")
			e.writeBobFile("a/sub/deep.txt", "y")
			tc.build(t, e)

			start := time.Now()
			code, _, body := e.do(http.MethodPatch,
				"/api/resources"+tc.src+"?action="+tc.action+"&destination="+url.QueryEscape(tc.dst), tok, nil, nil)
			took := time.Since(start)
			if len(body) > 200 {
				body = body[:200]
			}
			depth := deepestDirectoryBelow(e.bobDir)
			if code != http.StatusBadRequest {
				t.Errorf("%s of %s to %s = %d %q after %v, want 400", tc.action, tc.src, tc.dst, code, body, took)
			}
			if depth > 3 {
				t.Errorf("the copy went into its own output: directories %d deep, after %v", depth, took)
			}
			if took > 5*time.Second {
				t.Errorf("the request took %v", took)
			}
			for rel, want := range map[string]string{"a/file.txt": "x", "a/sub/deep.txt": "y"} {
				if data, err := os.ReadFile(filepath.Join(e.bobDir, filepath.FromSlash(rel))); err != nil || string(data) != want {
					t.Errorf("%s holds %q (%v) afterwards", rel, data, err)
				}
			}
		})
	}
}

// Copies that only look like it go through: next to the source under a new
// name, and of a link to a directory somewhere else into the directory the
// link is in.
func TestCopyOfADirectoryNextToItselfStillWorks(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	e.writeBobFile("a/file.txt", "x")
	e.writeBobFile("a/sub/deep.txt", "y")
	e.writeBobFile("other/o.txt", "z")
	e.writeBobFile("links/file.txt", "x")
	if err := os.Symlink(filepath.Join("..", "other"), filepath.Join(e.bobDir, "links", "elsewhere")); err != nil {
		t.Skip(err)
	}

	for _, rq := range []struct{ query, want, content string }{
		{"/api/resources/a?action=copy&rename=true&destination=%2Fa", "a(1)/sub/deep.txt", "y"},
		{"/api/resources/a?action=copy&destination=%2Fb", "b/sub/deep.txt", "y"},
		{"/api/resources/a/sub?action=copy&destination=%2Fa%2Fsub2", "a/sub2/deep.txt", "y"},
		{"/api/resources/links/elsewhere?action=copy&destination=%2Flinks%2Fcopy", "links/copy/o.txt", "z"},
	} {
		if code, _, body := e.do(http.MethodPatch, rq.query, tok, nil, nil); code != http.StatusOK {
			t.Errorf("PATCH %s = %d %q", rq.query, code, body)
		}
		if data, err := os.ReadFile(filepath.Join(e.bobDir, filepath.FromSlash(rq.want))); err != nil || string(data) != rq.content {
			t.Errorf("after PATCH %s, %s holds %q (%v)", rq.query, rq.want, data, err)
		}
	}
}
