package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/rules"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// A copy authorizes its source and then copies everything below it, so every
// path below it is checked first (GHSA-77x8-73f4-5485). The check walked the
// tree without following symbolic links, and the copy follows one: the one
// at its source. Copying a link to a directory copied the whole directory
// behind it, and nothing in it had been checked. What a rule denied in there
// was in the copy, under a path no rule mentions.
func TestCopyOfALinkToADirectoryChecksWhatIsBehindIt(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("box/private/plans.txt", "do not read")
	e.writeBobFile("box/open.txt", "open")
	e.writeBobFile("plain/open.txt", "open")
	for link, target := range map[string]string{"alias": "box", "plain-alias": "plain", "file-alias": filepath.Join("box", "open.txt")} {
		if err := os.Symlink(target, filepath.Join(e.bobDir, link)); err != nil {
			t.Skip(err)
		}
	}
	e.setBob(func(u *users.User) { u.Rules = []rules.Rule{{Allow: false, Path: "/box/private"}} })
	tok := e.login("bob", fullEnvUserPassword)

	patch := func(src, action, dst string) (int, string) {
		t.Helper()
		code, _, body := e.do(http.MethodPatch, "/api/resources"+src+"?action="+action+"&destination="+strings.ReplaceAll(dst, "/", "%2F"), tok, nil, nil)
		return code, body
	}

	// The directory itself was always refused.
	if code, body := patch("/box", "copy", "/copy-of-box"); code != http.StatusForbidden {
		t.Fatalf("copy of the directory that holds a denied one = %d %q, want 403", code, body)
	}

	if code, body := patch("/alias", "copy", "/copy-of-alias"); code != http.StatusForbidden {
		t.Errorf("copy of a link to that directory = %d %q, want 403", code, body)
	}
	if pathExists(filepath.Join(e.bobDir, "copy-of-alias", "private", "plans.txt")) {
		t.Error("the denied file was copied to where no rule denies it")
	}
	if code, _, body := e.do(http.MethodGet, "/api/raw/copy-of-alias/private/plans.txt", tok, nil, nil); code == http.StatusOK {
		t.Errorf("the denied file is served from the copy: %q", body)
	}

	// A move is checked like a copy: it copies, link followed, when it
	// cannot rename (across a mount point inside the scope). So the link to
	// that directory cannot be renamed either, although renaming it would
	// usually only move the link.
	if code, body := patch("/alias", "rename", "/alias-moved"); code != http.StatusForbidden {
		t.Errorf("rename of a link to that directory = %d %q, want 403", code, body)
	}
	if code, body := patch("/plain-alias", "rename", "/plain-moved"); code != http.StatusOK {
		t.Errorf("rename of a link to a directory without denied files = %d %q", code, body)
	}
	if code, body := patch("/plain-moved", "rename", "/plain-alias"); code != http.StatusOK {
		t.Errorf("rename back = %d %q", code, body)
	}

	// Links that lead to nothing denied are copied as before: the directory
	// behind one, and the file behind another.
	if code, body := patch("/plain-alias", "copy", "/copy-of-plain"); code != http.StatusOK {
		t.Errorf("copy of a link to a directory without denied files = %d %q", code, body)
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "copy-of-plain", "open.txt")); string(data) != "open" {
		t.Errorf("the copy holds %q", data)
	}
	if code, body := patch("/file-alias", "copy", "/copy-of-file.txt"); code != http.StatusOK {
		t.Errorf("copy of a link to a file = %d %q", code, body)
	}

	// Deleting such a link removes the link, not what is behind it, and
	// stays possible.
	if code, _, body := e.do(http.MethodDelete, "/api/resources/alias", tok, nil, nil); code != http.StatusNoContent {
		t.Errorf("delete of the link = %d %q, want 204", code, body)
	}
	if data, _ := os.ReadFile(filepath.Join(e.bobDir, "box", "private", "plans.txt")); string(data) != "do not read" {
		t.Errorf("the denied file holds %q after the link was deleted", data)
	}
}
