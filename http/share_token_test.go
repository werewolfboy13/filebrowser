package fbhttp

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func (e *fullEnv) shareInfoToken(hash, sub, password string) string {
	e.t.Helper()
	var hdr map[string]string
	if password != "" {
		hdr = map[string]string{"X-SHARE-PASSWORD": password}
	}
	code, _, body := e.do(http.MethodGet, "/api/public/share/"+hash+sub, "", nil, hdr)
	if code != http.StatusOK {
		e.t.Fatalf("share info = %d %q", code, body)
	}
	var info struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		e.t.Fatal(err)
	}
	return info.Token
}

// Downloads and previews of a password-protected share cannot send the
// password in a header, so the share page is handed a token to put in their
// URLs. That token was one fixed secret per share: once it had been in a URL
// (browser history, a proxy log, a copied link) it opened the share without
// the password for as long as the share existed (GHSA-3v48-283x-f2w4). The
// page is now handed a token that is derived for the occasion and expires.
func TestShareTokenIsTemporary(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("secret.txt", "protected content")
	e.writeBobFile("album/pic.txt", "a picture")
	e.writeBobFile("open.txt", "open content")
	tok := e.login("bob", fullEnvUserPassword)
	share := e.createShare(tok, "/secret.txt", `{"password":"sharepw"}`)
	dirShare := e.createShare(tok, "/album", `{"password":"sharepw"}`)
	openShare := e.createShare(tok, "/open.txt", "{}")

	link, err := e.st.Share.GetByHash(share)
	if err != nil {
		t.Fatal(err)
	}
	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}

	download := func(hash, sub, token string) (int, string) {
		code, _, body := e.do(http.MethodGet, "/api/public/dl/"+hash+sub+"?token="+token, "", nil, nil)
		return code, body
	}

	// What the share page gets after the password was entered, and uses.
	token := e.shareInfoToken(share, "", "sharepw")
	if token == "" {
		t.Fatal("no token for a password-protected share")
	}
	// The page puts it into URLs as it is.
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(token) {
		t.Errorf("the token is not safe to put in a URL unescaped: %q", token)
	}
	if code, body := download(share, "", token); code != http.StatusOK || body != "protected content" {
		t.Errorf("download with the token of the share page = %d %q", code, body)
	}
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/"+share+"?inline=true&token="+token, "", nil, nil); code != http.StatusOK || body != "protected content" {
		t.Errorf("inline download with the token = %d %q", code, body)
	}

	// The fixed secret stored with the share no longer opens it.
	if token == link.Token {
		t.Error("the share page is handed the fixed secret of the share")
	}
	if code, body := download(share, "", link.Token); code != http.StatusUnauthorized || strings.Contains(body, "protected") {
		t.Errorf("download with the fixed secret of the share = %d, want 401", code)
	}

	// A token is good for its own share only, and only until it expires.
	dirToken := e.shareInfoToken(dirShare, "", "sharepw")
	if code, _ := download(share, "", dirToken); code != http.StatusUnauthorized {
		t.Errorf("download with the token of another share = %d, want 401", code)
	}
	if code, body := download(dirShare, "/pic.txt", dirToken); code != http.StatusOK || body != "a picture" {
		t.Errorf("download of a file in a shared directory with its token = %d %q", code, body)
	}
	if code, _, _ := e.do(http.MethodGet, "/api/public/dl/"+dirShare+"?algo=zip&token="+dirToken, "", nil, nil); code != http.StatusOK {
		t.Errorf("archive of a shared directory with its token = %d", code)
	}

	almostExpired := shareToken(set.Key, link, time.Now().Add(time.Minute))
	if code, _ := download(share, "", almostExpired); code != http.StatusOK {
		t.Errorf("download with a token that is still valid = %d", code)
	}
	expired := shareToken(set.Key, link, time.Now().Add(-time.Second))
	if code, _ := download(share, "", expired); code != http.StatusUnauthorized {
		t.Errorf("download with an expired token = %d, want 401", code)
	}
	// The expiry is part of what is signed: it cannot be pushed back.
	expiry, signature, _ := strings.Cut(expired, ".")
	later, _, _ := strings.Cut(shareToken(set.Key, link, time.Now().Add(shareTokenLifetime)), ".")
	if expiry == later {
		t.Fatal("test tokens have the same expiry")
	}
	for _, forged := range []string{later + "." + signature, later + ".", "." + signature, later, "", "..", "zz.zz"} {
		if code, _ := download(share, "", forged); code != http.StatusUnauthorized {
			t.Errorf("download with the forged token %q = %d, want 401", forged, code)
		}
	}

	// A token lasts long enough for a film watched through the share page.
	if expires := shareTokenExpiry(token); expires.Before(time.Now().Add(12 * time.Hour)) {
		t.Errorf("the token expires at %v, less than half a day from now", expires)
	}

	// Without the password there is no token to be had, and a share without a
	// password has none.
	if code, _, body := e.do(http.MethodGet, "/api/public/share/"+share, "", nil, nil); code != http.StatusUnauthorized || strings.Contains(body, "token") {
		t.Errorf("share info without the password = %d %q", code, body)
	}
	if got := e.shareInfoToken(openShare, "", ""); got != "" {
		t.Errorf("a share without a password hands out the token %q", got)
	}
}
