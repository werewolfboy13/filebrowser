package fbhttp

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tarNames returns the entry names of a tar archive.
func tarNames(t *testing.T, archive string) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(strings.NewReader(archive))
	for {
		h, err := tr.Next()
		if err != nil {
			return names
		}
		names = append(names, h.Name)
	}
}

func countSuffix(names []string, suffix string) int {
	n := 0
	for _, name := range names {
		if strings.HasSuffix(name, suffix) {
			n++
		}
	}
	return n
}

// The archive download followed directory symlinks without remembering where
// it had been. A link back to its own directory multiplied the tree by the
// kernel's symlink limit (40 levels), and two such links made the walk
// exponential (2^40 branches), growing in memory until the process died. It is
// reachable without logging in, through a public share of such a directory.
// Every real directory is now archived once.
func TestArchiveVisitsEachDirectoryOnce(t *testing.T) {
	e := newFullEnv(t)
	tree := filepath.Join(e.bobDir, "tree")
	e.writeBobFile("tree/data.bin", strings.Repeat("D", 1024))
	e.writeBobFile("tree/sub/inner.txt", "inner")
	e.writeBobFile("other/linked.txt", "linked")
	link := func(name, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(tree, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	tok := e.login("bob", fullEnvUserPassword)
	hash := e.createShare(tok, "/tree", "{}")

	// archives downloads the tree both ways and checks how often each file
	// is in the archive. linked is how often linked.txt is expected in the
	// owner's own download; the share is rooted at /tree, so a link that
	// leaves it is not followed there.
	archives := func(stage string, linked int) {
		t.Helper()
		requests := map[string]struct {
			path, token string
			linked      int
		}{
			"authenticated": {"/api/raw/tree/?algo=tar", tok, linked},
			"public share":  {"/api/public/dl/" + hash + "?algo=tar", "", 0},
		}
		for name, rq := range requests {
			done := make(chan struct{})
			var (
				code int
				body string
			)
			go func() {
				defer close(done)
				code, _, body = e.do(http.MethodGet, rq.path, rq.token, nil, nil)
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatalf("%s, %s: the archive did not finish: the walk is following symlink cycles", stage, name)
			}

			if code != http.StatusOK {
				t.Fatalf("%s, %s: archive = %d", stage, name, code)
			}
			names := tarNames(t, body)
			for file, want := range map[string]int{"data.bin": 1, "inner.txt": 1, "linked.txt": rq.linked} {
				if n := countSuffix(names, file); n != want {
					t.Errorf("%s, %s: %s is in the archive %d times, want %d", stage, name, file, n, want)
				}
			}
		}
	}

	// One cycle first. A walk that follows it without remembering where it
	// has been still ends, when the kernel gives up on the link (after 40
	// levels), with every file in the archive that many times: it fails here,
	// on the counts, at once.
	link("self", ".")
	archives("one cycle", 0)
	if t.Failed() {
		// With a second cycle such a walk has 2^40 branches. It would not
		// come back, and it would keep running, and growing, behind every
		// test that follows.
		t.Fatal("the walk follows a symlink cycle: not adding a second one")
	}

	link("again", ".")         // second cycle: exponential without a visited set
	link("up", "sub/..")       // the same directory by another spelling
	link("other", "../other")  // a different directory: its content stays in
	link("twice", "../other/") // ... but only once
	archives("two cycles and aliases", 1)
}

// The walk that collects the files for an archive must stop when the request
// is done, instead of finishing a large tree for a client that has left.
func TestArchiveWalkStopsWhenTheRequestIsDone(t *testing.T) {
	e := newFullEnv(t)
	for _, f := range []string{"a/1.txt", "a/b/2.txt", "c/3.txt"} {
		e.writeBobFile("tree/"+f, "x")
	}
	bob := e.user(2)
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	d := &data{settings: set, server: e.server, store: e.st, user: bob}

	ctx, cancel := context.WithCancel(context.Background())
	walk := newArchiveWalk(ctx, d, "/tree")
	if entries, err := walk.files("/tree"); err != nil || len(entries) != 6 {
		t.Fatalf("walk of the whole tree = %d entries, %v; want 6", len(entries), err)
	}

	cancel()
	walk = newArchiveWalk(ctx, d, "/tree")
	entries, err := walk.files("/tree")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("walk under a cancelled context returned %d entries and %v, want context.Canceled", len(entries), err)
	}

	// Through the handler: a request that is already cancelled produces no
	// archive at all.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/raw/tree/?algo=tar", http.NoBody)
	req.Header.Set("X-Auth", e.login("bob", fullEnvUserPassword))
	if res, err := e.srv.Client().Do(req); err == nil {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		res.Body.Close()
		t.Errorf("a cancelled request was answered with %d and %d bytes", res.StatusCode, buf.Len())
	}
}
