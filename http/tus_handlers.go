package fbhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	gopath "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/spf13/afero"
)

// maxPatchDrainBytes bounds how much of a rejected PATCH body is discarded to
// keep the connection reusable. It comfortably covers a default-sized chunk;
// beyond that the body is not worth reading just to throw away.
const maxPatchDrainBytes = 32 << 20 // 32MB

// drainRequestBody discards what the client already put on the wire for a
// request the handler answered without reading. net/http only drains 256KiB on
// its own before giving up and closing the connection, and a connection closed
// while the client is still streaming a chunk reaches the browser as a transport
// error instead of the status we replied with. The client then cannot tell a
// conflict from a network fault, retries blindly, and the upload stalls.
func drainRequestBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxPatchDrainBytes))
}

// keepUploadActive periodically touches the cache entry to prevent eviction during transfer
func keepUploadActive(cache UploadCache, filePath string) func() {
	stop := make(chan bool)

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				cache.Touch(filePath)
			}
		}
	}()

	return func() {
		close(stop)
	}
}

// tusBusy holds the uploads a request is currently working on, by the real
// path of their destination (see tusUploadKey).
//
// The TUS handlers read the state of an upload (does it exist, how much has
// been written) and then act on it, which is only correct while nobody else
// changes that state. Rather than making a second request for the same upload
// wait, it is refused: a client that stalls in the middle of a chunk would
// otherwise hold up everyone queued behind it for as long as it likes. The
// mutex guards the set only and is never held while a request does its work.
//
// The set is local to this process. With several replicas sharing one upload
// cache, two of them can still work on the same upload at once.
var tusBusy = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: map[string]struct{}{}}

// tusAcquire marks the upload to the request path as being worked on. It
// reports false, without waiting, when another request already is; otherwise
// the returned function must be called when the work is done.
func tusAcquire(d *data, r *http.Request) (release func(), ok bool) {
	key := tusUploadKey(d, r.URL.Path)

	tusBusy.Lock()
	defer tusBusy.Unlock()

	if _, busy := tusBusy.paths[key]; busy {
		return nil, false
	}
	tusBusy.paths[key] = struct{}{}

	return func() {
		tusBusy.Lock()
		delete(tusBusy.paths, key)
		tusBusy.Unlock()
	}, true
}

// errTusBusy is the answer to a request for an upload another request is
// working on. 423 is what the TUS protocol defines for it, and unlike 409 the
// web interface retries it.
var errTusBusy = errors.New("another request for this upload is in progress")

// tusTempPath returns the temporary file the data of an upload to dst is
// written to until the upload is complete. The name is derived from the name
// of dst, so that every request of an upload finds it without the upload cache
// having to carry it, and it has a fixed length, so that it fits wherever the
// name of dst does.
func tusTempPath(dst string) string {
	dir, name := gopath.Split(dst)
	sum := sha256.Sum256([]byte(name))
	return dir + fileutils.TempPrefix + "tus-" + hex.EncodeToString(sum[:16])
}

// tusUploadKey returns the key an upload to dst is known by, in the upload
// cache and in tusBusy: where dst is on disk once symbolic links are
// followed.
//
// A directory that is reached through a link gives the files in it a second
// name, and the temporary file of an upload lies in the directory itself.
// Under the path as written, the two names of one upload were two uploads to
// the cache and two locks, working on one file.
func tusUploadKey(d *data, dst string) string {
	lexical := filepath.Clean(d.user.FullPath(slashClean(dst)))
	if resolved, ok := d.resolvePath(lexical); ok {
		return resolved
	}
	return lexical
}

// tusWritePath returns the file the data of an upload to dst is appended to.
// That is its temporary file, which takes the place of dst once the upload is
// complete, so that an upload that is interrupted or abandoned leaves dst
// untouched. An upload that could not be given a temporary file (see
// tusPostHandler) writes to dst itself, as every upload used to.
func tusWritePath(d *data, dst string) (name string, temp bool, err error) {
	tmp := tusTempPath(dst)

	info, err := lstatPath(d.user.Fs, tmp)
	switch {
	case err == nil && info.Mode().IsRegular():
		return tmp, true, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", false, err
	}

	return dst, false, nil
}

func tusPostHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		// Creating an upload truncates what a PATCH in progress is appending
		// to, so it is serialised with the other requests for the upload.
		release, ok := tusAcquire(d, r)
		if !ok {
			return http.StatusLocked, errTusBusy
		}
		defer release()

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		switch {
		case errors.Is(err, afero.ErrFileNotFound):
			dirPath := filepath.Dir(r.URL.Path)
			if _, statErr := d.user.Fs.Stat(dirPath); os.IsNotExist(statErr) {
				if mkdirErr := d.user.Fs.MkdirAll(dirPath, d.settings.DirMode); mkdirErr != nil {
					return http.StatusInternalServerError, err
				}
			}
		case err != nil:
			return errToStatus(err), err
		}

		override := r.URL.Query().Get("override") == "true"

		// if file exists
		exists := file != nil
		if exists {
			if file.IsDir {
				return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", r.URL.Path)
			}

			// Existing files will remain untouched unless explicitly instructed to override
			if !override {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			// Opening a named pipe to write to it blocks until something
			// reads from it. A link whose target does not exist yet is fine.
			if info, statErr := d.user.Fs.Stat(r.URL.Path); statErr == nil && !info.Mode().IsRegular() {
				return http.StatusBadRequest, fmt.Errorf("cannot upload to %s: not a regular file", r.URL.Path)
			}
		}

		// The destination of an upload in progress does not exist yet, so the
		// check above does not see it. A second upload to the same path would
		// start the first one over; without override it is a conflict, as it
		// was when the destination was created up front.
		uploadKey := tusUploadKey(d, r.URL.Path)
		if !exists && !override {
			if _, cacheErr := cache.GetLength(uploadKey); cacheErr == nil {
				if _, temp, _ := tusWritePath(d, r.URL.Path); temp {
					return http.StatusConflict, nil
				}
			}
		}

		// Validated before anything is touched: a request that is refused must
		// not have created or emptied a file on its way.
		uploadLength, err := getUploadLength(r)
		if err != nil || uploadLength < 0 {
			return http.StatusBadRequest, fmt.Errorf("invalid upload length: %w", err)
		}

		sweepStaleUploads(d, r.URL.Path)

		// The data is collected in a temporary file next to the destination,
		// which is left alone until the upload is complete. Truncating the
		// destination here, as this handler used to, destroyed the original
		// as soon as an overwrite began, and the cleanup of an abandoned
		// upload then deleted it, whether or not the user may delete files.
		//
		// Where replacing the destination with another file would change more
		// than its content (it is a symbolic link or not a regular file), or
		// where the directory takes no new file, the upload writes to the
		// destination directly, as before.
		tmp := tusTempPath(r.URL.Path)
		direct := exists && (file.IsSymlink || !file.Mode.IsRegular())
		if !direct {
			tmpFile, tmpErr := d.user.Fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, d.settings.FileMode)
			if tmpErr != nil {
				direct = true
			} else {
				_ = tmpFile.Close()
			}
		}

		if direct {
			// A temporary file left by an earlier upload to this path would
			// be taken for this one's.
			_ = d.user.Fs.Remove(tmp)

			fileFlags := os.O_CREATE | os.O_WRONLY
			if exists {
				fileFlags |= os.O_TRUNC
			}
			openFile, openErr := d.user.Fs.OpenFile(r.URL.Path, fileFlags, d.settings.FileMode)
			if openErr != nil {
				return errToStatus(openErr), openErr
			}
			_ = openFile.Close()
		}

		basePath := "/" + strings.Trim(strings.TrimSpace(d.server.BaseURL), "/")
		if basePath == "/" {
			basePath = ""
		}
		w.Header().Set("Location", basePath+"/api/tus"+r.URL.EscapedPath())

		// An empty upload is complete as it is: no PATCH follows.
		if uploadLength == 0 {
			if !direct {
				if err := fileutils.CommitTemp(d.user.Fs, tmp, r.URL.Path, d.settings.FileMode, override && d.user.Perm.Modify); err != nil {
					return errToStatus(err), err
				}
			}
			return http.StatusCreated, nil
		}

		// Enables the user to utilize the PATCH endpoint for uploading file data.
		// The removal callback deletes an abandoned upload through the user's
		// scoped filesystem, so eviction cannot follow a symlink out of scope.
		// It only ever removes what this upload created: its temporary file,
		// or the destination when that did not exist before.
		var remove func() error
		switch {
		case !direct:
			remove = func() error { return d.user.Fs.Remove(tmp) }
		case !exists:
			uploadPath := r.URL.Path
			remove = func() error { return d.user.Fs.Remove(uploadPath) }
		}
		cache.Register(uploadKey, uploadInfo{Length: uploadLength, Override: override, Owner: d.user.ID}, remove)

		// The cache removes the temporary file when the upload expires, if
		// it can. This is what does when it cannot: see tusTemps.
		if !direct {
			tusTemps.track(d.user.Fs, tmp, tusTempKey(d, tmp))
		}

		return http.StatusCreated, nil
	})
}

func tusHeadHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("Cache-Control", "no-store")
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		writePath, _, err := tusWritePath(d, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}

		info, err := d.user.Fs.Stat(writePath)
		if err != nil {
			return errToStatus(err), err
		}

		uploadLength, err := cache.GetLength(tusUploadKey(d, r.URL.Path))
		if err != nil {
			return http.StatusNotFound, err
		}

		w.Header().Set("Upload-Offset", strconv.FormatInt(info.Size(), 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(uploadLength, 10))

		return http.StatusOK, nil
	})
}

func tusPatchHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		status, err := tusPatchUpload(w, r, d, cache)
		// A rejected chunk is still a chunk the client is streaming: read what is
		// left of it so the answer reaches the client on a connection that stays
		// usable, instead of being lost to a reset.
		if status >= 400 {
			drainRequestBody(r)
		}
		return status, err
	})
}

func tusPatchUpload(w http.ResponseWriter, r *http.Request, d *data, cache UploadCache) (int, error) {
	if !d.user.Perm.Create || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		return http.StatusUnsupportedMediaType, nil
	}

	uploadOffset, err := getUploadOffset(r)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid upload offset")
	}

	// One request at a time per upload: the check below that the file is as
	// long as the offset says, and the append that follows it, must not
	// interleave with those of another request. Two requests at the same
	// offset would otherwise both pass the check and both append.
	release, ok := tusAcquire(d, r)
	if !ok {
		return http.StatusLocked, errTusBusy
	}
	defer release()

	writePath, temp, err := tusWritePath(d, r.URL.Path)
	if err != nil {
		return errToStatus(err), err
	}

	info, err := d.user.Fs.Stat(writePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, nil
	case err != nil:
		return errToStatus(err), err
	}

	uploadKey := tusUploadKey(d, r.URL.Path)
	upload, err := cache.Get(uploadKey)
	if err != nil {
		return http.StatusNotFound, err
	}
	uploadLength := upload.Length

	if uploadOffset > uploadLength {
		return http.StatusBadRequest, fmt.Errorf("upload offset %d exceeds declared length %d", uploadOffset, uploadLength)
	}

	// Prevent the upload from being evicted during the transfer
	stop := keepUploadActive(cache, uploadKey)
	defer stop()

	switch {
	case info.IsDir():
		return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", r.URL.Path)
	case !info.Mode().IsRegular():
		return http.StatusBadRequest, fmt.Errorf("cannot upload to %s: not a regular file", r.URL.Path)
	case info.Size() != uploadOffset:
		return http.StatusConflict, fmt.Errorf(
			"%s file size doesn't match the provided offset: %d",
			r.URL.Path,
			uploadOffset,
		)
	}

	openFile, err := d.user.Fs.OpenFile(writePath, os.O_WRONLY|os.O_APPEND, d.settings.FileMode)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not open file: %w", err)
	}
	defer openFile.Close()

	_, err = openFile.Seek(uploadOffset, 0)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not seek file: %w", err)
	}

	// The server closes the request body once the handler returns; closing it
	// here would run before the caller gets to drain a rejected chunk, so every
	// status raised below this point would still cost the connection.
	//
	// Enforce the declared Upload-Length: never write more than the bytes
	// still expected for this upload. Reading one byte past the remainder
	// lets an over-length body be detected and rejected. Without this bound a
	// PATCH could stream arbitrary data to disk regardless of the length the
	// client declared when the upload was created.
	remaining := uploadLength - uploadOffset
	bytesWritten, err := io.Copy(openFile, io.LimitReader(r.Body, remaining+1))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not write to file: %w", err)
	}
	if bytesWritten > remaining {
		// The client sent more than it declared; roll this chunk back so the
		// file stays consistent with the tracked offset, and reject it.
		if truncErr := openFile.Truncate(uploadOffset); truncErr != nil {
			return http.StatusInternalServerError, fmt.Errorf("could not truncate file: %w", truncErr)
		}
		return http.StatusRequestEntityTooLarge, fmt.Errorf("upload exceeds declared length of %d bytes", uploadLength)
	}

	// Sync the file to ensure all data is written to storage
	// to prevent file corruption.
	if err := openFile.Sync(); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not sync file: %w", err)
	}

	newOffset := uploadOffset + bytesWritten
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))

	if newOffset >= uploadLength {
		if temp {
			// The upload is complete: only now does it take the place of the
			// destination. The file has to be closed before it can be renamed
			// on Windows.
			if err := openFile.Close(); err != nil {
				return http.StatusInternalServerError, fmt.Errorf("could not close file: %w", err)
			}

			// What the creation of the upload checked was true then. A file
			// can be at the destination now that was not, and the user's
			// permissions can have changed: an upload can take as long as
			// its client likes. The destination is replaced only by an
			// upload that asked to, from a user who may now. Otherwise the
			// upload ends here, as a second upload to a path in use always
			// did, and its data is discarded.
			err := fileutils.CommitTemp(d.user.Fs, writePath, r.URL.Path, d.settings.FileMode, upload.Override && d.user.Perm.Modify)
			switch {
			case errors.Is(err, fberrors.ErrExist):
				cache.Complete(uploadKey)
				if upload.Override {
					return http.StatusForbidden, nil
				}
				return http.StatusConflict, nil
			case err != nil:
				return http.StatusInternalServerError, fmt.Errorf("could not store the upload: %w", err)
			}
		}

		cache.Complete(uploadKey)
		_ = d.RunHook(func() error { return nil }, "upload", r.URL.Path, "", d.user)
	}

	return http.StatusNoContent, nil
}

