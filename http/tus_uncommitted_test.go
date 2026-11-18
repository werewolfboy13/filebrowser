package fbhttp

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/filebrowser/filebrowser/v2/users"
)

// stuckFs is a filesystem on which, while stuck is set, a complete temporary
// file cannot be put in the place of an existing file: renaming fails, and
// so does opening a file that is not a temporary one to write into it.
type stuckFs struct {
	afero.Fs
	stuck *atomic.Bool
}

var errStuck = errors.New("input/output error")

func (f stuckFs) Rename(oldname, newname string) error {
	if f.stuck.Load() {
		return errStuck
	}
	return f.Fs.Rename(oldname, newname)
}

func (f stuckFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if f.stuck.Load() && flag&os.O_CREATE != 0 && !fileutils.IsTempName(filepath.Base(name)) {
		return nil, errStuck
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f stuckFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return f.Fs.(afero.Lstater).LstatIfPossible(name)
}

// tusHead sends a HEAD request for the upload at url.
func (f *tusTestFixture) head(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth", f.token)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// When the last chunk of an upload is written but putting the file in place
// fails (the disk, a quota, a network mount), the chunk is answered 500 and
// the complete temporary file is kept. A client that then asks how far the
// upload is was told offset = length, which TUS clients take to mean that the
// upload is done: the web interface reported a success, and the file was
// never stored.
func TestTusHeadDoesNotReportAnUploadThatWasNotStoredAsComplete(t *testing.T) {
	scope, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)
	stuck := &atomic.Bool{}
	st.Users.(*customFSUser).fs = afero.NewBasePathFs(stuckFs{Fs: afero.NewOsFs(), stuck: stuck}, scope)
	cache := newMemoryUploadCache()
	t.Cleanup(cache.Close)
	srv := newTusTestServer(t, st, cache)
	f := &tusTestFixture{srv: srv, client: srv.Client(), token: signToken(t, perm, key), scope: scope}

	// An upload that replaces a file, so that what is there afterwards
	// tells whether it was stored.
	if err := os.WriteFile(filepath.Join(scope, "report.pdf"), []byte("the old report"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, url := f.createOverride(t, "report.pdf", 10)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	res, _ := f.patch(t, url, 0, []byte("12345"))
	res.Body.Close()
	if head := f.head(t, url); head.StatusCode != http.StatusOK || head.Header.Get("Upload-Offset") != "5" {
		t.Fatalf("control: HEAD half way = %d, offset %q", head.StatusCode, head.Header.Get("Upload-Offset"))
	}

	// The last chunk arrives, and the file cannot be put in place.
	stuck.Store(true)
	res, _ = f.patch(t, url, 5, []byte("67890"))
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("the last chunk on a filesystem that cannot store the file = %d, want 500", res.StatusCode)
	}
	if got := f.read(t, "report.pdf"); got != "the old report" {
		t.Fatalf("control: report.pdf = %q after an upload that could not be stored", got)
	}

	// What the client does next: ask how far the upload is.
	head := f.head(t, url)
	if head.StatusCode != http.StatusInternalServerError {
		t.Errorf("HEAD of an upload that has all its data but was not stored = %d, want 500", head.StatusCode)
	}
	if offset := head.Header.Get("Upload-Offset"); offset != "" {
		t.Errorf("HEAD reports the offset %q of %q for an upload that was not stored", offset, head.Header.Get("Upload-Length"))
	}

	// While a request is working on the upload (the last chunk, still being
	// put in place), it is not known yet which way it goes: try again.
	release, ok := tusAcquireKey(filepath.Join(scope, "report.pdf"))
	if !ok {
		t.Fatal("the upload is held")
	}
	if head := f.head(t, url); head.StatusCode != http.StatusLocked || head.Header.Get("Upload-Offset") != "" {
		t.Errorf("HEAD while a request is working on the upload = %d, offset %q; want 423 and none", head.StatusCode, head.Header.Get("Upload-Offset"))
	}
	release()

	// The data is all there: once the filesystem works again, a chunk
	// without data at the end of the upload puts it in place.
	stuck.Store(false)
	res, _ = f.patch(t, url, 10, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("a chunk without data at the end of the upload = %d, want 204", res.StatusCode)
	}
	if got := f.read(t, "report.pdf"); got != "1234567890" {
		t.Errorf("report.pdf = %q", got)
	}
	if head := f.head(t, url); head.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD of the upload after it was stored = %d, want 404", head.StatusCode)
	}
	if temps := uploadTempsIn(t, scope); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}
