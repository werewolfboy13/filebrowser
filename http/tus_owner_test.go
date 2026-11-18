package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"github.com/werewolfboy13/filebrowser/v2/users"
)

// tusOwnerEnv is an instance with three users who see the same directory:
// bob and carol, who may create files and nothing else, and the
// administrator, who may do everything.
func tusOwnerEnv(t *testing.T) (e *fullEnv, bob, carol, admin string) {
	t.Helper()
	e = newFullEnv(t)
	createOnly := users.Permissions{Create: true, Download: true}
	e.setBob(func(u *users.User) { u.Perm = createOnly })
	hash, err := users.HashPwd("carol-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Users.Save(&users.User{Username: "carol", Password: hash, Scope: "/users/bob", Perm: createOnly}); err != nil {
		t.Fatal(err)
	}
	return e, e.login("bob", fullEnvUserPassword), e.login("carol", "carol-password-123"), e.login("admin", fullEnvAdminPassword)
}

// An upload in progress belonged to whoever sent the next request for its
// path. The user who created it was written down, but only cancelling looked:
//   - creating an upload at the same path with override=true started it over,
//     for anyone who may create files, and made them its owner, who may cancel
//     it. That is cancelling somebody else's upload without the Delete
//     permission, which DELETE refuses;
//   - HEAD and PATCH served anyone who may create files there, so one user
//     could finish another user's upload with bytes of their own.
func TestTusUploadIsOnlyContinuedByItsOwner(t *testing.T) {
	half := strings.Repeat("b", 50)

	t.Run("a user without the Delete permission", func(t *testing.T) {
		e, bob, carol, _ := tusOwnerEnv(t)
		length := map[string]string{"Upload-Length": "100"}

		if code, _, body := e.do(http.MethodPost, "/api/tus/shared.bin", bob, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/shared.bin", bob, strings.NewReader(half), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("first half = %d %q", code, body)
		}

		if code, _, _ := e.do(http.MethodDelete, "/api/tus/shared.bin", carol, nil, nil); code != http.StatusForbidden {
			t.Errorf("control: cancelling somebody else's upload without Delete = %d, want 403", code)
		}
		for _, query := range []string{"?override=true", "?override=false", ""} {
			if code, _, _ := e.do(http.MethodPost, "/api/tus/shared.bin"+query, carol, http.NoBody, length); code != http.StatusConflict {
				t.Errorf("creating an upload%s at the path of somebody else's upload in progress = %d, want 409", query, code)
			}
		}
		if code, h, _ := e.do(http.MethodHead, "/api/tus/shared.bin", carol, nil, nil); code != http.StatusForbidden || h.Get("Upload-Offset") != "" {
			t.Errorf("HEAD of somebody else's upload = %d, offset %q; want 403 and none", code, h.Get("Upload-Offset"))
		}
		if code, _, _ := e.do(http.MethodPatch, "/api/tus/shared.bin", carol, strings.NewReader(strings.Repeat("c", 50)), tusChunk(50)); code != http.StatusForbidden {
			t.Errorf("a chunk for somebody else's upload = %d, want 403", code)
		}
		if code, _, _ := e.do(http.MethodDelete, "/api/tus/shared.bin", carol, nil, nil); code != http.StatusForbidden {
			t.Errorf("cancelling it after all that = %d, want 403", code)
		}

		// The upload is where its owner left it, and theirs to finish.
		if code, h, _ := e.do(http.MethodHead, "/api/tus/shared.bin", bob, nil, nil); code != http.StatusOK || h.Get("Upload-Offset") != "50" {
			t.Fatalf("the owner's HEAD = %d, offset %q; want 200 and 50", code, h.Get("Upload-Offset"))
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/shared.bin", bob, strings.NewReader(half), tusChunk(50)); code != http.StatusNoContent {
			t.Fatalf("the owner's second half = %d %q", code, body)
		}
		if data, _ := os.ReadFile(filepath.Join(e.bobDir, "shared.bin")); string(data) != half+half {
			t.Errorf("shared.bin holds %q", data)
		}
	})

	// Whoever may delete files may cancel any upload, and so may start one
	// over. Continuing it stays the owner's: two users writing one upload is
	// never what either of them meant.
	t.Run("a user with the Delete permission", func(t *testing.T) {
		e, bob, _, admin := tusOwnerEnv(t)
		length := map[string]string{"Upload-Length": "20"}
		const adminPath = "/api/tus/users/bob/report.pdf"

		if code, _, body := e.do(http.MethodPost, "/api/tus/report.pdf", bob, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("BBBBBBBBBB"), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("first half = %d %q", code, body)
		}
		if code, h, _ := e.do(http.MethodHead, adminPath, admin, nil, nil); code != http.StatusForbidden || h.Get("Upload-Offset") != "" {
			t.Errorf("HEAD of somebody else's upload = %d, offset %q; want 403 and none", code, h.Get("Upload-Offset"))
		}
		if code, _, _ := e.do(http.MethodPatch, adminPath, admin, strings.NewReader("AAAAAAAAAA"), tusChunk(10)); code != http.StatusForbidden {
			t.Errorf("a chunk for somebody else's upload = %d, want 403", code)
		}
		if code, _, _ := e.do(http.MethodPost, adminPath, admin, http.NoBody, length); code != http.StatusConflict {
			t.Errorf("creating an upload at the path of one in progress, without override = %d, want 409", code)
		}

		// Starting it over makes it the administrator's.
		if code, _, body := e.do(http.MethodPost, adminPath+"?override=true", admin, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("starting somebody else's upload over with the Delete permission = %d %q, want 201", code, body)
		}
		if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("BBBBBBBBBB"), tusChunk(10)); code != http.StatusForbidden {
			t.Errorf("the first owner's next chunk = %d, want 403", code)
		}
		if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("BBBBBBBBBB"), tusChunk(0)); code != http.StatusForbidden {
			t.Errorf("the first owner's chunk at the start = %d, want 403", code)
		}
		if code, _, body := e.do(http.MethodPatch, adminPath, admin, strings.NewReader("AAAAAAAAAAAAAAAAAAAA"), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("the administrator's upload = %d %q", code, body)
		}
		if data, _ := os.ReadFile(filepath.Join(e.bobDir, "report.pdf")); string(data) != "AAAAAAAAAAAAAAAAAAAA" {
			t.Errorf("report.pdf holds %q", data)
		}
	})

	// The owner may start their own upload over, as before.
	t.Run("the owner starts over", func(t *testing.T) {
		e, bob, _, _ := tusOwnerEnv(t)
		length := map[string]string{"Upload-Length": "100"}
		if code, _, body := e.do(http.MethodPost, "/api/tus/mine.bin", bob, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus/mine.bin", bob, strings.NewReader(half), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("first half = %d %q", code, body)
		}
		if code, _, _ := e.do(http.MethodPost, "/api/tus/mine.bin", bob, http.NoBody, length); code != http.StatusConflict {
			t.Errorf("a second upload to the path of the own one, without override = %d, want 409", code)
		}
		if code, _, body := e.do(http.MethodPost, "/api/tus/mine.bin?override=true", bob, http.NoBody, length); code != http.StatusCreated {
			t.Fatalf("starting the own upload over = %d %q, want 201", code, body)
		}
		if code, h, _ := e.do(http.MethodHead, "/api/tus/mine.bin", bob, nil, nil); code != http.StatusOK || h.Get("Upload-Offset") != "0" {
			t.Errorf("HEAD after starting over = %d, offset %q; want 200 and 0", code, h.Get("Upload-Offset"))
		}
	})

	// An upload that is finished by somebody else, the way it was possible:
	// the administrator uploads a file into bob's directory, and bob sends
	// the second half.
	t.Run("the other way round", func(t *testing.T) {
		e, bob, _, admin := tusOwnerEnv(t)
		const adminPath = "/api/tus/users/bob/report.pdf"
		if code, _, body := e.do(http.MethodPost, adminPath, admin, http.NoBody, map[string]string{"Upload-Length": "20"}); code != http.StatusCreated {
			t.Fatalf("create = %d %q", code, body)
		}
		if code, _, body := e.do(http.MethodPatch, adminPath, admin, strings.NewReader("AAAAAAAAAA"), tusChunk(0)); code != http.StatusNoContent {
			t.Fatalf("first half = %d %q", code, body)
		}
		if code, h, _ := e.do(http.MethodHead, "/api/tus/report.pdf", bob, nil, nil); code != http.StatusForbidden || h.Get("Upload-Length") != "" {
			t.Errorf("HEAD of somebody else's upload = %d, length %q; want 403 and none", code, h.Get("Upload-Length"))
		}
		if code, _, _ := e.do(http.MethodPatch, "/api/tus/report.pdf", bob, strings.NewReader("BOBBOBBOBB"), tusChunk(10)); code != http.StatusForbidden {
			t.Errorf("a chunk for somebody else's upload = %d, want 403", code)
		}
		if code, _, body := e.do(http.MethodPatch, adminPath, admin, strings.NewReader("CCCCCCCCCC"), tusChunk(10)); code != http.StatusNoContent {
			t.Errorf("the owner's second half = %d %q, want 204", code, body)
		}
		if data, _ := os.ReadFile(filepath.Join(e.bobDir, "report.pdf")); string(data) != "AAAAAAAAAACCCCCCCCCC" {
			t.Errorf("report.pdf holds %q", data)
		}
	})
}

