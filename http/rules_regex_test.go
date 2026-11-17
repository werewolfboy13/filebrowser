package fbhttp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/users"
)

const invalidRegexRule = `{"allow":false,"path":"","regex":true,"regexp":{"raw":"("}}`

// A regex rule is compiled when it is matched. One that does not compile used
// to be stored as-is and then made every rule check of the affected users
// panic. It must be refused when it is saved.
func TestInvalidRegexRuleIsRefused(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)
	bob := e.login("bob", fullEnvUserPassword)

	t.Run("user rule", func(t *testing.T) {
		payload := `{"what":"user","which":["rules"],"current_password":"` + fullEnvAdminPassword +
			`","data":{"id":2,"rules":[` + invalidRegexRule + `]}}`
		code, _, body := e.do(http.MethodPut, "/api/users/2", admin, strings.NewReader(payload), nil)
		if code != http.StatusBadRequest {
			t.Errorf("PUT user with an invalid regex rule = %d %q, want 400", code, strings.TrimSpace(body))
		}
		if u, err := e.st.Users.Get(e.root, false, uint(2)); err != nil || len(u.Rules) != 0 {
			t.Errorf("invalid rule was stored for the user: %+v (err %v)", u.Rules, err)
		}
	})

	t.Run("new user", func(t *testing.T) {
		payload := `{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword +
			`","data":{"username":"carol","password":"a-long-enough-password-3","rules":[` + invalidRegexRule + `]}}`
		code, _, body := e.do(http.MethodPost, "/api/users", admin, strings.NewReader(payload), nil)
		if code != http.StatusBadRequest {
			t.Errorf("POST user with an invalid regex rule = %d %q, want 400", code, strings.TrimSpace(body))
		}
	})

	t.Run("global rule", func(t *testing.T) {
		_, _, current := e.do(http.MethodGet, "/api/settings", admin, nil, nil)
		if !strings.Contains(current, `"rules":[]`) {
			t.Fatalf("unexpected settings document: %s", current)
		}
		bad := strings.Replace(current, `"rules":[]`, `"rules":[`+invalidRegexRule+`]`, 1)
		code, _, body := e.do(http.MethodPut, "/api/settings", admin, strings.NewReader(bad), nil)
		if code != http.StatusBadRequest {
			t.Errorf("PUT settings with an invalid regex rule = %d %q, want 400", code, strings.TrimSpace(body))
		}
		if set, err := e.st.Settings.Get(); err != nil || len(set.Rules) != 0 {
			t.Errorf("invalid global rule was stored: %+v (err %v)", set.Rules, err)
		}
	})

	if code, _, body := e.do(http.MethodPost, "/api/resources/x.txt", bob, strings.NewReader("x"), nil); code != http.StatusOK {
		t.Errorf("upload after the refused rules = %d %q, want 200", code, body)
	}
}

// An invalid rule that is already in the database (stored by an older version,
// the CLI or an import) must not take the server down either: requests are
// answered, and a deny rule that cannot be evaluated denies.
func TestStoredInvalidRegexRuleDoesNotPanic(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Rules = []rules.Rule{{Regex: true, Allow: false, Regexp: &rules.Regexp{Raw: "("}}}
	})
	bob := e.login("bob", fullEnvUserPassword)

	code, _, body := e.do(http.MethodPost, "/api/resources/x.txt", bob, strings.NewReader("x"), nil)
	if code != http.StatusForbidden {
		t.Errorf("request under an unusable deny rule = %d %q, want 403", code, body)
	}

	// The admin can still remove it through the API.
	admin := e.login("admin", fullEnvAdminPassword)
	payload := `{"what":"user","which":["rules"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"rules":[]}}`
	if code, _, body := e.do(http.MethodPut, "/api/users/2", admin, strings.NewReader(payload), nil); code != http.StatusOK {
		t.Fatalf("removing the rule = %d %q", code, body)
	}
	if code, _, body := e.do(http.MethodPost, "/api/resources/x.txt", bob, strings.NewReader("x"), nil); code != http.StatusOK {
		t.Errorf("upload after removing the rule = %d %q, want 200", code, body)
	}
}
