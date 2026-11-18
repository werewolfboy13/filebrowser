package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxLinkHops bounds the symbolic links followed while resolving one path,
// over all of its elements, so that a loop ends. It is the bound of the Linux
// kernel: a path that takes more is refused there, so there is nothing under
// it to resolve, and following it further only costs time (one lstat for
// every element of every link target on the way).
const maxLinkHops = 40

// errTooManyLinks is returned for a path that goes through more symbolic links
// than maxLinkHops, which is what a loop of links looks like.
var errTooManyLinks = errors.New("too many levels of symbolic links")

// EvalPath follows the symbolic links in the absolute path p the way the
// kernel does when it opens p, and returns where that leads.
//
// The kernel reads a path one element at a time. It follows a link as soon as
// it meets one, and ".." then means the parent of where it is by then, not the
// parent of the name it came through: with sub a link to /a/b, "sub/../x" is
// /a/x. Reading the path as text first ("sub/../x" is "x") gives another
// answer, and anything decided on that answer is decided about a file the
// request never touches. For the same reason the target of a link is spliced
// into the path as it is written, not cleaned first.
//
// Unlike filepath.EvalSymlinks, a path that does not exist is resolved as
// well, to where it would be created: the part that exists is followed, the
// rest appended, and a dangling link is followed to its target, which is
// where a write through it lands.
//
// It returns an error for a path the kernel refuses too: one that goes
// through something that is not a directory, that steps back ("..") out of
// something that does not exist, or that loops.
func EvalPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", &os.PathError{Op: "resolve", Path: p, Err: errors.New("not an absolute path")}
	}

	vol := filepath.VolumeName(p)
	hops := 0
	return evalFrom(vol+string(filepath.Separator), vol, splitPath(p[len(vol):]), &hops)
}

// splitPath cuts a path into its elements, leaving out the empty ones a
// leading, trailing or doubled separator makes.
func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool {
		return r == filepath.Separator || r == '/'
	})
}

// evalFrom resolves the elements in pending, one after the other, starting in
// dir, which must be a directory that is already resolved (or the root). vol
// is the volume dir is on: where a link whose target starts with a separator
// leads on Windows.
func evalFrom(dir, vol string, pending []string, hops *int) (string, error) {
	cur := dir

	// missing is set once an element does not exist. What follows it cannot
	// be a link, so it is appended as it is written.
	missing := false

	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]

		switch name {
		case ".":
			continue
		case "..":
			if missing {
				// The kernel does not step back out of what is not there.
				return "", &os.PathError{Op: "resolve", Path: cur, Err: fs.ErrNotExist}
			}
			// cur holds no links any more, so its parent is the directory
			// that really contains it. The parent of the root is the root.
			cur = filepath.Dir(cur)
			continue
		}

		next := filepath.Join(cur, name)
		if missing {
			cur = next
			continue
		}

		info, err := os.Lstat(next)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = true
			cur = next
			continue
		case err != nil:
			// Among others: cur is not a directory, or may not be searched.
			return "", err
		}

		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() && len(pending) > 0 {
				return "", &os.PathError{Op: "resolve", Path: next, Err: syscall.ENOTDIR}
			}
			cur = next
			continue
		}

		if *hops++; *hops > maxLinkHops {
			return "", &os.PathError{Op: "resolve", Path: next, Err: errTooManyLinks}
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if target == "" {
			return "", &os.PathError{Op: "resolve", Path: next, Err: fs.ErrNotExist}
		}

		// The target takes the place of the link in the path: what is left of
		// the path is read after it, from wherever it leads. A relative
		// target starts in the directory that really holds the link, which is
		// cur.
		switch targetVol := filepath.VolumeName(target); {
		case filepath.IsAbs(target):
			vol = targetVol
			cur = vol + string(filepath.Separator)
			target = target[len(targetVol):]
		case targetVol != "":
			// Relative to the current directory of another drive (Windows):
			// there is no telling where that is.
			return "", &os.PathError{Op: "resolve", Path: next, Err: errors.New("drive-relative link target")}
		case os.IsPathSeparator(target[0]):
			// Rooted on the volume of the link (Windows).
			cur = vol + string(filepath.Separator)
		}
		pending = append(splitPath(target), pending...)
	}

	return cur, nil
}

// maxResolvedDirs bounds the directories a PathResolver remembers.
const maxResolvedDirs = 1024

// PathResolver resolves paths like EvalPath and remembers the directories it
// went through, so that resolving every entry of a directory (which is what
// listing it, searching it or archiving it does) costs one lstat per entry
// instead of one per element of its path.
//
// Only directories are remembered, never the paths asked for, and at most
// maxResolvedDirs of them: a walk over a tree of any size keeps a bounded
// amount. It is meant to live for one request, and is not safe for concurrent
// use.
type PathResolver struct {
	// dirs maps a directory, as it is written, to where it leads.
	dirs map[string]resolvedDir
}

// resolvedDir is where a directory leads, and the symbolic links that were
// followed to get there: they count for every path that goes through the
// directory, as they do for the kernel. An empty path means that the
// directory cannot be resolved.
type resolvedDir struct {
	path string
	hops int
}

// Resolve returns where the absolute path full leads; see EvalPath. It
// reports false when the path cannot be resolved.
func (r *PathResolver) Resolve(full string) (string, bool) {
	if !filepath.IsAbs(full) {
		return "", false
	}
	resolved := r.resolve(filepath.Clean(full))
	return resolved.path, resolved.path != ""
}

func (r *PathResolver) resolve(full string) resolvedDir {
	parent := filepath.Dir(full)
	if parent == full {
		// The root of the filesystem, or of a volume.
		return resolvedDir{path: full}
	}

	realParent := r.dir(parent)
	if realParent.path == "" {
		return resolvedDir{}
	}

	// The parent is resolved already, so the rest of the path is read from
	// there. That is what the kernel does with it too: it reads a path from
	// the left, and what a prefix leads to does not depend on what follows.
	// The links it took to get there are counted on.
	hops := realParent.hops
	resolved, err := evalFrom(realParent.path, filepath.VolumeName(realParent.path), []string{filepath.Base(full)}, &hops)
	if err != nil {
		return resolvedDir{}
	}
	return resolvedDir{path: resolved, hops: hops}
}

// dir is resolve for a path that is the parent of another, and remembers the
// answer.
func (r *PathResolver) dir(p string) resolvedDir {
	if cached, ok := r.dirs[p]; ok {
		return cached
	}

	resolved := r.resolve(p)

	// Starting over when full keeps the memory bounded without bookkeeping.
	// A walk visits a tree depth first, so what it needs next is found again
	// after a few lstats.
	if r.dirs == nil || len(r.dirs) >= maxResolvedDirs {
		r.dirs = map[string]resolvedDir{}
	}
	r.dirs[p] = resolved

	return resolved
}
