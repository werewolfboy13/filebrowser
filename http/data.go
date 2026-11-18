package fbhttp

import (
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

	// token is the session token the request was authenticated with.
	token string

	// loginToken tells that token was issued by a login that presented the
	// password, and not by a renewal: see authToken.PasswordLogin.
	loginToken bool

	// limits holds the limiters for failed logins and share passwords.
	limits *limits

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

	// resolver finds out where paths on disk lead once symbolic links are
	// followed, and remembers, for the length of the request, the directories
	// it went through. See resolvePath.
	resolver files.PathResolver

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
	//
	// No element of the path may be such a name, not only the last one. A
	// directory can be created by creating something inside it, and one with
	// the name of a temporary file holds the place that file needs: the
	// uploads that would use it could no longer be written safely.
	for _, element := range strings.Split(rulePath, "/") {
		if fileutils.IsTempName(element) {
			return false
		}
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

	switch resolved, where := d.resolvedRulePath(path); where {
	case leadsInside:
		if resolved != rulePath {
			return d.rulesAllow(resolved)
		}
	case leadsNowhere:
		// Where the path leads cannot be told, so it cannot be told whether
		// a rule denies that place. The kernel does not open such a path
		// either (it goes through a loop of links, or through something that
		// is not a directory), so nothing is lost by refusing it; should the
		// two ever disagree, this is the side to err on.
		return false
	}

	return true
}

// Where a path leads once symbolic links are followed, as far as the rules
// are concerned.
const (
	// leadsInside: to a place inside the scope the rules are relative to.
	leadsInside = iota
	// leadsOutside: out of that scope, or the filesystem is not one on disk.
	// The scoped filesystem refuses such a path on its own, and when
	// external symlinks are allowed no rule can describe it.
	leadsOutside
	// leadsNowhere: it cannot be resolved.
	leadsNowhere
)

// protected reports whether path names one of the server's own files that lie
// inside the served directory (see settings.Server.Protect): the database,
// which holds the key that signs sessions and every password hash, the
// configuration file, the cache directory, the TLS key and the log.
//
// A file can be reached by more names than the one it was registered under,
// so the path is compared as written, with symbolic links followed, and, for
// an existing file, by identity, which covers hard links.
//
// The identity is that of the file the kernel opens under the name, asked of
// the kernel itself: whatever resolvePath makes of the name, no other name of
// a protected file that exists gets past this.
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

	if resolved, ok := d.resolvePath(lexical); ok && resolved != lexical && d.server.IsProtected(resolved) {
		return true
	}

	info, err := os.Stat(lexical)
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
// that path leads to once symbolic links are followed, and which of
// leadsInside, leadsOutside and leadsNowhere it is. The path is only
// meaningful for leadsInside.
func (d *data) resolvedRulePath(path string) (resolved string, where int) {
	if files.BasePath(d.user.Fs) == nil {
		return "", leadsOutside
	}

	onDisk, ok := d.realPath(path)
	if !ok {
		return "", leadsNowhere
	}

	root := d.scopeRoot
	if root == "" {
		root = filepath.Clean(d.user.FullPath("/"))
	}
	root, ok = d.resolvePath(root)
	if !ok {
		return "", leadsNowhere
	}

	rel, err := filepath.Rel(root, onDisk)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", leadsOutside
	}

	return slashClean(filepath.ToSlash(rel)), leadsInside
}

// realPath returns where path, a path in the user's filesystem, is on disk
// once symbolic links are followed.
func (d *data) realPath(path string) (string, bool) {
	if files.BasePath(d.user.Fs) == nil {
		return "", false
	}

	return d.resolvePath(filepath.Clean(d.user.FullPath(slashClean(path))))
}

// resolvePath follows the symbolic links in the absolute path full the way
// the kernel does and returns where it leads; see files.EvalPath. A path that
// does not exist resolves to where it would be created: the nearest existing
// ancestor is resolved and the rest appended, and a dangling link is followed
// to its target, which is where a write through it would land. It reports
// false when the path cannot be resolved, which is when the kernel does not
// open it either.
//
// The directories on the way are kept for the request. Listing a directory
// checks every entry in it, and this way the directory itself is resolved
// once and each entry costs a single lstat. See files.PathResolver for the
// bound on what is kept.
func (d *data) resolvePath(full string) (string, bool) {
	return d.resolver.Resolve(full)
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
	return handleWith(fn, prefix, store, server, newLimits())
}

// handleWith is handle with the failure limiters of the handler the route
// belongs to. NewHandler shares one set between its routes.
func handleWith(fn handleFunc, prefix string, store *storage.Storage, server *settings.Server, limits *limits) http.Handler {
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
			limits:   limits,
		})

		// The handler is done with the request body, whether it read it or
		// not, and the server reads what is left of it before it sends the
		// answer. Give that a limit: a client that announces a body and never
		// sends it would otherwise hold the connection, and this goroutine,
		// for as long as it likes. An error is usually answered without the
		// body having been read (every unauthenticated request to a route
		// that takes a body ends that way), but so is a request that has no
		// use for a body and is carried out all the same: a delete, a copy,
		// a new directory. The limit used to be set for errors only.
		//
		// It is set here, after the handler, and never while it runs: a
		// deadline that passes while a handler is at work cancels the
		// request. A body that was read in full is not waited for, and the
		// server lifts the deadline before it reads the next request, so
		// neither an upload nor the request that follows is cut short.
		if r.ContentLength != 0 {
			limitBodyWait(w, leftoverBodyTimeout)
		}

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
