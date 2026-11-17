package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/spf13/afero"
)

var resourceGetHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file, err := files.NewFileInfo(&files.FileOptions{
		Fs:         d.user.Fs,
		Path:       r.URL.Path,
		Modify:     d.user.Perm.Modify,
		Expand:     true,
		ReadHeader: d.server.TypeDetectionByHeader,
		Checker:    d,
		Content:    d.user.Perm.Download,
	})
	if err != nil {
		return errToStatus(err), err
	}

	encoding := r.Header.Get("X-Encoding")
	if file.IsDir {
		file.Sorting = d.user.Sorting
		file.ApplySort()
		return renderJSON(w, r, file)
	} else if encoding == "true" {
		if !d.user.Perm.Download {
			return http.StatusAccepted, nil
		}
		if file.Type != "text" {
			return renderJSON(w, r, file)
		}

		f, err := d.user.Fs.Open(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		defer f.Close()

		data, err := io.ReadAll(f)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(data)
		return 0, err
	}

	if checksum := r.URL.Query().Get("checksum"); checksum != "" {
		if !d.user.Perm.Download {
			return http.StatusAccepted, nil
		}

		err := file.Checksum(checksum)
		if errors.Is(err, fberrors.ErrInvalidOption) {
			return http.StatusBadRequest, nil
		} else if err != nil {
			return http.StatusInternalServerError, err
		}

		// do not waste bandwidth if we just want the checksum
		file.Content = ""
	}

	return renderJSON(w, r, file)
})

func resourceDeleteHandler(fileCache FileCache) handleFunc {
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

		if err = checkDescendants(d, r.URL.Path, ""); err != nil {
			return errToStatus(err), err
		}

		// delete thumbnails
		err = delThumbs(r.Context(), fileCache, file)
		if err != nil {
			return errToStatus(err), err
		}

		err = d.RunHook(func() error {
			return d.user.Fs.RemoveAll(r.URL.Path)
		}, "delete", r.URL.Path, "", d.user)

		// Remove the shares of what is gone, whoever owns them: a share that
		// outlives its target would publish whatever is created at that path
		// later. After a failure, only the shares of what was actually
		// removed go.
		if shareErr := deleteSharesUnder(d, file.Path, err != nil); shareErr != nil {
			log.Printf("WARNING: Error(s) occurred while deleting associated shares with file: %s", shareErr)
		}

		if err != nil {
			return errToStatus(err), err
		}

		return http.StatusNoContent, nil
	})
}

func resourcePostHandler(fileCache FileCache) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		// Directories creation on POST.
		if strings.HasSuffix(r.URL.Path, "/") {
			err := d.RunHook(func() error {
				return d.user.Fs.MkdirAll(r.URL.Path, d.settings.DirMode)
			}, "upload", r.URL.Path, "", d.user)
			return errToStatus(err), err
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err == nil {
			// An existing directory is never something an upload can replace.
			if file.IsDir || r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			err = delThumbs(r.Context(), fileCache, file)
			if err != nil {
				return errToStatus(err), err
			}
		}

		sweepStaleUploads(d, r.URL.Path)

		err = d.RunHook(func() error {
			info, writeErr := writeFile(d.user.Fs, r.URL.Path, r.Body, d.settings.FileMode, d.settings.DirMode)
			if writeErr != nil {
				return writeErr
			}

			etag := fmt.Sprintf(`"%x%x"`, info.ModTime().UnixNano(), info.Size())
			w.Header().Set("ETag", etag)
			return nil
		}, "upload", r.URL.Path, "", d.user)

		// Nothing is cleaned up here after a failure. The upload goes to a
		// temporary file, which the writer removes itself, so whatever is at
		// the path now was put there by someone else: this handler used to
		// remove it as "the partial file this request created".
		return errToStatus(err), err
	})
}

var resourcePutHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.user.Perm.Modify || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}

	// Only allow PUT for files.
	if strings.HasSuffix(r.URL.Path, "/") {
		return http.StatusMethodNotAllowed, nil
	}

	exists, err := afero.Exists(d.user.Fs, r.URL.Path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !exists {
		return http.StatusNotFound, nil
	}

	sweepStaleUploads(d, r.URL.Path)

	err = d.RunHook(func() error {
		info, writeErr := writeFile(d.user.Fs, r.URL.Path, r.Body, d.settings.FileMode, d.settings.DirMode)
		if writeErr != nil {
			return writeErr
		}

		etag := fmt.Sprintf(`"%x%x"`, info.ModTime().UnixNano(), info.Size())
		w.Header().Set("ETag", etag)
		return nil
	}, "save", r.URL.Path, "", d.user)

	return errToStatus(err), err
})

