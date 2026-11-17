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
