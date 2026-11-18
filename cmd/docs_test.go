package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// docs/cli is generated from the commands, their help texts and their flags
// (`task docs:cli:generate`), and is only as good as the last time that was
// done: the page of the health command once lacked three lines of its help
// text. The pages have to be what the commands generate now.
func TestGeneratedDocsAreUpToDate(t *testing.T) {
	committed := filepath.Join("..", "docs", "cli")
	if _, err := os.Stat(committed); err != nil {
		// The documentation is not among the files that the image is built
		// from, where these tests run too.
		t.Skipf("no generated documentation to compare with: %v", err)
	}

	// Through the root command, as `filebrowser docs` runs: it is what adds
	// the completion commands, which have pages too. And so in a process of
	// its own, which leaves the root command of the other tests as it is.
	if !inOwnProcess(t) {
		return
	}
	generated := t.TempDir()
	rootCmd.SetArgs([]string{"docs", "--out", generated})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}

	pages := func(dir string) map[string][]byte {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		pages := map[string][]byte{}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			// As Git checks the pages out on Windows.
			pages[entry.Name()] = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		}
		return pages
	}

	want, got := pages(generated), pages(committed)
	for name, page := range want {
		switch current, ok := got[name]; {
		case !ok:
			t.Errorf("docs/cli/%s is missing: run `task docs:cli:generate`", name)
		case !bytes.Equal(current, page):
			t.Errorf("docs/cli/%s is out of date: run `task docs:cli:generate`", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("docs/cli/%s is of a command that no longer exists: run `task docs:cli:generate`", name)
		}
	}
}
