package fbhttp

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stalledPatch starts a PATCH whose body is fed by the returned pipe, and
// reports its status on the channel once the request ends.
func (f *tusTestFixture) stalledPatch(t *testing.T, url string, offset int) (*io.PipeWriter, <-chan int) {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPatch, url, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth", f.token)
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", strconv.Itoa(offset))

	status := make(chan int, 1)
	go func() {
		res, err := f.client.Do(req)
		if err != nil {
			status <- -1
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		status <- res.StatusCode
	}()
	return pw, status
}

// Two PATCH requests at the same offset used to pass the "file size equals
// offset" check together and then both append, leaving the file longer than
// the declared Upload-Length (GHSA-4r8p-gqj2-mwgm). Only one request at a time
// may work on an upload; the other is told to come back.
func TestTusConcurrentPatchesDoNotBothAppend(t *testing.T) {
	f := newTusTestFixture(t)
	url := f.create(t, "race.bin", 10)

	// Both requests are held in the handler, past any check made before the
	// body is read, and then released together.
	writers := make([]*io.PipeWriter, 2)
	statuses := make([]<-chan int, 2)
	for i := range writers {
		writers[i], statuses[i] = f.stalledPatch(t, url, 0)
	}
	time.Sleep(300 * time.Millisecond)

	var wg sync.WaitGroup
	for i, pw := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pw.Write(bytes.Repeat([]byte{byte('A' + i)}, 10))
			_ = pw.Close()
		}()
	}
	wg.Wait()

	codes := []int{<-statuses[0], <-statuses[1]}
	sort.Ints(codes)
	if codes[0] != http.StatusNoContent || codes[1] != http.StatusLocked {
		t.Errorf("statuses = %v, want one 204 and one 423", codes)
	}

	data, err := os.ReadFile(filepath.Join(f.scope, "race.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 10 {
		t.Fatalf("the file holds %d bytes %q, declared length 10", len(data), data)
	}
	if !bytes.Equal(data, bytes.Repeat(data[:1], 10)) {
		t.Errorf("the file mixes two requests: %q", data)
	}
}