// An upload stays registered for as long as requests for it keep coming, and
// while it is, its path is taken for everyone who may not cancel it. The
// requests of those others must not count: a user who is refused, and tries
// again, would keep the upload of somebody who has long gone registered for
// as long as they keep trying, and the path taken with it.
func TestTusUploadIsNotKeptAliveByThoseItRefuses(t *testing.T) {
	e, bob, carol, _ := tusOwnerEnv(t)
	length := map[string]string{"Upload-Length": "100"}
	if code, _, body := e.do(http.MethodPost, "/api/tus/shared.bin", bob, http.NoBody, length); code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, body)
	}
	expires := func() time.Time {
		t.Helper()
		item := e.cache.cache.Get(filepath.Join(e.bobDir, "shared.bin"), ttlcache.WithDisableTouchOnHit[string, memoryUploadEntry]())
		if item == nil {
			t.Fatal("the upload is not registered")
		}
		return item.ExpiresAt()
	}

	before := expires()
	time.Sleep(20 * time.Millisecond)
	for _, rq := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/tus/shared.bin", http.StatusConflict},
		{http.MethodPost, "/api/tus/shared.bin?override=true", http.StatusConflict},
		{http.MethodHead, "/api/tus/shared.bin", http.StatusForbidden},
		{http.MethodPatch, "/api/tus/shared.bin", http.StatusForbidden},
		{http.MethodDelete, "/api/tus/shared.bin", http.StatusForbidden},
	} {
		hdr := map[string]string{"Upload-Length": "100", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}
		if code, _, _ := e.do(rq.method, rq.path, carol, strings.NewReader("carol"), hdr); code != rq.want {
			t.Errorf("%s %s by another user = %d, want %d", rq.method, rq.path, code, rq.want)
		}
		if after := expires(); !after.Equal(before) {
			t.Errorf("%s %s by another user put off the end of the upload by %s", rq.method, rq.path, after.Sub(before))
		}
	}

	// Its owner's requests do count.
	if code, _, _ := e.do(http.MethodHead, "/api/tus/shared.bin", bob, nil, nil); code != http.StatusOK {
		t.Fatalf("the owner's HEAD = %d", code)
	}
	afterHead := expires()
	if !afterHead.After(before) {
		t.Error("the owner's HEAD did not keep the upload alive")
	}
	time.Sleep(20 * time.Millisecond)
	if code, _, _ := e.do(http.MethodPatch, "/api/tus/shared.bin", bob, strings.NewReader("bob"), tusChunk(0)); code != http.StatusNoContent {
		t.Fatalf("the owner's chunk = %d", code)
	}
	if !expires().After(afterHead) {
		t.Error("the owner's chunk did not keep the upload alive")
	}
}

