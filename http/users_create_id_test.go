package fbhttp

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/users"
)

// POST /api/users stored the new user under the "id" of the request. The
// frontend sends 0, and the store then gives the user the next ID of its
// counter; with any other number the user was written over whatever that ID
// held, or had held, and the counter was not moved.
//
// The session tokens of an account name it by its ID. So a user created
// under the ID of a deleted account was handed the sessions of that account,
// one created under the ID of an existing account replaced it and kept its
// sessions, and one created ahead of the counter was replaced, sessions and
// all, by the next user created the ordinary way.

// createUser creates a user as the administrator, with the given "id" in
// the request, and returns the record that was stored for it.
func (e *fullEnv) createUser(adminToken string, id uint, name string) *users.User {
	e.t.Helper()
	body := `{"what":"user","which":[],"current_password":"` + fullEnvAdminPassword +
		`","data":{"id":` + strconv.FormatUint(uint64(id), 10) + `,"username":"` + name + `","password":"` + name + `-password-long-1","scope":"/users/` + name +
		`","perm":{"create":true,"download":true},"locale":"en","viewMode":"list","commands":[],"rules":[]}}`
	code, h, res := e.do(http.MethodPost, "/api/users", adminToken, strings.NewReader(body), nil)
	if code != http.StatusCreated {
		e.t.Fatalf("create %s with id %d = %d %q, want 201", name, id, code, res)
	}
	stored, err := e.st.Users.Get(e.root, false, name)
	if err != nil {
		e.t.Fatalf("%s was not stored: %v", name, err)
	}
	if want := "/settings/users/" + strconv.FormatUint(uint64(stored.ID), 10); h.Get("Location") != want {
		e.t.Errorf("create %s: Location %q, want %q, where the user is", name, h.Get("Location"), want)
	}
	return stored
}

func (e *fullEnv) deleteBob(adminToken string) {
	e.t.Helper()
	del := `{"current_password":"` + fullEnvAdminPassword + `"}`
	if code, _, body := e.do(http.MethodDelete, "/api/users/2", adminToken, strings.NewReader(del), nil); code != http.StatusOK {
		e.t.Fatalf("delete bob = %d %q", code, body)
	}
}

func TestUserCreatedWithTheIDOfADeletedUserDoesNotGetItsSessions(t *testing.T) {
	e := newFullEnv(t)
	admin := e.session("admin")
	bob := e.session("bob")

	if !e.works(bob) {
		t.Fatal("bob's token does not work to begin with")
	}
	e.deleteBob(admin)
	if e.works(bob) {
		t.Fatal("bob's token works after bob was deleted")
	}

	carol := e.createUser(admin, 2, "carol")
	if carol.ID == 2 {
		t.Errorf("carol was stored under the ID the request named, which was bob's")
	}
	if e.works(bob) {
		t.Errorf("the token of the deleted bob works again, as carol (ID %d)", carol.ID)
	}
}

func TestUserCreatedWithAnExistingIDDoesNotReplaceThatAccount(t *testing.T) {
	e := newFullEnv(t)
	admin := e.session("admin")
	bob := e.session("bob")

	carol := e.createUser(admin, 2, "carol")
	if carol.ID == 2 {
		t.Errorf("carol was stored under the ID the request named, which is bob's")
	}
	if stored, err := e.st.Users.Get(e.root, false, uint(2)); err != nil || stored.Username != "bob" {
		t.Errorf("the account with ID 2 after carol was created: %+v, %v; want bob as he was", stored, err)
	}
	if _, err := e.st.Users.Get(e.root, false, "bob"); err != nil {
		t.Errorf("bob is gone after carol was created: %v", err)
	}
	// Bob's session is still bob's.
	if code, _, body := e.do(http.MethodGet, "/api/users/2", bob, nil, nil); code != http.StatusOK || !strings.Contains(body, `"username":"bob"`) {
		t.Errorf("bob reading his own account = %d %q, want 200 and bob", code, body)
	}
}

func TestUserCreatedWithAnIDAheadOfTheCounterIsNotOverwritten(t *testing.T) {
	e := newFullEnv(t)
	admin := e.session("admin")

	// The counter stands at 2: 3 is the ID it gives next. Stored under 3
	// because the request said so, dave left the counter where it was.
	dave := e.createUser(admin, 3, "dave")

	// As the frontend sends it: the next ID of the counter.
	erin := e.createUser(admin, 0, "erin")
	if erin.ID == dave.ID {
		t.Errorf("erin was stored under dave's ID (%d)", dave.ID)
	}
	for _, name := range []string{"dave", "erin"} {
		if _, err := e.st.Users.Get(e.root, false, name); err != nil {
			t.Errorf("%s is gone after both were created: %v", name, err)
		}
	}
	if stored, err := e.st.Users.Get(e.root, false, dave.ID); err != nil || stored.Username != "dave" {
		t.Errorf("the account with dave's ID after erin was created: %+v, %v; want dave", stored, err)
	}
}

// The control: created the ordinary way, a user never gets the ID of one
// that was deleted, and the deleted user's token stays dead.
func TestUserCreatedTheOrdinaryWayDoesNotReuseAnID(t *testing.T) {
	e := newFullEnv(t)
	admin := e.session("admin")
	bob := e.session("bob")

	e.deleteBob(admin)
	again := e.createUser(admin, 0, "bob")
	if again.ID == 2 {
		t.Error("the new bob has the ID of the deleted one")
	}
	if e.works(bob) {
		t.Error("the deleted bob's token works for the new bob")
	}
}
