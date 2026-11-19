package fbhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/diskcache"

	"github.com/werewolfboy13/filebrowser/v3/rules"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// POST with override=true onto an existing directory used to fail inside
// writeFile and then run the upload cleanup, which removed the whole tree:
// without the Delete permission and regardless of deny rules on descendants
// (GHSA-c4fr-5f24-4wrj). The directory must be refused and left alone.
func TestResourcePostOverrideOnDirectoryKeepsTree(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Perm = users.Permissions{Create: true, Modify: true, Download: true} // no Delete
		u.Rules = []rules.Rule{{Path: "/photos/private", Allow: false}}
	})
	e.writeBobFile("photos/2024.jpg", "jpg")
	secret := e.writeBobFile("photos/private/passport.jpg", "jpg")

	tok := e.login("bob", fullEnvUserPassword)

	// Controls: the same user can neither delete the tree nor see the denied file.
	if code, _, _ := e.do(http.MethodDelete, "/api/resources/photos", tok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("control: DELETE /photos = %d, want 403", code)
	}
	if code, _, _ := e.do(http.MethodGet, "/api/resources/photos/private/passport.jpg", tok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("control: GET of the rule-denied file = %d, want 403", code)
	}

	code, _, body := e.do(http.MethodPost, "/api/resources/photos?override=true", tok, strings.NewReader("x"), nil)
	if code != http.StatusConflict {
		t.Errorf("POST override onto a directory = %d %q, want 409", code, strings.TrimSpace(body))
	}
	for _, p := range []string{filepath.Join(e.bobDir, "photos"), filepath.Join(e.bobDir, "photos", "2024.jpg"), secret} {
		if !pathExists(p) {
			t.Errorf("%s was removed by a POST override onto its directory", p)
		}
	}
}

// meanwhileBody is the body of an upload during which something else happens:
// it yields some data, then runs meanwhile, and then either ends or fails like
// the body of a request whose client went away.
type meanwhileBody struct {
	data      string
	meanwhile func()
	fail      bool
	step      int
}

func (b *meanwhileBody) Read(p []byte) (int, error) {
	b.step++
	switch b.step {
	case 1:
		return copy(p, b.data), nil
	case 2:
		b.meanwhile()
	}
	if b.fail {
		return 0, errors.New("simulated client disconnect")
	}
	return 0, io.EOF
}

// A failed upload of a new file used to remove whatever was at its path
// afterwards: it had found nothing there when it started, so it took what was
// there at the end for its own partial file. The upload writes to a temporary
// file, though, and never puts anything at the path before it is complete.
// What the cleanup removed was a file someone else had stored there in the
// meantime, on behalf of a user who may neither delete nor modify files.
func TestFailedUploadKeepsWhatWasStoredAtItsPathMeanwhile(t *testing.T) {
	scope := t.TempDir()
	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true, Download: true} // no Modify, no Delete
	st := scopedUserStorage(t, scope, perm, key)
	token := signToken(t, perm, key)
	target := filepath.Join(scope, "report.txt")

	// The handler is called directly, so that it has returned, and its
	// cleanup has run, by the time the file is looked at.
	body := &meanwhileBody{data: "PARTIAL UPLOAD", fail: true, meanwhile: func() {
		if temps := uploadTempsIn(t, scope); len(temps) != 1 {
			t.Errorf("the upload in progress has %d temporary files, want 1", len(temps))
		}
		if pathExists(target) {
			t.Error("the upload in progress put something at its destination")
		}
		// Someone else stores a file at the same path while the upload runs.
		if err := os.WriteFile(target, []byte("STORED MEANWHILE"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	req, _ := http.NewRequest(http.MethodPost, "/report.txt", io.NopCloser(body))
	req.Header.Set("X-Auth", token)
	rec := httptest.NewRecorder()
	handle(resourcePostHandler(diskcache.NewNoOp()), "", st, &settings.Server{}).ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("the interrupted upload was answered with %d", rec.Code)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "STORED MEANWHILE" {
		t.Errorf("after the failed upload the file stored meanwhile holds %q (%v)", data, err)
	}
	if temps := uploadTempsIn(t, scope); len(temps) != 0 {
		t.Errorf("the failed upload left its temporary file behind: %v", temps)
	}

	// A failed upload still leaves nothing of its own.
	req, _ = http.NewRequest(http.MethodPost, "/broken.txt", io.NopCloser(&failingBody{data: "partial"}))
	req.Header.Set("X-Auth", token)
	rec = httptest.NewRecorder()
	handle(resourcePostHandler(diskcache.NewNoOp()), "", st, &settings.Server{}).ServeHTTP(rec, req)
	if rec.Code < 400 || pathExists(filepath.Join(scope, "broken.txt")) {
		t.Errorf("a failed upload was answered with %d and left a file: %v", rec.Code, pathExists(filepath.Join(scope, "broken.txt")))
	}
}
