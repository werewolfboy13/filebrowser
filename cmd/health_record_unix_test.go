//go:build unix

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordOwnerTrusted(t *testing.T) {
	const server, other, root = 1000, 1001, 0

	tests := map[string]struct {
		file, self, process uint32
		processKnown        bool
		want                bool
	}{
		"the health check of the image: everything is the server's user":    {file: server, self: server, process: server, processKnown: true, want: true},
		"the health command run as root, of a server that is not":           {file: server, self: root, process: server, processKnown: true, want: true},
		"a file root planted, where the server could not replace it":        {file: root, self: server, process: server, processKnown: true, want: false},
		"a file another user planted":                                       {file: other, self: server, process: server, processKnown: true, want: false},
		"a file another user planted, naming a process of their own":        {file: other, self: server, process: other, processKnown: true, want: true},
		"no /proc: the file is the user's own":                              {file: server, self: server, want: true},
		"no /proc: the file is somebody else's":                             {file: other, self: server, want: false},
		"no /proc: user 0 is not taken for the owner of an unknown process": {file: root, self: server, want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := ownerTrusted(tt.file, tt.self, tt.process, tt.processKnown); got != tt.want {
				t.Errorf("ownerTrusted = %v, want %v", got, tt.want)
			}
		})
	}

	// On a real file: one this process wrote is its own.
	path := filepath.Join(t.TempDir(), "health.json")
	writeTestFile(t, path, "{}")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !recordOwnerTrusted(info, os.Getpid()) {
		t.Error("a file this process wrote is not trusted")
	}
	if uid, ok := processOwner(os.Getpid()); ok && int(uid) != os.Geteuid() {
		t.Errorf("this process runs as user %d according to /proc, and as %d according to itself", uid, os.Geteuid())
	}
}
