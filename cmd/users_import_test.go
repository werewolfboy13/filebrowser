package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asdine/storm/v3"

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
