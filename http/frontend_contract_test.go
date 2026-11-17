package fbhttp

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/filebrowser/filebrowser/v2/auth"
	"github.com/filebrowser/filebrowser/v2/settings"
)

// The user requests of the web interface, with the exact "which" lists it
// sends (camelCase), and when it sends the current password with them. This
// pins the contract between the two: display preferences go through without
// the password, everything else needs it.
func TestUserUpdatePayloadsOfTheWebInterface(t *testing.T) {
	e := newFullEnv(t)
	bob := e.login("bob", fullEnvUserPassword)
	admin := e.login("admin", fullEnvAdminPassword)

	prefs := `"locale":"fr","hideDotfiles":true,"singleClick":true,"redirectAfterCopyMove":true,"dateFormat":true,"aceEditorTheme":"chrome","sorting":{"by":"modified","asc":true},"viewMode":"mosaic"`

	withoutPassword := map[string]string{
		"profile settings": `["locale","hideDotfiles","singleClick","redirectAfterCopyMove","dateFormat","aceEditorTheme"]`,
		"sorting":          `["sorting"]`,
		"view mode":        `["viewMode"]`,
	}
	for name, which := range withoutPassword {
		t.Run(name+" without current_password", func(t *testing.T) {
			payload := `{"what":"user","which":` + which + `,"data":{"id":2,` + prefs + `}}`
			if code, body := e.putUser(bob, 2, payload); code != http.StatusOK {
				t.Errorf("which=%s = %d %q, want 200", which, code, body)
			}
		})
	}
	u := e.user(2)
	if u.Locale != "fr" || !u.HideDotfiles || !u.SingleClick || !u.RedirectAfterCopyMove || !u.DateFormat ||
		u.AceEditorTheme != "chrome" || u.Sorting.By != "modified" || !u.Sorting.Asc || u.ViewMode != "mosaic" {
		t.Errorf("the preferences were not stored: %+v", u)
	}

	// Everything else the interface sends carries the current password, and
	// is refused without it.
	stored, _ := json.Marshal(e.user(2))
	var whole map[string]interface{}
	_ = json.Unmarshal(stored, &whole)
	whole["password"] = ""
	wholeUser, _ := json.Marshal(whole)

	withPassword := []struct {
		name, method, path, token, password, which, data string
		want                                             int
	}{
		{"own password", http.MethodPut, "/api/users/2", bob, fullEnvUserPassword, `["password"]`, `{"id":2,"password":"a-brand-new-password-5"}`, http.StatusOK},
		{"user edit form", http.MethodPut, "/api/users/2", admin, fullEnvAdminPassword, `["all"]`, string(wholeUser), http.StatusOK},
		{"new user", http.MethodPost, "/api/users", admin, fullEnvAdminPassword, `[]`, `{"username":"carol","password":"a-long-enough-password-3","scope":"/users/carol"}`, http.StatusCreated},
	}
	for _, rq := range withPassword {
		t.Run(rq.name, func(t *testing.T) {
			without := `{"what":"user","which":` + rq.which + `,"data":` + rq.data + `}`
			if code, _, body := e.do(rq.method, rq.path, rq.token, strings.NewReader(without), nil); code != http.StatusBadRequest {
				t.Errorf("without current_password = %d %q, want 400", code, strings.TrimSpace(body))
			}
			empty := `{"what":"user","which":` + rq.which + `,"current_password":"","data":` + rq.data + `}`
			if code, _, body := e.do(rq.method, rq.path, rq.token, strings.NewReader(empty), nil); code != http.StatusBadRequest {
				t.Errorf("with an empty current_password = %d %q, want 400", code, strings.TrimSpace(body))
			}
			with := `{"what":"user","which":` + rq.which + `,"current_password":"` + rq.password + `","data":` + rq.data + `}`
			if code, _, body := e.do(rq.method, rq.path, rq.token, strings.NewReader(with), nil); code != rq.want {
				t.Errorf("with current_password = %d %q, want %d", code, strings.TrimSpace(body), rq.want)
			}
		})
	}

	// Under an authentication method without passwords the interface sends an
	// empty current_password, and nothing is asked for.
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	set.AuthMethod = auth.MethodProxyAuth
	if err := e.st.Settings.Save(set); err != nil {
		t.Fatal(err)
	}
	payload := `{"what":"user","which":["all"],"current_password":"","data":` + string(wholeUser) + `}`
	if code, body := e.putUser(admin, 2, payload); code != http.StatusOK {
		t.Errorf("user edit form under proxy authentication = %d %q, want 200", code, body)
	}
	create := `{"what":"user","which":[],"current_password":"","data":{"username":"dave","password":"a-long-enough-password-3","scope":"/users/dave"}}`
	if code, _, body := e.do(http.MethodPost, "/api/users", admin, strings.NewReader(create), nil); code != http.StatusCreated {
		t.Errorf("new user under proxy authentication = %d %q, want 201", code, strings.TrimSpace(body))
	}
}

