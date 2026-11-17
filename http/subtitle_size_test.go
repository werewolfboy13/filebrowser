package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Converting a subtitle file to WebVTT parses all of it in memory, at several
// times its size. Without a limit, any file named *.srt (or .ass/.ssa) let a
// user with the Download permission make the server allocate without bound
// (GHSA-448h-jr2h-3vhp). Files larger than any real subtitle are refused.
func TestSubtitleConversionRefusesOversizedFiles(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)

	// Sparse files: they cost no disk space and, with the fix, are never read.
	for _, name := range []string{"movie.srt", "movie.ass", "movie.ssa"} {
		f, err := os.Create(filepath.Join(e.bobDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxSubtitleSize + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()

		code, _, body := e.do(http.MethodGet, "/api/subtitle/"+name, tok, nil, nil)
		if code != http.StatusRequestEntityTooLarge {
			t.Errorf("GET /api/subtitle/%s (%d bytes) = %d, want 413 (body %d bytes)", name, maxSubtitleSize+1, code, len(body))
		}
	}

	// A file at the limit is still converted, and WebVTT, which is streamed
	// rather than converted, has no limit.
	cue := "1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"
	e.writeBobFile("ok.srt", cue)
	if code, _, body := e.do(http.MethodGet, "/api/subtitle/ok.srt", tok, nil, nil); code != http.StatusOK || !strings.Contains(body, "Hello") {
		t.Errorf("small .srt = %d %q, want the converted subtitle", code, body)
	}

	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n\n"
	e.writeBobFile("big.vtt", vtt+strings.Repeat("\n", maxSubtitleSize))
	if code, _, body := e.do(http.MethodGet, "/api/subtitle/big.vtt", tok, nil, nil); code != http.StatusOK || !strings.HasPrefix(body, "WEBVTT") {
		t.Errorf("large .vtt = %d, want it served as is", code)
	}
}
