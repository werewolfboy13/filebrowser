package fbhttp

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/werewolfboy13/filebrowser/v3/users"
)

// sweepPastTheAbandonedAge lets the sweeper of abandoned uploads look, and
// look again after longer than an upload may go without being written to.
func sweepPastTheAbandonedAge(clock *fakeClock) {
	clock.Advance(tusSweepInterval)
	tusTemps.sweep()
	clock.Advance(tusAbandonedAge + time.Minute)
	tusTemps.sweep()
}

// tusChunk is the headers of a PATCH at offset.
func tusChunk(offset int) map[string]string {
	return map[string]string{"Content-Type": "application/offset+octet-stream", "Upload-Offset": strconv.Itoa(offset)}
}

// The sweeper took the temporary file of every upload that had not been
// written to for an hour, also of one that was still registered: a client
// that only asks how far its upload is (HEAD), or a chunk that stalls, keeps
// an upload registered for as long as it likes. What the upload did next is
// the test below. An upload that is registered is not abandoned.
func TestTusSweepLeavesTheFileOfARegisteredUpload(t *testing.T) {
	for name, wrap := range map[string]func(*memoryUploadCache) UploadCache{
		"a cache that removes nothing itself (Redis)": func(c *memoryUploadCache) UploadCache { return redisLikeUploadCache{c} },
		"the memory cache": func(c *memoryUploadCache) UploadCache { return c },
	} {
		t.Run(name, func(t *testing.T) {
			clock := useTusSweepClock(t)
			scope := t.TempDir()
			key := []byte("test-signing-key")
			perm := users.Permissions{Create: true, Modify: true, Download: true}
			st := scopedUserStorage(t, scope, perm, key)
			mem := newMemoryUploadCache()
			t.Cleanup(mem.Close)
			srv := newTusTestServer(t, st, wrap(mem))
			f := &tusTestFixture{srv: srv, client: srv.Client(), token: signToken(t, perm, key), scope: scope}

			url := f.create(t, "movie.mkv", 1000)
			res, _ := f.patch(t, url, 0, bytes.Repeat([]byte("x"), 500))
			res.Body.Close()

			// Quiet for longer than an abandoned upload is given, twice over,
			// but registered all along.
			sweepPastTheAbandonedAge(clock)
			sweepPastTheAbandonedAge(clock)
			if temps := uploadTempsIn(t, scope); len(temps) != 1 {
				t.Fatalf("temporary files of an upload that is still registered, after the sweep: %v", temps)
			}

			// It goes on where it was, and ends up complete.
			req, _ := http.NewRequest(http.MethodHead, url, http.NoBody)
			req.Header.Set("X-Auth", f.token)
			head, err := f.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			head.Body.Close()
			if head.StatusCode != http.StatusOK || head.Header.Get("Upload-Offset") != "500" {
				t.Fatalf("HEAD = %d, offset %q; want 200 and 500", head.StatusCode, head.Header.Get("Upload-Offset"))
			}
			res, _ = f.patch(t, url, 500, bytes.Repeat([]byte("y"), 500))
			res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("last chunk = %d", res.StatusCode)
			}
			if got := f.read(t, "movie.mkv"); got != strings.Repeat("x", 500)+strings.Repeat("y", 500) {
				t.Errorf("movie.mkv holds %d bytes, not what was uploaded", len(got))
			}

			// Once the upload is no longer registered, its file is abandoned.
			url = f.create(t, "gone.mkv", 1000)
			res, _ = f.patch(t, url, 0, bytes.Repeat([]byte("x"), 500))
			res.Body.Close()
			mem.cache.DeleteAll()
			sweepPastTheAbandonedAge(clock)
			if temps := uploadTempsIn(t, scope); len(temps) != 0 {
				t.Errorf("temporary files of an upload that is no longer registered: %v", temps)
			}
		})
	}
}

