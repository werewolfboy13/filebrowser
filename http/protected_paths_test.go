package fbhttp

import (
	"archive/tar"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/share"
	"github.com/filebrowser/filebrowser/v2/users"
)

// protectedEnv is an instance laid out the way the bare binary lays itself out
// by default: the database, the configuration file, the cache directory, the
// TLS key and the log all sit inside the directory being served, and both
// users have that directory as their scope.
func protectedEnv(t *testing.T, protect bool) *fullEnv {
	t.Helper()

	e := newFullEnvOpts(t, fullEnvOptions{
		database: "state/filebrowser.db",
		configure: func(root string, server *settings.Server) {
			for name, content := range map[string]string{
				"config/settings.json": `{"secret":"config"}`,
				"cache/ab/thumb":       "cached thumbnail",
				"tls/key.pem":          "PRIVATE KEY",
				"tls/cert.pem":         "certificate",
				"logs/filebrowser.log": "admin password: hunter2",
				"logs/filebrowser-2026-11-17T10-00-00.000.log": "older log",
				"logs/notes.txt":  "notes",
				"docs/readme.txt": "hello",
			} {
				p := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if !protect {
				return
			}
			for _, p := range []string{"state/filebrowser.db", "config/settings.json", "cache", "tls/key.pem"} {
				if !server.Protect(filepath.Join(root, filepath.FromSlash(p))) {
					t.Fatalf("%s was not registered", p)
				}
			}
			if !server.ProtectLog(filepath.Join(root, "logs", "filebrowser.log")) {
				t.Fatal("the log file was not registered")
			}
		},
	})
	e.setBob(func(u *users.User) { u.Scope = "/" })
	e.bobDir = e.root
	return e
}

// With the binary's own defaults the database lies inside every user's scope.
// Any user with the Download permission could fetch it, read the key that
// signs sessions out of it, and issue themselves an admin token.
func TestDatabaseInsideTheRootCannotBeDownloaded(t *testing.T) {
	e := protectedEnv(t, true)
	bob := e.login("bob", fullEnvUserPassword)

	if code, _, _ := e.do(http.MethodGet, "/api/users", bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("control: user list as a non-admin = %d, want 403", code)
	}

	code, _, stolen := e.do(http.MethodGet, "/api/raw/state/filebrowser.db", bob, nil, nil)
	if code != http.StatusForbidden {
		t.Errorf("download of the database = %d (%d bytes), want 403", code, len(stolen))
	}
	if code != http.StatusOK {
		return
	}

	// What the download is worth: the signing key, and with it any identity.
	copyPath := filepath.Join(t.TempDir(), "stolen.db")
	if err := os.WriteFile(copyPath, []byte(stolen), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := storm.Open(copyPath)
	if err != nil {
		t.Fatalf("the downloaded copy cannot be opened: %v", err)
	}
	defer db.Close()
	var set settings.Settings
	if err := db.Get("config", "settings", &set); err != nil {
		t.Fatal(err)
	}
	claims := &authToken{
		User: userInfo{ID: 1},
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(set.Key)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.do(http.MethodGet, "/api/users", forged, nil, nil); code == http.StatusOK {
		t.Error("a non-admin downloaded the database and forged a working admin token")
	}
}

// The server's own files are refused on every route that reads, writes, moves
// or publishes a file, for every user, by every name they can be reached by.
func TestServerFilesInsideTheRootAreRefusedEverywhere(t *testing.T) {
	e := protectedEnv(t, true)
	// Other names for the same files.
	if err := os.Symlink("state/filebrowser.db", filepath.Join(e.root, "alias.db")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("config", filepath.Join(e.root, "cfg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Link(filepath.Join(e.root, "tls", "key.pem"), filepath.Join(e.root, "docs", "hard.pem")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	protected := []string{
		"/state/filebrowser.db",
		"/config/settings.json",
		"/cache/ab/thumb",
		"/tls/key.pem",
		"/logs/filebrowser.log",
		"/logs/filebrowser-2026-11-17T10-00-00.000.log",
		"/alias.db",
		"/cfg/settings.json",
		"/docs/hard.pem",
	}
	secrets := []string{`"secret":"config"`, "cached thumbnail", "PRIVATE KEY", "hunter2", "older log"}
	leaks := func(body string) bool {
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				return true
			}
		}
		return false
	}

	for _, user := range []struct{ name, password string }{{"bob", fullEnvUserPassword}, {"admin", fullEnvAdminPassword}} {
		tok := e.login(user.name, user.password)

		for _, p := range protected {
			q := url.QueryEscape(p)
			requests := []struct{ method, path, body string }{
				{http.MethodGet, "/api/raw" + p, ""},
				{http.MethodGet, "/api/raw" + p + "?inline=true", ""},
				{http.MethodGet, "/api/resources" + p, ""},
				{http.MethodGet, "/api/resources" + p + "?checksum=md5", ""},
				{http.MethodPost, "/api/resources" + p + "?override=true", "overwritten"},
				{http.MethodPut, "/api/resources" + p, "overwritten"},
				{http.MethodDelete, "/api/resources" + p, ""},
				{http.MethodPatch, "/api/resources" + p + "?action=rename&destination=%2Fmoved", ""},
				{http.MethodPatch, "/api/resources" + p + "?action=copy&destination=%2Fcopied", ""},
				{http.MethodPatch, "/api/resources/docs/readme.txt?action=copy&override=true&destination=" + q, ""},
				{http.MethodPatch, "/api/resources/docs/readme.txt?action=rename&override=true&destination=" + q, ""},
				{http.MethodGet, "/api/preview/big" + p, ""},
				{http.MethodGet, "/api/preview/thumb" + p, ""},
				{http.MethodGet, "/api/subtitle" + p, ""},
				{http.MethodGet, "/api/usage" + p, ""},
				{http.MethodPost, "/api/tus" + p + "?override=true", ""},
				{http.MethodHead, "/api/tus" + p, ""},
				{http.MethodPatch, "/api/tus" + p, "data"},
				{http.MethodDelete, "/api/tus" + p, ""},
				{http.MethodPost, "/api/share" + p, "{}"},
			}
			for _, rq := range requests {
				code, _, body := e.do(rq.method, rq.path, tok, strings.NewReader(rq.body), map[string]string{
					"Upload-Length": "4", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream",
				})
				if code != http.StatusForbidden {
					t.Errorf("%s: %s %s = %d, want 403", user.name, rq.method, rq.path, code)
				}
				if leaks(body) {
					t.Errorf("%s: %s %s returned protected content", user.name, rq.method, rq.path)
				}
			}
		}

		// A directory that holds a protected file cannot be moved, copied or
		// deleted as a whole: the file would go with it, and a copy or a move
		// would put it at a path that is no longer protected.
		for _, dir := range []string{"/state", "/config", "/tls", "/logs", "/cache"} {
			for _, rq := range []struct{ method, path string }{
				{http.MethodDelete, "/api/resources" + dir},
				{http.MethodPatch, "/api/resources" + dir + "?action=rename&destination=%2Frenamed"},
				{http.MethodPatch, "/api/resources" + dir + "?action=copy&destination=%2Fduplicate"},
			} {
				if code, _, _ := e.do(rq.method, rq.path, tok, nil, nil); code != http.StatusForbidden {
					t.Errorf("%s: %s %s = %d, want 403", user.name, rq.method, rq.path, code)
				}
			}
		}

		// Listings, searches and archives leave the protected files out.
		for _, dir := range []string{"/", "/state/", "/config/", "/tls/", "/logs/", "/cfg/"} {
			_, _, listing := e.do(http.MethodGet, "/api/resources"+dir, tok, nil, nil)
			for _, name := range []string{"filebrowser.db", "settings.json", "key.pem", "filebrowser.log", "filebrowser-2026", `"cache"`, "alias.db"} {
				if strings.Contains(listing, name) {
					t.Errorf("%s: the listing of %s names %s", user.name, dir, name)
				}
			}
		}
		_, _, recursive := e.do(http.MethodGet, "/api/resources/recursive/", tok, nil, nil)
		_, _, search := e.do(http.MethodGet, "/api/search/?query=", tok, nil, nil)
		for _, name := range []string{"filebrowser.db", "settings.json", "key.pem", "filebrowser.log", "filebrowser-2026", "thumb", "alias.db", "hard.pem"} {
			if strings.Contains(recursive, name) {
				t.Errorf("%s: the recursive listing names %s", user.name, name)
			}
			if strings.Contains(search, name) {
				t.Errorf("%s: the search names %s", user.name, name)
			}
		}
		for _, target := range []string{"/api/raw/?algo=tar", "/api/raw/?algo=tar&files=" + url.QueryEscape("/state,/config,/tls,/logs,/cache,/docs")} {
			_, _, archive := e.do(http.MethodGet, target, tok, nil, nil)
			if leaks(archive) {
				t.Errorf("%s: %s contains protected content", user.name, target)
			}
			tr := tar.NewReader(strings.NewReader(archive))
			for {
				h, err := tr.Next()
				if err != nil {
					break
				}
				if strings.Contains(h.Name, "filebrowser.db") || strings.Contains(h.Name, "alias.db") {
					t.Errorf("%s: %s contains %s", user.name, target, h.Name)
				}
			}
		}

		// The files next to them are served as before.
		for path, want := range map[string]string{"/logs/notes.txt": "notes", "/docs/readme.txt": "hello", "/tls/cert.pem": "certificate"} {
			if code, _, body := e.do(http.MethodGet, "/api/raw"+path, tok, nil, nil); code != http.StatusOK || body != want {
				t.Errorf("%s: GET %s = %d %q, want %q", user.name, path, code, body, want)
			}
		}
	}

	// Nothing was changed, moved or created on the way.
	for path, want := range map[string]string{
		"config/settings.json": `{"secret":"config"}`,
		"tls/key.pem":          "PRIVATE KEY",
		"logs/filebrowser.log": "admin password: hunter2",
		"cache/ab/thumb":       "cached thumbnail",
		"docs/readme.txt":      "hello",
	} {
		if data, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(path))); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v", path, data, err)
		}
	}
	for _, created := range []string{"moved", "copied", "renamed", "duplicate"} {
		if pathExists(filepath.Join(e.root, created)) {
			t.Errorf("/%s was created", created)
		}
	}
	if info, err := os.Stat(filepath.Join(e.root, "state", "filebrowser.db")); err != nil || info.Size() == 0 {
		t.Errorf("the database was damaged: %v", err)
	}
	if links, err := e.st.Share.All(); err == nil && len(links) != 0 {
		t.Errorf("a share of a protected path was created: %+v", links[0])
	}
}

