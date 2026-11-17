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
}

// Cancelling an upload required the Delete permission, from the time when it
// removed the destination. It only discards the upload's temporary file now,
// which nobody else can reach: a user who may not delete files could not
// cancel their own upload, and it stayed behind, hidden, until it expired.
// Whoever started an upload may cancel it; for anyone else's it still takes
// the Delete permission. In no case is anything removed but that file.
func TestTusUploadCanBeCancelledByItsOwner(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Perm = users.Permissions{Create: true, Modify: true, Download: true} // no Delete
	})
	original := e.writeBobFile("docs/report.txt", "ORIGINAL REPORT")
	other := e.writeBobFile("docs/other.txt", "other")
	if err := os.Symlink("report.txt", filepath.Join(e.bobDir, "docs", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)
	docs := filepath.Join(e.bobDir, "docs")

	chunk := map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}
	start := func(token, path string) {
		t.Helper()
		if code, _, body := e.do(http.MethodPost, "/api/tus"+path, token, nil, map[string]string{"Upload-Length": "100"}); code != http.StatusCreated {
			t.Fatalf("create %s = %d %q", path, code, body)
		}
		if code, _, body := e.do(http.MethodPatch, "/api/tus"+strings.Split(path, "?")[0], token, strings.NewReader("partial data"), chunk); code != http.StatusNoContent {
			t.Fatalf("first chunk of %s = %d %q", path, code, body)
		}
	}
	intact := func(when string) {
		t.Helper()
		if data, err := os.ReadFile(original); err != nil || string(data) != "ORIGINAL REPORT" {
			t.Errorf("%s: report.txt holds %q (%v)", when, data, err)
		}
		if data, err := os.ReadFile(other); err != nil || string(data) != "other" {
			t.Errorf("%s: other.txt holds %q (%v)", when, data, err)
		}
		if fi, err := os.Lstat(filepath.Join(docs, "link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: link.txt is gone or no longer a link (%v)", when, err)
		}
	}

	// Their own uploads: of a new file, and over an existing one.
	for _, path := range []string{"/docs/new.bin", "/docs/report.txt?override=true"} {
		start(bob, path)
		if temps := uploadTempsIn(t, docs); len(temps) != 1 {
			t.Fatalf("temporary files of the upload to %s: %v", path, temps)
		}
		if code, _, body := e.do(http.MethodDelete, "/api/tus"+strings.Split(path, "?")[0], bob, nil, nil); code != http.StatusNoContent {
			t.Errorf("cancelling the own upload to %s without the Delete permission = %d %q, want 204", path, code, body)
		}
		if temps := uploadTempsIn(t, docs); len(temps) != 0 {
			t.Errorf("the cancelled upload to %s left its temporary file: %v", path, temps)
		}
		intact("after cancelling the upload to " + path)
	}
	if pathExists(filepath.Join(docs, "new.bin")) {
		t.Error("the cancelled upload left its destination behind")
	}

	// It is still not a way to delete a file: not one without an upload, and
	// not the destination of an upload that writes to it directly.
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/other.txt", bob, nil, nil); code != http.StatusNotFound {
		t.Errorf("DELETE of a file without an upload = %d, want 404", code)
	}
	if code, _, _ := e.do(http.MethodDelete, "/api/resources/docs/other.txt", bob, nil, nil); code != http.StatusForbidden {
		t.Errorf("control: deleting a file without the Delete permission = %d, want 403", code)
	}
	if err := os.WriteFile(original, []byte("ORIGINAL REPORT"), 0o644); err != nil {
		t.Fatal(err)
	}
	start(bob, "/docs/link.txt?override=true")
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/link.txt", bob, nil, nil); code != http.StatusNoContent {
		t.Errorf("cancelling the own upload through a link = %d, want 204", code)
	}
	if !pathExists(original) || !pathExists(filepath.Join(docs, "link.txt")) {
		t.Error("cancelling an upload through a symlink deleted the link or its target")
	}
	if err := os.WriteFile(original, []byte("ORIGINAL REPORT"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Somebody else's upload takes the Delete permission.
	start(admin, "/users/bob/docs/theirs.bin")
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/theirs.bin", bob, nil, nil); code != http.StatusForbidden {
		t.Errorf("cancelling somebody else's upload without the Delete permission = %d, want 403", code)
	}
	if temps := uploadTempsIn(t, docs); len(temps) != 1 {
		t.Errorf("somebody else's upload lost its temporary file: %v", temps)
	}
	// It goes on, and can be cancelled by someone who may delete.
	if code, _, _ := e.do(http.MethodPatch, "/api/tus/users/bob/docs/theirs.bin", admin, strings.NewReader("more"), map[string]string{
		"Upload-Offset": "12", "Content-Type": "application/offset+octet-stream",
	}); code != http.StatusNoContent {
		t.Errorf("the upload somebody tried to cancel does not go on: %d", code)
	}
	start(bob, "/docs/mine.bin")
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/users/bob/docs/mine.bin", admin, nil, nil); code != http.StatusNoContent {
		t.Errorf("cancelling somebody else's upload with the Delete permission = %d, want 204", code)
	}
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/users/bob/docs/theirs.bin", admin, nil, nil); code != http.StatusNoContent {
		t.Errorf("cancelling the own upload = %d, want 204", code)
	}

	// A user who may neither create nor delete has nothing to cancel.
	e.setBob(func(u *users.User) { u.Perm = users.Permissions{Download: true} })
	if code, _, _ := e.do(http.MethodDelete, "/api/tus/docs/other.txt", bob, nil, nil); code != http.StatusForbidden {
		t.Errorf("DELETE without the Create and Delete permissions = %d, want 403", code)
	}

	intact("at the end")
	if temps := uploadTempsIn(t, docs); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
	list, _ := os.ReadDir(docs)
	if len(list) != 3 {
		t.Errorf("the directory holds %d entries at the end, want the 3 it started with", len(list))
	}
}
