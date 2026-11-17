package fbhttp

import (
	"net/http"
	"strings"
	"testing"
)

// A user create or update request without the "data" object used to
// dereference a nil pointer. net/http recovers the panic, but the client only
// sees a dropped connection. It is a malformed request: 400.
func TestUserRequestWithoutDataIsBadRequest(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)
	bob := e.login("bob", fullEnvUserPassword)

	cases := map[string]struct{ method, path, token, body string }{
		"PUT own profile":     {http.MethodPut, "/api/users/2", bob, `{"what":"user","which":["locale"]}`},
		"PUT null data":       {http.MethodPut, "/api/users/2", bob, `{"what":"user","which":["locale"],"data":null}`},
		"PUT all as admin":    {http.MethodPut, "/api/users/2", admin, `{"what":"user","which":["all"],"current_password":"` + fullEnvAdminPassword + `"}`},
		"POST new user":       {http.MethodPost, "/api/users", admin, `{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword + `"}`},
		"POST new user, null": {http.MethodPost, "/api/users", admin, `{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword + `","data":null}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, body := e.do(tc.method, tc.path, tc.token, strings.NewReader(tc.body), nil)
			if code != http.StatusBadRequest {
				t.Errorf("%s %s without data = %d %q, want 400", tc.method, tc.path, code, strings.TrimSpace(body))
			}
		})
	}
}
