package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
)

// At startup, the server's own files that lie inside the served directory are
// registered as protected; those outside it are not.
func TestProtectServerFiles(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "srv")
	outside := filepath.Join(base, "state")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("bare binary defaults: everything inside the root", func(t *testing.T) {
		// Relative paths, as the defaults are, are resolved against the
		// working directory.
		t.Chdir(root)
		server := &settings.Server{Root: root, TLSKey: "key.pem", Log: "filebrowser.log"}

		got := protectServerFiles(server, "./filebrowser.db", filepath.Join(root, ".filebrowser.json"), "cache")
		want := []string{"database", "configuration file", "cache directory", "TLS key", "log file"}
		if !slices.Equal(got, want) {
			t.Errorf("registered %v, want %v", got, want)
		}
		for _, p := range []string{"filebrowser.db", ".filebrowser.json", "cache/x", "key.pem", "filebrowser.log", "filebrowser-2026-11-17T10-00-00.000.log"} {
			if !server.IsProtected(filepath.Join(root, p)) {
				t.Errorf("%s is not protected", p)
			}
		}
		if server.IsProtected(filepath.Join(root, "holiday.jpg")) {
			t.Error("an ordinary file is protected")
		}
	})

	t.Run("container layout: nothing inside the root", func(t *testing.T) {
		for _, logTarget := range []string{"stdout", "stderr", "", filepath.Join(outside, "fb.log")} {
			server := &settings.Server{Root: root, TLSKey: filepath.Join(outside, "key.pem"), Log: logTarget}

			got := protectServerFiles(server, filepath.Join(outside, "filebrowser.db"), filepath.Join(outside, "settings.json"), "")
			if len(got) != 0 || server.HasProtectedPaths() {
				t.Errorf("log %q: registered %v", logTarget, got)
			}
		}
	})
}
