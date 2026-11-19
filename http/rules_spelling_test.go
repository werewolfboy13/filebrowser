package fbhttp

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/rules"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// escapePath escapes a path for a request line, element by element.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// A filesystem that ignores case, or how a name is composed, opens a file
// under more spellings than the one a rule was written with. APFS does both:
// a directory denied as "café" was served as "cafe" + a combining accent,
// "straße" as "STRASSE", "ﬁles" as "files".
//
// The test runs on whatever the temporary directory is on. Where that keeps
// the spellings apart, the other spellings are directories of their own, with
// the same content, and the server is told that the filesystem ignores case:
// the rules are what is tested, and they decide by the name alone. Point
// TMPDIR at a filesystem that does ignore them to see it for real.
func TestRulesDenyEverySpellingOfADeniedName(t *testing.T) {
	e := newFullEnv(t)
	ignoresCase := e.server.CaseInsensitiveFs
	t.Logf("the filesystem under test ignores case: %v", ignoresCase)

	denied := []struct {
		rule      string
		spellings []string
	}{
		{"/caf\u00e9", []string{"/cafe\u0301", "/CAF\u00c9", "/CAFE\u0301"}},
		{"/stra\u00dfe", []string{"/STRASSE", "/strasse", "/STRA\u1e9eE"}},
		{"/\ufb01les", []string{"/files", "/FILES", "/Files"}},
		{"/plain", []string{"/PLAIN", "/Plain"}},
		// Cherokee has both cases, and folding gave each letter in the
		// other one: with something else in the name to undo (here how the
		// "é" is composed), the small letters got past a rule written with
		// the capitals. APFS opens the same directory under both.
		{"/\u13a0\u00e9", []string{"/\uab70e\u0301", "/\uab70\u00e9", "/\u13a0e\u0301"}},
		// The capital of a dotless i is "I": one name, to a filesystem that
		// compares in upper case (NTFS).
		{"/priv\u00e9", []string{"/pr\u0131ve\u0301", "/PR\u0131V\u00c9"}},
	}

	var list []rules.Rule
	for _, d := range denied {
		list = append(list, rules.Rule{Allow: false, Path: d.rule})
		e.writeBobFile(strings.TrimPrefix(d.rule, "/")+"/secret.txt", "top secret")
		for _, spelling := range d.spellings {
			dir := filepath.Join(e.bobDir, filepath.FromSlash(spelling))
			if _, err := os.Stat(dir); err == nil {
				continue // the same directory, to this filesystem
			}
			e.writeBobFile(strings.TrimPrefix(spelling, "/")+"/secret.txt", "top secret")
		}
	}
	e.setBob(func(u *users.User) { u.Rules = list })
	e.server.CaseInsensitiveFs = true
	tok := e.login("bob", fullEnvUserPassword)

	for _, d := range denied {
		for _, spelling := range append([]string{d.rule}, d.spellings...) {
			for _, route := range []string{"/api/raw", "/api/resources"} {
				code, _, body := e.do(http.MethodGet, route+escapePath(spelling+"/secret.txt"), tok, nil, nil)
				if code != http.StatusForbidden || strings.Contains(body, "top secret") {
					t.Errorf("GET %s%+q (denied as %+q) = %d %.40q, want 403", route, spelling+"/secret.txt", d.rule, code, body)
				}
			}
			code, _, _ := e.do(http.MethodDelete, "/api/resources"+escapePath(spelling+"/secret.txt"), tok, nil, nil)
			if code != http.StatusForbidden {
				t.Errorf("DELETE %+q (denied as %+q) = %d, want 403", spelling+"/secret.txt", d.rule, code)
			}
		}
	}

	// Nothing else is denied.
	e.writeBobFile("open/readme.txt", "hello")
	if code, _, body := e.do(http.MethodGet, "/api/raw/open/readme.txt", tok, nil, nil); code != http.StatusOK || body != "hello" {
		t.Errorf("GET of a file no rule denies = %d %q", code, body)
	}
}

// How a name is composed never tells two names apart for a deny rule, also
// where the filesystem is taken to tell upper from lower case: there are
// filesystems that ignore the one and not the other, and nobody can see the
// difference in a name.
func TestRulesDenyTheOtherCompositionOnACaseSensitiveFilesystem(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("caf\u00e9/secret.txt", "top secret")
	if _, err := os.Stat(filepath.Join(e.bobDir, "cafe\u0301")); err != nil {
		e.writeBobFile("cafe\u0301/secret.txt", "top secret")
	}
	e.setBob(func(u *users.User) { u.Rules = []rules.Rule{{Allow: false, Path: "/caf\u00e9"}} })
	e.server.CaseInsensitiveFs = false
	tok := e.login("bob", fullEnvUserPassword)

	for _, spelling := range []string{"/caf\u00e9", "/cafe\u0301"} {
		code, _, body := e.do(http.MethodGet, "/api/raw"+escapePath(spelling+"/secret.txt"), tok, nil, nil)
		if code != http.StatusForbidden || strings.Contains(body, "top secret") {
			t.Errorf("GET %+q = %d %.40q, want 403", spelling+"/secret.txt", code, body)
		}
	}
}

