package fbhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/werewolfboy13/filebrowser/v2/diskcache"
	"github.com/werewolfboy13/filebrowser/v2/fileutils"
	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// failingBody yields some data and then fails, like the body of a request
// whose client went away.
type failingBody struct {
	data string
	done bool
}

func (f *failingBody) Read(p []byte) (int, error) {
	if !f.done {
		f.done = true
		return copy(p, f.data), nil
	}
	return 0, errors.New("simulated client disconnect")
}

func uploadTempsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var temps []string
	for _, entry := range entries {
		if fileutils.IsTempName(entry.Name()) {
			temps = append(temps, entry.Name())
		}
	}
	return temps
}

// An overwrite used to truncate the destination and stream the request body
// into it. When the body broke off (a dropped connection, a closed tab, a full
// disk), POST then deleted the file and PUT left it truncated: either way the
// original content was gone. The original must survive a failed overwrite.
func TestInterruptedOverwriteKeepsTheOriginal(t *testing.T) {
	scope := t.TempDir()
	original := filepath.Join(scope, "report.txt")
	if err := os.WriteFile(original, []byte("ORIGINAL CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)
	token := signToken(t, perm, key)

	requests := map[string]struct {
		method, target string
		handler        handleFunc
	}{
		"POST override": {http.MethodPost, "/report.txt?override=true", resourcePostHandler(diskcache.NewNoOp())},
		"PUT":           {http.MethodPut, "/report.txt", resourcePutHandler},
	}
	for name, rq := range requests {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(rq.method, rq.target, io.NopCloser(&failingBody{data: "new-partial"}))
			req.Header.Set("X-Auth", token)
			rec := httptest.NewRecorder()
			handle(rq.handler, "", st, &settings.Server{}).ServeHTTP(rec, req)

			if rec.Code < 400 {
				t.Errorf("interrupted %s = %d, want an error status", name, rec.Code)
			}
			data, err := os.ReadFile(original)
			if err != nil {
				t.Fatalf("the original file is gone: %v", err)
			}
			if string(data) != "ORIGINAL CONTENT" {
				t.Errorf("the original content was replaced by %q", data)
			}
			if temps := uploadTempsIn(t, scope); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		})
	}

	// An interrupted upload of a new file leaves nothing behind.
	req, _ := http.NewRequest(http.MethodPost, "/fresh.txt", io.NopCloser(&failingBody{data: "partial"}))
	req.Header.Set("X-Auth", token)
	rec := httptest.NewRecorder()
	handle(resourcePostHandler(diskcache.NewNoOp()), "", st, &settings.Server{}).ServeHTTP(rec, req)
	if entries, _ := os.ReadDir(scope); len(entries) != 1 {
		t.Errorf("an interrupted upload of a new file left %d entries in the directory, want only the original", len(entries))
	}
}

// Successful writes behave as before: the content is replaced, the ETag is
// reported, and the file keeps its own mode.
func TestOverwriteReplacesContentAndKeepsMode(t *testing.T) {
	e := newFullEnv(t)
	target := e.writeBobFile("doc.txt", "old")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	tok := e.login("bob", fullEnvUserPassword)

	for method, path := range map[string]string{http.MethodPut: "/api/resources/doc.txt", http.MethodPost: "/api/resources/doc.txt?override=true"} {
		content := "written by " + method
		code, hdr, body := e.do(method, path, tok, strings.NewReader(content), nil)
		if code != http.StatusOK {
			t.Fatalf("%s = %d %q", method, code, body)
		}
		if hdr.Get("ETag") == "" {
			t.Errorf("%s: no ETag header", method)
		}
		if data, _ := os.ReadFile(target); string(data) != content {
			t.Errorf("%s: content = %q", method, data)
		}
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: the mode of the overwritten file changed: %v, %v", method, info.Mode().Perm(), err)
		}
	}

	if code, _, body := e.do(http.MethodPost, "/api/resources/new/deep/file.txt", tok, strings.NewReader("x"), nil); code != http.StatusOK {
		t.Fatalf("upload of a new file = %d %q", code, body)
	}
	info, err := os.Stat(filepath.Join(e.bobDir, "new", "deep", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&^settings.DefaultFileMode != 0 {
		t.Errorf("a new file has mode %o, want at most %o", info.Mode().Perm(), settings.DefaultFileMode)
	}
	if temps := uploadTempsIn(t, e.bobDir); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}

// The temporary file an upload is written to is an internal detail: it is not
// listed, cannot be addressed through the API, and a leftover from a crash is
// removed by a later upload into the same directory.
func TestUploadTemporariesAreInternal(t *testing.T) {
	e := newFullEnv(t)
	recent := fileutils.TempPrefix + strings.Repeat("a", 32)
	stale := fileutils.TempPrefix + "tus-" + strings.Repeat("b", 32)
	e.writeBobFile("box/"+recent, "in flight")
	e.writeBobFile("box/"+stale, "abandoned")
	e.writeBobFile("box/visible.txt", "x")
	e.writeBobFile("box/.dotfile", "x")
	old := time.Now().Add(-2 * staleUploadAge)
	if err := os.Chtimes(filepath.Join(e.bobDir, "box", stale), old, old); err != nil {
		t.Fatal(err)
	}
	tok := e.login("bob", fullEnvUserPassword)

	_, _, listing := e.do(http.MethodGet, "/api/resources/box/", tok, nil, nil)
	var dir struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(listing), &dir); err != nil {
		t.Fatalf("listing: %v\n%s", err, listing)
	}
	names := map[string]bool{}
	for _, item := range dir.Items {
		names[item.Name] = true
	}
	if names[recent] || names[stale] {
		t.Errorf("upload temporaries are listed: %v", names)
	}
	if !names["visible.txt"] || !names[".dotfile"] {
		t.Errorf("ordinary files are missing from the listing: %v", names)
	}

	for _, rq := range []struct{ method, path string }{
		{http.MethodGet, "/api/raw/box/" + recent},
		{http.MethodGet, "/api/resources/box/" + recent},
		{http.MethodPut, "/api/resources/box/" + recent},
		{http.MethodPost, "/api/resources/box/" + recent + "?override=true"},
		{http.MethodDelete, "/api/resources/box/" + recent},
	} {
		if code, _, _ := e.do(rq.method, rq.path, tok, strings.NewReader("x"), nil); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", rq.method, rq.path, code)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "box", recent)); string(data) != "in flight" {
		t.Errorf("a temporary file was changed through the API: %q", data)
	}

	// An upload into the directory sweeps the abandoned temporary and leaves
	// the recent one, which may belong to a write in progress.
	if code, _, body := e.do(http.MethodPost, "/api/resources/box/new.txt", tok, strings.NewReader("x"), nil); code != http.StatusOK {
		t.Fatalf("upload = %d %q", code, body)
	}
	if pathExists(filepath.Join(e.bobDir, "box", stale)) {
		t.Error("the abandoned temporary file was not removed")
	}
	if !pathExists(filepath.Join(e.bobDir, "box", recent)) {
		t.Error("a recent temporary file was removed")
	}
}

