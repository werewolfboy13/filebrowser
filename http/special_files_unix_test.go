//go:build unix

package fbhttp

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/werewolfboy13/filebrowser/v2/settings"
)

// Opening a named pipe blocks until something opens its other end, which for
// a file that merely sits in a served directory is never. Only the raw
// download of the pipe itself was guarded; every other way of reading or
// writing a file's content hung the request for good, including for a visitor
// of a public share (GHSA-8q5j-8wcr-8v2v). Nothing but a regular file may be
// opened for its content, however it is reached.
func TestRequestsDoNotBlockOnNamedPipes(t *testing.T) {
	e := newFullEnvWith(t, func(_ string, server *settings.Server) {
		// Exercise the paths that read file headers and image dimensions.
		server.TypeDetectionByHeader = true
		server.ImageResolutionCal = true
	})

	for _, f := range []string{"r/normal.txt", "plain/normal.txt"} {
		e.writeBobFile(f, "x")
	}
	for _, dir := range []string{"r", "w", "links", "img"} {
		if err := os.MkdirAll(filepath.Join(e.bobDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Read and write cases use separate pipes: a reader left blocked by one
	// case would otherwise let a later writer through, and hide its hang.
	pipes := map[string]string{}
	for _, name := range []string{
		"r/pipe", "r/pipe.srt", "r/pipe.vtt", "img/pipe.png",
		"w/postpipe", "w/putpipe", "w/tuspipe", "w/copydst", "w/target", "w/target.png", "w/target.srt",
	} {
		p := filepath.Join(e.bobDir, filepath.FromSlash(name))
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Skipf("named pipes unavailable: %v", err)
		}
		pipes[name] = p
	}
	// The same pipes reached through symbolic links: the link is what is
	// stat'ed first, and it looks like any other link.
	for link, target := range map[string]string{
		"links/pipe":       "../w/target",
		"links/pipe.png":   "../w/target.png",
		"links/pipe.srt":   "../w/target.srt",
		"links/writepipe":  "../w/target",
		"links/uploadpipe": "../w/target",
	} {
		if err := os.Symlink(target, filepath.Join(e.bobDir, filepath.FromSlash(link))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	// However the test ends, release every handler that is stuck on a pipe so
	// the server can shut down: open each pipe for writing (releases blocked
	// readers) and for reading (releases blocked writers).
	t.Cleanup(func() {
		for round := 0; round < 50; round++ {
			for _, p := range pipes {
				if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					f.Close()
				}
				if f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = f.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
					_, _ = io.Copy(io.Discard, f)
					f.Close()
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	tok := e.login("bob", fullEnvUserPassword)
	dirShare := e.createShare(tok, "/r", "{}")
	pipeShare := e.createShare(tok, "/r/pipe", "{}")
	linkShare := e.createShare(tok, "/links/pipe", "{}")

	type request struct {
		name, method, path string
		anonymous          bool
		body               []byte
		header             map[string]string
	}
	upload := map[string]string{"Upload-Length": "10"}
	chunk := bytes.Repeat([]byte("A"), 128<<10)
	requests := []request{
		{name: "control: archive of a directory without a pipe", method: "GET", path: "/api/raw/plain/?algo=zip"},
		{name: "raw download of the pipe", method: "GET", path: "/api/raw/r/pipe"},
		{name: "raw download through a symlink", method: "GET", path: "/api/raw/links/pipe"},
		{name: "archive of a directory holding a pipe", method: "GET", path: "/api/raw/r/?algo=zip"},
		{name: "archive of a directory holding a symlink to a pipe", method: "GET", path: "/api/raw/links/?algo=zip"},
		{name: "listing of a directory holding a pipe", method: "GET", path: "/api/resources/r/"},
		{name: "listing with image dimensions", method: "GET", path: "/api/resources/img/"},
		{name: "listing of a directory holding symlinks to pipes", method: "GET", path: "/api/resources/links/"},
		{name: "resource of the pipe", method: "GET", path: "/api/resources/r/pipe"},
		{name: "resource through a symlink", method: "GET", path: "/api/resources/links/pipe"},
		{name: "resource of an image-named pipe through a symlink", method: "GET", path: "/api/resources/links/pipe.png"},
		{name: "text content of the pipe", method: "GET", path: "/api/resources/r/pipe", header: map[string]string{"X-Encoding": "true"}},
		{name: "checksum", method: "GET", path: "/api/resources/r/pipe?checksum=md5"},
		{name: "checksum through a symlink", method: "GET", path: "/api/resources/links/pipe?checksum=md5"},
		{name: "subtitle", method: "GET", path: "/api/subtitle/r/pipe.srt"},
		{name: "subtitle served as is", method: "GET", path: "/api/subtitle/r/pipe.vtt"},
		{name: "subtitle through a symlink", method: "GET", path: "/api/subtitle/links/pipe.srt"},
		{name: "preview", method: "GET", path: "/api/preview/thumb/img/pipe.png"},
		{name: "preview through a symlink", method: "GET", path: "/api/preview/big/links/pipe.png"},
		{name: "public share of a directory: archive", method: "GET", path: "/api/public/dl/" + dirShare + "?algo=zip", anonymous: true},
		{name: "public share of a directory: listing", method: "GET", path: "/api/public/share/" + dirShare, anonymous: true},
		{name: "public share of a directory: the pipe", method: "GET", path: "/api/public/dl/" + dirShare + "/pipe", anonymous: true},
		{name: "public share of the pipe: download", method: "GET", path: "/api/public/dl/" + pipeShare, anonymous: true},
		{name: "public share of the pipe: info", method: "GET", path: "/api/public/share/" + pipeShare, anonymous: true},
		{name: "public share of a symlink to a pipe", method: "GET", path: "/api/public/dl/" + linkShare, anonymous: true},
		{name: "copy of a directory holding a pipe", method: "PATCH", path: "/api/resources/r?action=copy&destination=%2Frcopy"},
		{name: "copy of a file onto a pipe", method: "PATCH", path: "/api/resources/plain/normal.txt?action=copy&destination=%2Fw%2Fcopydst&override=true"},
		{name: "POST onto the pipe", method: "POST", path: "/api/resources/w/postpipe?override=true", body: chunk},
		{name: "PUT onto the pipe", method: "PUT", path: "/api/resources/w/putpipe", body: chunk},
		{name: "PUT through a symlink", method: "PUT", path: "/api/resources/links/writepipe", body: chunk},
		{name: "TUS upload onto the pipe", method: "POST", path: "/api/tus/w/tuspipe?override=true", header: upload},
		{name: "TUS upload through a symlink", method: "POST", path: "/api/tus/links/uploadpipe?override=true", header: upload},
	}

	client := &http.Client{Timeout: 3 * time.Second}
	for _, rq := range requests {
		t.Run(rq.name, func(t *testing.T) {
			var body io.Reader = http.NoBody
			if rq.body != nil {
				body = bytes.NewReader(rq.body)
			}
			req, err := http.NewRequest(rq.method, e.srv.URL+rq.path, body)
			if err != nil {
				t.Fatal(err)
			}
			if !rq.anonymous {
				req.Header.Set("X-Auth", tok)
			}
			for k, v := range rq.header {
				req.Header.Set(k, v)
			}

			start := time.Now()
			res, err := client.Do(req)
			if err == nil {
				_, err = io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			if err != nil {
				t.Fatalf("no answer after %s: the handler is blocked on the pipe (%v)", time.Since(start).Round(time.Millisecond), err)
			}
			// A directory copy reports what it could not copy as a failure,
			// after copying the rest.
			if res.StatusCode == http.StatusInternalServerError && !strings.HasPrefix(rq.name, "copy of a directory") {
				t.Errorf("answered %d, want a deliberate refusal rather than a server error", res.StatusCode)
			}
		})
	}

	// Refusing the pipes must not have cost the regular files next to them.
	if code, _, body := e.do(http.MethodGet, "/api/raw/r/normal.txt", tok, nil, nil); code != http.StatusOK || body != "x" {
		t.Errorf("regular file next to the pipes = %d %q", code, body)
	}
	if data, err := os.ReadFile(filepath.Join(e.bobDir, "rcopy", "normal.txt")); err != nil || string(data) != "x" {
		t.Errorf("the copy did not copy the regular file next to the pipe: %q, %v", data, err)
	}
}
