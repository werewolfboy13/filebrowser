package fbhttp

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	gopath "path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tomasen/realip"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/runner"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/storage"
	"github.com/filebrowser/filebrowser/v2/users"
)

type handleFunc func(w http.ResponseWriter, r *http.Request, d *data) (int, error)

type data struct {
	*runner.Runner
	settings *settings.Settings
	server   *settings.Server
	store    *storage.Storage
	user     *users.User
	raw      interface{}

	// checkerPrefix is prepended to every path before evaluating rules. It is
	// set when the user's filesystem has been rebased onto a subdirectory (as
	// done for public shares), so that rules — which are relative to the user's
	// original scope — are still matched against the real path instead of the
	// rebased one. Empty for regular requests.
	checkerPrefix string

	// scopeRoot is the on-disk root of the scope the rules are relative to. It
	// is set, like checkerPrefix, when the user's filesystem is rebased; for
	// regular requests it is empty and the root of the user's filesystem is
	// used.
	scopeRoot string

	// resolved caches, for the length of the request, where paths on disk
	// lead once symbolic links are followed. See resolvePath.
	resolved map[string]string

	// protectedInfo caches the identities of the server's own files. See
	// protectedFiles.
	protectedInfo []os.FileInfo
}

// Check implements rules.Checker.
func (d *data) Check(path string) bool {
	rulePath := d.rulePath(path)

	if d.user.HideDotfiles && rules.MatchHidden(rulePath) {
		return false
	}

	// The temporary file an upload is written to is an internal detail of the
	// upload: it is not listed and cannot be read, replaced or removed by
	// name. Like the dotfile check, this is not part of CheckRules, so it does
	// not stop a user from operating on a directory that contains one.
	if fileutils.IsTempName(gopath.Base(rulePath)) {
		return false
	}

	return d.CheckRules(path)
}

// CheckRules reports whether the global and user rules allow path. Unlike
// Check, it ignores HideDotfiles: hiding dotfiles is a display preference, so
// it must not stop a user from operating on a tree that contains one.
//
// The rules are applied to the path as it is written and to the path it leads
// to. They are written for the former, but the filesystem follows symbolic
// links that stay inside the scope: without the second check, a link to a
// denied file or directory would open it under a name no rule mentions.
func (d *data) CheckRules(path string) bool {
	// The server's own files are refused before any rule is looked at: no
	// rule, and no user, is meant to open them.
	if d.protected(path) {
		return false
	}

	rulePath := d.rulePath(path)
	if !d.rulesAllow(rulePath) {
		return false
	}

	// Without rules there is nothing a link could get around, and resolving
	// costs a system call per path.
	if len(d.settings.Rules) == 0 && len(d.user.Rules) == 0 {
		return true
	}

	if resolved, ok := d.resolvedRulePath(path); ok && resolved != rulePath {
		return d.rulesAllow(resolved)
	}

	return true
}

// protected reports whether path names one of the server's own files that lie
// inside the served directory (see settings.Server.Protect): the database,
// which holds the key that signs sessions and every password hash, the
// configuration file, the cache directory, the TLS key and the log.
//
// A file can be reached by more names than the one it was registered under,
// so the path is compared as written, with symbolic links followed, and, for
// an existing file, by identity, which covers hard links.
//
// It costs nothing when nothing is registered, which is the case whenever
// those files are kept outside the served directory.
func (d *data) protected(path string) bool {
	if !d.server.HasProtectedPaths() || files.BasePath(d.user.Fs) == nil {
		return false
	}

	lexical := filepath.Clean(d.user.FullPath(slashClean(path)))
	if d.server.IsProtected(lexical) {
		return true
	}

	real, ok := d.resolvePath(lexical)
	if !ok {
		real = lexical
	} else if real != lexical && d.server.IsProtected(real) {
		return true
	}

	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	for _, other := range d.protectedFiles() {
		if os.SameFile(info, other) {
			return true
		}
	}

	return false
}

// protectedFiles returns the FileInfo of the registered paths that exist, for
// comparing identities. It is looked up once per request.
func (d *data) protectedFiles() []os.FileInfo {
	if d.protectedInfo == nil {
		d.protectedInfo = []os.FileInfo{}
		for _, p := range d.server.ProtectedPaths() {
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				d.protectedInfo = append(d.protectedInfo, info)
			}
		}
	}
	return d.protectedInfo
}

// rulesAllow evaluates the global and user rules against a path that is
// already in the form the rules are written in.
func (d *data) rulesAllow(path string) bool {
	allow := true
	for _, rule := range d.settings.Rules {
		if rule.Matches(path, d.server.CaseInsensitiveFs) {
			allow = rule.Allow
		}
	}

	for _, rule := range d.user.Rules {
		if rule.Matches(path, d.server.CaseInsensitiveFs) {
			allow = rule.Allow
		}
	}

	return allow
}

