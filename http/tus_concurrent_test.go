package fbhttp

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
