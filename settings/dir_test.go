package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/werewolfboy13/filebrowser/v2/users"
)

// A user provisioned with CreateUserDir must receive a per-user home directory
// derived from its username, not the default scope which normalizes to the
// server root.
func TestCreateUserHomeDerivesPerUserScope(t *testing.T) {
	s := &Settings{CreateUserDir: true, UserHomeBasePath: "/users"}

	user := &users.User{Username: "alice", Scope: "."}
	derived, err := s.CreateUserHome(user, t.TempDir(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !derived {
		t.Error("expected the scope to be reported as derived")
	}
	if user.Scope != "/users/alice" {
		t.Errorf("expected derived scope /users/alice, got %q", user.Scope)
	}
}

// A scope explicitly supplied by the caller (e.g. returned by an auth hook) must
// be preserved instead of being replaced by a derived home directory, and must
// not be reported as derived: it is legitimate for several users to share it.
func TestCreateUserHomePreservesExplicitScope(t *testing.T) {
	s := &Settings{CreateUserDir: true, UserHomeBasePath: "/users"}

	user := &users.User{Username: "alice", Scope: "/custom"}
	derived, err := s.CreateUserHome(user, t.TempDir(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if derived {
		t.Error("an explicit scope must not be reported as derived")
	}
	if user.Scope != "/custom" {
		t.Errorf("explicit scope should be preserved, got %q", user.Scope)
	}
}

// User home directories were created with mode 0777 (minus the umask) instead
// of the configured directory mode, leaving a new user's home readable, and
// with a permissive umask writable, by every local account.
func TestMakeUserDirUsesTheConfiguredMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	// Make the result independent of the umask of the test process.
	old := syscallUmask(0)
	defer syscallUmask(old)

	cases := map[string]struct {
		dirMode os.FileMode
		want    os.FileMode
	}{
		"configured mode":   {0o700, 0o700},
		"default when zero": {0, DefaultDirMode},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s := &Settings{CreateUserDir: true, UserHomeBasePath: "/users", DirMode: tc.dirMode}

			scope, err := s.MakeUserDir("alice", "", root)
			if err != nil {
				t.Fatal(err)
			}
			if scope != "/users/alice" {
				t.Fatalf("scope = %q", scope)
			}

			for _, dir := range []string{"users", filepath.Join("users", "alice")} {
				info, err := os.Stat(filepath.Join(root, dir))
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != tc.want {
					t.Errorf("%s has mode %o, want %o", dir, got, tc.want)
				}
			}
		})
	}
}