// A share that already points at one of the server's files, or at a directory
// holding one, does not serve it.
func TestServerFilesAreNotServedThroughPublicShares(t *testing.T) {
	e := protectedEnv(t, true)
	for hash, path := range map[string]string{"database": "/state/filebrowser.db", "statedir": "/state", "root": "/", "log": "/logs/filebrowser.log"} {
		if err := e.st.Share.Save(&share.Link{Hash: hash, Path: path, UserID: 2}); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{
		"database",
		"log",
		"statedir/filebrowser.db",
		"root/state/filebrowser.db",
		"root/config/settings.json",
		"root/tls/key.pem",
		"root/cache/ab/thumb",
		"root/logs/filebrowser-2026-11-17T10-00-00.000.log",
	} {
		for _, endpoint := range []string{"/api/public/dl/", "/api/public/share/"} {
			code, _, body := e.do(http.MethodGet, endpoint+path, "", nil, nil)
			if code != http.StatusForbidden {
				t.Errorf("anonymous GET %s%s = %d (%d bytes), want 403", endpoint, path, code, len(body))
			}
		}
	}

	for _, target := range []string{"/api/public/dl/root?algo=tar", "/api/public/dl/statedir?algo=tar", "/api/public/share/root", "/api/public/share/statedir"} {
		_, _, body := e.do(http.MethodGet, target, "", nil, nil)
		for _, secret := range []string{"PRIVATE KEY", "hunter2", "filebrowser.db", `"secret":"config"`} {
			if strings.Contains(body, secret) {
				t.Errorf("anonymous GET %s exposes %q", target, secret)
			}
		}
	}
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/root/docs/readme.txt", "", nil, nil); code != http.StatusOK || body != "hello" {
		t.Errorf("an ordinary file in the share = %d %q", code, body)
	}
}

