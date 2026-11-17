package fbhttp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// countingReader reports how much of a request body was consumed.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// The authenticated JSON endpoints decoded request bodies of any size, and the
// profile fields a user can set for themselves had no length limit. A
// non-admin could store a 32 MiB "locale", which then went into every token
// issued to them and into every user listing. Bodies are now capped and those
// fields validated.
func TestAuthenticatedJSONBodiesAreCapped(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)
	bob := e.login("bob", fullEnvUserPassword)

	const huge = 8 << 20
	filler := strings.Repeat("A", huge)

	_, _, settingsDoc := e.do(http.MethodGet, "/api/settings", admin, nil, nil)
	e.writeBobFile("f.txt", "x")

	cases := map[string]struct{ method, path, token, body string }{
		"update own locale": {http.MethodPut, "/api/users/2", bob,
			`{"what":"user","which":["locale"],"data":{"id":2,"locale":"` + filler + `"}}`},
		"create user": {http.MethodPost, "/api/users", admin,
			`{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword + `","data":{"username":"` + filler + `","password":"a-long-enough-password-3"}}`},
		"delete user": {http.MethodDelete, "/api/users/2", bob,
			`{"current_password":"` + fullEnvUserPassword + `","padding":"` + filler + `"}`},
		"create share": {http.MethodPost, "/api/share/f.txt", bob,
			`{"password":"` + filler + `"}`},
		"update settings": {http.MethodPut, "/api/settings", admin,
			strings.Replace(settingsDoc, `"userHomeBasePath":"`, `"userHomeBasePath":"`+filler, 1)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := &countingReader{r: strings.NewReader(tc.body)}
			code, _, res := e.do(tc.method, tc.path, tc.token, body, nil)
			if code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s %s with a %d MiB body = %d %q, want 413", tc.method, tc.path, huge>>20, code, strings.TrimSpace(res))
			}
		})
	}

	if u := e.user(2); len(u.Locale) > 100 {
		t.Errorf("a %d byte locale was stored", len(u.Locale))
	}
	if set, err := e.st.Settings.Get(); err != nil || len(set.UserHomeBasePath) > 1000 {
		t.Errorf("oversized settings were stored (err %v)", err)
	}
	if links, err := e.st.Share.FindByUserID(2); err == nil && len(links) != 0 {
		t.Error("a share was created from an oversized body")
	}
	if code, _, _ := e.tryLogin("bob", fullEnvUserPassword); code != http.StatusOK {
		t.Error("the user was deleted by an oversized request")
	}
}

// Within the body limit, the fields a user can set on their own account are
// limited in length too: they are copied into every token and listing.
func TestUserProfileFieldsHaveLengthLimits(t *testing.T) {
	e := newFullEnv(t)
	bob := e.login("bob", fullEnvUserPassword)
	long := strings.Repeat("A", 4096)

	cases := map[string]string{
		"locale":         `{"what":"user","which":["locale"],"data":{"id":2,"locale":"` + long + `"}}`,
		"viewMode":       `{"what":"user","which":["viewMode"],"data":{"id":2,"viewMode":"` + long + `"}}`,
		"aceEditorTheme": `{"what":"user","which":["aceEditorTheme"],"data":{"id":2,"aceEditorTheme":"` + long + `"}}`,
		"sorting":        `{"what":"user","which":["sorting"],"data":{"id":2,"sorting":{"by":"` + long + `"}}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if code, body := e.putUser(bob, 2, payload); code != http.StatusBadRequest {
				t.Errorf("update with a %d byte %s = %d %q, want 400", len(long), name, code, body)
			}
		})
	}
	u := e.user(2)
	if len(u.Locale)+len(u.ViewMode)+len(u.AceEditorTheme)+len(u.Sorting.By) > 200 {
		t.Errorf("an oversized field was stored: %+v", u)
	}

	// Ordinary values are unaffected.
	ok := `{"what":"user","which":["locale","aceEditorTheme"],"data":{"id":2,"locale":"pt-br","aceEditorTheme":"tomorrow_night_eighties"}}`
	if code, body := e.putUser(bob, 2, ok); code != http.StatusOK {
		t.Errorf("ordinary profile update = %d %q, want 200", code, body)
	}

	// The same limits apply when an admin creates a user or stores a whole
	// one, and to the name chosen at signup.
	admin := e.login("admin", fullEnvAdminPassword)
	create := `{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword + `","data":{"username":"` + long + `","password":"a-long-enough-password-3"}}`
	if code, _, body := e.do(http.MethodPost, "/api/users", admin, strings.NewReader(create), nil); code != http.StatusBadRequest {
		t.Errorf("create user with a %d byte name = %d %q, want 400", len(long), code, strings.TrimSpace(body))
	}

	set, err := e.st.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	set.Signup = true
	if err := e.st.Settings.Save(set); err != nil {
		t.Fatal(err)
	}
	signup := `{"username":"` + long + `","password":"a-long-enough-password-3"}`
	if code, _, body := e.do(http.MethodPost, "/api/signup", "", strings.NewReader(signup), nil); code != http.StatusBadRequest {
		t.Errorf("signup with a %d byte name = %d %q, want 400", len(long), code, strings.TrimSpace(body))
	}
}
