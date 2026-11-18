package fbhttp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/werewolfboy13/filebrowser/v2/fileutils"
)

// The periodic look at the temporary files wrote down what it saw of each
// (size, time, since when) without the sweeper's lock, while the creation of
// an upload reads the same fields with it, to find the file to forget when
// the sweeper is full. Run with -race: without the lock around the writes,
// the detector reports them.
func TestTusSweepSharesItsFilesWithNewUploadsSafely(t *testing.T) {
	scope := t.TempDir()
	afs := afero.NewBasePathFs(afero.NewOsFs(), scope)
	const full = 50
	s := &tusSweeper{files: map[string]*tusTempFile{}, interval: time.Hour, maxAge: time.Hour, maxFiles: full, now: time.Now}
	name := func(i int) string { return fmt.Sprintf("/%stus-%032x", fileutils.TempPrefix, i) }
	create := func(i int) {
		if err := os.WriteFile(filepath.Join(scope, name(i)[1:]), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		s.track(afs, name(i), name(i), unregistered{}, name(i))
	}
	for i := 0; i < full; i++ {
		create(i)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // uploads in progress: their files keep changing
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for i := 0; i < full; i++ {
				if f, err := os.OpenFile(filepath.Join(scope, name(i)[1:]), os.O_APPEND|os.O_WRONLY, 0); err == nil {
					_, _ = f.Write([]byte("y"))
					_ = f.Close()
				}
			}
		}
	}()
	go func() { // the periodic look
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.sweep()
			}
		}
	}()

	// New uploads: the sweeper is full, so each one looks for the file that
	// has been watched the longest.
	for i := full; i < full+300; i++ {
		create(i)
	}
	close(stop)
	wg.Wait()

	s.mu.Lock()
	watched := len(s.files)
	s.mu.Unlock()
	if watched != full {
		t.Errorf("%d files are watched, limit %d", watched, full)
	}
}
