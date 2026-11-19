package fbhttp

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/werewolfboy13/filebrowser/v3/diskcache"
	"github.com/werewolfboy13/filebrowser/v3/img"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// recordingImgService records the context Resize was called with.
type recordingImgService struct {
	ctx context.Context
}

func (s *recordingImgService) FormatFromExtension(string) (img.Format, error) {
	return img.FormatPng, nil
}

func (s *recordingImgService) Resize(ctx context.Context, _ io.Reader, _, _ int, out io.Writer, _ ...img.Option) error {
	s.ctx = ctx
	_, err := out.Write([]byte("resized"))
	return err
}

// The preview used to be created under context.Background(), so a resize that
// was queued behind others, or already running, went on for a client that had
// left. It must run under the request's context.
func TestPreviewResizeRunsUnderTheRequestContext(t *testing.T) {
	scope := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "pic.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	key := []byte("test-signing-key")
	perm := users.Permissions{Download: true}
	st := scopedUserStorage(t, scope, perm, key)

	svc := &recordingImgService{}
	router := mux.NewRouter()
	router.PathPrefix("/api/preview/{size}/{path:.*}").Handler(
		handle(previewHandler(svc, diskcache.NewNoOp(), true, true), "/api/preview", st, &settings.Server{}))

	type ctxKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "request"))
	req := httptest.NewRequest(http.MethodGet, "/api/preview/thumb/pic.png", http.NoBody).WithContext(ctx)
	req.Header.Set("X-Auth", signToken(t, perm, key))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d %q", rec.Code, rec.Body.String())
	}
	if svc.ctx == nil {
		t.Fatal("Resize was not called")
	}
	if svc.ctx.Value(ctxKey{}) != "request" {
		t.Error("Resize was not called with the request's context")
	}
	cancel()
	if svc.ctx.Err() == nil {
		t.Error("cancelling the request did not cancel the resize context")
	}
}

// oversizedPNG builds a 16-bit RGBA PNG of zeros, 6000 pixels a side: a few
// megabytes on disk, 288 MiB once decoded.
func oversizedPNG(t *testing.T) []byte {
	t.Helper()

	const side = 6000
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	chunk := func(typ string, data []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(data)))
		out.Write(l[:])
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		out.WriteString(typ)
		out.Write(data)
		binary.BigEndian.PutUint32(l[:], crc.Sum32())
		out.Write(l[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], side)
	binary.BigEndian.PutUint32(ihdr[4:], side)
	ihdr[8] = 16 // bit depth
	ihdr[9] = 6  // RGBA
	chunk("IHDR", ihdr)

	var idat bytes.Buffer
	zw, err := zlib.NewWriterLevel(&idat, zlib.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]byte, 1+side*8)
	for i := 0; i < side; i++ {
		if _, err := zw.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	chunk("IDAT", idat.Bytes())
	chunk("IEND", nil)

	return out.Bytes()
}

// End to end: a small upload that would decode to hundreds of megabytes is
// never decoded. The thumbnail is refused with 413, and the preview the viewer
// asks for is the file as it is, which costs the server nothing.
func TestPreviewOfAnImageOverTheDecodeBudget(t *testing.T) {
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)

	bomb := oversizedPNG(t)
	if code, _, body := e.do(http.MethodPost, "/api/resources/bomb.png", tok, bytes.NewReader(bomb), nil); code != http.StatusOK {
		t.Fatalf("upload = %d %q", code, body)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	if code, _, _ := e.do(http.MethodGet, "/api/preview/thumb/bomb.png", tok, nil, nil); code != http.StatusRequestEntityTooLarge {
		t.Errorf("preview/thumb of the oversized image = %d, want 413", code)
	}

	// Four at once, as a viewer and its neighbours would ask.
	type answer struct {
		code int
		hdr  http.Header
		body string
	}
	answers := make(chan answer, 4)
	for i := 0; i < cap(answers); i++ {
		go func() {
			code, hdr, body := e.do(http.MethodGet, "/api/preview/big/bomb.png", tok, nil, nil)
			answers <- answer{code, hdr, body}
		}()
	}
	for i := 0; i < cap(answers); i++ {
		got := <-answers
		if got.code != http.StatusOK || got.body != string(bomb) {
			t.Errorf("preview/big of the oversized image = %d with %d bytes, want 200 and the %d bytes of the file", got.code, len(got.body), len(bomb))
		}
		// Served like any other download of the file.
		if got.hdr.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(strings.Join(got.hdr.Values("Content-Security-Policy"), " "), "script-src 'none'") {
			t.Errorf("the original was served without the headers of a download: %v", got.hdr)
		}
	}

	runtime.ReadMemStats(&after)
	// Decoding it once takes 288 MiB. Reading the file and sending it four
	// times allocates a few times its size.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 96<<20 {
		t.Errorf("the previews allocated %d MiB", allocated>>20)
	}
}
