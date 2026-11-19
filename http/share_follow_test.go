package fbhttp

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/users"
)

func (e *fullEnv) rename(token, src, dst string) int {
	e.t.Helper()
	code, _, _ := e.do(http.MethodPatch, "/api/resources"+src+"?action=rename&destination="+url.QueryEscape(dst), token, nil, nil)
	return code
}

func (e *fullEnv) publicDownload(hash string) (int, string) {
	e.t.Helper()
	code, _, body := e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, nil)
	return code, body
}

// A share only records a path. When the shared item was renamed, the share
// stayed behind at the old path, and whatever was created there later was
// published through the old link (GHSA-m8v4-4w34-rrvf). The share now follows
// the item.
func TestShareFollowsRename(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("notes.txt", "public notes")
	e.writeBobFile("album/2024/pic.txt", "a picture")
	tok := e.login("bob", fullEnvUserPassword)

	fileShare := e.createShare(tok, "/notes.txt", "{}")
	nestedShare := e.createShare(tok, "/album/2024/pic.txt", "{}")
	dirShare := e.createShare(tok, "/album", "{}")

	if code := e.rename(tok, "/notes.txt", "/archive.txt"); code != http.StatusOK {
		t.Fatalf("rename of the file = %d", code)
	}
	if code := e.rename(tok, "/album", "/photos"); code != http.StatusOK {
		t.Fatalf("rename of the directory = %d", code)
	}

	// Unrelated content appears at the old paths.
	e.writeBobFile("notes.txt", "PRIVATE: unrelated")
	e.writeBobFile("album/2024/pic.txt", "PRIVATE: unrelated")

	if code, body := e.publicDownload(fileShare); code != http.StatusOK || body != "public notes" {
		t.Errorf("file share after the rename = %d %q, want the renamed file", code, body)
	}
	if code, body := e.publicDownload(nestedShare); code != http.StatusOK || body != "a picture" {
		t.Errorf("share of a file inside the renamed directory = %d %q, want that file", code, body)
	}
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/"+dirShare+"/2024/pic.txt", "", nil, nil); code != http.StatusOK || body != "a picture" {
		t.Errorf("share of the renamed directory = %d %q, want its content", code, body)
	}

	want := map[string]string{fileShare: "/archive.txt", nestedShare: "/photos/2024/pic.txt", dirShare: "/photos"}
	for hash, path := range want {
		link, err := e.st.Share.GetByHash(hash)
		if err != nil {
			t.Errorf("share %s is gone: %v", hash, err)
			continue
		}
		if link.Path != path || link.UserID != 2 {
			t.Errorf("share %s points at %q (user %d), want %q (user 2)", hash, link.Path, link.UserID, path)
		}
	}
}

// Deleting an item removed only the deleting user's own shares of it. When
// someone else deleted it (an admin, or a user with an overlapping scope), the
// owner's share survived and later published a new file at the same path
// (GHSA-r6pg-pg54-rcr5). Shares of every owner are now removed.
func TestDeleteRemovesSharesOfEveryOwner(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("report.txt", "the report")
	e.writeBobFile("folder/inside.txt", "inside")
	e.writeBobFile("keep.txt", "kept")
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)

	gone := []string{
		e.createShare(bob, "/report.txt", "{}"),
		e.createShare(bob, "/folder/inside.txt", "{}"),
		e.createShare(bob, "/folder", "{}"),
		e.createShare(admin, "/users/bob/folder/inside.txt", "{}"),
	}
	kept := e.createShare(bob, "/keep.txt", "{}")

	// The admin deletes inside bob's scope.
	for _, path := range []string{"/users/bob/report.txt", "/users/bob/folder"} {
		if code, _, body := e.do(http.MethodDelete, "/api/resources"+path, admin, nil, nil); code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d %q", path, code, body)
		}
	}

	e.writeBobFile("report.txt", "PRIVATE: unrelated")
	e.writeBobFile("folder/inside.txt", "PRIVATE: unrelated")

	for _, hash := range gone {
		if code, body := e.publicDownload(hash); code != http.StatusNotFound {
			t.Errorf("share %s of a deleted item = %d %q, want 404", hash, code, body)
		}
	}
	if code, body := e.publicDownload(kept); code != http.StatusOK || body != "kept" {
		t.Errorf("share of an untouched file = %d %q", code, body)
	}
}