// An upload writes to a temporary file, which takes the place of the
// destination when the upload is complete: that is where it is decided
// whether the upload may replace what is there. When the temporary file was
// gone (the sweeper above took it, or anything else did), the requests of the
// upload fell back to the destination itself: HEAD reported its size as the
// offset, and PATCH appended to it, without asking whether the upload may
// replace anything.
//
// So a user who may only create files could append to a file somebody else
// stored at the path of their upload, and an upload that was to replace a
// file left it as its old content followed by the tail of the new one.
func TestTusUploadWithoutItsFileNeverWritesToTheDestination(t *testing.T) {
	const adminContent = "ADMIN REPORT"

	// createOnly is an instance in which bob may create files and nothing
	// else, with an upload of his to /report.pdf registered.
	createOnly := func(t *testing.T) (e *fullEnv, bob, admin, tmp, target string) {
		t.Helper()
		e = newFullEnv(t)
		e.setBob(func(u *users.User) { u.Perm = users.Permissions{Create: true, Download: true} })
		bob = e.login("bob", fullEnvUserPassword)
		admin = e.login("admin", fullEnvAdminPassword)
		if code, _, body := e.do(http.MethodPost, "/api/tus/report.pdf", bob, http.NoBody, map[string]string{"Upload-Length": "1000000"}); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		return e, bob, admin, filepath.Join(e.bobDir, filepath.Base(tusTempPath("/report.pdf"))), filepath.Join(e.bobDir, "report.pdf")
	}
	adminStores := func(t *testing.T, e *fullEnv, admin string) {
		t.Helper()
		if code, _, body := e.do(http.MethodPost, "/api/resources/users/bob/report.pdf", admin, strings.NewReader(adminContent), nil); code != http.StatusOK {
			t.Fatalf("admin stores report.pdf = %d %q", code, body)
		}
	}
	untouched := func(t *testing.T, target, want string) {
		t.Helper()
		if data, err := os.ReadFile(target); err != nil || string(data) != want {
			t.Errorf("the destination holds %q (%v), want %q", data, err, want)
		}
	}

	// As it happened: somebody stores the file while the upload is
	// registered, the upload goes quiet, and the sweeper comes by.
	t.Run("the sweeper comes by, then the upload goes on", func(t *testing.T) {
		clock := useTusSweepClock(t)
		e, bob, admin, _, target := createOnly(t)
		adminStores(t, e, admin)
		sweepPastTheAbandonedAge(clock)

		code, h, _ := e.do(http.MethodHead, "/api/tus/report.pdf", bob, nil, nil)
		if code != http.StatusOK || h.Get("Upload-Offset") != "0" {
			t.Errorf("HEAD = %d, offset %q; want the offset of the upload itself (200, 0)", code, h.Get("Upload-Offset"))
		}
		if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader(" <appended by bob>"), tusChunk(len(adminContent))); code == http.StatusNoContent {
			t.Errorf("a chunk at the size of the file at the destination was accepted")
		}
		untouched(t, target, adminContent)
	})

	for _, method := range []string{http.MethodHead, http.MethodPatch, http.MethodDelete} {
		t.Run("the temporary file is gone: "+method, func(t *testing.T) {
			e, bob, admin, tmp, target := createOnly(t)
			adminStores(t, e, admin)
			if err := os.Remove(tmp); err != nil {
				t.Fatal(err)
			}

			var hdr map[string]string
			var body *strings.Reader
			if method == http.MethodPatch {
				hdr, body = tusChunk(len(adminContent)), strings.NewReader(" <appended by bob>")
			} else {
				body = strings.NewReader("")
			}
			code, h, _ := e.do(method, "/api/tus/report.pdf", bob, body, hdr)
			if code != http.StatusNotFound {
				t.Errorf("%s for an upload whose temporary file is gone = %d, want 404", method, code)
			}
			if offset := h.Get("Upload-Offset"); offset != "" {
				t.Errorf("%s reports the offset %q", method, offset)
			}
			untouched(t, target, adminContent)

			// At the start of the file neither.
			if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("bob"), tusChunk(0)); code != http.StatusNotFound {
				t.Errorf("a chunk at offset 0 afterwards = %d, want 404", code)
			}
			untouched(t, target, adminContent)
		})
	}

	// The other user uploads the file through TUS, as the web interface does.
	t.Run("the destination is uploaded through TUS meanwhile", func(t *testing.T) {
		e, bob, admin, tmp, target := createOnly(t)
		length := map[string]string{"Upload-Length": strconv.Itoa(len(adminContent))}

		// While bob's upload is there, the path is taken.
		if code, _, _ := e.do(http.MethodPost, "/api/tus/users/bob/report.pdf", admin, http.NoBody, length); code != http.StatusConflict {
			t.Errorf("a second upload to the path of one in progress = %d, want 409", code)
		}

		// Once its file is gone it is not, and the new upload is all there is.
		if err := os.Remove(tmp); err != nil {
			t.Fatal(err)
		}
		if code, _, body := e.do(http.MethodPost, "/api/tus/users/bob/report.pdf", admin, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("upload to the path of an upload that lost its file = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/users/bob/report.pdf", admin, strings.NewReader(adminContent), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("its only chunk = %d %q", code, body)
		}
		for _, offset := range []int{0, len(adminContent)} {
			if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("bob"), tusChunk(offset)); code != http.StatusNotFound {
				t.Errorf("a chunk of the first upload at %d afterwards = %d, want 404", offset, code)
			}
		}
		if code, h, _ := e.do(http.MethodHead, "/api/tus/report.pdf", bob, nil, nil); code != http.StatusNotFound || h.Get("Upload-Offset") != "" {
			t.Errorf("HEAD of the first upload afterwards = %d, offset %q; want 404", code, h.Get("Upload-Offset"))
		}
		untouched(t, target, adminContent)
	})

	// An upload that is to replace a file, by a user who may.
	t.Run("an upload that replaces a file", func(t *testing.T) {
		const original, replacement = "OLDOLDOLD!", "NEWNEWNEWNEWNEWNEW!!"
		e := newFullEnv(t)
		target := e.writeBobFile("important.txt", original)
		bob := e.login("bob", fullEnvUserPassword)
		if code, _, body := e.do(http.MethodPost, "/api/tus/important.txt?override=true", bob, http.NoBody, map[string]string{"Upload-Length": strconv.Itoa(len(replacement))}); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/important.txt", bob, strings.NewReader(replacement[:5]), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("first chunk = %d %q", code, body)
		}
		if err := os.Remove(filepath.Join(e.bobDir, filepath.Base(tusTempPath("/important.txt")))); err != nil {
			t.Fatal(err)
		}

		// The client resumes the way TUS clients do: it asks for the offset
		// and sends the rest from there.
		code, h, _ := e.do(http.MethodHead, "/api/tus/important.txt", bob, nil, nil)
		if code != http.StatusNotFound || h.Get("Upload-Offset") != "" {
			t.Errorf("HEAD = %d, offset %q; want 404 and none", code, h.Get("Upload-Offset"))
		}
		for _, offset := range []int{5, len(original)} {
			if code, _, _ := e.do(http.MethodPatch, "/api/tus/important.txt", bob, strings.NewReader(replacement[offset:]), tusChunk(offset)); code != http.StatusNotFound {
				t.Errorf("the rest of the upload from %d = %d, want 404", offset, code)
			}
		}
		untouched(t, target, original)

		// Starting over works, and replaces the file as a whole.
		if code, _, body := e.do(http.MethodPost, "/api/tus/important.txt?override=true", bob, http.NoBody, map[string]string{"Upload-Length": strconv.Itoa(len(replacement))}); code != http.StatusCreated {
			t.Fatalf("starting over = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/important.txt", bob, strings.NewReader(replacement), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("the upload after starting over = %d %q", code, body)
		}
		untouched(t, target, replacement)
	})
}

