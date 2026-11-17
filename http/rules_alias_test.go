package fbhttp

import (
	"archive/tar"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/users"
)

// aliasEnv prepares bob's scope with a rule-denied directory and symbolic
// links, inside the scope, that lead into it.
func aliasEnv(t *testing.T) *fullEnv {
	t.Helper()
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{{Path: "/denied", Allow: false}}
	})
	e.writeBobFile("denied/secret.txt", "TOP-SECRET")
	e.writeBobFile("allowed/ok.txt", "fine")
	e.writeBobFile("pub/readme.txt", "public")
	for link, target := range map[string]string{
		"allowed/link":    "../denied",            // directory alias
		"pub/flink":       "../denied/secret.txt", // file alias
		"pub/chain":       "flink",                // alias of an alias
		"pub/newlink":     "../denied/new.txt",    // dangling: a write would create the target
		"pub/harmless":    "readme.txt",           // an alias of something allowed
		"allowed/samedir": ".",
	} {
		if err := os.Symlink(target, filepath.Join(e.bobDir, filepath.FromSlash(link))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	return e
}

// Rules were matched against the path as it was written, while the filesystem
// follows symbolic links that stay inside the scope. A link to a rule-denied
// file or directory therefore opened it under a name no rule mentions: for
// reading, for overwriting, for a copy that left the denied content behind as
// an ordinary file, and for a visitor of a public share
// (GHSA-7w29-q235-57m9). The rules now also apply to the path a request
// resolves to.
func TestRulesApplyToTheResolvedPath(t *testing.T) {
	e := aliasEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	secret := filepath.Join(e.bobDir, "denied", "secret.txt")

	// Control: the denied path itself.
	if code, _, _ := e.do(http.MethodGet, "/api/raw/denied/secret.txt", tok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("control: GET of the denied file = %d, want 403", code)
	}

	forbidden := []struct{ method, path, body string }{
		{http.MethodGet, "/api/raw/allowed/link/secret.txt", ""},
		{http.MethodGet, "/api/raw/pub/flink", ""},
		{http.MethodGet, "/api/raw/pub/chain", ""},
		{http.MethodGet, "/api/resources/allowed/link/", ""},
		{http.MethodGet, "/api/resources/allowed/link/secret.txt", ""},
		{http.MethodGet, "/api/resources/allowed/samedir/link/secret.txt", ""},
		{http.MethodGet, "/api/resources/recursive/allowed/link", ""},
		{http.MethodGet, "/api/preview/big/pub/flink", ""},
		{http.MethodGet, "/api/subtitle/allowed/link/secret.txt", ""},
		{http.MethodPut, "/api/resources/allowed/link/secret.txt", "PWNED"},
		{http.MethodPut, "/api/resources/pub/flink", "PWNED"},
		{http.MethodPost, "/api/resources/pub/flink?override=true", "PWNED"},
		{http.MethodPost, "/api/resources/allowed/link/new.txt", "PWNED"},
		{http.MethodPost, "/api/resources/pub/newlink?override=true", "PWNED"},
		{http.MethodPost, "/api/tus/pub/flink?override=true", ""},
		{http.MethodPost, "/api/tus/allowed/link/upload.bin", ""},
		{http.MethodDelete, "/api/resources/allowed/link/secret.txt", ""},
		{http.MethodPost, "/api/share/pub/flink", "{}"},
		{http.MethodPost, "/api/share/allowed/link", "{}"},
		{http.MethodPatch, "/api/resources/allowed/link/secret.txt?action=rename&destination=%2Fstolen.txt", ""},
		{http.MethodPatch, "/api/resources/pub/readme.txt?action=copy&destination=%2Fallowed%2Flink%2Fplanted.txt", ""},
		// A copy of the directory would follow the link and leave the denied
		// content behind as an ordinary file.
		{http.MethodPatch, "/api/resources/pub?action=copy&destination=%2Fdst", ""},
	}
	for _, rq := range forbidden {
		code, _, body := e.do(rq.method, rq.path, tok, strings.NewReader(rq.body), map[string]string{"Upload-Length": "5"})
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", rq.method, rq.path, code)
		}
		if strings.Contains(body, "TOP-SECRET") {
			t.Errorf("%s %s returned the denied content", rq.method, rq.path)
		}
	}

	if data, err := os.ReadFile(secret); err != nil || string(data) != "TOP-SECRET" {
		t.Errorf("the denied file was changed through an alias: %q, %v", data, err)
	}
	for _, created := range []string{"denied/new.txt", "denied/upload.bin", "denied/planted.txt", "dst/flink", "stolen.txt"} {
		if pathExists(filepath.Join(e.bobDir, filepath.FromSlash(created))) {
			t.Errorf("%s was created through an alias", created)
		}
	}
	if links, err := e.st.Share.FindByUserID(2); err == nil && len(links) != 0 {
		t.Errorf("a share of a denied path was created through an alias: %+v", links[0])
	}

	// Listings and archives leave the aliases of denied paths out, and keep
	// everything else, including aliases of allowed paths.
	_, _, listing := e.do(http.MethodGet, "/api/resources/pub/", tok, nil, nil)
	for name, listed := range map[string]bool{`"flink"`: false, `"chain"`: false, `"newlink"`: false, `"readme.txt"`: true, `"harmless"`: true} {
		if strings.Contains(listing, `"name":`+name) != listed {
			t.Errorf("listing of /pub: %s listed = %v, want %v", name, !listed, listed)
		}
	}

	_, _, archive := e.do(http.MethodGet, "/api/raw/?algo=tar", tok, nil, nil)
	if strings.Contains(archive, "TOP-SECRET") {
		t.Error("the archive of the scope contains the denied content")
	}
	names := map[string]bool{}
	tr := tar.NewReader(strings.NewReader(archive))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = true
	}
	for _, want := range []string{"allowed/ok.txt", "pub/readme.txt", "pub/harmless"} {
		if !names[want] {
			t.Errorf("%s is missing from the archive: %v", want, names)
		}
	}

	_, _, found := e.do(http.MethodGet, "/api/search/?query=link", tok, nil, nil)
	if strings.Contains(found, "flink") || strings.Contains(found, "allowed/link") {
		t.Errorf("search names the aliases of denied paths: %s", found)
	}

	// What the rules allow still works through a link.
	if code, _, body := e.do(http.MethodGet, "/api/raw/pub/harmless", tok, nil, nil); code != http.StatusOK || body != "public" {
		t.Errorf("alias of an allowed file = %d %q", code, body)
	}
	if code, _, body := e.do(http.MethodGet, "/api/raw/allowed/samedir/ok.txt", tok, nil, nil); code != http.StatusOK || body != "fine" {
		t.Errorf("allowed file through a directory alias = %d %q", code, body)
	}
}

