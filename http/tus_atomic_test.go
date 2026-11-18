package fbhttp

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/werewolfboy13/filebrowser/v2/users"
)

// recordingUploadCache is an upload cache that keeps the removal callback of
// every registered upload, so a test can run the cleanup of an abandoned
// upload without waiting for the entry to expire.
type recordingUploadCache struct {
	UploadCache
	mu      sync.Mutex
	removal map[string]func() error
}

func (c *recordingUploadCache) Register(filePath string, upload uploadInfo, remove func() error) {
	c.mu.Lock()
	c.removal[filePath] = remove
	c.mu.Unlock()
	c.UploadCache.Register(filePath, upload, remove)
}

func (c *recordingUploadCache) Complete(filePath string) {
	c.mu.Lock()
	delete(c.removal, filePath)
	c.mu.Unlock()
	c.UploadCache.Complete(filePath)
}

// abandon runs the cleanup the cache performs when an upload expires.
func (c *recordingUploadCache) abandon(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.removal) == 0 {
		t.Fatal("no upload was registered")
	}
	for path, remove := range c.removal {
		// As when an upload expires: it is gone from the cache by the time
		// its cleanup runs.
		c.UploadCache.Complete(path)
		if remove != nil {
			_ = remove()
		}
	}
	c.removal = map[string]func() error{}
}

// tusOverrideFixture is a TUS fixture whose user may create and modify files,
// but not delete them, and whose cache cleanup can be triggered by the test.
func newTusOverrideFixture(t *testing.T) (*tusTestFixture, *recordingUploadCache) {
	t.Helper()

	scope := t.TempDir()
	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)

	memory := newMemoryUploadCache()
	t.Cleanup(memory.Close)
	cache := &recordingUploadCache{UploadCache: memory, removal: map[string]func() error{}}

	srv := newTusTestServer(t, st, cache)
	return &tusTestFixture{srv: srv, client: srv.Client(), token: signToken(t, perm, key), scope: scope}, cache
}

// createOverride sends the TUS creation request for an existing file.
func (f *tusTestFixture) createOverride(t *testing.T, name string, length int) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/tus/"+name+"?override=true", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth", f.token)
	req.Header.Set("Upload-Length", strconv.Itoa(length))
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, f.srv.URL + res.Header.Get("Location")
}

func (f *tusTestFixture) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.scope, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(data)
}

// A TUS upload that overwrites a file used to truncate it when the upload was
// created. If the upload was then abandoned, the cache cleanup deleted the
// file after three minutes: an interrupted replace destroyed the original, and
// a user without the Delete permission could delete files. The original must
// stay as it is until the upload is complete.
func TestTusAbandonedOverrideKeepsTheOriginal(t *testing.T) {
	f, cache := newTusOverrideFixture(t)
	target := filepath.Join(f.scope, "important.txt")
	if err := os.WriteFile(target, []byte("IMPORTANT ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, url := f.createOverride(t, "important.txt", 10)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if got := f.read(t, "important.txt"); got != "IMPORTANT ORIGINAL" {
		t.Fatalf("creating the upload changed the file to %q", got)
	}

	// Half of the data arrives, then the client disappears.
	res, _ := f.patch(t, url, 0, []byte("12345"))
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("first chunk = %d", res.StatusCode)
	}
	if got := f.read(t, "important.txt"); got != "IMPORTANT ORIGINAL" {
		t.Fatalf("a partial upload changed the file to %q", got)
	}

	cache.abandon(t)

	if got := f.read(t, "important.txt"); got != "IMPORTANT ORIGINAL" {
		t.Errorf("after the abandoned upload was cleaned up the file holds %q", got)
	}
	if entries, _ := os.ReadDir(f.scope); len(entries) != 1 {
		t.Errorf("the cleanup left %d entries in the directory, want only the original", len(entries))
	}
}

// A completed overwrite replaces the content and nothing else.
func TestTusOverrideReplacesTheFileOnCompletion(t *testing.T) {
	f, _ := newTusOverrideFixture(t)
	target := filepath.Join(f.scope, "doc.txt")
	if err := os.WriteFile(target, []byte("old content, longer than the new"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, url := f.createOverride(t, "doc.txt", 8)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}

	// HEAD reports the progress of the upload, not the size of the original.
	head := func() string {
		req, _ := http.NewRequest(http.MethodHead, url, http.NoBody)
		req.Header.Set("X-Auth", f.token)
		res, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Upload-Offset")
	}
	if offset := head(); offset != "0" {
		t.Errorf("Upload-Offset after create = %q, want 0", offset)
	}

	res, _ := f.patch(t, url, 0, []byte("1234"))
	res.Body.Close()
	if offset := head(); offset != "4" {
		t.Errorf("Upload-Offset after the first chunk = %q, want 4", offset)
	}
	res, _ = f.patch(t, url, 4, []byte("5678"))
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("last chunk = %d", res.StatusCode)
	}

	if got := f.read(t, "doc.txt"); got != "12345678" {
		t.Errorf("content = %q", got)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the mode of the overwritten file changed: %v %v", info.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(f.scope); len(entries) != 1 {
		t.Errorf("%d entries in the directory after the upload, want 1", len(entries))
	}
}

