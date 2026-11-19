package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The name of an upload's temporary file was refused only as the last element
// of a path. A directory with that name could be made by creating something
// inside it, and it then held the place the temporary file of an upload
// needs.
func TestTemporaryNamesAreRefusedInEveryPathElement(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	e.writeBobFile("data.txt", "data")

	temp := filepath.Base(tusTempPath("/report.txt"))
	for _, name := range []string{temp, strings.ToUpper(temp), ".fbtmp-" + strings.Repeat("0", 32)} {
		for _, rq := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/resources/" + name + "/", ""},
			{http.MethodPost, "/api/resources/" + name + "/x/", ""},
			{http.MethodPost, "/api/resources/" + name + "/x/y/", ""},
			{http.MethodPost, "/api/resources/sub/" + name + "/x/", ""},
			{http.MethodPost, "/api/resources/" + name + "/x.txt", "content"},
			{http.MethodPost, "/api/tus/" + name + "/x.bin", ""},
			{http.MethodPatch, "/api/resources/data.txt?action=copy&destination=%2F" + name + "%2Fdata.txt", ""},
			{http.MethodPatch, "/api/resources/data.txt?action=rename&destination=%2F" + name + "%2Fdata.txt", ""},
			{http.MethodGet, "/api/resources/" + name + "/x/", ""},
		} {
			code, _, body := e.do(rq.method, rq.path, tok, strings.NewReader(rq.body), map[string]string{"Upload-Length": "5"})
			if code != http.StatusForbidden {
				t.Errorf("%s %s = %d %q, want 403", rq.method, rq.path, code, body)
			}
		}
		for _, dir := range []string{name, filepath.Join("sub", name)} {
			if pathExists(filepath.Join(e.bobDir, dir)) {
				t.Errorf("%s was created", dir)
			}
		}
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "data.txt")); string(data) != "data" {
		t.Errorf("data.txt holds %q", data)
	}

	// A directory that holds a temporary file is still a directory like any
	// other: only the names are refused.
	e.writeBobFile("box/"+temp, "in flight")
	e.writeBobFile("box/visible.txt", "x")
	if code, _, body := e.do(http.MethodGet, "/api/raw/box/visible.txt", tok, nil, nil); code != http.StatusOK || body != "x" {
		t.Errorf("GET of a file next to a temporary one = %d %q", code, body)
	}
}