// The same through a public share: its visitors are bound by the owner's
// rules, also for what a link inside the share leads to.
func TestRulesApplyToTheResolvedPathInPublicShares(t *testing.T) {
	e := aliasEnv(t)
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{{Path: "/denied", Allow: false}, {Path: "/sub/denied", Allow: false}}
	})
	e.writeBobFile("sub/denied/inner.txt", "INNER-SECRET")
	e.writeBobFile("sub/open/readme.txt", "open")
	if err := os.Symlink("../denied/inner.txt", filepath.Join(e.bobDir, "sub", "open", "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tok := e.login("bob", fullEnvUserPassword)
	rootShare := e.createShare(tok, "/", "{}")
	subShare := e.createShare(tok, "/sub", "{}")

	for _, path := range []string{
		rootShare + "/denied/secret.txt", // control
		rootShare + "/pub/flink",
		rootShare + "/pub/chain",
		rootShare + "/allowed/link/secret.txt",
		subShare + "/denied/inner.txt", // control
		subShare + "/open/alias",
	} {
		for _, endpoint := range []string{"/api/public/dl/", "/api/public/share/"} {
			code, _, body := e.do(http.MethodGet, endpoint+path, "", nil, nil)
			if code != http.StatusForbidden || strings.Contains(body, "SECRET") {
				t.Errorf("anonymous GET %s%s = %d, want 403", endpoint, path, code)
			}
		}
	}

	for _, share := range []string{rootShare, subShare} {
		if _, _, archive := e.do(http.MethodGet, "/api/public/dl/"+share+"?algo=tar", "", nil, nil); strings.Contains(archive, "SECRET") {
			t.Errorf("the archive of share %s contains denied content", share)
		}
	}
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/"+subShare+"/open/readme.txt", "", nil, nil); code != http.StatusOK || body != "open" {
		t.Errorf("allowed file in the share = %d %q", code, body)
	}
}