// A new file only appears once its upload is complete, and an upload through
// a symbolic link still writes to the link's target.
func TestTusUploadTargets(t *testing.T) {
	f, cache := newTusOverrideFixture(t)

	url := f.create(t, "new.bin", 6)
	res, _ := f.patch(t, url, 0, []byte("abc"))
	res.Body.Close()
	if pathExists(filepath.Join(f.scope, "new.bin")) {
		t.Error("the destination exists before the upload is complete")
	}
	res, _ = f.patch(t, url, 3, []byte("def"))
	res.Body.Close()
	if got := f.read(t, "new.bin"); got != "abcdef" {
		t.Errorf("new.bin = %q", got)
	}

	// While an upload of a new file is in progress, a second one to the same
	// path is a conflict unless it asks to override.
	url = f.create(t, "busy.bin", 6)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/tus/busy.bin?override=false", http.NoBody)
	req.Header.Set("X-Auth", f.token)
	req.Header.Set("Upload-Length", "6")
	if res, err := f.client.Do(req); err != nil || res.StatusCode != http.StatusConflict {
		t.Errorf("second create for an upload in progress = %v, %v; want 409", res.StatusCode, err)
	} else {
		res.Body.Close()
	}
	res, _ = f.patch(t, url, 0, []byte("abcdef"))
	res.Body.Close()
	if got := f.read(t, "busy.bin"); got != "abcdef" {
		t.Errorf("busy.bin = %q", got)
	}

	// An abandoned upload of a new file leaves nothing.
	url = f.create(t, "gone.bin", 6)
	res, _ = f.patch(t, url, 0, []byte("abc"))
	res.Body.Close()
	cache.abandon(t)
	if entries, _ := os.ReadDir(f.scope); len(entries) != 2 {
		t.Errorf("%d entries after an abandoned upload of a new file, want 2", len(entries))
	}

	if err := os.WriteFile(filepath.Join(f.scope, "real.txt"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(f.scope, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	code, url := f.createOverride(t, "link.txt", 4)
	if code != http.StatusCreated {
		t.Fatalf("create through a symlink = %d", code)
	}
	res, _ = f.patch(t, url, 0, []byte("data"))
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH through a symlink = %d", res.StatusCode)
	}
	if info, err := os.Lstat(filepath.Join(f.scope, "link.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symbolic link was replaced (%v)", err)
	}
	if got := f.read(t, "real.txt"); got != "data" {
		t.Errorf("the link target holds %q", got)
	}
}

// An empty upload has no PATCH: the creation request is the whole upload. The
// file has to exist afterwards and must not be registered as an upload in
// progress, whose cleanup would delete it.
func TestTusEmptyUpload(t *testing.T) {
	f, cache := newTusOverrideFixture(t)

	f.create(t, "empty.txt", 0)
	if got := f.read(t, "empty.txt"); got != "" {
		t.Errorf("empty.txt = %q", got)
	}
	if len(cache.removal) != 0 {
		t.Error("an empty upload was registered as in progress")
	}

	if err := os.WriteFile(filepath.Join(f.scope, "full.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.createOverride(t, "full.txt", 0); code != http.StatusCreated {
		t.Fatalf("empty override = %d", code)
	}
	if got := f.read(t, "full.txt"); got != "" {
		t.Errorf("full.txt = %q, want it emptied", got)
	}
	if entries, _ := os.ReadDir(f.scope); len(entries) != 2 {
		t.Errorf("%d entries in the directory, want 2", len(entries))
	}
}

// A creation request that is refused must not have touched the file.
func TestTusRefusedCreateKeepsTheOriginal(t *testing.T) {
	f, _ := newTusOverrideFixture(t)
	if err := os.WriteFile(filepath.Join(f.scope, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, length := range []string{"", "-1", "abc"} {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/tus/keep.txt?override=true", http.NoBody)
		req.Header.Set("X-Auth", f.token)
		if length != "" {
			req.Header.Set("Upload-Length", length)
		}
		res, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("create with Upload-Length %q = %d, want 400", length, res.StatusCode)
		}
		if got := f.read(t, "keep.txt"); got != "keep" {
			t.Fatalf("a refused create (Upload-Length %q) changed the file to %q", length, got)
		}
	}
	if entries, _ := os.ReadDir(f.scope); len(entries) != 1 {
		t.Errorf("refused creates left %d entries, want 1", len(entries))
	}
}
