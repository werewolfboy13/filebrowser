package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"github.com/werewolfboy13/filebrowser/v2/diskcache"
	"github.com/werewolfboy13/filebrowser/v2/files"
	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/storage"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// lastMomentFs is the filesystem of the operating system, on which a file
// appears at a path in the last possible moment before something is renamed
// to that path: after every look at the path that came before the rename.
// It is the race an upload that looks and then renames cannot win, made to
// happen every time.
type lastMomentFs struct {
	afero.Fs
	path    string // on disk
	content string

	mu      sync.Mutex
	planted bool
}

func (f *lastMomentFs) Rename(oldname, newname string) error {
	f.mu.Lock()
	if newname == f.path && !f.planted {
		if err := os.WriteFile(f.path, []byte(f.content), 0o600); err != nil {
			f.mu.Unlock()
			return err
		}
		f.planted = true
	}
	f.mu.Unlock()
	return f.Fs.Rename(oldname, newname)
}

func (f *lastMomentFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return f.Fs.(afero.Lstater).LstatIfPossible(name)
}

func (f *lastMomentFs) wasPlanted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planted
}

// productionFsUser gives the user the filesystem users.User.Clean builds in
// production (a ScopedFs on real paths, which is what can make hard links),
// on top of the filesystem the test supplies.
type productionFsUser struct {
	users.Store
	source afero.Fs
	scope  string
}

func (pu *productionFsUser) Get(baseScope string, followExternalSymlinks bool, id interface{}) (*users.User, error) {
	user, err := pu.Store.Get(baseScope, followExternalSymlinks, id)
	if err != nil {
		return nil, err
	}
	user.Fs = files.NewFs(pu.source, pu.scope, false)
	return user, nil
}

// lastMomentStorage returns a storage whose user works in scope, on a
// filesystem where content appears at scope/name in the moment before
// anything is renamed there.
func lastMomentStorage(t *testing.T, scope, name, content string, perm users.Permissions, key []byte) (*storage.Storage, *lastMomentFs) {
	t.Helper()

	// The hard link is what the test is about. Where there are none, looking
	// and renaming is all that is left, and what it cannot do is documented.
	probe := filepath.Join(scope, "probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(probe, probe+"-link"); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	_ = os.Remove(probe)
	_ = os.Remove(probe + "-link")

	st := scopedUserStorage(t, scope, perm, key)
	fs := &lastMomentFs{Fs: afero.NewOsFs(), path: filepath.Join(scope, name), content: content}
	st.Users = &productionFsUser{Store: st.Users.(*customFSUser).Store, source: fs, scope: scope}
	return st, fs
}

// An upload that may not replace a file is put in place with a hard link,
// which fails when the name is taken: the check and the creation are one
// step. Looking whether the name is free and renaming then are two, and a
// file that appears between them is replaced, by an upload that did not ask
// to, from a user who may not modify files.
//
// The tests that create a file while an upload is on its way do not tell the
// two apart, because the file is there before the look. Here it appears
// after the last look, inside the rename, if there is one. So either no
// rename happened and the upload is stored, or one happened and the file that
// appeared must have survived it.
func TestUploadIsPutInPlaceWithoutAMomentToLoseAFileIn(t *testing.T) {
	const stored = "STORED IN THE LAST MOMENT"
	key := []byte("test-signing-key")
	perm := users.Permissions{Create: true}

	check := func(t *testing.T, fs *lastMomentFs, target string, code, created, conflict int) {
		t.Helper()
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if fs.wasPlanted() {
			// The upload looked, found the name free, and renamed.
			if string(data) != stored || code != conflict {
				t.Errorf("a file that appeared at the destination after the upload had looked was replaced: "+
					"the destination holds %q, the upload was answered %d; want %q and %d", data, code, stored, conflict)
			}
			return
		}
		if string(data) != "UPLOADED" || code != created {
			t.Errorf("the destination holds %q, the upload was answered %d; want the upload and %d", data, code, created)
		}
	}

	t.Run("upload in one request", func(t *testing.T) {
		scope := t.TempDir()
		st, fs := lastMomentStorage(t, scope, "report.txt", stored, perm, key)

		req, _ := http.NewRequest(http.MethodPost, "/report.txt", strings.NewReader("UPLOADED"))
		req.Header.Set("X-Auth", signToken(t, perm, key))
		rec := httptest.NewRecorder()
		handle(resourcePostHandler(diskcache.NewNoOp()), "", st, &settings.Server{Root: scope}).ServeHTTP(rec, req)

		check(t, fs, filepath.Join(scope, "report.txt"), rec.Code, http.StatusOK, http.StatusConflict)
		if temps := uploadTempsIn(t, scope); len(temps) != 0 {
			t.Errorf("temporary files left behind: %v", temps)
		}
	})

	t.Run("resumable upload", func(t *testing.T) {
		scope := t.TempDir()
		st, fs := lastMomentStorage(t, scope, "report.txt", stored, perm, key)
		cache := newMemoryUploadCache()
		t.Cleanup(cache.Close)
		server := &settings.Server{Root: scope}
		token := signToken(t, perm, key)

		req, _ := http.NewRequest(http.MethodPost, "/report.txt", http.NoBody)
		req.Header.Set("X-Auth", token)
		req.Header.Set("Upload-Length", "8")
		rec := httptest.NewRecorder()
		handle(tusPostHandler(cache), "", st, server).ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("tus create = %d %q", rec.Code, rec.Body.String())
		}

		req, _ = http.NewRequest(http.MethodPatch, "/report.txt", strings.NewReader("UPLOADED"))
		req.Header.Set("X-Auth", token)
		req.Header.Set("Upload-Offset", "0")
		req.Header.Set("Content-Type", "application/offset+octet-stream")
		rec = httptest.NewRecorder()
		handle(tusPatchHandler(cache), "", st, server).ServeHTTP(rec, req)

		check(t, fs, filepath.Join(scope, "report.txt"), rec.Code, http.StatusNoContent, http.StatusConflict)
		if temps := uploadTempsIn(t, scope); len(temps) != 0 {
			t.Errorf("temporary files left behind: %v", temps)
		}
	})
}
