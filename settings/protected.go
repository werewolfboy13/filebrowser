package settings

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/werewolfboy13/filebrowser/v2/rules"
)

// protectedPath is a file or directory of the server itself that lies inside
// the directory it serves.
type protectedPath struct {
	path string

	// log marks the log file, whose rotated copies, written next to it, are
	// protected as well.
	log bool

	// The forms the path is compared in, worked out when it is registered:
	// every path that is served is compared with every registered one, and
	// working them out for each comparison made a listing several times
	// slower.
	forms spelling

	// For the log file: its directory, and the two parts of its name that
	// the stamp of a rotated copy goes between.
	dir, prefix, ext spelling
}

// spelling is a path, or a part of one, in the forms it is compared in: as
// written, in lower case, and canonical (see rules.CanonicalName) without
// and with case folded. Which of the last two is used depends on
// Server.CaseInsensitiveFs, which is found out after the paths were
// registered; both are kept.
type spelling struct {
	raw, lower string
	canonical  [2]string
	ascii      bool
}

func newSpelling(raw string) spelling {
	return spelling{
		raw:       raw,
		lower:     strings.ToLower(raw),
		canonical: [2]string{rules.CanonicalName(raw, false), rules.CanonicalName(raw, true)},
		ascii:     isASCII(raw),
	}
}

// canon returns the canonical form for a filesystem that folds case, or one
// that does not.
func (s *spelling) canon(fold bool) string {
	if fold {
		return s.canonical[1]
	}
	return s.canonical[0]
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// checkedPath is a path that is compared with the registered ones. Its
// forms are worked out when the first comparison needs them, once for all
// of the registered paths.
type checkedPath struct {
	raw   string
	fold  bool
	ascii bool

	lower, canonical       string
	hasLower, hasCanonical bool
}

func newCheckedPath(raw string, fold bool) *checkedPath {
	return &checkedPath{raw: raw, fold: fold, ascii: isASCII(raw)}
}

func (c *checkedPath) inLower() string {
	if !c.hasLower {
		c.lower, c.hasLower = strings.ToLower(c.raw), true
	}
	return c.lower
}

func (c *checkedPath) inCanonical() string {
	if !c.hasCanonical {
		c.canonical, c.hasCanonical = rules.CanonicalName(c.raw, c.fold), true
	}
	return c.canonical
}

// same is samePath for a registered path.
func (c *checkedPath) same(p *spelling) bool {
	if c.raw == p.raw || (c.fold && strings.EqualFold(c.raw, p.raw)) {
		return true
	}
	// Two names that are all ASCII have just been compared in their
	// canonical forms: these are the names themselves, or the names in
	// lower case.
	if c.ascii && p.ascii {
		return false
	}
	return c.inCanonical() == p.canon(c.fold)
}

// inside is strictlyInside for a registered directory.
func (c *checkedPath) inside(p *spelling) bool {
	if below(c.raw, p.raw) || (c.fold && below(c.inLower(), p.lower)) {
		return true
	}
	if c.ascii && p.ascii {
		return false
	}
	return below(c.inCanonical(), p.canon(c.fold))
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

	p := protectedPath{path: path, log: log, forms: newSpelling(path)}
	if log {
		ext := filepath.Ext(path)
		p.dir = newSpelling(filepath.Dir(path))
		p.prefix = newSpelling(strings.TrimSuffix(filepath.Base(path), ext))
		p.ext = newSpelling(ext)
	}
	s.protected = append(s.protected, p)
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
//
// Names are compared as samePath and strictlyInside compare them.
func (s *Server) IsProtected(path string) bool {
	if len(s.protected) == 0 {
		return false
	}

	checked := newCheckedPath(filepath.Clean(path), s.CaseInsensitiveFs)
	var name, dir *checkedPath // of the path, for the log: when one is registered

	for i := range s.protected {
		p := &s.protected[i]
		if checked.same(&p.forms) || checked.inside(&p.forms) {
			return true
		}
		if !p.log {
			continue
		}
		if name == nil {
			name = newCheckedPath(filepath.Base(checked.raw), s.CaseInsensitiveFs)
			dir = newCheckedPath(filepath.Dir(checked.raw), s.CaseInsensitiveFs)
		}
		if isRotatedLog(name, dir, p) {
			return true
		}
	}

	return false
}

// isRotatedLog reports whether a file, given by its name and its directory,
// is a rotated copy of the log file: a file in the same directory named like
// it, with a timestamp before the extension and possibly compressed.
//
// The name is looked at first: next to none of the names that are checked
// look like a rotated log, and the directory need not be compared for those.
func isRotatedLog(name, dir *checkedPath, log *protectedPath) bool {
	fold := name.fold

	named := rotatedCopy(name.raw, log.prefix.raw, log.ext.raw) ||
		(fold && rotatedCopy(name.inLower(), log.prefix.lower, log.ext.lower))
	// The canonical forms of names that are all ASCII are the ones that
	// were just compared.
	if !named && (!name.ascii || !log.prefix.ascii || !log.ext.ascii) {
		named = rotatedCopy(name.inCanonical(), log.prefix.canon(fold), log.ext.canon(fold))
	}

	return named && dir.same(&log.dir)
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
