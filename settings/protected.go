package settings

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/filebrowser/filebrowser/v2/rules"
)

// protectedPath is a file or directory of the server itself that lies inside
// the directory it serves.
type protectedPath struct {
	path string

	// log marks the log file, whose rotated copies, written next to it, are
	// protected as well.
	log bool
}

// rotatedLogStamp is what the log rotation inserts between the name of the log
// file and its extension: "-2006-01-02T15-04-05.000".
var rotatedLogStamp = regexp.MustCompile(`^-\d{4}-\d{2}-\d{2}[Tt]\d{2}-\d{2}-\d{2}\.\d{3}$`)

// Protect registers path, a file or directory the server itself depends on
// (its database, configuration file, cache directory, TLS key), as one that
// must never be served: the database holds the key that signs sessions and
// every password hash, so a user who can download it owns the installation.
//
// Only a path that lies inside Root can be reached through the file API, so
// only such a path is registered. Protect reports whether it did: the caller
// is expected to warn about it, since the path is still exposed to everything
// else that can read the served directory.
//
// With FollowExternalSymlinks that is not so: a symbolic link inside Root is
// followed wherever it leads, to the database as well. The path is then
// registered wherever it is, unless it is Root or contains it, which would
// refuse everything that is served. Protect still reports whether the path
// is inside Root: outside it, there is nothing to warn about.
//
// Root and FollowExternalSymlinks must be set, and Root absolute, before
// Protect is called.
func (s *Server) Protect(path string) bool {
	return s.protect(path, false)
}

// ProtectLog is Protect for the log file. The copies the log rotation leaves
// next to it are protected with it.
func (s *Server) ProtectLog(path string) bool {
	return s.protect(path, true)
}

func (s *Server) protect(path string, log bool) bool {
	if path == "" || s.Root == "" {
		return false
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Both the path and the root are taken as written and with symbolic links
	// followed: either may be reached through a link.
	inside := false
	for _, candidate := range withResolved(abs) {
		within, around := false, false
		for _, root := range withResolved(filepath.Clean(s.Root)) {
			if strictlyInside(candidate, root, s.CaseInsensitiveFs) {
				within = true
			}
			if samePath(candidate, root, s.CaseInsensitiveFs) || strictlyInside(root, candidate, s.CaseInsensitiveFs) {
				around = true
			}
		}

		switch {
		case within:
			inside = true
			s.addProtected(candidate, log)
		case s.FollowExternalSymlinks && !around:
			s.addProtected(candidate, log)
		}
	}

	return inside
}

func (s *Server) addProtected(path string, log bool) {
	for _, p := range s.protected {
		if p.path == path {
			return
		}
	}
	s.protected = append(s.protected, protectedPath{path: path, log: log})
}

// HasProtectedPaths reports whether any path was registered with Protect.
func (s *Server) HasProtectedPaths() bool {
	return len(s.protected) != 0
}

// ProtectedPaths returns the registered paths.
func (s *Server) ProtectedPaths() []string {
	paths := make([]string, 0, len(s.protected))
	for _, p := range s.protected {
		paths = append(paths, p.path)
	}
	return paths
}

// IsProtected reports whether path, an absolute path on disk, is one of the
// registered paths, lies inside one, or is a rotated copy of the log file.
func (s *Server) IsProtected(path string) bool {
	path = filepath.Clean(path)

	for _, p := range s.protected {
		if samePath(path, p.path, s.CaseInsensitiveFs) || strictlyInside(path, p.path, s.CaseInsensitiveFs) {
			return true
		}
		if p.log && isRotatedLog(path, p.path, s.CaseInsensitiveFs) {
			return true
		}
	}

	return false
}

// isRotatedLog reports whether name is a rotated copy of the log file: a file
// in the same directory named like it, with a timestamp before the extension
// and possibly compressed.
func isRotatedLog(name, logFile string, fold bool) bool {
	if !samePath(filepath.Dir(name), filepath.Dir(logFile), fold) {
		return false
	}

	ext := filepath.Ext(logFile)
	prefix := strings.TrimSuffix(filepath.Base(logFile), ext)
	base := filepath.Base(name)

	if rotatedCopy(base, prefix, ext) {
		return true
	}
	if fold && rotatedCopy(strings.ToLower(base), strings.ToLower(prefix), strings.ToLower(ext)) {
		return true
	}
	return rotatedCopy(rules.CanonicalName(base, fold), rules.CanonicalName(prefix, fold), rules.CanonicalName(ext, fold))
}

// rotatedCopy reports whether base is prefix and ext with the stamp of a
// rotation between them, possibly compressed.
func rotatedCopy(base, prefix, ext string) bool {
	base = strings.TrimSuffix(base, ".gz")
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ext) || len(base) < len(prefix)+len(ext) {
		return false
	}

	return rotatedLogStamp.MatchString(base[len(prefix) : len(base)-len(ext)])
}

// withResolved returns path and, when it differs, the path with the symbolic
// links in it followed. A path that does not exist yet (a log file before the
// first line is written) is resolved through its directory.
func withResolved(path string) []string {
	paths := []string{path}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		dir, dirErr := filepath.EvalSymlinks(filepath.Dir(path))
		if dirErr != nil {
			return paths
		}
		resolved = filepath.Join(dir, filepath.Base(path))
	}
	if resolved != path {
		paths = append(paths, resolved)
	}

	return paths
}

// samePath reports whether a and b are the same path, or could be to the
// filesystem: with fold, it ignores case, and it always ignores how a name is
// composed. See rules.CanonicalName, which errs on the side of taking two
// names for one; that is the side to err on for a path that is refused. What
// lower-casing alone takes for the same stays the same.
func samePath(a, b string, fold bool) bool {
	if a == b || (fold && strings.EqualFold(a, b)) {
		return true
	}
	return rules.CanonicalName(a, fold) == rules.CanonicalName(b, fold)
}

// strictlyInside reports whether path lies below dir, and is not dir itself.
// Names are compared as samePath compares them.
func strictlyInside(path, dir string, fold bool) bool {
	if below(path, dir) || (fold && below(strings.ToLower(path), strings.ToLower(dir))) {
		return true
	}
	return below(rules.CanonicalName(path, fold), rules.CanonicalName(dir, fold))
}

func below(path, dir string) bool {
	if !strings.HasSuffix(dir, string(os.PathSeparator)) {
		dir += string(os.PathSeparator)
	}
	return len(path) > len(dir) && strings.HasPrefix(path, dir)
}