// The cleanup of an upload that expired, like the sweeper, runs some time
// after the upload was last heard of. Another upload to the same destination
// can have started by then, and it has the same temporary file: the cleanup
// of the first took the file of the second from under it.
func TestTusCleanupLeavesTheFileOfAnUploadThatStartedAgain(t *testing.T) {
	f, cache := newTusOverrideFixture(t)

	url := f.create(t, "again.bin", 10)
	res, _ := f.patch(t, url, 0, []byte("first"))
	res.Body.Close()

	// The first upload expires...
	cache.mu.Lock()
	var (
		key     string
		cleanup func() error
	)
	for k, remove := range cache.removal {
		key, cleanup = k, remove
	}
	cache.mu.Unlock()
	if cleanup == nil {
		t.Fatal("the upload has no cleanup")
	}
	cache.UploadCache.Complete(key)

	// ... a second one to the same path starts before its cleanup has run...
	code, url := f.createOverride(t, "again.bin", 10)
	if code != http.StatusCreated {
		t.Fatalf("second upload = %d", code)
	}
	if err := cleanup(); err != nil {
		t.Errorf("the cleanup of the first upload: %v", err)
	}

	// ... and is complete in the end.
	if temps := uploadTempsIn(t, f.scope); len(temps) != 1 {
		t.Fatalf("temporary files after the cleanup of the first upload: %v, want that of the second", temps)
	}
	res, _ = f.patch(t, url, 0, []byte("the second"))
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("the second upload = %d", res.StatusCode)
	}
	if got := f.read(t, "again.bin"); got != "the second" {
		t.Errorf("again.bin = %q", got)
	}
}

// Nor is the file of an upload removed while a request is working on it.
func TestTusDiscardAbandoned(t *testing.T) {
	scope := t.TempDir()
	afs := afero.NewBasePathFs(afero.NewOsFs(), scope)
	name := filepath.Base(tusTempPath("/upload.bin"))
	tmp := filepath.Join(scope, name)
	if err := os.WriteFile(tmp, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	const key = "/somewhere/upload.bin"

	release, ok := tusAcquireKey(key)
	if !ok {
		t.Fatal("the upload is busy")
	}
	if kept, err := tusDiscardAbandoned(unregistered{}, key, afs, "/"+name); !kept || err != nil || !pathExists(tmp) {
		t.Errorf("with a request working on the upload: kept = %v, %v; the file exists: %v", kept, err, pathExists(tmp))
	}
	release()

	if kept, err := tusDiscardAbandoned(registeredUploadCache{}, key, afs, "/"+name); !kept || err != nil || !pathExists(tmp) {
		t.Errorf("with the upload registered: kept = %v, %v; the file exists: %v", kept, err, pathExists(tmp))
	}

	if kept, err := tusDiscardAbandoned(unregistered{}, key, afs, "/"+name); kept || err != nil || pathExists(tmp) {
		t.Errorf("abandoned: kept = %v, %v; the file exists: %v", kept, err, pathExists(tmp))
	}
	if release, ok := tusAcquireKey(key); ok {
		release()
	} else {
		t.Error("the upload is still held after the look")
	}
}
