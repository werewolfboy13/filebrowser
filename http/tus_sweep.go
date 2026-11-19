package fbhttp

import (
	"errors"
	"io/fs"
	"os"
	gopath "path"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/afero"

	"github.com/werewolfboy13/filebrowser/v3/fileutils"
)

const (
	// tusAbandonedAge is how long the temporary file of an upload may go
	// without being written before the upload is taken to be abandoned. The
	// upload cache gives up on an upload after uploadCacheTTL without a
	// request, so by then it can no longer be continued.
	tusAbandonedAge = time.Hour

	// tusSweepInterval is how often the temporary files are looked at.
	tusSweepInterval = 10 * time.Minute

	// tusSweepMaxFiles bounds the temporary files that are kept an eye on.
	tusSweepMaxFiles = 4096
)

// tusTemps cleans up after uploads that were abandoned: a closed tab, a lost
// connection, a client that never came back.
//
// The upload cache is what notices that an upload has gone quiet, and the one
// in memory removes its temporary file then. The Redis one cannot: all it
// has is a key that expires, and nothing runs when it does. The file stayed
// where it was, hidden from every listing and refused by name, so nobody
// could see or remove it through the web interface. This works the same with
// either cache, and across replicas that share the storage.
//
// It remembers the temporary file of every upload created here, with the
// filesystem of the user who created it, and looks at them every
// tusSweepInterval. One that has not changed for tusAbandonedAge is removed,
// once its upload is no longer registered: until then the upload can be
// continued, and its temporary file is the only place it writes to.
//
// "Not changed" is judged by this process's clock: the size and the
// modification time of the file are compared with what they were at the last
// look, and the time since they were first seen that way is what counts. The
// modification time itself is set by whatever stores the file (another
// machine, for a network mount), whose clock need not agree with this one,
// and an upload in progress must not be taken away because of that.
//
// What it removes is the very file the upload created, and nothing else:
//   - the path is the one the upload was given, with a temporary name, and it
//     is reached through the user's scoped filesystem, which does not follow
//     a link out of the user's scope;
//   - it must still be a regular file, and the same file (not merely the same
//     name) that was created for the upload.
//
// It is bounded: tusSweepMaxFiles files are remembered, and a look costs one
// lstat for each. Past that, and after a restart, which forgets them all, a
// file is left to the sweep of stale temporary files that every upload into
// its directory runs (see sweepStaleUploads).
var tusTemps = &tusSweeper{
	files:    map[string]*tusTempFile{},
	interval: tusSweepInterval,
	maxAge:   tusAbandonedAge,
	maxFiles: tusSweepMaxFiles,
	now:      time.Now,
}

type tusSweeper struct {
	mu      sync.Mutex
	files   map[string]*tusTempFile
	running bool

	interval time.Duration
	maxAge   time.Duration
	maxFiles int
	now      func() time.Time
}

// tusTempFile is the temporary file of one upload.
type tusTempFile struct {
	fs   afero.Fs
	path string

	// cache and uploadKey are where the upload is registered, and what it
	// is known by there.
	cache     UploadCache
	uploadKey string

	// created identifies the file that was created for the upload.
	created os.FileInfo

	// size and modTime are what the file looked like at the last look, and
	// since is when it was first seen that way.
	size    int64
	modTime time.Time
	since   time.Time
}

// track starts keeping an eye on the temporary file at path, which was just
// created through afs, the filesystem of the uploading user, for the upload
// that cache knows by uploadKey.
func (s *tusSweeper) track(afs afero.Fs, path string, key string, cache UploadCache, uploadKey string) {
	// The name is ours by construction. Anything else is not something to
	// ever remove.
	if !fileutils.IsOwnTempName(gopath.Base(path)) {
		return
	}
	info, err := lstatPath(afs, path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, known := s.files[key]; !known && len(s.files) >= s.maxFiles {
		// Full: forget the one that has been watched the longest. It is left
		// to the sweep of its directory.
		var (
			oldestKey string
			oldest    *tusTempFile
		)
		for k, f := range s.files {
			if oldest == nil || f.since.Before(oldest.since) {
				oldestKey, oldest = k, f
			}
		}
		delete(s.files, oldestKey)
	}

	s.files[key] = &tusTempFile{
		fs:        afs,
		path:      path,
		cache:     cache,
		uploadKey: uploadKey,

		created: info,
		size:    info.Size(),
		modTime: info.ModTime(),
		since:   s.now(),
	}

	if !s.running {
		s.running = true
		go s.run()
	}
}

// run looks at the files every interval, and ends when there are none left.
// track starts it again.
func (s *tusSweeper) run() {
	for {
		time.Sleep(s.interval)
		s.sweep()

		s.mu.Lock()
		if len(s.files) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// sweep looks at every remembered file once, and removes the abandoned ones.
func (s *tusSweeper) sweep() {
	s.mu.Lock()
	now := s.now()
	files := make(map[string]*tusTempFile, len(s.files))
	for key, f := range s.files {
		files[key] = f
	}
	s.mu.Unlock()

	// The filesystem is not touched with the lock held: an upload that is
	// being created must not wait for a slow disk.
	done := []string{}
	for key, f := range files {
		if s.look(f, now) {
			done = append(done, key)
		}
	}

	s.mu.Lock()
	for _, key := range done {
		// Unless another upload has taken the place in the meantime.
		if s.files[key] == files[key] {
			delete(s.files, key)
		}
	}
	s.mu.Unlock()
}

// look checks on the file, removes it if it was abandoned, and reports
// whether there is nothing left to keep an eye on.
//
// The filesystem is asked without the lock, but what is remembered of the file
// is only read and changed with it: track reads the same fields, with the
// lock, when it looks for the file to forget.
func (s *tusSweeper) look(f *tusTempFile, now time.Time) (done bool) {
	info, err := lstatPath(f.fs, f.path)

	s.mu.Lock()
	changed := err == nil && (info.Size() != f.size || !info.ModTime().Equal(f.modTime))
	if changed {
		// It was written since the last look: the upload is alive.
		f.size, f.modTime, f.since = info.Size(), info.ModTime(), now
	}
	quiet := now.Sub(f.since)
	s.mu.Unlock()

	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The upload was completed or cancelled.
		return true
	case err != nil:
		// It cannot be told (the scope refuses the path now, the disk is
		// away): try again later, for as long as it could still be ours.
		return quiet > 2*s.maxAge
	case !info.Mode().IsRegular() || !os.SameFile(info, f.created):
		// Something else has taken the name: not ours.
		return true
	case changed || quiet < s.maxAge:
		return false
	}

	// Quiet for long enough. That alone does not make it abandoned: a client
	// that only asks how far its upload is, or a chunk that stalls, keeps the
	// upload registered without a byte being written. Such an upload is
	// looked at again later.
	//
	// Removed through the user's filesystem, like everything the upload did.
	// A failure leaves it to the sweep of its directory.
	kept, _ := tusDiscardAbandoned(f.cache, f.uploadKey, f.fs, f.path)
	return !kept
}

// tusTempKey returns the key the temporary file at path, in the filesystem of
// the user, is remembered under: its location on disk.
func tusTempKey(d *data, path string) string {
	return filepath.Clean(d.user.FullPath(path))
}
