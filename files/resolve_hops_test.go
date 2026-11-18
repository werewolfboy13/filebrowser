package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// linkChain makes n links in dir, named prefix0 to prefix<n-1>, each leading
// to the next and the last one to target.
func linkChain(t *testing.T, dir, prefix string, n int, target string) {
	t.Helper()
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("%s%d", prefix, i+1)
		if i == n-1 {
			next = target
		}
		if err := os.Symlink(next, filepath.Join(dir, fmt.Sprintf("%s%d", prefix, i))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
}

// The kernel follows 40 symbolic links while it resolves one path, all of its
// elements taken together, and refuses the path after that. The resolver
// followed 255, and PathResolver started counting again with every element of
// a path: a path of a few elements, each a long chain, was walked in full
// (with one lstat for every element of every target on the way) although
// nothing can ever be opened under it. With rules in place that is paid for
// every entry of a listing or a search.
func TestResolversFollowAsManyLinksAsTheKernel(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"d", "d1/d2"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Chains of 40 and of 41 links to the directory d.
	linkChain(t, root, "forty", 40, "d")
	linkChain(t, root, "fortyone", 41, "d")
	// Two chains of 25, one behind the other: a leads to d1, and b, in d1,
	// leads to d2.
	linkChain(t, root, "a", 25, "d1")
	linkChain(t, filepath.Join(root, "d1"), "b", 25, "d2")

	kernelRefuses := func(p string) bool {
		_, err := os.Stat(p)
		return errors.Is(err, syscall.ELOOP)
	}
	if runtime.GOOS == "linux" {
		for rel, want := range map[string]bool{
			"forty0": false, "fortyone0": true, "a0": false, "a0/b0": true, "a0/b10": false, "a0/b9": true, "a0/../a0": true,
		} {
			// Not filepath.Join, which would clean the ".." away.
			if got := kernelRefuses(root + string(filepath.Separator) + filepath.FromSlash(rel)); got != want {
				t.Fatalf("control: the kernel refuses %s: %v, want %v", rel, got, want)
			}
		}
	}

	cases := map[string]struct {
		path string
		want string // "" when the path is refused
	}{
		"40 links in a row":                        {path: "forty0/x", want: "d/x"},
		"41 links in a row":                        {path: "fortyone0/x"},
		"25 links":                                 {path: "a0/x", want: "d1/x"},
		"25 links and then 25 more":                {path: "a0/b0/x"},
		"25 links and then 25 more, to the link":   {path: "a0/b0"},
		"25 links and then 15 more":                {path: "a0/b10/x", want: "d1/d2/x"},
		"25 links and then 16 more":                {path: "a0/b9/x"},
		"25 links, back up, and the same 25 again": {path: "a0/../a0/x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			full := root + string(filepath.Separator) + filepath.FromSlash(tc.path)
			want := ""
			if tc.want != "" {
				want = filepath.Join(root, filepath.FromSlash(tc.want))
			}

			got, err := EvalPath(full)
			if (err == nil) != (want != "") || got != want {
				t.Errorf("EvalPath = %q, %v; want %q", got, err, want)
			}

			// PathResolver cleans the path it is given, so "a0/../a0" is
			// "a0" to it: that case is EvalPath's alone.
			if filepath.Clean(full) != full {
				return
			}
			for _, warm := range []bool{false, true} {
				var r PathResolver
				if warm {
					// With the directories on the way already remembered.
					r.Resolve(filepath.Join(filepath.Dir(full), "other"))
					r.Resolve(filepath.Dir(full))
				}
				got, ok := r.Resolve(full)
				if ok != (want != "") || got != want {
					t.Errorf("PathResolver.Resolve (directories remembered: %v) = %q, %v; want %q", warm, got, ok, want)
				}
			}
		})
	}
}
