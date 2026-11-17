package fbhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/users"
)

func (e *fullEnv) putUser(token string, id uint, body string) (int, string) {
	e.t.Helper()
	code, _, res := e.do(http.MethodPut, "/api/users/"+string(rune('0'+id)), token, strings.NewReader(body), nil)
	return code, strings.TrimSpace(res)
}

func (e *fullEnv) user(id uint) *users.User {
	e.t.Helper()
	u, err := e.st.Users.Get(e.root, false, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// Under JSON auth a user update needs the current password unless it only
// touches display preferences. The check used to look at a deny-list of
// lower-cased field names before the names were canonicalised, which left
// several ways around it for anyone holding an admin token: an empty "which"
// (applied as "all"), "lockPassword" (the list had it in mixed case), "rules"
// (not listed) and "ſcope" (U+017F, which title-cases to "Scope" but does not
// lower-case to "scope"). GHSA-hxw8-4h9j-hq2r.
func TestUserUpdateWithoutCurrentPasswordIsRefused(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)

	full, err := json.Marshal(e.user(2))
	if err != nil {
		t.Fatal(err)
	}
	fullWithPassword := strings.Replace(string(full), `"password":"`+e.user(2).Password+`"`, `"password":"attacker-chosen-pass-9"`, 1)
	if fullWithPassword == string(full) {
		t.Fatal("could not build the full-update payload")
	}

	cases := map[string]string{
		"password (control)": `{"what":"user","which":["password"],"data":{"id":2,"password":"attacker-chosen-pass-9"}}`,
		"scope (control)":    `{"what":"user","which":["scope"],"data":{"id":2,"scope":"/"}}`,
		"all (control)":      `{"what":"user","which":["all"],"data":` + fullWithPassword + `}`,
		"empty which":        `{"what":"user","which":[],"data":` + fullWithPassword + `}`,
		"missing which":      `{"what":"user","data":` + fullWithPassword + `}`,
		"lockPassword":       `{"what":"user","which":["lockPassword"],"data":{"id":2,"lockPassword":true}}`,
		"long s scope":       `{"what":"user","which":["ſcope"],"data":{"id":2,"scope":"/"}}`,
		"rules":              `{"what":"user","which":["rules"],"data":{"id":2,"rules":[{"allow":false,"path":"/x"}]}}`,
		"perm":               `{"what":"user","which":["perm"],"data":{"id":2,"perm":{"admin":true}}}`,
		"commands":           `{"what":"user","which":["commands"],"data":{"id":2,"commands":["sh"]}}`,
		"username":           `{"what":"user","which":["username"],"data":{"id":2,"username":"mallory"}}`,
		"harmless + scope":   `{"what":"user","which":["locale","scope"],"data":{"id":2,"locale":"fr","scope":"/"}}`,
		"upper-case ALL":     `{"what":"user","which":["ALL"],"data":` + fullWithPassword + `}`,
	}
	before, _ := json.Marshal(e.user(2))
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			code, body := e.putUser(admin, 2, payload)
			if code != http.StatusBadRequest {
				t.Errorf("update without current_password = %d %q, want 400", code, body)
			}
			if after, _ := json.Marshal(e.user(2)); string(after) != string(before) {
				t.Errorf("the user was changed:\n before %s\n after  %s", before, after)
			}
		})
	}

	// Same with a wrong password.
	wrong := `{"what":"user","which":[],"current_password":"not-the-password","data":` + fullWithPassword + `}`
	if code, body := e.putUser(admin, 2, wrong); code != http.StatusBadRequest {
		t.Errorf("update with a wrong current_password = %d %q, want 400", code, body)
	}
	if code, _, _ := e.tryLogin("bob", "attacker-chosen-pass-9"); code == http.StatusOK {
		t.Error("the password was replaced without the current password")
	}
}