// The identity check that catches other names of a protected file looked at the
// path a lexical reading of a link's target leads to. A link whose target
// climbs out of another link ("sub/../filebrowser.db", with sub leading into
// the directory of the database) got around it, and around the path check:
// the database, and with it the key that signs sessions, could be downloaded.
func TestServerFilesAreRefusedThroughLinksThatClimbOutOfALink(t *testing.T) {
	e := protectedEnv(t, true)
	for _, dir := range []string{"state/x", "pub", "docs/deep"} {
		if err := os.MkdirAll(filepath.Join(e.root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range [][2]string{
		{"pub/sub", "../state/x"},
		{"pub/alias", "sub/../filebrowser.db"}, // the kernel: state/filebrowser.db
		{"pub/statedir", "sub/.."},             // the kernel: state/
		{"pub/docs", "../docs/deep"},
		{"pub/cachealias", "docs/../../cache"}, // the kernel: cache/
		{"pub/keyalias", "docs/../../tls/key.pem"},
		{"pub/logalias", "docs/../../logs/filebrowser-2026-11-17T10-00-00.000.log"},
	} {
		if err := os.Symlink(l[1], filepath.Join(e.root, filepath.FromSlash(l[0]))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	database := filepath.Join(e.root, "state", "filebrowser.db")
	before, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{"cached thumbnail", "PRIVATE KEY", "older log"}

	if err := e.st.Share.Save(&share.Link{Hash: "pubshare", Path: "/pub", UserID: 2}); err != nil {
		t.Fatal(err)
	}

	for _, user := range []struct{ name, password string }{{"bob", fullEnvUserPassword}, {"admin", fullEnvAdminPassword}} {
		tok := e.login(user.name, user.password)

		if code, _, _ := e.do(http.MethodGet, "/api/raw/state/filebrowser.db", tok, nil, nil); code != http.StatusForbidden {
			t.Fatalf("control: %s: database download = %d, want 403", user.name, code)
		}

		for _, p := range []string{"/pub/alias", "/pub/statedir/filebrowser.db", "/pub/cachealias/ab/thumb", "/pub/keyalias", "/pub/logalias"} {
			for _, rq := range []struct{ method, path, body string }{
				{http.MethodGet, "/api/raw" + p, ""},
				{http.MethodGet, "/api/resources" + p, ""},
				{http.MethodGet, "/api/resources" + p + "?checksum=md5", ""},
				{http.MethodGet, "/api/preview/big" + p, ""},
				{http.MethodPut, "/api/resources" + p, "overwritten"},
				{http.MethodPost, "/api/resources" + p + "?override=true", "overwritten"},
				{http.MethodPost, "/api/tus" + p + "?override=true", ""},
				{http.MethodDelete, "/api/resources" + p, ""},
				{http.MethodPatch, "/api/resources" + p + "?action=copy&destination=%2Fcopied", ""},
				{http.MethodPost, "/api/share" + p, "{}"},
			} {
				code, _, body := e.do(rq.method, rq.path, tok, strings.NewReader(rq.body), map[string]string{"Upload-Length": "4"})
				if code != http.StatusForbidden {
					t.Errorf("%s: %s %s = %d (%d bytes), want 403", user.name, rq.method, rq.path, code, len(body))
				}
				for _, secret := range secrets {
					if strings.Contains(body, secret) {
						t.Errorf("%s: %s %s returned protected content", user.name, rq.method, rq.path)
					}
				}
			}
		}

		_, _, listing := e.do(http.MethodGet, "/api/resources/pub/", tok, nil, nil)
		_, _, statedir := e.do(http.MethodGet, "/api/resources/pub/statedir/", tok, nil, nil)
		_, _, recursive := e.do(http.MethodGet, "/api/resources/recursive/pub", tok, nil, nil)
		_, _, search := e.do(http.MethodGet, "/api/search/pub/?query=", tok, nil, nil)
		for _, name := range []string{`"alias"`, `"cachealias"`, `"keyalias"`, `"logalias"`, `"filebrowser.db"`, `"thumb"`} {
			for where, body := range map[string]string{"listing of /pub": listing, "listing of /pub/statedir": statedir, "recursive listing": recursive} {
				if strings.Contains(body, `"name":`+name) {
					t.Errorf("%s: the %s names %s", user.name, where, name)
				}
			}
			if strings.Contains(search, strings.Trim(name, `"`)+`"`) {
				t.Errorf("%s: the search names %s: %s", user.name, name, search)
			}
		}

		for _, target := range []string{"/api/raw/pub/?algo=tar", "/api/raw/?algo=tar&files=" + url.QueryEscape("/pub/alias,/pub/statedir,/pub/cachealias")} {
			_, _, archive := e.do(http.MethodGet, target, tok, nil, nil)
			tr := tar.NewReader(strings.NewReader(archive))
			for {
				h, err := tr.Next()
				if err != nil {
					break
				}
				for _, name := range []string{"alias", "filebrowser.db", "thumb", "key.pem"} {
					if strings.HasSuffix(h.Name, name) {
						t.Errorf("%s: %s contains %s", user.name, target, h.Name)
					}
				}
			}
			for _, secret := range secrets {
				if strings.Contains(archive, secret) {
					t.Errorf("%s: %s contains protected content", user.name, target)
				}
			}
		}
	}

	// And for a visitor of a public share of the directory holding the links.
	for _, sub := range []string{"/alias", "/statedir/filebrowser.db", "/cachealias/ab/thumb", "/keyalias"} {
		for _, endpoint := range []string{"/api/public/dl/", "/api/public/share/"} {
			code, _, body := e.do(http.MethodGet, endpoint+"pubshare"+sub, "", nil, nil)
			if code != http.StatusForbidden {
				t.Errorf("anonymous GET %spubshare%s = %d (%d bytes), want 403", endpoint, sub, code, len(body))
			}
		}
	}
	_, _, archive := e.do(http.MethodGet, "/api/public/dl/pubshare?algo=tar", "", nil, nil)
	for _, secret := range secrets {
		if strings.Contains(archive, secret) {
			t.Errorf("the archive of the share contains protected content")
		}
	}

	after, err := os.Stat(database)
	if err != nil || after.Size() != before.Size() {
		t.Errorf("the database was changed through an alias: %v", err)
	}
	for path, want := range map[string]string{"tls/key.pem": "PRIVATE KEY", "cache/ab/thumb": "cached thumbnail"} {
		if data, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(path))); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v", path, data, err)
		}
	}
	if pathExists(filepath.Join(e.root, "copied")) {
		t.Error("/copied was created")
	}
	if links, err := e.st.Share.All(); err == nil && len(links) != 1 {
		t.Errorf("a share of a protected path was created: %d shares", len(links))
	}
}

// Without anything registered (the server's files are outside the root, as in
// the container image) nothing is refused and nothing is looked up.
func TestNothingIsProtectedByDefault(t *testing.T) {
	e := protectedEnv(t, false)
	tok := e.login("bob", fullEnvUserPassword)
	if e.server.HasProtectedPaths() {
		t.Fatal("paths are registered without Protect having been called")
	}
	if code, _, body := e.do(http.MethodGet, "/api/raw/tls/key.pem", tok, nil, nil); code != http.StatusOK || body != "PRIVATE KEY" {
		t.Errorf("GET of an unregistered file = %d %q", code, body)
	}
}
