package fbhttp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/filebrowser/filebrowser/v2/users"
)

// redisLikeUploadCache keeps uploads the way the Redis cache does: it stores
// what was registered and lets it expire, and has nothing to run when it
// does. The removal callback is dropped, exactly as redisUploadCache.Register
// drops it.
type redisLikeUploadCache struct {
	UploadCache
}

func (c redisLikeUploadCache) Register(filePath string, upload uploadInfo, _ func() error) {
	c.UploadCache.Register(filePath, upload, nil)
}

// unregistered is an upload cache in which no upload is registered: the
// temporary files the tests below hand to the sweeper belong to none.
type unregistered struct {
	UploadCache
}

func (unregistered) Has(string) bool { return false }

// useTusSweepClock puts the sweeper of abandoned uploads on a clock the test
// moves, and empties it before and after.
func useTusSweepClock(t *testing.T) *fakeClock {
	t.Helper()
	clock := newFakeClock()
	reset := func(now func() time.Time) {
		tusTemps.mu.Lock()
		tusTemps.files = map[string]*tusTempFile{}
		tusTemps.now = now
		tusTemps.mu.Unlock()
	}
	reset(clock.Now)
	t.Cleanup(func() { reset(time.Now) })
	return clock
}

func tusTempsWatched() int {
	tusTemps.mu.Lock()
	defer tusTemps.mu.Unlock()
	return len(tusTemps.files)
}

// With the Redis upload cache nothing removed the temporary file of an upload
// that was abandoned: a closed tab or a lost connection left it behind,
// hidden from listings and refused by name, so that nobody could see or
// remove it through the web interface.
func TestAbandonedTusUploadsAreSweptWithEitherCache(t *testing.T) {
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

			if err := os.WriteFile(filepath.Join(scope, "keep.txt"), []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			abandoned := f.create(t, "movie.mkv", 1000)
			res, _ := f.patch(t, abandoned, 0, bytes.Repeat([]byte("x"), 500))
			res.Body.Close()
			alive := f.create(t, "alive.mkv", 1000)
			res, _ = f.patch(t, alive, 0, bytes.Repeat([]byte("y"), 100))
			res.Body.Close()
			if temps := uploadTempsIn(t, scope); len(temps) != 2 || tusTempsWatched() != 2 {
				t.Fatalf("temporary files: %v, watched: %d", temps, tusTempsWatched())
			}

			// The entries expire the way a Redis key does: nothing runs.
			mem.cache.DeleteAll()

			// The first look after the last chunk. From here on the file of
			// the abandoned upload does not change any more.
			clock.Advance(tusSweepInterval)
			tusTemps.sweep()

			// Nothing is removed while an upload could still be going on...
			clock.Advance(tusAbandonedAge / 2)
			tusTemps.sweep()
			if temps := uploadTempsIn(t, scope); len(temps) != 2 {
				t.Fatalf("temporary files after half the time: %v", temps)
			}

			// ... or while it is: one of the two is written to again.
			tmp := filepath.Join(scope, filepath.Base(tusTempPath("/alive.mkv")))
			file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = file.Write(bytes.Repeat([]byte("y"), 100))
			_ = file.Close()

			clock.Advance(tusAbandonedAge/2 + time.Minute)
			tusTemps.sweep()
			temps := uploadTempsIn(t, scope)
			if len(temps) != 1 || temps[0] != filepath.Base(tmp) {
				t.Errorf("after the time an abandoned upload is given: %v, want only the temporary file of the upload that is alive", temps)
			}

			// It goes too once it has been quiet for as long.
			clock.Advance(tusAbandonedAge + time.Minute)
			tusTemps.sweep()
			if temps := uploadTempsIn(t, scope); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
			if tusTempsWatched() != 0 {
				t.Errorf("%d files are still watched", tusTempsWatched())
			}
			if data, _ := os.ReadFile(filepath.Join(scope, "keep.txt")); string(data) != "keep" {
				t.Errorf("another file in the directory was touched: %q", data)
			}
		})
	}
}