func resourcePatchHandler(fileCache FileCache) handleFunc {
	return withUser(func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		src := r.URL.Path
		dst := r.URL.Query().Get("destination")
		action := r.URL.Query().Get("action")
		dst, err := url.QueryUnescape(dst)
		dst = slashClean(dst)
		src = slashClean(src)
		if !d.Check(src) || !d.Check(dst) {
			return http.StatusForbidden, nil
		}
		if err != nil {
			return errToStatus(err), err
		}
		if dst == "/" || src == "/" {
			return http.StatusForbidden, nil
		}

		err = checkParent(src, dst)
		if err != nil {
			return http.StatusBadRequest, err
		}

		srcInfo, _ := d.user.Fs.Stat(src)
		dstInfo, _ := d.user.Fs.Stat(dst)
		same := os.SameFile(srcInfo, dstInfo)

		if action != "rename" || !same {
			override := r.URL.Query().Get("override") == "true"
			rename := r.URL.Query().Get("rename") == "true"
			if !override && !rename {
				if _, err = d.user.Fs.Stat(dst); err == nil {
					return http.StatusConflict, nil
				}
			}
			if rename {
				dst = addVersionSuffix(dst, d.user.Fs)
			}

			if override && !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}
		}

		if err = checkDescendants(d, src, dst); err != nil {
			return errToStatus(err), err
		}

		err = d.RunHook(func() error {
			return patchAction(r.Context(), action, src, dst, d, fileCache)
		}, action, src, dst, d.user)

		return errToStatus(err), err
	})
}

// checkDescendants reports an error if the rules deny any path inside the src
// tree or, when dst is not empty, the location that tree would land on. The
// same goes for the server's own files (see data.protected): a directory that
// holds one cannot be deleted, moved or copied as a whole. The
// handlers only authorize the root of the operation, but copy, rename and
// delete then recurse through the whole subtree, so a rule denying a descendant
// of an allowed directory would be bypassed by operating on the parent instead.
func checkDescendants(d *data, src, dst string) error {
	// The walk is only needed when something can be refused below the root of
	// the operation: a rule, or one of the server's own files.
	if len(d.settings.Rules) == 0 && len(d.user.Rules) == 0 && !d.server.HasProtectedPaths() {
		return nil
	}

	return afero.Walk(d.user.Fs, src, func(fPath string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !d.CheckRules(fPath) {
			return fberrors.ErrPermissionDenied
		}

		if dst == "" {
			return nil
		}

		rel, err := filepath.Rel(src, fPath)
		if err != nil {
			return err
		}

		if !d.CheckRules(filepath.Join(dst, rel)) {
			return fberrors.ErrPermissionDenied
		}

		return nil
	})
}

func checkParent(src, dst string) error {
	rel, err := filepath.Rel(src, dst)
	if err != nil {
		return err
	}

	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") && rel != ".." && rel != "." {
		return fberrors.ErrSourceIsParent
	}

	return nil
}

func addVersionSuffix(source string, afs afero.Fs) string {
	counter := 1
	dir, name := path.Split(source)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for {
		if _, err := afs.Stat(source); err != nil {
			break
		}
		renamed := fmt.Sprintf("%s(%d)%s", base, counter, ext)
		source = path.Join(dir, renamed)
		counter++
	}

	return source
}

// writeFile stores the content read from in at dst, creating the directories
// leading to it. The content only takes the place of an existing file once it
// has been received completely: see fileutils.WriteFileAtomic.
func writeFile(afs afero.Fs, dst string, in io.Reader, fileMode, dirMode fs.FileMode) (os.FileInfo, error) {
	dir, _ := path.Split(dst)
	err := afs.MkdirAll(dir, dirMode)
	if err != nil {
		return nil, err
	}

	return fileutils.WriteFileAtomic(afs, dst, in, fileMode)
}

// uploadSweeps remembers when the temporary files of a directory were last
// looked at, so that a burst of uploads into one directory lists it once.
var uploadSweeps = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

const (
	// staleUploadAge is how long a temporary upload file is left alone after
	// it was last written. An upload in progress touches its file with every
	// chunk, so anything older was abandoned by a crash or a restart.
	staleUploadAge = 24 * time.Hour

	uploadSweepInterval = time.Hour
	uploadSweepMemory   = 1024
)

// sweepStaleUploads removes the temporary upload files in the directory of
// dst that have not been written for staleUploadAge. They are never listed and
// cannot be addressed through the API, so this is what cleans up after a
// crash or a restart in the middle of an upload.
func sweepStaleUploads(d *data, dst string) {
	dir, _ := path.Split(dst)
	if dir == "" {
		dir = "/"
	}
	key := filepath.Clean(d.user.FullPath(dir))

	now := time.Now()
	uploadSweeps.Lock()
	if last, ok := uploadSweeps.last[key]; ok && now.Sub(last) < uploadSweepInterval {
		uploadSweeps.Unlock()
		return
	}
	if len(uploadSweeps.last) >= uploadSweepMemory {
		clear(uploadSweeps.last)
	}
	uploadSweeps.last[key] = now
	uploadSweeps.Unlock()

	// A directory that does not exist yet has nothing to sweep.
	_ = fileutils.RemoveStaleTemps(d.user.Fs, dir, func(info os.FileInfo) bool {
		return now.Sub(info.ModTime()) > staleUploadAge
	})
}