// Shares are matched by where they point on disk, through their owner's scope.
// A share whose path merely has the same spelling in another user's scope is a
// different file and must be left alone (GHSA-5ww9-jg6q-38r7 stays closed).
func TestShareCleanupLeavesOtherScopesAlone(t *testing.T) {
	e := newFullEnv(t)
	carolHash, err := users.HashPwd("carol-long-password-4")
	if err != nil {
		t.Fatal(err)
	}
	perm := users.Permissions{Create: true, Rename: true, Modify: true, Delete: true, Share: true, Download: true}
	if err := e.st.Users.Save(&users.User{Username: "carol", Password: carolHash, Scope: "/users/carol", Perm: perm}); err != nil {
		t.Fatal(err)
	}
	e.writeBobFile("same.txt", "bob's")
	e.writeBobFile("../carol/same.txt", "carol's")
	e.writeBobFile("../carol/moved.txt", "carol's other")

	bob := e.login("bob", fullEnvUserPassword)
	carol := e.login("carol", "carol-long-password-4")
	bobShare := e.createShare(bob, "/same.txt", "{}")
	carolShare := e.createShare(carol, "/same.txt", "{}")
	carolMoved := e.createShare(carol, "/moved.txt", "{}")

	if code := e.rename(carol, "/moved.txt", "/renamed.txt"); code != http.StatusOK {
		t.Fatalf("carol's rename = %d", code)
	}
	if code, _, _ := e.do(http.MethodDelete, "/api/resources/same.txt", carol, nil, nil); code != http.StatusNoContent {
		t.Fatalf("carol's delete = %d", code)
	}

	if code, body := e.publicDownload(bobShare); code != http.StatusOK || body != "bob's" {
		t.Errorf("bob's share after carol's delete = %d %q, want bob's file", code, body)
	}
	if code, _ := e.publicDownload(carolShare); code != http.StatusNotFound {
		t.Errorf("carol's share of her deleted file = %d, want 404", code)
	}
	if code, body := e.publicDownload(carolMoved); code != http.StatusOK || body != "carol's other" {
		t.Errorf("carol's share after her rename = %d %q", code, body)
	}
}

// A move can take an item out of the scope of a share's owner. The owner can
// no longer reach it, so the share cannot follow and is removed.
func TestShareIsRemovedWhenItsTargetLeavesTheOwnersScope(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("doc.txt", "bob's document")
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)
	hash := e.createShare(bob, "/doc.txt", "{}")
	adminHash := e.createShare(admin, "/users/bob/doc.txt", "{}")

	if code := e.rename(admin, "/users/bob/doc.txt", "/elsewhere.txt"); code != http.StatusOK {
		t.Fatalf("admin's move = %d", code)
	}
	e.writeBobFile("doc.txt", "PRIVATE: unrelated")

	if code, body := e.publicDownload(hash); code != http.StatusNotFound {
		t.Errorf("bob's share after the item left his scope = %d %q, want 404", code, body)
	}
	if _, err := e.st.Share.GetByHash(hash); err == nil {
		t.Error("bob's share record was kept")
	}
	// The admin's own share of the same item follows it.
	if code, body := e.publicDownload(adminHash); code != http.StatusOK || body != "bob's document" {
		t.Errorf("admin's share after the move = %d %q, want the moved file", code, body)
	}
	link, err := e.st.Share.GetByHash(adminHash)
	if err != nil || link.Path != "/elsewhere.txt" {
		t.Errorf("admin's share = %+v, %v; want path /elsewhere.txt", link, err)
	}
}
