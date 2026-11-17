package fbhttp

import (
	"net/http"
	"testing"
)

// A token whose user no longer exists is not a server fault: the session is
// simply over. It used to be answered with 500, which the frontend does not
// treat as "logged out".
func TestTokenOfDeletedUserIsUnauthorized(t *testing.T) {
	e := newFullEnv(t)
	bob := e.login("bob", fullEnvUserPassword)

	if code, _, _ := e.do(http.MethodGet, "/api/resources/", bob, nil, nil); code != http.StatusOK {
		t.Fatalf("control: request before deletion = %d", code)
	}
	if err := e.st.Users.Delete(uint(2)); err != nil {
		t.Fatal(err)
	}

	for _, rq := range []struct{ method, path string }{
		{http.MethodGet, "/api/resources/"},
		{http.MethodPost, "/api/renew"},
		{http.MethodGet, "/api/users/2"},
	} {
		if code, _, body := e.do(rq.method, rq.path, bob, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a deleted user's token = %d %q, want 401", rq.method, rq.path, code, body)
		}
	}
}
