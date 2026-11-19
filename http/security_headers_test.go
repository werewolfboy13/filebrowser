package fbhttp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/auth"
	"github.com/werewolfboy13/filebrowser/v3/settings"
)

func cspHas(h http.Header, directive string) bool {
	for _, policy := range h.Values("Content-Security-Policy") {
		for _, d := range strings.Split(policy, ";") {
			if strings.TrimSpace(d) == directive {
				return true
			}
		}
	}
	return false
}

// No response forbade being framed by another site, and none set a referrer
// policy (share links carry a token in the query string). The application page
// in particular is not a matched route: it is served by the router's
// NotFoundHandler, which the router's middleware does not run for, so it had
// none of the headers the API responses get.
func TestEveryResponseCarriesFramingAndReferrerHeaders(t *testing.T) {
	for _, baseURL := range []string{"", "/files"} {
		e := newFullEnvWith(t, func(_ string, server *settings.Server) { server.BaseURL = baseURL })
		e.writeBobFile("doc.txt", "x")
		tok := e.login("bob", fullEnvUserPassword)
		hash := e.createShare(tok, "/doc.txt", "{}")

		requests := []struct{ name, method, path, token string }{
			{"application page", http.MethodGet, "/", ""},
			{"application page, login route", http.MethodGet, "/login", ""},
			{"application page, files route", http.MethodGet, "/files/some/dir", ""},
			{"application page, share route", http.MethodGet, "/share/" + hash, ""},
			{"health", http.MethodGet, "/health", ""},
			{"static asset", http.MethodGet, "/static/missing.css", ""},
			{"login", http.MethodPost, "/api/login", ""},
			{"unauthenticated API call", http.MethodGet, "/api/resources/", ""},
			{"listing", http.MethodGet, "/api/resources/", tok},
			{"raw download", http.MethodGet, "/api/raw/doc.txt", tok},
			{"inline raw", http.MethodGet, "/api/raw/doc.txt?inline=true", tok},
			{"public share", http.MethodGet, "/api/public/share/" + hash, ""},
			{"public download", http.MethodGet, "/api/public/dl/" + hash, ""},
			{"unknown API route", http.MethodGet, "/api/nope", ""},
			{"method without a route", http.MethodDelete, "/api/login", ""},
		}
		for _, rq := range requests {
			t.Run(baseURL+" "+rq.name, func(t *testing.T) {
				code, h, _ := e.do(rq.method, baseURL+rq.path, rq.token, nil, nil)
				if code == -1 {
					t.Fatal("no response")
				}
				if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
					t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
				}
				if !cspHas(h, "frame-ancestors 'self'") {
					t.Errorf("no frame-ancestors 'self' in Content-Security-Policy %q", h.Values("Content-Security-Policy"))
				}
				if got := h.Get("Referrer-Policy"); got != "same-origin" {
					t.Errorf("Referrer-Policy = %q, want same-origin", got)
				}
			})
		}

		// The policies that were there are still there.
		_, h, _ := e.do(http.MethodGet, baseURL+"/api/raw/doc.txt?inline=true", tok, nil, nil)
		for _, directive := range []string{"default-src 'self'", "style-src 'unsafe-inline'", "script-src 'none'"} {
			if !cspHas(h, directive) {
				t.Errorf("inline raw: %q is missing from %q", directive, h.Values("Content-Security-Policy"))
			}
		}
		// The application page gets no policy that would restrict its own
		// scripts and styles.
		_, h, body := e.do(http.MethodGet, baseURL+"/", "", nil, nil)
		if !strings.Contains(body, "window.FileBrowser") {
			t.Fatalf("the application page was not served: %q", body)
		}
		for _, policy := range h.Values("Content-Security-Policy") {
			if strings.Contains(policy, "default-src") || strings.Contains(policy, "script-src") {
				t.Errorf("the application page carries a fetch policy: %q", policy)
			}
		}
	}
}

// With reCAPTCHA enabled, the application page lets the origin (and nothing
// more) through to other sites, which reCAPTCHA needs to validate the domain.
// Every other response keeps the referrer within the site.
func TestReferrerPolicyWithReCaptcha(t *testing.T) {
	e := newFullEnv(t)
	if err := e.st.Auth.Save(&auth.JSONAuth{ReCaptcha: &auth.ReCaptcha{Host: "https://www.google.com", Key: "site-key", Secret: "secret"}}); err != nil {
		t.Fatal(err)
	}

	_, h, body := e.do(http.MethodGet, "/login", "", nil, nil)
	if !strings.Contains(body, "site-key") {
		t.Fatalf("reCAPTCHA is not enabled on the page: %q", body)
	}
	if got := h.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("application page: Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("application page: X-Frame-Options = %q", got)
	}

	_, h, _ = e.do(http.MethodGet, "/api/resources/", "", nil, nil)
	if got := h.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("API response: Referrer-Policy = %q, want same-origin", got)
	}
}
