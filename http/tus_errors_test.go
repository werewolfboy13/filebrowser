package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TUS error messages are echoed to the client, so they must name the path as
// the user sees it. They used to include the absolute path on the server,
// which the user API deliberately hides from non-admins.
func TestTusErrorsDoNotDiscloseServerPath(t *testing.T) {
	e := newFullEnv(t)
	if err := os.MkdirAll(filepath.Join(e.bobDir, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeBobFile("partial.bin", "12345")
	tok := e.login("bob", fullEnvUserPassword)

	code, _, body := e.do(http.MethodPost, "/api/tus/somedir", tok, nil, map[string]string{"Upload-Length": "1"})
	if code != http.StatusBadRequest {
		t.Fatalf("TUS POST onto a directory = %d %q, want 400", code, body)
	}
	if strings.Contains(body, e.root) {
		t.Errorf("TUS POST error discloses the server path: %q", body)
	}
	if !strings.Contains(body, "/somedir") {
		t.Errorf("TUS POST error should name the requested path, got %q", body)
	}

	// Same message on the PATCH side, reached once an upload is registered for
	// a path that then turns out to be a directory.
	code, _, body = e.do(http.MethodPost, "/api/tus/up.bin", tok, nil, map[string]string{"Upload-Length": "4"})
	if code != http.StatusCreated {
		t.Fatalf("TUS create = %d %q", code, body)
	}
	for _, target := range []string{"/api/tus/up.bin", "/api/tus/somedir"} {
		for _, offset := range []string{"0", "3"} {
			_, _, body = e.do(http.MethodPatch, target, tok, strings.NewReader("abcd"), map[string]string{
				"Upload-Offset": offset, "Content-Type": "application/offset+octet-stream",
			})
			if strings.Contains(body, e.root) {
				t.Errorf("TUS PATCH %s at offset %s discloses the server path: %q", target, offset, body)
			}
		}
	}
}