// An allow rule opens what a deny rule closes, so it is not widened with the
// deny rules: the directory it names is open, and one that merely folds to
// the same letters stays closed.
func TestAllowRulesAreNotWidenedBySpelling(t *testing.T) {
	e := newFullEnv(t)
	ignoresCase := e.server.CaseInsensitiveFs
	e.writeBobFile("closed/stra\u00dfe/note.txt", "allowed")
	if _, err := os.Stat(filepath.Join(e.bobDir, "closed", "strasse")); err != nil {
		e.writeBobFile("closed/strasse/note.txt", "not allowed")
	}
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{
			{Allow: false, Path: "/closed"},
			{Allow: true, Path: "/closed/stra\u00dfe"},
		}
	})
	e.server.CaseInsensitiveFs = true
	tok := e.login("bob", fullEnvUserPassword)

	if code, _, body := e.do(http.MethodGet, "/api/raw"+escapePath("/closed/stra\u00dfe/note.txt"), tok, nil, nil); code != http.StatusOK || body != "allowed" {
		t.Errorf("GET of the allowed directory = %d %q", code, body)
	}
	code, _, body := e.do(http.MethodGet, "/api/raw/closed/strasse/note.txt", tok, nil, nil)
	if code != http.StatusForbidden {
		t.Errorf("GET of a directory that folds to the allowed one = %d %q, want 403 (the filesystem ignores case: %v)", code, body, ignoresCase)
	}
}

// The temporary file of an upload in progress is refused by name, in every
// spelling a filesystem can open it under: with "ff" written as the
// ligature, the file of another user's upload was served.
func TestUploadTemporariesAreInternalInFoldedSpellings(t *testing.T) {
	e := newFullEnv(t)
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)

	// A destination whose temporary name holds an "ff".
	var dst, temp string
	for i := 0; i < 1000; i++ {
		dst = "secret-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".bin"
		if temp = filepath.Base(tusTempPath("/" + dst)); strings.Contains(temp, "ff") {
			break
		}
	}
	if !strings.Contains(temp, "ff") {
		t.Fatal("found no name whose temporary name can be folded")
	}

	code, hdr, body := e.do(http.MethodPost, "/api/tus/"+dst, bob, http.NoBody, map[string]string{"Upload-Length": "20"})
	if code != http.StatusCreated {
		t.Fatalf("tus create = %d %q", code, body)
	}
	code, _, _ = e.do(http.MethodPatch, hdr.Get("Location"), bob, strings.NewReader("CONFIDENTI"), map[string]string{
		"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0",
	})
	if code != http.StatusNoContent {
		t.Fatalf("first chunk = %d", code)
	}
	if temps := uploadTempsIn(t, e.bobDir); len(temps) != 1 || temps[0] != temp {
		t.Fatalf("temporary files of the upload: %v, want %s", temps, temp)
	}

	// As in TestUploadTemporariesAreInternalInAnySpelling: where the
	// filesystem keeps the spellings apart, a file plays the other one.
	spellings := []string{
		strings.Replace(temp, "ff", "\ufb00", 1),
		strings.ToUpper(strings.Replace(temp, "ff", "\ufb00", 1)),
		strings.Replace(temp, "tus", "tu\u017f", 1),
	}
	for _, name := range spellings {
		if _, err := os.Stat(filepath.Join(e.bobDir, name)); err != nil {
			e.writeBobFile(name, "CONFIDENTI")
		}
	}

	for _, name := range spellings {
		for _, rq := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/raw/users/bob/" + url.PathEscape(name), ""},
			{http.MethodGet, "/api/resources/users/bob/" + url.PathEscape(name), ""},
			{http.MethodPut, "/api/resources/users/bob/" + url.PathEscape(name), "CHANGED"},
			{http.MethodDelete, "/api/resources/users/bob/" + url.PathEscape(name), ""},
		} {
			code, _, body := e.do(rq.method, rq.path, admin, strings.NewReader(rq.body), nil)
			if code != http.StatusForbidden || strings.Contains(body, "CONFIDENTI") {
				t.Errorf("%s %+q = %d %q, want 403", rq.method, name, code, body)
			}
		}
		if data, err := os.ReadFile(filepath.Join(e.bobDir, name)); err != nil || string(data) != "CONFIDENTI" {
			t.Errorf("%+q holds %q (%v) after the requests", name, data, err)
		}
	}

	_, _, listing := e.do(http.MethodGet, "/api/resources/users/bob/", admin, nil, nil)
	if strings.Contains(listing, "fbtmp") || strings.Contains(listing, "FBTMP") {
		t.Errorf("a temporary file is listed: %s", listing)
	}
}
