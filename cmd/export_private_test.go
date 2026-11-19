package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asdine/storm/v3"

	"github.com/werewolfboy13/filebrowser/v2/auth"
	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/storage/bolt"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// exportTestDatabase creates a database with a signing key and a user in
// it, and returns its path.
func exportTestDatabase(t *testing.T, dir string) string {
	t.Helper()

	dbPath := filepath.Join(dir, "filebrowser.db")
	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{
		Key:        []byte("the-key-that-signs-sessions"),
		AuthMethod: auth.MethodJSONAuth,
		Defaults:   settings.UserDefaults{Scope: ".", Locale: "en"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.SaveServer(&settings.Server{Root: dir}); err != nil {
		t.Fatal(err)
	}
	if err := st.Auth.Save(&auth.JSONAuth{}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Save(&users.User{Username: "alice", Password: "hash-of-alice", Scope: "/alice"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	return dbPath
}

// `users export` writes the password hashes of every user, and `config
// export` the key that signs their sessions. Both files were created for
// everyone on the machine to read.
func TestExportsAreReadableByTheirOwnerOnly(t *testing.T) {
	// The key is exported as JSON encodes bytes, in base64.
	const exportedKey = "dGhlLWtleS10aGF0LXNpZ25zLXNlc3Npb25z"

	exports := map[string]struct {
		command []string
		secret  string
	}{
		"users export":  {command: []string{"users", "export"}, secret: "hash-of-alice"},
		"config export": {command: []string{"config", "export"}, secret: exportedKey},
	}
	files := map[string]func(t *testing.T, path string){
		"a new file": func(*testing.T, string) {},
		// A wider file from an earlier export must not keep its mode.
		"a file that others could read": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("an earlier export, which is longer than the new one: "+strings.Repeat("x", 4096)), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, export := range exports {
		for file, prepare := range files {
			t.Run(name+" to "+file, func(t *testing.T) {
				dir := t.TempDir()
				t.Chdir(dir)
				t.Setenv("HOME", dir)
				t.Setenv(envConfig, "")
				dbPath := exportTestDatabase(t, dir)

				out := filepath.Join(dir, "export.json")
				prepare(t, out)

				t.Cleanup(func() {
					_ = rootCmd.PersistentFlags().Set("database", "./filebrowser.db")
					rootCmd.SetArgs(nil)
				})
				rootCmd.SetArgs(append(append([]string{}, export.command...), out, "--database", dbPath))
				if err := rootCmd.Execute(); err != nil {
					t.Fatalf("%s: %v", name, err)
				}

				content := readTestFile(t, out)
				if !strings.Contains(content, export.secret) {
					t.Fatalf("the export does not hold %q, so this test proves nothing:\n%s", export.secret, content)
				}
				if strings.Contains(content, "an earlier export") || strings.Contains(content, "xxxx") {
					t.Errorf("the export still holds what the file held before")
				}

				if runtime.GOOS == "windows" {
					return
				}
				info, err := os.Stat(out)
				if err != nil {
					t.Fatal(err)
				}
				if perm := info.Mode().Perm(); perm != 0o600 {
					t.Errorf("mode is %o, want 600: the file holds %q", perm, export.secret)
				}
			})
		}
	}
}
