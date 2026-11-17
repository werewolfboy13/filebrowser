package fbhttp

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/users"
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

// A failed upload must only ever clean up what the request itself created.
func TestResourcePostFailureKeepsExistingPaths(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Perm = users.Permissions{Create: true, Download: true} // no Modify, no Delete
	})
	kept := e.writeBobFile("docs/a.txt", "keep")
	tok := e.login("bob", fullEnvUserPassword)

	for _, target := range []string{"/api/resources/docs?override=true", "/api/resources/docs/a.txt/child"} {
		code, _, _ := e.do(http.MethodPost, target, tok, strings.NewReader("x"), nil)
		if code < 400 {
			t.Errorf("POST %s = %d, want an error status", target, code)
		}
		if !pathExists(kept) {
			t.Fatalf("POST %s destroyed %s", target, kept)
		}
	}
}
