package fbhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/asdine/storm/v3"

	"github.com/filebrowser/filebrowser/v2/auth"
	"github.com/filebrowser/filebrowser/v2/diskcache"
	"github.com/filebrowser/filebrowser/v2/img"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/storage"
	"github.com/filebrowser/filebrowser/v2/storage/bolt"
	"github.com/filebrowser/filebrowser/v2/users"
)

const (
	fullEnvAdminPassword = "correct-horse-battery-1"
	fullEnvUserPassword  = "another-long-password-2"
)

// fullEnvHashes hashes the passwords of the two accounts, once for all the
// instances of a test run: a hash is what takes long in setting one up, most
// of all under the race detector, and it is the same work every time.
var fullEnvHashes = sync.OnceValues(func() (hashes [2]string, err error) {
	for i, password := range []string{fullEnvAdminPassword, fullEnvUserPassword} {
		if hashes[i], err = users.HashPwd(password); err != nil {
			return hashes, err
		}
	}
	return hashes, nil
})

// fullEnv is a production-like instance for regression tests: real bolt
// storage, the real users store (so user.Fs is the ScopedFs built by
// users.User.Clean, as in production), JSON auth and the complete router from
// NewHandler, served over a real listener.
//
// It holds two accounts: "admin" (ID 1, scope "/") and "bob" (ID 2, scope
// "/users/bob", every permission except admin).
type fullEnv struct {
	t      *testing.T
	root   string
	bobDir string
	st     *storage.Storage
	srv    *httptest.Server
	server *settings.Server
	limits *limits
	cache  *memoryUploadCache
}

func newFullEnv(t *testing.T) *fullEnv {
	t.Helper()
	return newFullEnvWith(t, nil)
}

// newFullEnvWith is newFullEnv with a hook that can adjust the server settings
// (and prepare the root directory) before the handler is built.
func newFullEnvWith(t *testing.T, configure func(root string, server *settings.Server)) *fullEnv {
	t.Helper()
	return newFullEnvOpts(t, fullEnvOptions{configure: configure})
}

type fullEnvOptions struct {
	// database is the path of the database below the root. Left empty, the
	// database lives outside the root, as in the official container layout.
	database string

	// configure can adjust the server settings, and prepare the root
	// directory, before the handler is built. Root is already set.
	configure func(root string, server *settings.Server)

	// assets replaces the embedded frontend build.
	assets fstest.MapFS
}

func newFullEnvOpts(t *testing.T, opts fullEnvOptions) *fullEnv {
	t.Helper()

	// Resolve symlinks in the temporary directory itself (macOS keeps it under
	// a symlinked /var), so real paths compare equal in tests.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bobDir := filepath.Join(root, "users", "bob")
	if err := os.MkdirAll(bobDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "db")
	if opts.database != "" {
		dbPath = filepath.Join(root, filepath.FromSlash(opts.database))
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}

	key, err := settings.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{
		Key:        key,
		AuthMethod: auth.MethodJSONAuth,
		FileMode:   settings.DefaultFileMode,
		DirMode:    settings.DefaultDirMode,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Auth.Save(&auth.JSONAuth{}); err != nil {
		t.Fatal(err)
	}
	server := &settings.Server{Root: root, EnableThumbnails: true, ResizePreview: true}
	if opts.configure != nil {
		opts.configure(root, server)
	}
	if err := st.Settings.SaveServer(server); err != nil {
		t.Fatal(err)
	}

	all := users.Permissions{Create: true, Rename: true, Modify: true, Delete: true, Share: true, Download: true}
	adminPerm := all
	adminPerm.Admin = true
	hashes, err := fullEnvHashes()
	if err != nil {
		t.Fatal(err)
	}
	adminHash, userHash := hashes[0], hashes[1]
	if err := st.Users.Save(&users.User{Username: "admin", Password: adminHash, Scope: "/", Perm: adminPerm}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Save(&users.User{Username: "bob", Password: userHash, Scope: "/users/bob", Perm: all}); err != nil {
		t.Fatal(err)
	}

	assets := opts.assets
	if assets == nil {
		assets = fstest.MapFS{
			"public/index.html": &fstest.MapFile{Data: []byte("<html><script>window.FileBrowser = [{[ .Json ]}];</script></html>")},
		}
	}
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	limits := newLimits()
	h, err := newHandler(img.New(1), diskcache.NewNoOp(), cache, st, server, assets, limits)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &fullEnv{t: t, root: root, bobDir: bobDir, st: st, srv: srv, server: server, limits: limits, cache: cache}
}

// do sends a request and returns the status, headers and body. A transport
// error (for example a connection dropped by a panicking handler) is reported
// as status -1 with the error text as the body.
func (e *fullEnv) do(method, path, token string, body io.Reader, hdr map[string]string) (int, http.Header, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Auth", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		return -1, nil, err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

// tryLogin posts the credentials and returns the status and the body (the
// token on success).
func (e *fullEnv) tryLogin(user, password string) (int, http.Header, string) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": password})
	return e.do(http.MethodPost, "/api/login", "", bytes.NewReader(body), nil)
}

func (e *fullEnv) login(user, password string) string {
	e.t.Helper()
	code, _, tok := e.tryLogin(user, password)
	if code != http.StatusOK {
		e.t.Fatalf("login %s failed: %d %s", user, code, tok)
	}
	return tok
}

// setBob replaces bob's stored record fields through the store, bypassing the
// HTTP API, so a test can shape the account without depending on the handler
// under test.
func (e *fullEnv) setBob(mutate func(u *users.User)) {
	e.t.Helper()
	u, err := e.st.Users.Get(e.root, false, uint(2))
	if err != nil {
		e.t.Fatal(err)
	}
	mutate(u)
	if err := e.st.Users.Save(u); err != nil {
		e.t.Fatal(err)
	}
}

func (e *fullEnv) writeBobFile(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.bobDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
