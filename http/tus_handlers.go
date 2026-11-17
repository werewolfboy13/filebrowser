package fbhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/filebrowser/filebrowser/v2/files"
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
// path of their destination.
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
	key := filepath.Clean(d.user.FullPath(r.URL.Path))

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

		fileFlags := os.O_CREATE | os.O_WRONLY

		// if file exists
		if file != nil {
			if file.IsDir {
				return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", r.URL.Path)
			}

			// Existing files will remain untouched unless explicitly instructed to override
			if r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			fileFlags |= os.O_TRUNC
		}

		openFile, err := d.user.Fs.OpenFile(r.URL.Path, fileFlags, d.settings.FileMode)
		if err != nil {
			return errToStatus(err), err
		}
		defer openFile.Close()

		file, err = files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: false,
			Checker:    d,
			Content:    false,
		})
		if err != nil {
			return errToStatus(err), err
		}

		uploadLength, err := getUploadLength(r)
		if err != nil || uploadLength < 0 {
			return http.StatusBadRequest, fmt.Errorf("invalid upload length: %w", err)
		}

		// Enables the user to utilize the PATCH endpoint for uploading file data.
		// The removal callback deletes an abandoned upload through the user's
		// scoped filesystem, so eviction cannot follow a symlink out of scope.
		uploadPath := r.URL.Path
		cache.Register(file.RealPath(), uploadLength, func() error {
			return d.user.Fs.Remove(uploadPath)
		})

		basePath := "/" + strings.Trim(strings.TrimSpace(d.server.BaseURL), "/")
		if basePath == "/" {
			basePath = ""
		}

		w.Header().Set("Location", basePath+"/api/tus"+r.URL.EscapedPath())
		return http.StatusCreated, nil
	})
}

func tusHeadHandler(cache UploadCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("Cache-Control", "no-store")
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		uploadLength, err := cache.GetLength(file.RealPath())
		if err != nil {
			return http.StatusNotFound, err
		}

		w.Header().Set("Upload-Offset", strconv.FormatInt(file.Size, 10))
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
		return http.StatusNotFound, nil
	case err != nil:
		return errToStatus(err), err
	}

	uploadLength, err := cache.GetLength(file.RealPath())
	if err != nil {
		return http.StatusNotFound, err
	}

	if uploadOffset > uploadLength {
		return http.StatusBadRequest, fmt.Errorf("upload offset %d exceeds declared length %d", uploadOffset, uploadLength)
	}

	// Prevent the upload from being evicted during the transfer
	stop := keepUploadActive(cache, file.RealPath())
	defer stop()

	switch {
	case file.IsDir:
		return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", r.URL.Path)
	case file.Size != uploadOffset:
		return http.StatusConflict, fmt.Errorf(
			"%s file size doesn't match the provided offset: %d",
			r.URL.Path,
			uploadOffset,
		)
	}

	openFile, err := d.user.Fs.OpenFile(r.URL.Path, os.O_WRONLY|os.O_APPEND, d.settings.FileMode)
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
		cache.Complete(file.RealPath())
		_ = d.RunHook(func() error { return nil }, "upload", r.URL.Path, "", d.user)
	}

	return http.StatusNoContent, nil
}

func tusDeleteHandler(cache UploadCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if r.URL.Path == "/" || !d.user.Perm.Delete {
			return http.StatusForbidden, nil
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		_, err = cache.GetLength(file.RealPath())
		if err != nil {
			return http.StatusNotFound, err
		}

		release, ok := tusAcquire(d, r)
		if !ok {
			return http.StatusLocked, errTusBusy
		}
		defer release()

		err = d.user.Fs.RemoveAll(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}

		cache.Complete(file.RealPath())

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