// An upload that completes, or is cancelled, is forgotten, and nothing that
// took the place of its temporary file is ever removed.
func TestTusSweepRemovesOnlyTheFileOfTheUpload(t *testing.T) {
	clock := useTusSweepClock(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(root, "scope")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(scope, "a"), filepath.Join(scope, "b"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	afs := files.NewScopedFs(afero.NewOsFs(), scope)
	name := filepath.Base(tusTempPath("/a/upload.bin"))
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := func() {
		clock.Advance(2*tusAbandonedAge + time.Minute)
		tusTemps.sweep()
	}

	// Something that is not a temporary file is never watched.
	write(filepath.Join(scope, "a", "report.txt"))
	tusTemps.track(afs, "/a/report.txt", filepath.Join(scope, "a", "report.txt"), unregistered{}, "")
	if tusTempsWatched() != 0 {
		t.Fatal("a file without a temporary name is watched")
	}

	// The file was replaced by another one of the same name.
	tmp := filepath.Join(scope, "a", name)
	write(tmp)
	tusTemps.track(afs, "/a/"+name, tmp, unregistered{}, "")
	// (Written before the first one is removed, so that it is another file
	// for certain, and not one that was given the same place on the disk.)
	write(filepath.Join(scope, "a", "other"))
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(scope, "a", "other"), tmp); err != nil {
		t.Fatal(err)
	}
	old()
	if !pathExists(tmp) {
		t.Error("a file that took the place of the temporary file was removed")
	}
	if tusTempsWatched() != 0 {
		t.Error("the replaced file is still watched")
	}

	// Its directory was swapped for a link out of the scope, where a file of
	// the same name lies: the scoped filesystem does not follow it.
	write(filepath.Join(scope, "b", name))
	tusTemps.track(afs, "/b/"+name, filepath.Join(scope, "b", name), unregistered{}, "")
	write(filepath.Join(outside, name))
	if err := os.RemoveAll(filepath.Join(scope, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(scope, "b")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	old()
	old()
	if !pathExists(filepath.Join(outside, name)) {
		t.Error("the sweep followed a link out of the scope and removed a file there")
	}
	if tusTempsWatched() != 0 {
		t.Error("a file that can no longer be reached is watched for ever")
	}

	// ... or for a link to another directory inside the scope, where a file
	// of the same name lies that is not the one that was created.
	if err := os.Remove(filepath.Join(scope, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(scope, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(scope, "c", name))
	tusTemps.track(afs, "/c/"+name, filepath.Join(scope, "c", name), unregistered{}, "")
	if err := os.Rename(filepath.Join(scope, "c"), filepath.Join(scope, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(scope, "c")); err != nil {
		t.Fatal(err)
	}
	old()
	if !pathExists(tmp) {
		t.Error("the sweep removed a file of the same name in the directory a link leads to")
	}

	// The memory is bounded.
	tusTemps.mu.Lock()
	tusTemps.maxFiles = 8
	tusTemps.mu.Unlock()
	t.Cleanup(func() {
		tusTemps.mu.Lock()
		tusTemps.maxFiles = tusSweepMaxFiles
		tusTemps.mu.Unlock()
	})
	for i := 0; i < 40; i++ {
		sum := []byte("0123456789abcdef0123456789abcdef")
		sum[0], sum[1] = "0123456789abcdef"[i%16], "0123456789abcdef"[i/16]
		p := "/a/" + fileutils.TempPrefix + "tus-" + string(sum)
		write(filepath.Join(scope, filepath.FromSlash(p)))
		tusTemps.track(afs, p, filepath.Join(scope, filepath.FromSlash(p)), unregistered{}, "")
	}
	if watched := tusTempsWatched(); watched != 8 {
		t.Errorf("%d files are watched, limit 8", watched)
	}
}

// The value the Redis cache stores says what the upload was created with, and
// a value written before it did still reads as an upload of that length.
func TestUploadInfoEncoding(t *testing.T) {
	for _, upload := range []uploadInfo{
		{Length: 0},
		{Length: 1 << 40},
		{Length: 10, Override: true},
		{Length: 10, Owner: 7},
		{Length: 10, Direct: true},
		{Length: 123456, Override: true, Owner: 4294967295, Direct: true},
	} {
		got, err := decodeUploadInfo(encodeUploadInfo(upload))
		if err != nil || got != upload {
			t.Errorf("%+v -> %q -> %+v, %v", upload, encodeUploadInfo(upload), got, err)
		}
	}
	if encoded := encodeUploadInfo(uploadInfo{Length: 42}); encoded != "42" {
		t.Errorf("a plain upload is stored as %q, want the bare length", encoded)
	}
	if got, err := decodeUploadInfo("42:x:o:future"); err != nil || got != (uploadInfo{Length: 42, Override: true}) {
		t.Errorf("a value with fields this version does not know = %+v, %v", got, err)
	}
	// A value from before uploads said where they write: to a temporary
	// file, so that such an upload is never continued at its destination.
	for _, old := range []string{"42", "42:o:u7"} {
		if got, err := decodeUploadInfo(old); err != nil || got.Direct {
			t.Errorf("decodeUploadInfo(%q) = %+v, %v; want an upload that does not write to its destination", old, got, err)
		}
	}
	for _, bad := range []string{"", "abc", ":o", "12abc"} {
		if _, err := decodeUploadInfo(bad); err == nil {
			t.Errorf("decodeUploadInfo(%q) succeeded", bad)
		}
	}
}
