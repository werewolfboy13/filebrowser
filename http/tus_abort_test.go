package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/users"
)

// The web interface cancels its uploads in flight when the user logs out, is
// logged out, or cancels them, by sending the TUS DELETE. Cancelling must
// discard what the upload has written so far and nothing else: an upload that
// was going to overwrite a file leaves that file as it was.
func TestTusDeleteDiscardsOnlyTheUpload(t *testing.T) {
	e := newFullEnv(t)
	original := e.writeBobFile("docs/report.txt", "ORIGINAL REPORT")
	e.writeBobFile("docs/other.txt", "other")
	if err := os.Symlink("report.txt", filepath.Join(e.bobDir, "docs", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tok := e.login("bob", fullEnvUserPassword)

	chunk := map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}
	start := func(path string) {
		t.Helper()
		if code, _, body := e.do(http.MethodPost, "/api/tus"+path, tok, nil, map[string]string{"Upload-Length": "100"}); code != http.StatusCreated {
			t.Fatalf("create %s = %d %q", path, code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus"+strings.Split(path, "?")[0], tok, strings.NewReader("partial data"), chunk); code != http.StatusNoContent {
			t.Fatalf("first chunk of %s = %d %q", path, code, body)
		}
	}
	entries := func() []string {
		t.Helper()
		list, err := os.ReadDir(filepath.Join(e.bobDir, "docs"))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range list {
			names = append(names, entry.Name())
		}
		return names
	}
	before := entries()

	t.Run("overwrite", func(t *testing.T) {
		start("/docs/report.txt?override=true")
		if code, _, body := e.do(http.MethodDelete, "/api/tus/docs/report.txt", tok, nil, nil); code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %q", code, body)
		}
		if data, err := os.ReadFile(original); err != nil || string(data) != "ORIGINAL REPORT" {
			t.Errorf("the file being overwritten holds %q after the upload was cancelled (%v)", data, err)
		}
	})

	t.Run("new file", func(t *testing.T) {
		start("/docs/new.bin")
		if code, _, body := e.do(http.MethodDelete, "/api/tus/docs/new.bin", tok, nil, nil); code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %q", code, body)
		}
		if pathExists(filepath.Join(e.bobDir, "docs", "new.bin")) {
			t.Error("the cancelled upload left its destination behind")
		}
	})

	// An upload through a symbolic link writes to the link's target directly;
	// what it has already replaced cannot be brought back, but cancelling
	// must not delete the file on top of that.
	t.Run("overwrite through a symlink", func(t *testing.T) {
		start("/docs/link.txt?override=true")
		if code, _, body := e.do(http.MethodDelete, "/api/tus/docs/link.txt", tok, nil, nil); code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %q", code, body)
		}
		if !pathExists(original) || !pathExists(filepath.Join(e.bobDir, "docs", "link.txt")) {
			t.Error("cancelling an upload through a symlink deleted the link or its target")
		}
	})

	if after := entries(); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("directory after the cancelled uploads: %v, before: %v", after, before)
	}

	// The upload is gone: it can neither be continued nor cancelled again.
	for _, path := range []string{"/api/tus/docs/report.txt", "/api/tus/docs/new.bin"} {
		if code, _, _ := e.do(http.MethodPatch, path, tok, strings.NewReader("more"), chunk); code != http.StatusNotFound {
			t.Errorf("PATCH %s after the cancel = %d, want 404", path, code)
		}
		if code, _, _ := e.do(http.MethodDelete, path, tok, nil, nil); code != http.StatusNotFound {
			t.Errorf("second DELETE %s = %d, want 404", path, code)
		}
	}
	// DELETE is not a way to delete a file that is not being uploaded.
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/other.txt", tok, nil, nil); code != http.StatusNotFound || !pathExists(filepath.Join(e.bobDir, "docs", "other.txt")) {
		t.Errorf("DELETE of a file without an upload = %d", code)
	}

	// A user who may not delete files is refused; the file being overwritten
	// is untouched either way, and the partial upload is cleaned up when it
	// expires.
	if err := os.WriteFile(original, []byte("ORIGINAL REPORT"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.setBob(func(u *users.User) { u.Perm.Delete = false })
	tok = e.login("bob", fullEnvUserPassword)
	start("/docs/report.txt?override=true")
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/report.txt", tok, nil, nil); code != http.StatusForbidden {
		t.Errorf("DELETE without the Delete permission = %d, want 403", code)
	}
	if data, _ := os.ReadFile(original); string(data) != "ORIGINAL REPORT" {
		t.Errorf("the file being overwritten holds %q", data)
	}
}
