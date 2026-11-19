package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/asdine/storm/v3"

	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/storage/bolt"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// "users import --replace" deletes every existing user after writing
// users.backup.json. The backup used to be written from the list being
// imported, so the accounts it claimed to preserve were lost for good. It also
// holds password hashes and was world-readable.
func TestUsersImportReplaceBacksUpTheOldUsers(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv(envConfig, "")

	dbPath := filepath.Join(dir, "filebrowser.db")
	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{Key: []byte("key")}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "oldbob"} {
		if err := st.Users.Save(&users.User{Username: name, Password: "hash-of-" + name, Scope: "/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	importFile := filepath.Join(dir, "new.json")
	if err := os.WriteFile(importFile, []byte(`[{"username":"newcarol","password":"hash-of-newcarol","scope":"/newcarol"}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	// A wider leftover from an earlier run must not keep its mode.
	backup := filepath.Join(dir, "users.backup.json")
	if err := os.WriteFile(backup, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = usersImportCmd.Flags().Set("replace", "false")
		_ = rootCmd.PersistentFlags().Set("database", "./filebrowser.db")
		rootCmd.SetArgs(nil)
	})
	rootCmd.SetArgs([]string{"users", "import", importFile, "--replace", "--database", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("users import --replace: %v", err)
	}

	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	var saved []users.User
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("backup is not a user list: %v\n%s", err, raw)
	}
	got := map[string]string{}
	for _, u := range saved {
		got[u.Username] = u.Password
	}
	if len(got) != 2 || got["alice"] != "hash-of-alice" || got["oldbob"] != "hash-of-oldbob" {
		t.Errorf("backup holds %v, want the two replaced users with their password hashes", got)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(backup)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("backup mode is %o, want 600: it holds password hashes", perm)
		}
	}

	// The import itself did what it says.
	db, err = storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err = bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.Users.Gets("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Username != "newcarol" {
		t.Errorf("users after the import: %d, want only newcarol", len(list))
	}
}

// newImportDB makes a database in a directory of its own, which becomes the
// working directory, with the named users in it: they get the IDs 1, 2 and
// so on, in the order given. It returns the path of the database.
func newImportDB(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv(envConfig, "")

	dbPath := filepath.Join(dir, "filebrowser.db")
	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{Key: []byte("key")}); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		// Each has a revocation mark of its own: sessions from before it
		// are no longer taken.
		user := &users.User{Username: name, Password: "hash-of-" + name, Scope: "/" + name, TokensNotBefore: int64(1000 * (i + 1))}
		if err := st.Users.Save(user); err != nil {
			t.Fatal(err)
		}
		if user.ID != uint(i+1) {
			t.Fatalf("%s got the ID %d, want %d", name, user.ID, i+1)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	return dbPath
}

// runUsersImport runs "users import" on a file with the given content.
func runUsersImport(t *testing.T, dbPath, content string, flags ...string) error {
	t.Helper()

	importFile := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(importFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	reset := func() {
		_ = usersImportCmd.Flags().Set("replace", "false")
		_ = usersImportCmd.Flags().Set("overwrite", "false")
		_ = rootCmd.PersistentFlags().Set("database", "./filebrowser.db")
		rootCmd.SetArgs(nil)
	}
	t.Cleanup(reset)
	reset()

	rootCmd.SetArgs(append([]string{"users", "import", importFile, "--database", dbPath}, flags...))
	return rootCmd.Execute()
}

// importedUsers returns the users of the database as "id:username:scope:mark",
// in the order of their IDs.
func importedUsers(t *testing.T, dbPath string) []string {
	t.Helper()

	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.Users.Gets("", false)
	if err != nil && !errors.Is(err, fberrors.ErrNotExist) {
		t.Fatal(err)
	}

	got := []string{}
	for _, u := range list {
		got = append(got, fmt.Sprintf("%d:%s:%s:%d", u.ID, u.Username, u.Scope, u.TokensNotBefore))
	}
	slices.Sort(got)
	return got
}

func assertUsers(t *testing.T, dbPath string, want ...string) {
	t.Helper()

	if got := importedUsers(t, dbPath); !slices.Equal(got, want) {
		t.Errorf("users in the database:\n got %v\nwant %v", got, want)
	}
}

// An ID means something in the installation that gave it out, and nothing in
// another one. With --overwrite, a user of the file was stored under its ID
// whoever had that ID here: the registered user was gone, and the imported
// one had its ID, and with the ID its revocation mark and whatever else goes
// by the ID.
func TestUsersImportOverwriteRefusesAnIDOfAnotherUser(t *testing.T) {
	dbPath := newImportDB(t, "alice", "bob")

	// The first user of the file is one that can be overwritten: nothing of
	// a file that is refused is imported.
	err := runUsersImport(t, dbPath, `[
		{"id":2,"username":"bob","password":"new-hash-of-bob","scope":"/elsewhere"},
		{"id":1,"username":"mallory","password":"hash-of-mallory","scope":"/mallory"}
	]`, "--overwrite")
	if err == nil {
		t.Error("a user with the ID of another registered user was imported")
	} else if msg := err.Error(); !strings.Contains(msg, "mallory") || !strings.Contains(msg, "alice") {
		t.Errorf("the error names neither user: %v", err)
	}

	assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000")
}

// What --overwrite is for: the same user, by ID and by username, is replaced
// by the one in the file. Its revocation mark stays.
func TestUsersImportOverwriteReplacesTheSameUser(t *testing.T) {
	dbPath := newImportDB(t, "alice", "bob")

	err := runUsersImport(t, dbPath, `[
		{"id":2,"username":"bob","password":"new-hash-of-bob","scope":"/elsewhere"},
		{"username":"carol","password":"hash-of-carol","scope":"/carol"}
	]`, "--overwrite")
	if err != nil {
		t.Fatal(err)
	}

	assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/elsewhere:2000", "3:carol:/carol:0")
}

// A user of the file that is not registered by its ID is a new one, and its
// username may not be taken: with or without --overwrite, and whether the
// file gives it an ID or not.
func TestUsersImportRefusesAUsernameThatIsTaken(t *testing.T) {
	for _, flags := range [][]string{nil, {"--overwrite"}} {
		for _, id := range []string{"", `"id":0,`, `"id":7,`} {
			dbPath := newImportDB(t, "alice", "bob")

			err := runUsersImport(t, dbPath, `[
				{"username":"carol","password":"hash-of-carol","scope":"/carol"},
				{`+id+`"username":"bob","password":"new-hash-of-bob","scope":"/elsewhere"}
			]`, flags...)
			if err == nil {
				t.Errorf("%v, %q: a user with a username that is taken was imported", flags, id)
			} else if !strings.Contains(err.Error(), "bob") {
				t.Errorf("%v, %q: the error does not name the user: %v", flags, id, err)
			}

			assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000")
		}
	}
}

// Without a flag, the users of the file are new ones, and the IDs they have
// in the file, from wherever the file comes, are not used: that an ID is
// taken here says nothing about the user.
func TestUsersImportWithoutAFlagDoesNotUseIDs(t *testing.T) {
	dbPath := newImportDB(t, "alice", "bob")

	err := runUsersImport(t, dbPath, `[
		{"id":1,"username":"erin","password":"hash-of-erin","scope":"/erin","tokensNotBefore":5},
		{"id":40,"username":"frank","password":"hash-of-frank","scope":"/frank"}
	]`)
	if err != nil {
		t.Fatal(err)
	}

	assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000", "3:erin:/erin:5", "4:frank:/frank:0")
}

// The IDs of new users are given out by the database, counting on from the
// last one. A user that was imported a moment ago was taken for a registered
// one when a later user of the file had the ID it had just got: without a
// flag the import stopped halfway, and with --overwrite the later user
// replaced the earlier one, which was gone without a word.
func TestUsersImportDoesNotTakeAnImportedUserForARegisteredOne(t *testing.T) {
	file := `[
		{"id":4,"username":"carol","password":"hash-of-carol","scope":"/carol"},
		{"id":3,"username":"dave","password":"hash-of-dave","scope":"/dave"}
	]`

	for _, flags := range [][]string{nil, {"--overwrite"}} {
		dbPath := newImportDB(t, "alice", "bob")

		if err := runUsersImport(t, dbPath, file, flags...); err != nil {
			t.Errorf("%v: %v", flags, err)
		}

		assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000", "3:carol:/carol:0", "4:dave:/dave:0")
	}

	t.Run("replace", func(t *testing.T) {
		dbPath := newImportDB(t, "alice", "bob")

		// With a user whose name is registered now: by the time the file is
		// imported that user is deleted, so the name is free, and the user
		// of the file is a new one like the others.
		withAlice := strings.Replace(file, "]", `,{"id":1,"username":"alice","password":"new-hash-of-alice","scope":"/elsewhere"}]`, 1)

		if err := runUsersImport(t, dbPath, withAlice, "--replace"); err != nil {
			t.Error(err)
		}

		assertUsers(t, dbPath, "3:carol:/carol:0", "4:dave:/dave:0", "5:alice:/elsewhere:0")
	})
}

// A file that names a user twice is refused as a whole, before --replace
// has deleted anyone.
func TestUsersImportRefusesAUsernameTwiceInTheFile(t *testing.T) {
	file := `[
		{"username":"carol","password":"hash-of-carol","scope":"/carol"},
		{"username":"carol","password":"other-hash","scope":"/other"}
	]`

	for _, flags := range [][]string{nil, {"--overwrite"}, {"--replace"}} {
		dbPath := newImportDB(t, "alice", "bob")

		err := runUsersImport(t, dbPath, file, flags...)
		if err == nil {
			t.Errorf("%v: a file with a username twice was imported", flags)
		} else if !strings.Contains(err.Error(), "carol") {
			t.Errorf("%v: the error does not name the user: %v", flags, err)
		}

		assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000")
	}
}

// makeAdministrators gives the named users of the database the administrator
// right.
func makeAdministrators(t *testing.T, dbPath string, names ...string) {
	t.Helper()

	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		user, err := st.Users.Get("", false, name)
		if err != nil {
			t.Fatal(err)
		}
		user.Perm.Admin = true
		if err := st.Users.Update(user, "Perm"); err != nil {
			t.Fatal(err)
		}
	}
}

// The store does not delete the last administrator, and --replace deletes
// the registered users one by one. With an administrator registered, it
// wrote the backup, deleted every user before the last administrator,
// stopped there and imported nothing, and a second attempt wrote the backup
// again, from the users that were left. It refuses before all of that.
func TestUsersImportReplaceRefusesWithAnAdministrator(t *testing.T) {
	file := `[{"username":"carol","password":"hash-of-carol","scope":"/carol"}]`

	for _, admins := range [][]string{{"bob"}, {"alice", "bob"}, {"alice"}} {
		dbPath := newImportDB(t, "alice", "bob")
		makeAdministrators(t, dbPath, admins...)

		err := runUsersImport(t, dbPath, file, "--replace")
		if err == nil {
			t.Errorf("administrators %v: the users were replaced", admins)
		} else if msg := err.Error(); !strings.Contains(msg, admins[0]) || !strings.Contains(msg, "Nothing was deleted or imported") {
			t.Errorf("administrators %v: the error does not name an administrator, or does not say that nothing was done: %v", admins, err)
		}

		assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000")

		// In the working directory, which is the one of the database.
		if _, err := os.Stat("users.backup.json"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("administrators %v: an import that was refused left a backup behind: %v", admins, err)
		}
	}
}

// A null in the list of the file is not a user. It was checked like one,
// which ended the command in a panic.
func TestUsersImportRefusesANullInTheFile(t *testing.T) {
	for _, file := range []struct{ content, null string }{
		{`[null]`, "entry 1 "},
		{`[{"username":"carol","password":"hash-of-carol","scope":"/carol"},null]`, "entry 2 "},
	} {
		for _, flags := range [][]string{nil, {"--overwrite"}, {"--replace"}} {
			// Without an administrator, so that --replace is not refused
			// for that.
			dbPath := newImportDB(t, "alice", "bob")

			err := runUsersImport(t, dbPath, file.content, flags...)
			if err == nil {
				t.Errorf("%s, %v: a file with a null in it was imported", file.content, flags)
			} else if !strings.Contains(err.Error(), file.null) {
				t.Errorf("%s, %v: the error does not say which entry is null: %v", file.content, flags, err)
			}

			assertUsers(t, dbPath, "1:alice:/alice:1000", "2:bob:/bob:2000")

			if _, err := os.Stat("users.backup.json"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s, %v: an import that was refused left a backup behind: %v", file.content, flags, err)
			}
		}
	}
}
