package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// The search walk is handed a nil FileInfo for an entry it could not stat,
// which is what the scoped filesystem answers for a symlink that leaves the
// scope. The search handler called IsDir on it and panicked, dropping the
// connection. Such entries are skipped; the rest of the tree is still searched.
func TestSearchSkipsEntriesItCannotStat(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "user")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(scope, "docs"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(scope, "docs", "wanted.txt"), filepath.Join(scope, "zz-wanted.txt"), filepath.Join(outside, "wanted-secret.txt")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The link's own name matches the query, so the walk reports it as a hit.
	if err := os.Symlink(outside, filepath.Join(scope, "link-wanted")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	key := []byte("test-signing-key")
	perm := users.Permissions{}
	st := scopedUserStorage(t, scope, perm, key)

	// With a matching term and with no term at all (which matches everything).
	for _, query := range []string{"wanted", ""} {
		req, _ := http.NewRequest(http.MethodGet, "/?query="+query, http.NoBody)
		req.Header.Set("X-Auth", signToken(t, perm, key))
		rec := httptest.NewRecorder()

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("query %q: the search handler panicked: %v", query, r)
				}
			}()
			handle(searchHandler, "", st, &settings.Server{}).ServeHTTP(rec, req)
		}()

		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("query %q: search = %d %q", query, rec.Code, body)
		}
		for _, want := range []string{"docs/wanted.txt", "zz-wanted.txt"} {
			if !strings.Contains(body, want) {
				t.Errorf("query %q: %s is missing from the results: %q", query, want, body)
			}
		}
		if strings.Contains(body, "secret") || strings.Contains(body, "link-wanted") {
			t.Errorf("query %q: the result names the link or what is behind it: %q", query, body)
		}
	}
}
