package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// resolveTree builds a directory with the kinds of links that matter: plain
// aliases, chains, a link that climbs out of another link with "..", absolute
// targets, loops and dangling links.
func resolveTree(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"denied/deep/deeper", "pub", "other/inner"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"denied/secret.txt", "pub/readme.txt", "other/inner/file.txt", "top.txt"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(file)), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Created in this order, which is also a valid dependency order.
	links := [][2]string{
		{"pub/sub", "../denied/deep"},
		{"pub/alias", "sub/../secret.txt"},
		{"pub/dalias", "sub/.."},
		{"pub/twice", "sub/deeper/../../secret.txt"},
		{"pub/chain", "alias"},
		{"pub/slash", "sub/../"},
		{"pub/dots", "./sub/./../secret.txt"},
		{"pub/abs", root + "/pub/sub/../secret.txt"}, // not filepath.Join, which would clean the ".." away
		{"pub/plain", "readme.txt"},
		{"pub/up", "../other"},
		{"pub/overroot", strings.Repeat("../", 64) + strings.TrimPrefix(filepath.ToSlash(root), "/") + "/top.txt"},
		{"pub/newalias", "sub/../new.txt"},
		{"pub/newdir", "sub/../made"},
		{"pub/loop", "loop"},
		{"pub/ping", "pong"},
		{"pub/pong", "ping"},
		{"pub/throughfile", "readme.txt/../readme.txt"},
		{"pub/gone", "nowhere/../readme.txt"},
	}
	for _, l := range links {
		if err := os.Symlink(l[1], filepath.Join(root, filepath.FromSlash(l[0]))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	return root
}

// EvalPath must lead where the kernel leads. For paths that exist that is what
// filepath.EvalSymlinks reports; the old resolver cleaned a link's target
// lexically, so "sub/../x" became "x" even when "sub" is itself a link.
func TestEvalPathAgreesWithTheKernel(t *testing.T) {
	root := resolveTree(t)

	existing := []string{
		"", "pub", "pub/readme.txt", "pub/sub", "pub/sub/deeper", "pub/alias", "pub/dalias",
		"pub/dalias/secret.txt", "pub/dalias/deep/deeper", "pub/twice", "pub/chain", "pub/slash",
		"pub/slash/secret.txt", "pub/dots", "pub/abs", "pub/plain", "pub/up", "pub/up/inner/file.txt",
		"pub/overroot", "denied/secret.txt", "pub/sub/../secret.txt",
	}
	for _, rel := range existing {
		// Built without filepath.Join, which would clean the ".." away.
		p := root + string(filepath.Separator) + filepath.FromSlash(rel)
		want, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("EvalSymlinks(%s): %v", rel, err)
		}
		got, err := EvalPath(p)
		if err != nil || got != want {
			t.Errorf("EvalPath(%s) = %q, %v; the kernel leads to %q", rel, got, err, want)
		}

		// And the kernel agrees with both: the file opened under the name is
		// the file at the resolved path.
		a, errA := os.Stat(p)
		b, errB := os.Stat(got)
		if errA != nil || errB != nil || !os.SameFile(a, b) {
			t.Errorf("EvalPath(%s) = %q is not the file the kernel opens (%v, %v)", rel, got, errA, errB)
		}
	}

	// A path that does not exist resolves to where it would be created.
	missing := map[string]string{
		"pub/new.txt":           "pub/new.txt",
		"pub/a/b/c.txt":         "pub/a/b/c.txt",
		"pub/sub/new.txt":       "denied/deep/new.txt",
		"pub/dalias/new.txt":    "denied/new.txt",
		"pub/newalias":          "denied/new.txt",
		"pub/newdir/inside.txt": "denied/made/inside.txt",
		"pub/up/inner/new":      "other/inner/new",
	}
	for rel, want := range missing {
		got, err := EvalPath(filepath.Join(root, filepath.FromSlash(rel)))
		if want = filepath.Join(root, filepath.FromSlash(want)); err != nil || got != want {
			t.Errorf("EvalPath(%s) = %q, %v; want %q", rel, got, err, want)
		}
	}

	// What the kernel refuses to open is not resolved either.
	for _, rel := range []string{"pub/loop", "pub/ping", "pub/loop/x", "pub/throughfile", "pub/readme.txt/x", "pub/gone"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("the kernel opens %s", rel)
		}
		if got, err := EvalPath(p); err == nil {
			t.Errorf("EvalPath(%s) = %q, want an error: the kernel refuses the path", rel, got)
		}
	}
}

