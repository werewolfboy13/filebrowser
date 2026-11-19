package users

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/werewolfboy13/filebrowser/v3/files"
)

// TestUserCleanFs verifies that Clean builds the user filesystem according to the
// followExternalSymlinks flag and that FullPath resolves correctly for either
// implementation.
func TestUserCleanFs(t *testing.T) {
	base := t.TempDir()
	want := filepath.Join(base, "data", "x")

	t.Run("default builds a symlink-confining ScopedFs", func(t *testing.T) {
		u := &User{Username: "u", Password: "p", Scope: "data"}
		if err := u.Clean(base, false); err != nil {
			t.Fatal(err)
		}
		if _, ok := u.Fs.(*files.ScopedFs); !ok {
			t.Fatalf("expected *files.ScopedFs, got %T", u.Fs)
		}
		if got := u.FullPath("/x"); got != want {
			t.Fatalf("FullPath: got %q, want %q", got, want)
		}
	})

	t.Run("followExternalSymlinks builds a bare BasePathFs", func(t *testing.T) {
		u := &User{Username: "u", Password: "p", Scope: "data"}
		if err := u.Clean(base, true); err != nil {
			t.Fatal(err)
		}
		if _, ok := u.Fs.(*afero.BasePathFs); !ok {
			t.Fatalf("expected *afero.BasePathFs, got %T", u.Fs)
		}
		if got := u.FullPath("/x"); got != want {
			t.Fatalf("FullPath: got %q, want %q", got, want)
		}
	})
}

func TestRevokeTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	u := &User{}
	if u.TokenRevoked(now.Add(-time.Hour)) {
		t.Error("an account that was never revoked refuses a token")
	}

	u.RevokeTokens(now)
	if !u.TokenRevoked(now) || !u.TokenRevoked(now.Add(-time.Hour)) {
		t.Error("a token issued up to the moment of the revocation is still accepted")
	}
	// What is issued next is stamped with IssueTime, and must be accepted.
	if issued := u.IssueTime(now); u.TokenRevoked(issued) {
		t.Errorf("a token issued right after the revocation (%v) is refused", issued)
	}

	// A second revocation within the same second ends what was issued after
	// the first one.
	issued := u.IssueTime(now)
	u.RevokeTokens(now)
	if !u.TokenRevoked(issued) {
		t.Error("a second revocation in the same second did not end the token issued in between")
	}

	// Without a revocation, tokens are stamped with the current time.
	if issued := (&User{}).IssueTime(now); !issued.Equal(now) {
		t.Errorf("IssueTime = %v, want %v", issued, now)
	}
}