// tusDeleteHandler cancels an upload in progress. It discards what the upload
// has written so far, which is its temporary file, and never the destination.
//
// Whoever created the upload may cancel it. That used to take the Delete
// permission, from the time when cancelling removed the destination; a user
// without it could not cancel an upload of their own, which then stayed
// behind, out of reach, until it expired. Cancelling somebody else's upload
// still takes the Delete permission.
func tusDeleteHandler(cache UploadCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		// Without either permission there is nothing the user could cancel.
		if r.URL.Path == "/" || (!d.user.Perm.Delete && !d.user.Perm.Create) {
			return http.StatusForbidden, nil
		}
		if !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		writePath, temp, err := tusWritePath(d, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}

		uploadKey := tusUploadKey(d, r.URL.Path)
		upload, err := cache.Get(uploadKey)
		if err != nil {
			return http.StatusNotFound, err
		}

		if !d.user.Perm.Delete && (upload.Owner == 0 || upload.Owner != d.user.ID) {
			return http.StatusForbidden, nil
		}

		release, ok := tusAcquire(d, r)
		if !ok {
			return http.StatusLocked, errTusBusy
		}
		defer release()

		// Only the temporary file is the upload's to remove. An upload that
		// writes to its destination directly does so because the destination
		// was there before it (a symbolic link, or a file in a directory that
		// takes no new one), and cancelling an upload is not deleting a file.
		//
		// temp says that writePath is the temporary file of this upload: a
		// regular file under the name tusTempPath gives it. That name cannot
		// be given to anything through the API (see data.Check), so this
		// never removes a file somebody stored.
		if temp && fileutils.IsTempName(gopath.Base(writePath)) {
			err = d.user.Fs.Remove(writePath)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return errToStatus(err), err
			}
		}

		cache.Complete(uploadKey)

		return http.StatusNoContent, nil
	})
}

func getUploadLength(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload length: %w", err)
	}
	return uploadOffset, nil
}

func getUploadOffset(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload offset: %w", err)
	}
	return uploadOffset, nil
}