// The serialisation must not let one client hold up another: a PATCH whose
// body stalls only blocks its own upload, and only for as long as it lasts.
func TestTusStalledPatchOnlyBlocksItsOwnUpload(t *testing.T) {
	f := newTusTestFixture(t)
	a := f.create(t, "a.bin", 8)
	b := f.create(t, "b.bin", 4)

	stalled, stalledStatus := f.stalledPatch(t, a, 0)
	time.Sleep(300 * time.Millisecond) // a's handler is now waiting for its body

	done := make(chan int, 1)
	go func() {
		res, _ := f.patch(t, b, 0, []byte("data"))
		res.Body.Close()
		done <- res.StatusCode
	}()
	select {
	case code := <-done:
		if code != http.StatusNoContent {
			t.Errorf("PATCH on another upload = %d, want 204", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a PATCH on another upload waited behind a stalled one")
	}

	// A second PATCH on the stalled upload is refused at once, not queued.
	res, _ := f.patch(t, a, 0, []byte("12345678"))
	res.Body.Close()
	if res.StatusCode != http.StatusLocked {
		t.Errorf("second PATCH on the stalled upload = %d, want 423", res.StatusCode)
	}

	// Once the stalled request ends (here: its client gives up), the upload
	// can be continued.
	_ = stalled.CloseWithError(io.ErrUnexpectedEOF)
	<-stalledStatus
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, _ := f.patch(t, a, 0, []byte("12345678"))
		res.Body.Close()
		if res.StatusCode == http.StatusNoContent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upload stayed locked after the stalled request ended: %d", res.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, _ := os.ReadFile(filepath.Join(f.scope, "a.bin")); string(data) != "12345678" {
		t.Errorf("a.bin = %q", data)
	}
}

// A directory that is reached through a symbolic link gives every file in it
// a second name. Two requests for one upload under its two names took
// different locks (the lock was on the name as written), while the temporary
// file they work on is one file on disk: a second request could empty it,
// or append to it, in the middle of the first one's chunk. They also looked
// the upload up under different keys, so the alias did not find it.
func TestTusRequestsThroughAnAliasAreOneUpload(t *testing.T) {
	e := newFullEnv(t)
	if err := os.Mkdir(filepath.Join(e.bobDir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(e.bobDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tok := e.login("bob", fullEnvUserPassword)
	chunk := func(offset int) map[string]string {
		return map[string]string{"Content-Type": "application/offset+octet-stream", "Upload-Offset": strconv.Itoa(offset)}
	}

	if code, _, body := e.do(http.MethodPost, "/api/tus/real/movie.bin", tok, nil, map[string]string{"Upload-Length": "20"}); code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, body)
	}
	if code, _, _ := e.do(http.MethodPatch, "/api/tus/real/movie.bin", tok, strings.NewReader("AAAAA"), chunk(0)); code != http.StatusNoContent {
		t.Fatalf("first chunk = %d", code)
	}

	// The other name finds the same upload.
	code, h, _ := e.do(http.MethodHead, "/api/tus/link/movie.bin", tok, nil, nil)
	if code != http.StatusOK || h.Get("Upload-Offset") != "5" || h.Get("Upload-Length") != "20" {
		t.Errorf("HEAD under the other name = %d, offset %q of %q; want 200, 5 of 20", code, h.Get("Upload-Offset"), h.Get("Upload-Length"))
	}

	// A chunk is on its way under one name...
	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPatch, e.srv.URL+"/api/tus/real/movie.bin", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Auth", tok)
	for k, v := range chunk(5) {
		req.Header.Set(k, v)
	}
	status := make(chan int, 1)
	go func() {
		res, err := e.srv.Client().Do(req)
		if err != nil {
			status <- -1
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		status <- res.StatusCode
	}()
	if _, err := pw.Write([]byte("BBBBB")); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(e.bobDir, "real", filepath.Base(tusTempPath("/real/movie.bin")))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(tmp); err == nil && info.Size() == 10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the chunk in flight never reached the temporary file")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// ... and every request for the upload under the other name is told to
	// come back, instead of working on the same file at the same time.
	for _, rq := range []struct {
		method, path, body string
		hdr                map[string]string
	}{
		{http.MethodPost, "/api/tus/link/movie.bin?override=true", "", map[string]string{"Upload-Length": "20"}},
		{http.MethodPatch, "/api/tus/link/movie.bin", "CCCCC", chunk(10)},
		{http.MethodDelete, "/api/tus/link/movie.bin", "", nil},
	} {
		if code, _, body := e.do(rq.method, rq.path, tok, strings.NewReader(rq.body), rq.hdr); code != http.StatusLocked {
			t.Errorf("%s %s while a chunk is in flight under the other name = %d %q, want 423", rq.method, rq.path, code, body)
		}
	}
	if info, err := os.Stat(tmp); err != nil || info.Size() != 10 {
		t.Errorf("the temporary file of the upload in flight: %v, %v; want its 10 bytes", info, err)
	}

	// The chunk in flight completes, and the upload goes on under either name.
	if _, err := pw.Write([]byte("BBBBB")); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	if code := <-status; code != http.StatusNoContent {
		t.Fatalf("the chunk in flight = %d, want 204", code)
	}
	if code, _, body := e.do(http.MethodPatch, "/api/tus/link/movie.bin", tok, strings.NewReader("CCCCC"), chunk(15)); code != http.StatusNoContent {
		t.Fatalf("last chunk under the other name = %d %q", code, body)
	}
	if data, err := os.ReadFile(filepath.Join(e.bobDir, "real", "movie.bin")); err != nil || string(data) != "AAAAABBBBBBBBBBCCCCC" {
		t.Errorf("the upload holds %q (%v)", data, err)
	}
	if temps := uploadTempsIn(t, filepath.Join(e.bobDir, "real")); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}