// An upload that does not say who created it (a value in Redis from before
// that was written down) is nobody's to continue.
func TestTusUploadWithoutAnOwnerIsNotContinued(t *testing.T) {
	scope := t.TempDir()
	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)
	srv := newTusTestServer(t, st, registeredUploadCache{upload: uploadInfo{Length: 10}})
	f := &tusTestFixture{srv: srv, client: srv.Client(), token: signToken(t, perm, key), scope: scope}
	tmp := filepath.Join(scope, filepath.Base(tusTempPath("/old.bin")))
	if err := os.WriteFile(tmp, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/api/tus/old.bin", http.NoBody)
	req.Header.Set("X-Auth", f.token)
	head, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()
	if head.StatusCode != http.StatusForbidden || head.Header.Get("Upload-Offset") != "" {
		t.Errorf("HEAD = %d, offset %q; want 403 and none", head.StatusCode, head.Header.Get("Upload-Offset"))
	}
	res, _ := f.patch(t, srv.URL+"/api/tus/old.bin", 5, []byte("67890"))
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("PATCH = %d, want 403", res.StatusCode)
	}
	if data, _ := os.ReadFile(tmp); string(data) != "12345" || pathExists(filepath.Join(scope, "old.bin")) {
		t.Errorf("the upload was written to: %q", data)
	}
}
