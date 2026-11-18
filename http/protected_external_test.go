package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
)

// With followExternalSymlinks, a symbolic link inside the served directory
// is followed wherever it leads, and the server's own files were only
// refused when they were inside that directory. A link to the database, as
// someone with access to the host can make it, served the database: the key
// that signs sessions and every password hash.
func TestServerFilesOutsideTheRootAreRefusedThroughExternalLinks(t *testing.T) {
	// Where the official image keeps them: next to the served directory.
	state := t.TempDir()
	for name, content := range map[string]string{
		"filebrowser.db":   "DATABASE",
		"cache/ab/thumb":   "cached thumbnail",
		"other/notes.txt":  "not the server's",
		"other/readme.txt": "hello",
	} {
		p := filepath.Join(state, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	e := newFullEnvWith(t, func(_ string, server *settings.Server) {
		server.FollowExternalSymlinks = true
		for _, p := range []string{"filebrowser.db", "cache"} {
			// Not inside the root, which is what Protect reports.
			if server.Protect(filepath.Join(state, p)) {
				t.Fatalf("%s is reported as inside the served directory", p)
			}
		}
	})
	for link, target := range map[string]string{
		"db-link":    filepath.Join(state, "filebrowser.db"),
		"state-link": state,
		"cache-link": filepath.Join(state, "cache"),
		"other-link": filepath.Join(state, "other"),
	} {
		if err := os.Symlink(target, filepath.Join(e.bobDir, link)); err != nil {
			t.Skip(err)
		}
	}
	tok := e.login("bob", fullEnvUserPassword)

	for _, path := range []string{
		"/db-link",
		"/state-link/filebrowser.db",
		"/cache-link/ab/thumb",
		"/state-link/cache/ab/thumb",
	} {
		for _, rq := range []struct{ method, route, query, body string }{
			{http.MethodGet, "/api/raw", "", ""},
			{http.MethodGet, "/api/resources", "", ""},
			{http.MethodPut, "/api/resources", "", "OVERWRITTEN"},
			{http.MethodDelete, "/api/resources", "", ""},
			{http.MethodPatch, "/api/resources", "?action=copy&destination=%2Fcopy.bin", ""},
		} {
			code, _, body := e.do(rq.method, rq.route+path+rq.query, tok, strings.NewReader(rq.body), nil)
			if code != http.StatusForbidden || strings.Contains(body, "DATABASE") || strings.Contains(body, "cached thumbnail") {
				t.Errorf("%s %s%s = %d %.60q, want 403", rq.method, rq.route, path, code, body)
			}
		}
	}
	if pathExists(filepath.Join(e.bobDir, "copy.bin")) {
		t.Error("a file of the server was copied")
	}
	for name, want := range map[string]string{"filebrowser.db": "DATABASE", "cache/ab/thumb": "cached thumbnail"} {
		if data, err := os.ReadFile(filepath.Join(state, filepath.FromSlash(name))); err != nil || string(data) != want {
			t.Errorf("%s holds %q (%v) after the requests", name, data, err)
		}
	}

	// A directory that holds one of them is not copied as a whole.
	code, _, body := e.do(http.MethodPatch, "/api/resources/state-link?action=copy&destination=%2Fstate-copy", tok, nil, nil)
	if code != http.StatusForbidden {
		t.Errorf("copy of the directory that holds the database = %d %q, want 403", code, body)
	}

	// What is not the server's is served through such a link as before:
	// that is what the option is for.
	for _, path := range []string{"/other-link/notes.txt", "/state-link/other/readme.txt"} {
		if code, _, body := e.do(http.MethodGet, "/api/raw"+path, tok, nil, nil); code != http.StatusOK {
			t.Errorf("GET %s = %d %q, want 200", path, code, body)
		}
	}
}