// When the temporary file of a resumable upload could not be opened, the
// upload wrote to its destination directly instead: it emptied the file it
// was to replace as soon as it was created, and an upload that was then
// abandoned left it empty. That is meant for a directory that takes no new
// file. It also happened when something that is not a file had the name of
// the temporary file, which a directory of that name made happen for
// everyone who uploaded a file of the matching name there. And where the
// name was a symbolic link, the file it led to was emptied.
//
// An upload whose temporary path is taken by something the server did not
// put there is refused, and nothing is touched.
func TestTusRefusesWhenItsTemporaryPathIsTaken(t *testing.T) {
	occupants := map[string]func(t *testing.T, e *fullEnv, temp string){
		"a directory": func(t *testing.T, _ *fullEnv, temp string) {
			if err := os.Mkdir(temp, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a directory with something in it": func(t *testing.T, _ *fullEnv, temp string) {
			if err := os.MkdirAll(filepath.Join(temp, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a link to a file": func(t *testing.T, e *fullEnv, temp string) {
			e.writeBobFile("victim.txt", "the victim's content")
			if err := os.Symlink("victim.txt", temp); err != nil {
				t.Skip(err)
			}
		},
		"a link to nothing": func(t *testing.T, _ *fullEnv, temp string) {
			if err := os.Symlink("not-there.txt", temp); err != nil {
				t.Skip(err)
			}
		},
		// The scoped filesystem does not even say what such a link is: it
		// refuses to look at it. That used to be taken for "nothing there".
		"a link to a file outside the scope": func(t *testing.T, e *fullEnv, temp string) {
			outside := filepath.Join(e.root, "outside.txt")
			if err := os.WriteFile(outside, []byte("not bob's"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, temp); err != nil {
				t.Skip(err)
			}
		},
		"a link to nothing outside the scope": func(t *testing.T, e *fullEnv, temp string) {
			if err := os.Symlink(filepath.Join(e.root, "outside-not-there.txt"), temp); err != nil {
				t.Skip(err)
			}
		},
	}

	for name, occupy := range occupants {
		for _, existing := range []bool{true, false} {
			title := name + ", new file"
			if existing {
				title = name + ", overwrite"
			}
			t.Run(title, func(t *testing.T) {
				e := newFullEnv(t)
				tok := e.login("bob", fullEnvUserPassword)
				target := filepath.Join(e.bobDir, "report.txt")
				if existing {
					e.writeBobFile("report.txt", "the original content")
				}
				temp := filepath.Join(e.bobDir, filepath.Base(tusTempPath("/report.txt")))
				occupy(t, e, temp)
				before, err := os.Lstat(temp)
				if err != nil {
					t.Fatal(err)
				}

				code, _, body := e.do(http.MethodPost, "/api/tus/report.txt?override=true", tok, nil, map[string]string{"Upload-Length": "5"})
				if code != http.StatusInternalServerError {
					t.Errorf("tus create = %d %q, want 500", code, body)
				}

				// Nothing was touched, and no upload is under way.
				if existing {
					if data, _ := os.ReadFile(target); string(data) != "the original content" {
						t.Errorf("report.txt holds %q after the refused upload", data)
					}
				} else if pathExists(target) {
					t.Error("the refused upload created its destination")
				}
				if after, err := os.Lstat(temp); err != nil || after.Mode() != before.Mode() {
					t.Errorf("what had the temporary name was changed: %v, %v", after, err)
				}
				if victim := filepath.Join(e.bobDir, "victim.txt"); pathExists(victim) {
					if data, _ := os.ReadFile(victim); string(data) != "the victim's content" {
						t.Errorf("the file the temporary name links to holds %q", data)
					}
				}
				if pathExists(filepath.Join(e.bobDir, "not-there.txt")) || pathExists(filepath.Join(e.root, "outside-not-there.txt")) {
					t.Error("the upload created a file through the link")
				}
				if outside := filepath.Join(e.root, "outside.txt"); pathExists(outside) {
					if data, _ := os.ReadFile(outside); string(data) != "not bob's" {
						t.Errorf("the file outside the scope holds %q", data)
					}
				}
				if code, _, _ := e.do(http.MethodHead, "/api/tus/report.txt", tok, nil, nil); code != http.StatusNotFound {
					t.Errorf("HEAD of the refused upload = %d, want 404", code)
				}
				code, _, _ = e.do(http.MethodPatch, "/api/tus/report.txt", tok, strings.NewReader("HELLO"), map[string]string{
					"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0",
				})
				if code != http.StatusNotFound {
					t.Errorf("PATCH of the refused upload = %d, want 404", code)
				}
			})
		}
	}
}

// What did not change: a temporary file an earlier upload left behind is the
// server's own, and the next upload to the destination starts over in it.
func TestTusStartsOverInALeftoverTemporaryFile(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	e.writeBobFile("report.txt", "the original content")
	e.writeBobFile(filepath.Base(tusTempPath("/report.txt")), "left behind by an upload that never finished")

	code, hdr, body := e.do(http.MethodPost, "/api/tus/report.txt?override=true", tok, nil, map[string]string{"Upload-Length": "5"})
	if code != http.StatusCreated {
		t.Fatalf("tus create = %d %q", code, body)
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "report.txt")); string(data) != "the original content" {
		t.Errorf("report.txt holds %q while the upload is under way", data)
	}
	code, _, _ = e.do(http.MethodPatch, hdr.Get("Location"), tok, strings.NewReader("HELLO"), map[string]string{
		"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0",
	})
	if code != http.StatusNoContent {
		t.Fatalf("chunk = %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "report.txt")); string(data) != "HELLO" {
		t.Errorf("report.txt holds %q after the upload", data)
	}
	if temps := uploadTempsIn(t, e.bobDir); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}
