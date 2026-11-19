package fbhttp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/rules"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// Share creation and the recursive listing looked the path up on disk without
// consulting the rules first, so their answer (200 or 404) told a user whether
// a path they are denied exists. Both must refuse a denied path before
// touching the filesystem, as the other resource handlers do.
func TestRuleDeniedPathsAreNotAnExistenceOracle(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{{Path: "/hr", Allow: false}}
	})
	e.writeBobFile("hr/salaries.xlsx", "x")
	if err := os.MkdirAll(filepath.Join(e.bobDir, "hr", "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	tok := e.login("bob", fullEnvUserPassword)

	requests := map[string]struct{ method, existing, missing string }{
		"resource (control)": {http.MethodGet, "/api/resources/hr/salaries.xlsx", "/api/resources/hr/nope.xlsx"},
		"share creation":     {http.MethodPost, "/api/share/hr/salaries.xlsx", "/api/share/hr/nope.xlsx"},
		"recursive listing":  {http.MethodGet, "/api/resources/recursive/hr/reviews", "/api/resources/recursive/hr/nope"},
	}
	for name, rq := range requests {
		t.Run(name, func(t *testing.T) {
			existing, _, _ := e.do(rq.method, rq.existing, tok, strings.NewReader("{}"), nil)
			missing, _, _ := e.do(rq.method, rq.missing, tok, strings.NewReader("{}"), nil)
			if existing != http.StatusForbidden || missing != http.StatusForbidden {
				t.Errorf("existing path = %d, missing path = %d; want 403 for both", existing, missing)
			}
		})
	}

	if links, err := e.st.Share.FindByUserID(2); err == nil && len(links) != 0 {
		t.Errorf("a share was created for a rule-denied path: %+v", links[0])
	}

	// Paths the rules allow keep working.
	e.writeBobFile("ok/file.txt", "x")
	if code, _, body := e.do(http.MethodPost, "/api/share/ok/file.txt", tok, strings.NewReader("{}"), nil); code != http.StatusOK {
		t.Errorf("share of an allowed path = %d %q, want 200", code, body)
	}
	if code, _, body := e.do(http.MethodGet, "/api/resources/recursive/ok", tok, nil, nil); code != http.StatusOK {
		t.Errorf("recursive listing of an allowed path = %d %q, want 200", code, body)
	}
}
