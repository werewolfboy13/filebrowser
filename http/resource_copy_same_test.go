package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Copying a file onto itself under another of its names emptied it: the
// destination is opened for writing before the source is read, and only the
// two paths were compared. A link to a directory inside the scope, or a hard
// link, as an administrator or another program on the host can make them,
// gives a file a second name.
func TestCopyOntoTheSameFileIsRefused(t *testing.T) {
	const content = "precious content"

	cases := map[string]struct {
		build    func(t *testing.T, e *fullEnv)
		src, dst string
		want     int
	}{
		"a file, through a link to its directory": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Symlink("docs", filepath.Join(e.bobDir, "alias")); err != nil {
					t.Skip(err)
				}
			},
			src: "/docs/a.txt", dst: "/alias/a.txt", want: http.StatusBadRequest,
		},
		"a file, onto a hard link to it": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Link(filepath.Join(e.bobDir, "docs", "a.txt"), filepath.Join(e.bobDir, "b.txt")); err != nil {
					t.Skip(err)
				}
			},
			src: "/docs/a.txt", dst: "/b.txt", want: http.StatusBadRequest,
		},
		"a directory, onto a link to it": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Symlink("docs", filepath.Join(e.bobDir, "alias")); err != nil {
					t.Skip(err)
				}
			},
			src: "/docs", dst: "/alias", want: http.StatusBadRequest,
		},
		"a directory, onto one that holds a hard link to a file in it": {
			build: func(t *testing.T, e *fullEnv) {
				if err := os.Mkdir(filepath.Join(e.bobDir, "copy"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(e.bobDir, "docs", "a.txt"), filepath.Join(e.bobDir, "copy", "a.txt")); err != nil {
					t.Skip(err)
				}
			},
			// The copy goes on with the other files of the directory, and
			// reports what it could not copy as it always did.
			src: "/docs", dst: "/copy", want: http.StatusInternalServerError,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newFullEnv(t)
			tok := e.login("bob", fullEnvUserPassword)
			e.writeBobFile("docs/a.txt", content)
			e.writeBobFile("docs/other.txt", "other")
			tc.build(t, e)

			code, _, body := e.do(http.MethodPatch, "/api/resources"+tc.src+"?action=copy&destination="+strings.ReplaceAll(tc.dst, "/", "%2F")+"&override=true", tok, nil, nil)
			if code != tc.want {
				t.Errorf("copy of %s onto %s = %d %q, want %d", tc.src, tc.dst, code, body, tc.want)
			}
			if code == http.StatusBadRequest && !strings.Contains(body, "same file") {
				t.Errorf("the answer does not say why: %q", body)
			}
			if strings.Contains(body, e.root) {
				t.Errorf("the answer names a path on the server: %q", body)
			}
			for file, want := range map[string]string{"docs/a.txt": content, "docs/other.txt": "other"} {
				if got, err := os.ReadFile(filepath.Join(e.bobDir, filepath.FromSlash(file))); err != nil || string(got) != want {
					t.Errorf("%s holds %q (%v) after the copy, want %q", file, got, err, want)
				}
			}
		})
	}
}

// A copy under a new name ("keep both") of a file that has a second name is
// a copy like any other.
func TestCopyNextToTheSameFileUnderANewNameWorks(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	e.writeBobFile("docs/a.txt", "precious content")
	if err := os.Symlink("docs", filepath.Join(e.bobDir, "alias")); err != nil {
		t.Skip(err)
	}

	code, _, body := e.do(http.MethodPatch, "/api/resources/docs/a.txt?action=copy&destination=%2Falias%2Fa.txt&rename=true", tok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("copy with rename = %d %q", code, body)
	}
	for _, file := range []string{"docs/a.txt", "docs/a(1).txt"} {
		if got, err := os.ReadFile(filepath.Join(e.bobDir, filepath.FromSlash(file))); err != nil || string(got) != "precious content" {
			t.Errorf("%s holds %q (%v)", file, got, err)
		}
	}
}