func delThumbs(ctx context.Context, fileCache FileCache, file *files.FileInfo) error {
	for _, previewSizeName := range PreviewSizeNames() {
		size, _ := ParsePreviewSize(previewSizeName)
		if err := fileCache.Delete(ctx, previewCacheKey(file, size)); err != nil {
			return err
		}
	}

	return nil
}

func patchAction(ctx context.Context, action, src, dst string, d *data, fileCache FileCache) error {
	switch action {
	case "copy":
		if !d.user.Perm.Create {
			return fberrors.ErrPermissionDenied
		}

		return fileutils.Copy(d.user.Fs, src, dst, d.settings.FileMode, d.settings.DirMode)
	case "rename":
		if !d.user.Perm.Rename {
			return fberrors.ErrPermissionDenied
		}
		src = path.Clean("/" + src)
		dst = path.Clean("/" + dst)

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       src,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: false,
			Checker:    d,
		})
		if err != nil {
			return err
		}

		// delete thumbnails
		err = delThumbs(ctx, fileCache, file)
		if err != nil {
			return err
		}

		err = fileutils.MoveFile(d.user.Fs, src, dst, d.settings.FileMode, d.settings.DirMode)
		if err != nil {
			return err
		}

		// Shares point at paths, so they have to be moved along with what
		// they share. Left behind, they would publish whatever is created at
		// the old path later.
		if shareErr := moveSharesUnder(d, src, dst); shareErr != nil {
			log.Printf("WARNING: Error(s) occurred while moving the shares of %s: %s", src, shareErr)
		}

		return nil
	default:
		return fmt.Errorf("unsupported action %s: %w", action, fberrors.ErrInvalidRequestParams)
	}
}

// RecursiveEntry is a single file/directory entry returned by the recursive listing endpoint.
type RecursiveEntry struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modified"`
	IsDir   bool      `json:"isDir"`
}

// resourceGetRecursiveHandler returns a flat list of every file and directory
// under the requested path, walking the tree recursively on the server side
// so the client only needs a single HTTP call.
var resourceGetRecursiveHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	rootPath := r.URL.Path
	if rootPath == "" {
		rootPath = "/"
	}

	// The walk below checks every entry against the rules, but not the root
	// it starts from. Refuse a denied root before looking it up, so the answer
	// does not reveal whether it exists.
	if !d.Check(rootPath) {
		return http.StatusForbidden, nil
	}

	// Make sure the root itself exists and is a directory.
	info, err := d.user.Fs.Stat(rootPath)
	if err != nil {
		return errToStatus(err), err
	}
	if !info.IsDir() {
		return http.StatusBadRequest, fmt.Errorf("path is not a directory")
	}

	entries := make([]RecursiveEntry, 0)

	err = afero.Walk(d.user.Fs, rootPath, func(fPath string, info os.FileInfo, err error) error {
		// Walking a large tree is expensive, and every entry is stat'ed through
		// the scoped filesystem. Stop as soon as nobody is waiting for the
		// answer instead of finishing the walk for a client that has gone.
		if ctxErr := r.Context().Err(); ctxErr != nil {
			return ctxErr
		}

		if err != nil {
			return nil // skip entries we cannot read
		}

		// Skip the root directory itself.
		if fPath == rootPath {
			return nil
		}

		// Respect user rules.
		if !d.Check(fPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entries = append(entries, RecursiveEntry{
			// afero.Walk joins paths with the OS separator, so on Windows fPath
			// uses backslashes. The web API contract is forward slashes, so
			// normalize it here (mirrors search/search.go).
			Path:    filepath.ToSlash(fPath),
			Name:    info.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
		return nil
	})
	if err != nil {
		// Once the request is done there is nobody left to answer, whatever the
		// walk reported. Asking for a status here only produces a second header
		// write on a connection that is already gone.
		if r.Context().Err() != nil {
			return 0, err
		}
		return http.StatusInternalServerError, err
	}

	return renderJSON(w, r, entries)
})

type DiskUsageResponse struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

var diskUsage = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file, err := files.NewFileInfo(&files.FileOptions{
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
	fPath := file.RealPath()
	if !file.IsDir {
		return renderJSON(w, r, &DiskUsageResponse{
			Total: 0,
			Used:  0,
		})
	}

	usage, err := disk.UsageWithContext(r.Context(), fPath)
	if err != nil {
		return errToStatus(err), err
	}
	return renderJSON(w, r, &DiskUsageResponse{
		Total: usage.Total,
		Used:  usage.Used,
	})
})
