package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/werewolfboy13/filebrowser/v2/users"
)

// Renaming a directory onto an existing file with override=true cannot
// succeed, and used to delete the file on its way to failing: a user with
// Rename and Modify, but not Delete, could remove files that way.
func TestRenameDirectoryOntoFileKeepsTheFile(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Perm = users.Permissions{Rename: true, Modify: true, Download: true} // no Delete
	})
	e.writeBobFile("dir/x", "x")
	target := e.writeBobFile("file.txt", "precious")
	if err := os.Mkdir(filepath.Join(e.bobDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	tok := e.login("bob", fullEnvUserPassword)

	if code, _, _ := e.do(http.MethodDelete, "/api/resources/file.txt", tok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("control: DELETE = %d, want 403", code)
	}

	code, _, body := e.do(http.MethodPatch, "/api/resources/dir?action=rename&destination=%2Ffile.txt&override=true", tok, nil, nil)
	if code < 400 {
		t.Errorf("rename of a directory onto a file = %d %q, want an error", code, body)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "precious" {
		t.Errorf("the destination file was lost: %q, %v", data, err)
	}
	if !pathExists(filepath.Join(e.bobDir, "dir", "x")) {
		t.Error("the source directory was lost")
	}

	code, _, body = e.do(http.MethodPatch, "/api/resources/file.txt?action=rename&destination=%2Fempty&override=true", tok, nil, nil)
	if code < 400 {
		t.Errorf("rename of a file onto a directory = %d %q, want an error", code, body)
	}
	if !pathExists(filepath.Join(e.bobDir, "empty")) {
		t.Error("the destination directory was lost")
	}
}