// The requests the web interface sends must keep working: preferences without
// the password, everything else with it.
func TestUserUpdateRequestsOfTheFrontend(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)
	bob := e.login("bob", fullEnvUserPassword)

	// Profile.vue, "updateSettings": no current_password.
	profile := `{"what":"user","which":["locale","hideDotfiles","singleClick","redirectAfterCopyMove","dateFormat","aceEditorTheme"],` +
		`"data":{"id":2,"locale":"fr","hideDotfiles":true,"singleClick":true,"redirectAfterCopyMove":true,"dateFormat":true,"aceEditorTheme":"chrome"}}`
	if code, body := e.putUser(bob, 2, profile); code != http.StatusOK {
		t.Fatalf("profile settings without current_password = %d %q, want 200", code, body)
	}
	// FileListing.vue: sorting and view mode, no current_password.
	if code, body := e.putUser(bob, 2, `{"what":"user","which":["sorting"],"data":{"id":2,"sorting":{"by":"size","asc":true}}}`); code != http.StatusOK {
		t.Fatalf("sorting without current_password = %d %q, want 200", code, body)
	}
	if code, body := e.putUser(bob, 2, `{"what":"user","which":["viewMode"],"data":{"id":2,"viewMode":"mosaic gallery"}}`); code != http.StatusOK {
		t.Fatalf("view mode without current_password = %d %q, want 200", code, body)
	}
	u := e.user(2)
	if u.Locale != "fr" || !u.HideDotfiles || !u.SingleClick || !u.RedirectAfterCopyMove || !u.DateFormat ||
		u.AceEditorTheme != "chrome" || u.Sorting.By != "size" || !u.Sorting.Asc || u.ViewMode != "mosaic gallery" {
		t.Errorf("preferences were not stored: %+v", u)
	}

	// Profile.vue, "updatePassword": with current_password.
	change := `{"what":"user","which":["password"],"current_password":"` + fullEnvUserPassword + `","data":{"id":2,"password":"brand-new-password-77"}}`
	if code, body := e.putUser(bob, 2, change); code != http.StatusOK {
		t.Fatalf("password change with current_password = %d %q, want 200", code, body)
	}
	if code, _, _ := e.tryLogin("bob", "brand-new-password-77"); code != http.StatusOK {
		t.Errorf("login with the new password = %d", code)
	}

	// User.vue: the whole user as "all", with the admin's own password.
	target := e.user(2)
	target.Password = ""
	target.Perm.Delete = false
	data, _ := json.Marshal(target)
	all := `{"what":"user","which":["all"],"current_password":"` + fullEnvAdminPassword + `","data":` + string(data) + `}`
	if code, body := e.putUser(admin, 2, all); code != http.StatusOK {
		t.Fatalf("full update with current_password = %d %q, want 200", code, body)
	}
	if e.user(2).Perm.Delete {
		t.Error("the full update was not stored")
	}
	if code, _, _ := e.tryLogin("bob", "brand-new-password-77"); code != http.StatusOK {
		t.Error("a full update without a password must keep the stored one")
	}

	// A non-admin still cannot touch what only admins may change, password
	// or not.
	for _, field := range []string{"perm", "scope", "rules", "commands", "username", "lockPassword"} {
		payload := `{"what":"user","which":["` + field + `"],"current_password":"brand-new-password-77","data":{"id":2,"scope":"/","perm":{"admin":true},"username":"x","lockPassword":false}}`
		if code, body := e.putUser(bob, 2, payload); code != http.StatusForbidden {
			t.Errorf("non-admin update of %s = %d %q, want 403", field, code, body)
		}
	}
	full := `{"what":"user","which":[],"current_password":"brand-new-password-77","data":` + string(data) + `}`
	if code, body := e.putUser(bob, 2, full); code != http.StatusForbidden {
		t.Errorf("non-admin full update = %d %q, want 403", code, body)
	}
}

// "which" may only name fields a user update can change.
func TestUserUpdateRefusesUnknownFields(t *testing.T) {
	e := newFullEnv(t)
	admin := e.login("admin", fullEnvAdminPassword)

	for _, which := range []string{`["id"]`, `["iD"]`, `["fs"]`, `["nope"]`, `["all","locale"]`, `["locale","all"]`, `[""]`} {
		payload := `{"what":"user","which":` + which + `,"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"locale":"fr"}}`
		if code, body := e.putUser(admin, 2, payload); code != http.StatusBadRequest {
			t.Errorf("which=%s = %d %q, want 400", which, code, body)
		}
	}
	if e.user(2).Locale == "fr" {
		t.Error("a refused update was applied")
	}
}