func TestPathResolverCachesAndStaysBounded(t *testing.T) {
	root := resolveTree(t)

	var r PathResolver
	for _, rel := range []string{"pub/alias", "pub/dalias/secret.txt", "pub/newalias", "pub/readme.txt", "pub/alias"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		want, err := EvalPath(p)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := r.Resolve(p); !ok || got != want {
			t.Errorf("Resolve(%s) = %q, %v; want %q", rel, got, ok, want)
		}
	}
	if _, ok := r.Resolve(filepath.Join(root, "pub", "loop", "x")); ok {
		t.Error("a path through a link loop was resolved")
	}

	// Only directories are remembered, and only so many of them.
	for i := 0; i < 3*maxResolvedDirs; i++ {
		dir := filepath.Join(root, "pub", "d"+strings.Repeat("x", i%7), string(rune('a'+i%26)), strings.Repeat("y", 1+i/26))
		if _, ok := r.Resolve(filepath.Join(dir, "file")); !ok {
			t.Fatalf("Resolve below %s failed", dir)
		}
		if len(r.dirs) > maxResolvedDirs {
			t.Fatalf("the resolver remembers %d directories, more than %d", len(r.dirs), maxResolvedDirs)
		}
	}
}

// A dangling link whose target climbs out of another link ("sub/../new.txt",
// with sub leading out of the scope) was checked where a lexical reading of
// the target puts it, inside the scope, while a write through it creates the
// file where the kernel puts it: outside.
func TestScopedFsRefusesDanglingLinksThatClimbOutOfTheScope(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(base, "srv", "users", "bob")
	if err := os.MkdirAll(filepath.Join(scope, "pub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scope, "inside", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{
		{"pub/sub", "../.."},                      // srv/users: outside the scope
		{"pub/esc", "sub/../new.txt"},             // the kernel: srv/new.txt
		{"pub/escabs", scope + "/pub/sub/../abs"}, // the kernel: srv/abs
		{"pub/escdir", "sub/../made"},             // the kernel: srv/made
		{"pub/in", "../inside/deep"},
		{"pub/ok", "in/../fine.txt"}, // the kernel: inside/fine.txt, within the scope
	} {
		if err := os.Symlink(l[1], filepath.Join(scope, filepath.FromSlash(l[0]))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	sfs := NewScopedFs(afero.NewOsFs(), scope)

	for _, name := range []string{"/pub/esc", "/pub/escabs", "/pub/escdir/file.txt"} {
		f, err := sfs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err == nil {
			_, _ = f.WriteString("planted")
			_ = f.Close()
		}
		if !os.IsPermission(err) {
			t.Errorf("OpenFile(%s) for writing = %v, want a permission error", name, err)
		}
		if err := afero.WriteFile(sfs, name, []byte("planted"), 0o644); !os.IsPermission(err) {
			t.Errorf("WriteFile(%s) = %v, want a permission error", name, err)
		}
	}
	if err := sfs.MkdirAll("/pub/escdir/a/b", 0o755); !os.IsPermission(err) {
		t.Errorf("MkdirAll through the escaping link = %v, want a permission error", err)
	}
	for _, planted := range []string{"srv/new.txt", "srv/abs", "srv/made"} {
		if _, err := os.Lstat(filepath.Join(base, filepath.FromSlash(planted))); err == nil {
			t.Errorf("%s was created outside the scope", planted)
		}
	}

	// The same shape of link that stays inside the scope keeps working.
	if err := afero.WriteFile(sfs, "/pub/ok", []byte("fine"), 0o644); err != nil {
		t.Errorf("WriteFile through a dangling link that stays inside = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(scope, "inside", "fine.txt")); err != nil || string(data) != "fine" {
		t.Errorf("inside/fine.txt = %q, %v", data, err)
	}
}
