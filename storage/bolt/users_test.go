package bolt

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/asdine/storm/v3"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// GetByScope must match case-insensitively: on a case-insensitive filesystem
// two scopes that differ only in case resolve to the same home directory, so
// the provisioning collision check has to treat them as the same scope.
func TestGetByScopeCaseInsensitive(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st, err := NewStorage(db)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	if err := st.Users.Save(&users.User{Username: "CaseVictim", Password: "pw", Scope: "/users/CaseVictim"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, scope := range []string{"/users/CaseVictim", "/users/casevictim", "/USERS/CASEVICTIM"} {
		u, err := st.Users.GetByScope(scope)
		if err != nil {
			t.Errorf("GetByScope(%q) unexpected error: %v", scope, err)
			continue
		}
		if u.Username != "CaseVictim" {
			t.Errorf("GetByScope(%q) returned %q, want CaseVictim", scope, u.Username)
		}
	}

	// A genuinely different scope must still miss (and the regex-escaped query
	// must not match a superstring).
	for _, scope := range []string{"/users/other", "/users/CaseVictimX"} {
		if _, err := st.Users.GetByScope(scope); !errors.Is(err, fberrors.ErrNotExist) {
			t.Errorf("GetByScope(%q) expected ErrNotExist, got %v", scope, err)
		}
	}
}

// An update of several fields was one transaction per field. One that failed
// on the way, or was cut off by a crash, left the fields before it stored and
// the rest not: a new password without the mark that ends the sessions that
// knew the old one, since the mark is the last field of such an update.
func TestUpdateOfSeveralFieldsIsAllOrNothing(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backend := usersBackend{db: db}

	if err := backend.Save(&users.User{Username: "bob", Password: "old-hash", Scope: "/users/bob", Locale: "en"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := backend.Save(&users.User{Username: "alice", Password: "alice-hash", Scope: "/users/alice"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	stored := func() *users.User {
		t.Helper()
		u, err := backend.GetBy("bob")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	id := stored().ID

	// A field that does not exist comes after one that does.
	update := &users.User{ID: id, Username: "bob", Password: "new-hash", TokensNotBefore: 1234}
	if err := backend.Update(update, "Password", "NoSuchField", "TokensNotBefore"); err == nil {
		t.Fatal("an update naming a field that does not exist succeeded")
	}
	if got := stored(); got.Password != "old-hash" || got.TokensNotBefore != 0 {
		t.Errorf("after a failed update: password %q, mark %d; want the record as it was", got.Password, got.TokensNotBefore)
	}

	// A field that cannot be stored (a user name that is taken) comes after
	// one that can.
	update = &users.User{ID: id, Username: "alice", Password: "new-hash", TokensNotBefore: 1234}
	if err := backend.Update(update, "Password", "TokensNotBefore", "Username"); err == nil {
		t.Fatal("an update to a user name that is taken succeeded")
	}
	if got := stored(); got.Password != "old-hash" || got.TokensNotBefore != 0 || got.Username != "bob" {
		t.Errorf("after a failed update: %q, password %q, mark %d; want the record as it was", got.Username, got.Password, got.TokensNotBefore)
	}

	// An update that works stores every field, and only those.
	update = &users.User{ID: id, Username: "robert", Password: "new-hash", TokensNotBefore: 1234, Locale: "fr"}
	if err := backend.Update(update, "Password", "TokensNotBefore", "Username"); err != nil {
		t.Fatal(err)
	}
	got, err := backend.GetBy("robert")
	if err != nil {
		t.Fatalf("the user is not found under the new name: %v", err)
	}
	if got.Password != "new-hash" || got.TokensNotBefore != 1234 || got.Locale != "en" || got.Scope != "/users/bob" {
		t.Errorf("after the update: %+v", got)
	}
	if _, err := backend.GetBy("bob"); !errors.Is(err, fberrors.ErrNotExist) {
		t.Errorf("the old user name still finds a user: %v", err)
	}
}
