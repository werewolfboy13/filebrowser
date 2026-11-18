package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectRegistersOnlyPathsInsideTheRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "srv")
	for _, dir := range []string{filepath.Join(root, "data"), filepath.Join(base, "database"), filepath.Join(root, "cache")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(root, "data", "filebrowser.db")
	if err := os.WriteFile(inside, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		path string
		want bool
	}{
		"database inside the root":        {inside, true},
		"cache directory inside the root": {filepath.Join(root, "cache"), true},
		"file that does not exist yet":    {filepath.Join(root, "logs", "fb.log"), true},
		"database next to the root":       {filepath.Join(base, "database", "filebrowser.db"), false},
		"sibling with the root as prefix": {root + "-backup/filebrowser.db", false},
		// A directory that contains the root, or is the root, cannot be
		// protected without refusing everything that is served.
		"the root itself":         {root, false},
		"an ancestor of the root": {base, false},
		"not configured":          {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &Server{Root: root}
			if got := s.Protect(tc.path); got != tc.want {
				t.Fatalf("Protect(%q) = %v, want %v", tc.path, got, tc.want)
			}
			if s.HasProtectedPaths() != tc.want {
				t.Errorf("HasProtectedPaths = %v after Protect returned %v", s.HasProtectedPaths(), tc.want)
			}
			if tc.want && !s.IsProtected(tc.path) {
				t.Errorf("IsProtected(%q) = false after it was registered", tc.path)
			}
			if s.IsProtected(filepath.Join(root, "data", "holiday.jpg")) || s.IsProtected(root) {
				t.Error("an ordinary served path is reported as protected")
			}
		})
	}
}

// A path can be reached through a symbolic link, in the root or in the path
// itself; both spellings are registered.
func TestProtectFollowsSymlinks(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realRoot := filepath.Join(base, "volume")
	if err := os.MkdirAll(filepath.Join(realRoot, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(realRoot, "db", "filebrowser.db")
	if err := os.WriteFile(database, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(base, "srv")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	outside := filepath.Join(base, "state")
	if err := os.Symlink(filepath.Join(realRoot, "db"), outside); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for name, tc := range map[string]struct{ root, path string }{
		"root is a link":                  {linkedRoot, database},
		"path goes through the root link": {realRoot, filepath.Join(linkedRoot, "db", "filebrowser.db")},
		"path goes through another link":  {realRoot, filepath.Join(outside, "filebrowser.db")},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{Root: tc.root}
			if !s.Protect(tc.path) {
				t.Fatalf("Protect(%q) with root %q = false", tc.path, tc.root)
			}
			if !s.IsProtected(database) {
				t.Errorf("the real location is not protected: %v", s.ProtectedPaths())
			}
		})
	}
}

func TestIsProtected(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator), "srv")
	s := &Server{Root: root}
	s.Protect(filepath.Join(root, "filebrowser.db"))
	s.Protect(filepath.Join(root, "cache"))
	s.ProtectLog(filepath.Join(root, "logs", "filebrowser.log"))

	protected := []string{
		"filebrowser.db",
		"cache",
		"cache/ab/cd/thumb",
		"logs/filebrowser.log",
		"logs/filebrowser-2026-11-17T10-00-00.000.log",
		"logs/filebrowser-2026-11-17T10-00-00.000.log.gz",
		"./cache/../filebrowser.db",
	}
	for _, p := range protected {
		if !s.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%s is not protected", p)
		}
	}

	served := []string{
		"",
		"filebrowser.db.bak",
		"filebrowser.dbx",
		"cached",
		"cache-old/thumb",
		"logs",
		"logs/other.log",
		"logs/filebrowser-notes.log",
		"logs/filebrowser-2026-11-17.log",
		"logs/sub/filebrowser-2026-11-17T10-00-00.000.log",
		"docs/filebrowser.db",
		"FILEBROWSER.DB",
	}
	for _, p := range served {
		if s.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%s is reported as protected", p)
		}
	}

	// On a case-insensitive filesystem the other spellings name the same file.
	folded := &Server{Root: root, CaseInsensitiveFs: true}
	folded.Protect(filepath.Join(root, "filebrowser.db"))
	folded.ProtectLog(filepath.Join(root, "Logs", "FileBrowser.log"))
	for _, p := range []string{"FILEBROWSER.DB", "logs/filebrowser-2026-11-17T10-00-00.000.LOG"} {
		if !folded.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%s is not protected on a case-insensitive filesystem", p)
		}
	}
	if folded.IsProtected(filepath.Join(root, "filebrowser.dbx")) {
		t.Error("folding widened the match")
	}
}

// A filesystem that ignores case does so in full, and one that normalises
// names ignores how they are composed (APFS does both): the cache directory
// "cache-files" is also "cache-ﬁles" there, and a rotated log
// "filebrowser-....log" is also "ﬁlebrowser-....log". The server's own files
// are refused under those spellings too. An existing regular file is also
// compared by identity (see the http package); a directory, what is in it,
// and a rotated log are known by their names alone.
func TestIsProtectedInEverySpelling(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator), "srv")

	folded := &Server{Root: root, CaseInsensitiveFs: true}
	folded.Protect(filepath.Join(root, "cache-files"))
	folded.Protect(filepath.Join(root, "stra\u00dfe.db"))
	folded.Protect(filepath.Join(root, "caf\u00e9.db"))
	folded.ProtectLog(filepath.Join(root, "logs", "filebrowser.log"))
	for _, p := range []string{
		"cache-\ufb01les",
		"cache-\ufb01les/ab/cd/thumb",
		"CACHE-\ufb01LES/ab/cd/thumb",
		"STRASSE.DB",
		"strasse.db",
		"cafe\u0301.db",
		"CAFE\u0301.DB",
		"logs/\ufb01lebrowser.log",
		"logs/\ufb01lebrowser-2026-11-17T10-00-00.000.log",
		"LOGS/\ufb01LEBROWSER-2026-11-17T10-00-00.000.LOG.gz",
		"logs/filebrowser-2026-11-17T10-00-00.000.log.GZ",
	} {
		if !folded.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%+q is not protected on a filesystem that ignores case", p)
		}
	}
	for _, p := range []string{"cache-\ufb01lesx/thumb", "strassen.db", "cafe.db", "logs/\ufb01lebrowser-notes.log"} {
		if folded.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%+q is reported as protected", p)
		}
	}

	// How a name is composed is ignored on every filesystem; case is not.
	exact := &Server{Root: root}
	exact.Protect(filepath.Join(root, "caf\u00e9"))
	exact.ProtectLog(filepath.Join(root, "logs", "caf\u00e9.log"))
	for _, p := range []string{"cafe\u0301", "cafe\u0301/thumb", "logs/cafe\u0301.log", "logs/cafe\u0301-2026-11-17T10-00-00.000.log"} {
		if !exact.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%+q is not protected", p)
		}
	}
	for _, p := range []string{"CAF\u00c9", "CAFE\u0301/thumb", "logs/CAFE\u0301.log"} {
		if exact.IsProtected(filepath.Join(root, filepath.FromSlash(p))) {
			t.Errorf("%+q is reported as protected on a filesystem that tells case apart", p)
		}
	}
}
