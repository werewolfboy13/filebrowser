package settings

import (
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v2/rules"
)

// isProtectedAsBefore is IsProtected as it was before the forms of the
// registered paths were worked out when they are registered, and those of
// the checked path once: every form, for every comparison.
func isProtectedAsBefore(s *Server, path string) bool {
	path = filepath.Clean(path)
	fold := s.CaseInsensitiveFs

	for _, p := range s.protected {
		if samePath(path, p.path, fold) || strictlyInside(path, p.path, fold) {
			return true
		}
		if !p.log || !samePath(filepath.Dir(path), filepath.Dir(p.path), fold) {
			continue
		}

		ext := filepath.Ext(p.path)
		prefix := strings.TrimSuffix(filepath.Base(p.path), ext)
		base := filepath.Base(path)
		if rotatedCopy(base, prefix, ext) ||
			(fold && rotatedCopy(strings.ToLower(base), strings.ToLower(prefix), strings.ToLower(ext))) ||
			rotatedCopy(rules.CanonicalName(base, fold), rules.CanonicalName(prefix, fold), rules.CanonicalName(ext, fold)) {
			return true
		}
	}
	return false
}

// IsProtected decides as it did, for paths put together at random from the
// names that are registered, in several spellings, and from others.
func TestIsProtectedDecidesAsItDidBeforeItsFormsWereKept(t *testing.T) {
	root := filepath.FromSlash("/srv")
	registered := []string{
		"/srv/filebrowser.db",
		"/srv/config/settings.json",
		"/srv/cache",
		"/srv/tls/key.pem",
		"/srv/Caf\u00e9/Stra\u00dfe.db",
		"/srv/\ufb01les/\u13a0",
		// Not ASCII as registered, and all ASCII in its canonical form: a
		// path that is all ASCII has to be compared with it canonically.
		"/srv/o\ufb03ce",
	}
	logs := []string{"/srv/logs/filebrowser.log", "/srv/J\u00f6urnal/Gr\u00f6\u00dfe.log"}

	parts := []string{
		"srv", "SRV", "filebrowser.db", "FILEBROWSER.DB", "f\u0131lebrowser.db", "config", "settings.json", "cache", "Cache",
		"tls", "key.pem", "Caf\u00e9", "cafe\u0301", "CAFE\u0301", "Stra\u00dfe.db", "STRASSE.DB", "strasse.db",
		"\ufb01les", "files", "FILES", "\u13a0", "\uab70", "logs", "LOGS", "J\u00f6urnal", "Jo\u0308urnal", "JO\u0308URNAL",
		"filebrowser.log", "filebrowser-2026-11-18T10-20-30.123.log", "FILEBROWSER-2026-11-18T10-20-30.123.LOG.gz",
		"filebrowser-2026-11-18t10-20-30.123.log", "\uff46ilebrowser-2026-11-18T10-20-30.123.log",
		"Gr\u00f6\u00dfe-2026-11-18T10-20-30.123.log", "GRO\u0308SSE-2026-11-18T10-20-30.123.LOG", "gr\u00f6sse-2026-11-18T10-20-30.123.log.gz",
		"Gr\u00f6\u00dfe.log", "other", "x.txt", "\u5831\u544a\u66f8.txt",
		"office", "OFFICE", "o\ufb03ce",
	}

	rng := rand.New(rand.NewSource(20261118))
	for _, fold := range []bool{false, true} {
		s := &Server{Root: root, CaseInsensitiveFs: fold}
		for _, p := range registered {
			s.addProtected(filepath.FromSlash(p), false)
		}
		for _, p := range logs {
			s.addProtected(filepath.FromSlash(p), true)
		}

		protected, failures := 0, 0
		for i := 0; i < 200000 && failures < 10; i++ {
			elements := make([]string, 1+rng.Intn(4))
			for j := range elements {
				elements[j] = parts[rng.Intn(len(parts))]
			}
			if rng.Intn(3) != 0 {
				elements[0] = "srv"
			}
			path := filepath.FromSlash("/" + strings.Join(elements, "/"))

			want := isProtectedAsBefore(s, path)
			if want {
				protected++
			}
			if got := s.IsProtected(path); got != want {
				failures++
				t.Errorf("IsProtected(%+q) = %v with fold %v, was %v", path, got, fold, want)
			}
		}
		// Guard the test: it has to meet both answers often.
		if protected < 5000 {
			t.Errorf("fold %v: only %d of the random paths are protected", fold, protected)
		}
	}
}

// The registered paths are compared in the form that goes with what the
// filesystem turns out to do, which is found out after they were
// registered.
func TestProtectedPathsFollowTheCaseSettingThatIsFoundOutLater(t *testing.T) {
	s := &Server{Root: filepath.FromSlash("/srv")}
	s.addProtected(filepath.FromSlash("/srv/Stra\u00dfe.db"), false)

	other := filepath.FromSlash("/srv/STRASSE.DB")
	if s.IsProtected(other) {
		t.Error("another case is protected on a filesystem that tells case apart")
	}
	s.CaseInsensitiveFs = true
	if !s.IsProtected(other) {
		t.Error("another case is not protected on a filesystem that ignores case")
	}
}

// A listing checks every name in a directory against every registered path.
func BenchmarkIsProtectedOverAListing(b *testing.B) {
	for _, names := range []struct{ name, format string }{
		{"ascii", "/srv/users/bob/big/report-final-version-"},
		{"not ascii", "/srv/users/bob/big/\u5831\u544a\u66f8-\u6700\u7d42\u7248-Gr\u00f6\u00dfe-\u00e9-"},
	} {
		paths := make([]string, 3000)
		for i := range paths {
			paths[i] = filepath.FromSlash(names.format + strings.Repeat("x", i%7) + ".txt")
		}
		for _, fold := range []bool{false, true} {
			s := &Server{Root: filepath.FromSlash("/srv"), CaseInsensitiveFs: fold}
			for _, p := range []string{"/srv/filebrowser.db", "/srv/config/settings.json", "/srv/cache", "/srv/tls/key.pem"} {
				s.addProtected(filepath.FromSlash(p), false)
			}
			s.addProtected(filepath.FromSlash("/srv/logs/filebrowser.log"), true)

			for _, impl := range []struct {
				name string
				fn   func(*Server, string) bool
			}{{"as before", isProtectedAsBefore}, {"now", (*Server).IsProtected}} {
				b.Run(names.name+", fold "+map[bool]string{false: "false", true: "true"}[fold]+", "+impl.name, func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						for _, p := range paths {
							impl.fn(s, p)
						}
					}
				})
			}
		}
	}
}