// The temporary file of an upload was hidden, and refused by name, under its
// exact lower-case name only. On a filesystem that does not tell upper from
// lower case (the default on macOS and Windows) the same file answers to
// ".FBTMP-TUS-...", and the name of a resumable upload's temporary file can
// be worked out from the name of its destination: anyone with access to the
// directory could read, change or delete an upload in progress.
func TestUploadTemporariesAreInternalInAnySpelling(t *testing.T) {
	e := newFullEnv(t)
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)

	code, hdr, body := e.do(http.MethodPost, "/api/tus/secret.bin", bob, http.NoBody, map[string]string{"Upload-Length": "20"})
	if code != http.StatusCreated {
		t.Fatalf("tus create = %d %q", code, body)
	}
	code, _, _ = e.do(http.MethodPatch, hdr.Get("Location"), bob, strings.NewReader("CONFIDENTI"), map[string]string{
		"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0",
	})
	if code != http.StatusNoContent {
		t.Fatalf("first chunk = %d", code)
	}
	temps := uploadTempsIn(t, e.bobDir)
	if len(temps) != 1 {
		t.Fatalf("temporary files of the upload: %v", temps)
	}

	// What a case-insensitive filesystem makes of the other spellings is
	// played by files that carry them: the check is on the name, and must
	// not depend on what the disk does with it.
	spellings := []string{temps[0], strings.ToUpper(temps[0]), strings.Replace(temps[0], "tus", "TUS", 1)}
	for _, name := range spellings[1:] {
		if !e.server.CaseInsensitiveFs {
			e.writeBobFile(name, "CONFIDENTI")
		}
	}

	for _, name := range spellings {
		for _, rq := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/raw/users/bob/" + name, ""},
			{http.MethodGet, "/api/resources/users/bob/" + name, ""},
			{http.MethodPut, "/api/resources/users/bob/" + name, "CHANGED"},
			{http.MethodPost, "/api/resources/users/bob/" + name + "?override=true", "CHANGED"},
			{http.MethodDelete, "/api/resources/users/bob/" + name, ""},
			{http.MethodPatch, "/api/resources/users/bob/" + name + "?action=rename&destination=%2Fusers%2Fbob%2Ftaken.bin", ""},
		} {
			code, _, body := e.do(rq.method, rq.path, admin, strings.NewReader(rq.body), nil)
			if code != http.StatusForbidden || strings.Contains(body, "CONFIDENTI") {
				t.Errorf("%s %s = %d %q, want 403", rq.method, rq.path, code, body)
			}
		}
		if data, err := os.ReadFile(filepath.Join(e.bobDir, name)); err != nil || string(data) != "CONFIDENTI" {
			t.Errorf("%s holds %q (%v) after the requests", name, data, err)
		}
	}

	_, _, listing := e.do(http.MethodGet, "/api/resources/users/bob/", admin, nil, nil)
	_, _, search := e.do(http.MethodGet, "/api/search/users/bob/?query=fbtmp", admin, nil, nil)
	for _, name := range spellings {
		if strings.Contains(listing, name) || strings.Contains(search, name) {
			t.Errorf("%s is listed or found", name)
		}
	}
}
