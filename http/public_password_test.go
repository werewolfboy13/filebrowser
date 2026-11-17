package fbhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// createShare creates a share of bob's path through the API and returns its
// hash.
func (e *fullEnv) createShare(token, path, body string) string {
	e.t.Helper()
	code, _, res := e.do(http.MethodPost, "/api/share"+path, token, strings.NewReader(body), nil)
	if code != http.StatusOK {
		e.t.Fatalf("creating a share of %s = %d %q", path, code, res)
	}
	var created struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(res), &created); err != nil || created.Hash == "" {
		e.t.Fatalf("unexpected share response %q: %v", res, err)
	}
	return created.Hash
}

// A share password that is not valid percent-encoding made the handler return
// (0, err), which the wrapper turns into an empty 200 response. No content was
// served, but a client could not tell the request had failed.
func TestPublicShareMalformedPasswordIsBadRequest(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("page.txt", "shared content")
	tok := e.login("bob", fullEnvUserPassword)
	hash := e.createShare(tok, "/page.txt", `{"password":"sharepw"}`)

	for _, endpoint := range []string{"/api/public/dl/", "/api/public/share/"} {
		code, _, body := e.do(http.MethodGet, endpoint+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": "%zz"})
		if code != http.StatusBadRequest {
			t.Errorf("%s with a malformed password = %d, want 400", endpoint, code)
		}
		if strings.Contains(body, "shared content") {
			t.Errorf("%s served the file to a malformed password", endpoint)
		}
	}

	// Controls: no password and a wrong password are still "unauthorized", and
	// the right one (percent-encoded, as the frontend sends it) works.
	if code, _, _ := e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no password = %d, want 401", code)
	}
	if code, _, _ := e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": "nope"}); code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", code)
	}
	code, _, body := e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": "sharepw"})
	if code != http.StatusOK || body != "shared content" {
		t.Errorf("right password = %d %q, want the file", code, body)
	}

	// A stored hash that cannot be evaluated is a server error, not an empty
	// 200 either.
	link, err := e.st.Share.GetByHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	link.PasswordHash = "not-a-bcrypt-hash"
	if err := e.st.Share.Save(link); err != nil {
		t.Fatal(err)
	}
	code, _, body = e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": "sharepw"})
	if code != http.StatusInternalServerError || strings.Contains(body, "shared content") {
		t.Errorf("unusable stored hash = %d %q, want 500", code, body)
	}
}

// The bypass token of a password-protected share is compared with the "token"
// query parameter. A record with a password hash but no token (none is written
// by the code today; it takes a hand-edited or imported database) compared ""
// with "" and so served the share to anyone. An empty stored token must never
// match.
func TestPublicShareEmptyStoredTokenDoesNotBypassPassword(t *testing.T) {
	e := newFullEnv(t)
	e.writeBobFile("page.txt", "shared content")
	tok := e.login("bob", fullEnvUserPassword)
	hash := e.createShare(tok, "/page.txt", `{"password":"sharepw"}`)

	link, err := e.st.Share.GetByHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	token := link.Token
	if token == "" {
		t.Fatal("a password-protected share was created without a token")
	}

	// Control: the stored token opens the share without the password.
	if code, _, body := e.do(http.MethodGet, "/api/public/dl/"+hash+"?token="+token, "", nil, nil); code != http.StatusOK || body != "shared content" {
		t.Fatalf("download with the share token = %d %q", code, body)
	}

	link.Token = ""
	if err := e.st.Share.Save(link); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?token="} {
		code, _, body := e.do(http.MethodGet, "/api/public/dl/"+hash+query, "", nil, nil)
		if code != http.StatusUnauthorized || strings.Contains(body, "shared content") {
			t.Errorf("no password, empty stored token, query %q = %d %q, want 401", query, code, body)
		}
	}
	if code, _, _ := e.do(http.MethodGet, "/api/public/dl/"+hash, "", nil, map[string]string{"X-SHARE-PASSWORD": "sharepw"}); code != http.StatusOK {
		t.Errorf("the password still has to work, got %d", code)
	}
}