// The web interface ships some of its scripts only compressed (the editor's
// modules and workers, as *.js.gz without a plain *.js next to them) and
// relies on /static/*.js being answered from the .js.gz file. Workers are
// started from these URLs, so the response must stay a same-origin script.
func TestStaticServesScriptsFromTheirGzipSibling(t *testing.T) {
	const script = `self.onmessage = function () { postMessage("ready") }`
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	assets := fstest.MapFS{
		"public/index.html":           &fstest.MapFile{Data: []byte("<html></html>")},
		"ace/worker-javascript.js.gz": &fstest.MapFile{Data: compressed.Bytes()},
		"ace/ace.js.gz":               &fstest.MapFile{Data: compressed.Bytes()},
		"assets/app.css":              &fstest.MapFile{Data: []byte("body{}")},
	}

	for _, baseURL := range []string{"", "/files"} {
		e := newFullEnvOpts(t, fullEnvOptions{
			assets:    assets,
			configure: func(_ string, server *settings.Server) { server.BaseURL = baseURL },
		})

		for _, name := range []string{"ace/worker-javascript.js", "ace/ace.js"} {
			for encoding, wantEncoded := range map[string]bool{"gzip": true, "identity": false} {
				req, _ := http.NewRequest(http.MethodGet, e.srv.URL+baseURL+"/static/"+name, http.NoBody)
				req.Header.Set("Accept-Encoding", encoding)
				// The transport must not decompress on its own.
				res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()

				if res.StatusCode != http.StatusOK {
					t.Fatalf("%s%s (Accept-Encoding %s) = %d", baseURL, name, encoding, res.StatusCode)
				}
				if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
					t.Errorf("%s: Content-Type = %q", name, ct)
				}
				if got := res.Header.Get("Content-Encoding") == "gzip"; got != wantEncoded {
					t.Errorf("%s (Accept-Encoding %s): Content-Encoding = %q", name, encoding, res.Header.Get("Content-Encoding"))
				}
				if wantEncoded {
					zr, err := gzip.NewReader(bytes.NewReader(body))
					if err != nil {
						t.Fatalf("%s: not gzip: %v", name, err)
					}
					body, _ = io.ReadAll(zr)
				}
				if string(body) != script {
					t.Errorf("%s (Accept-Encoding %s) = %q", name, encoding, body)
				}
				if res.Header.Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("%s: no X-Content-Type-Options", name)
				}
				// Nothing on the response stops the page from loading the
				// script or starting it as a worker of its own origin.
				if xfo := res.Header.Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
					t.Errorf("%s: X-Frame-Options = %q", name, xfo)
				}
				if cspHas(res.Header, "sandbox") || cspHas(res.Header, "script-src 'none'") {
					t.Errorf("%s: policy %q would stop the script", name, res.Header.Values("Content-Security-Policy"))
				}
			}
		}

		// A script that exists neither plain nor compressed is not found, and
		// other assets are served as they are.
		if code, _, _ := e.do(http.MethodGet, baseURL+"/static/ace/missing.js", "", nil, nil); code != http.StatusNotFound {
			t.Errorf("missing script = %d, want 404", code)
		}
		if code, h, body := e.do(http.MethodGet, baseURL+"/static/assets/app.css", "", nil, nil); code != http.StatusOK || body != "body{}" || !strings.HasPrefix(h.Get("Content-Type"), "text/css") {
			t.Errorf("stylesheet = %d %q (%q)", code, body, h.Get("Content-Type"))
		}
	}
}