// resolvedRulePath returns the path, in the form the rules are written in,
// that path leads to once symbolic links are followed. It reports false when
// that cannot be told or lies outside the scope the rules are relative to:
// the scoped filesystem refuses such a path on its own, and when external
// symlinks are allowed no rule can describe it.
func (d *data) resolvedRulePath(path string) (string, bool) {
	real, ok := d.realPath(path)
	if !ok {
		return "", false
	}

	root := d.scopeRoot
	if root == "" {
		root = filepath.Clean(d.user.FullPath("/"))
	}
	root, ok = d.resolvePath(root)
	if !ok {
		return "", false
	}

	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	return slashClean(filepath.ToSlash(rel)), true
}

// realPath returns where path, a path in the user's filesystem, is on disk
// once symbolic links are followed.
func (d *data) realPath(path string) (string, bool) {
	if files.BasePath(d.user.Fs) == nil {
		return "", false
	}

	return d.resolvePath(filepath.Clean(d.user.FullPath(slashClean(path))))
}

// maxLinkHops bounds the symbolic links followed while resolving one path, so
// that a loop ends.
const maxLinkHops = 255

// resolvePath follows the symbolic links in the absolute path full and returns
// where it leads. A path that does not exist resolves to where it would be
// created: the nearest existing ancestor is resolved and the rest appended,
// and a dangling link is followed to its target, which is where a write
// through it would land. It reports false when the path cannot be resolved.
//
// Results are kept for the request. Listing a directory checks every entry
// in it, and this way the directory itself is resolved once and each entry
// costs a single lstat.
func (d *data) resolvePath(full string) (string, bool) {
	hops := 0
	return d.resolvePathHops(full, &hops)
}

func (d *data) resolvePathHops(full string, hops *int) (string, bool) {
	if cached, ok := d.resolved[full]; ok {
		return cached, cached != ""
	}

	result, ok := d.resolvePathUncached(full, hops)
	if d.resolved == nil {
		d.resolved = map[string]string{}
	}
	if !ok {
		result = ""
	}
	d.resolved[full] = result

	return result, ok
}

func (d *data) resolvePathUncached(full string, hops *int) (string, bool) {
	parent := filepath.Dir(full)
	if parent == full {
		// The root of the filesystem, or of a volume.
		return full, true
	}

	realParent, ok := d.resolvePathHops(parent, hops)
	if !ok {
		return "", false
	}

	candidate := filepath.Join(realParent, filepath.Base(full))
	info, err := os.Lstat(candidate)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return candidate, true
	case err != nil:
		return "", false
	case info.Mode()&os.ModeSymlink == 0:
		return candidate, true
	}

	if *hops++; *hops > maxLinkHops {
		return "", false
	}
	target, err := os.Readlink(candidate)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		// Relative to the directory that really holds the link.
		target = filepath.Join(realParent, target)
	}

	return d.resolvePathHops(filepath.Clean(target), hops)
}

// rulePath canonicalizes path into the form the rules are written in.
func (d *data) rulePath(path string) string {
	// Rules are written as "/"-separated virtual paths, but callers hand us
	// paths built by the OS as well as ones taken from the request: afero.Walk
	// and filepath.Join use "\" on Windows, where the filesystem also treats it
	// as a separator. Canonicalize first so the authorization decision does not
	// depend on which separator the caller happened to use.
	path = slashClean(path)

	// When the filesystem has been rebased (e.g. a public share rooted at a
	// subdirectory), the incoming path is relative to that root. Resolve it
	// back to the user's original scope before matching rules, otherwise rules
	// targeting paths below the share root would be silently bypassed.
	if d.checkerPrefix != "" {
		path = gopath.Join(d.checkerPrefix, path)
	}

	return path
}

func handle(fn handleFunc, prefix string, store *storage.Storage, server *settings.Server) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range globalHeaders {
			w.Header().Set(k, v)
		}

		settings, err := store.Settings.Get()
		if err != nil {
			log.Fatalf("ERROR: couldn't get settings: %v\n", err)
			return
		}

		status, err := fn(w, r, &data{
			Runner:   &runner.Runner{Enabled: server.EnableExec, Settings: settings},
			store:    store,
			settings: settings,
			server:   server,
		})

		if status >= 400 || err != nil {
			clientIP := realip.FromRequest(r)
			log.Printf("%s: %v %s %v", r.URL.Path, status, clientIP, err)
		}

		if status != 0 {
			txt := http.StatusText(status)
			if status == http.StatusBadRequest && err != nil {
				txt += " (" + err.Error() + ")"
			}
			http.Error(w, strconv.Itoa(status)+" "+txt, status)
			return
		}
	})

	return stripPrefix(prefix, handler)
}
