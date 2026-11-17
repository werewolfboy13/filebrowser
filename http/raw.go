package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	gopath "path"
	"path/filepath"
	"strings"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/fileutils"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/mholt/archives"
	"github.com/spf13/afero"
)

func parseQueryFiles(r *http.Request, f *files.FileInfo, _ *users.User) ([]string, error) {
	var fileSlice []string
	names := strings.Split(r.URL.Query().Get("files"), ",")

	if len(names) == 0 {
		fileSlice = append(fileSlice, f.Path)
	} else {
		for _, name := range names {
			name, err := url.QueryUnescape(strings.ReplaceAll(name, "+", "%2B"))
			if err != nil {
				return nil, err
			}

			name = slashClean(name)
			fileSlice = append(fileSlice, filepath.Join(f.Path, name))
		}
	}

	return fileSlice, nil
}

func parseQueryAlgorithm(r *http.Request) (string, archives.Archival, error) {
	switch r.URL.Query().Get("algo") {
	case "zip", "true", "":
		return ".zip", archives.Zip{}, nil
	case "tar":
		return ".tar", archives.Tar{}, nil
	case "targz":
		return ".tar.gz", archives.CompressedArchive{Compression: archives.Gz{}, Archival: archives.Tar{}}, nil
	case "tarbz2":
		return ".tar.bz2", archives.CompressedArchive{Compression: archives.Bz2{}, Archival: archives.Tar{}}, nil
	case "tarxz":
		return ".tar.xz", archives.CompressedArchive{Compression: archives.Xz{}, Archival: archives.Tar{}}, nil
	case "tarlz4":
		return ".tar.lz4", archives.CompressedArchive{Compression: archives.Lz4{}, Archival: archives.Tar{}}, nil
	case "tarsz":
		return ".tar.sz", archives.CompressedArchive{Compression: archives.Sz{}, Archival: archives.Tar{}}, nil
	case "tarbr":
		return ".tar.br", archives.CompressedArchive{Compression: archives.Brotli{}, Archival: archives.Tar{}}, nil
	case "tarzst":
		return ".tar.zst", archives.CompressedArchive{Compression: archives.Zstd{}, Archival: archives.Tar{}}, nil
	default:
		return "", nil, errors.New("format not implemented")
	}
}

func setContentDisposition(w http.ResponseWriter, r *http.Request, file *files.FileInfo) {
	if r.URL.Query().Get("inline") == "true" {
		// As per RFC6266 section 4.3
		w.Header().Set("Content-Disposition", "inline; filename*=utf-8''"+url.PathEscape(file.Name))
	} else {
		// As per RFC6266 section 4.3
		w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(file.Name))
		w.Header().Set("Content-Type", "application/octet-stream")
	}
}

var rawHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.user.Perm.Download {
		return http.StatusAccepted, nil
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

	if files.IsNamedPipe(file.Mode) {
		setContentDisposition(w, r, file)
		return 0, nil
	}

	if !file.IsDir {
		return rawFileHandler(w, r, file)
	}

	return rawDirHandler(w, r, d, file)
})

// archiveWalk collects the files of an archive download.
type archiveWalk struct {
	ctx        context.Context
	d          *data
	commonPath string

	// visited holds the real path of every directory that has been listed.
	// Stat follows symlinks, so without it a link to an ancestor would be
	// followed until the kernel gives up (40 levels), and two of them would
	// make the walk exponential. Each real directory is archived once, under
	// the first name it is reached by.
	visited map[string]struct{}
}

func newArchiveWalk(ctx context.Context, d *data, commonPath string) *archiveWalk {
	return &archiveWalk{ctx: ctx, d: d, commonPath: commonPath, visited: map[string]struct{}{}}
}

// enter reports whether the directory at path has not been listed yet, and
// records it. A directory whose real location cannot be determined is not
// entered through a symlink, which keeps the walk finite in that case too.
func (w *archiveWalk) enter(path string) bool {
	real := path
	resolved := false
	if files.BasePath(w.d.user.Fs) != nil {
		if p, err := filepath.EvalSymlinks(w.d.user.FullPath(path)); err == nil {
			real, resolved = p, true
		}
	}

	if !resolved {
		lstater, ok := w.d.user.Fs.(afero.Lstater)
		if !ok {
			return false
		}
		info, _, err := lstater.LstatIfPossible(path)
		if err != nil || !info.IsDir() {
			return false
		}
	}

	if _, seen := w.visited[real]; seen {
		return false
	}
	w.visited[real] = struct{}{}
	return true
}

// files returns the archive entries for path and, when it is a directory,
// everything below it. It stops with the context's error once the request is
// done: collecting a large tree for a client that has left only costs memory.
func (w *archiveWalk) files(path string) ([]archives.FileInfo, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}

	d := w.d
	if !d.Check(path) {
		return nil, nil
	}

	info, err := d.user.Fs.Stat(path)
	if err != nil {
		return nil, err
	}

	if info.IsDir() && !w.enter(path) {
		return nil, nil
	}

	var archiveFiles []archives.FileInfo

	if path != w.commonPath {
		nameInArchive := strings.TrimPrefix(path, w.commonPath)
		nameInArchive = strings.TrimPrefix(nameInArchive, string(filepath.Separator))
		nameInArchive = filepath.ToSlash(nameInArchive)
		// A backslash is a legal filename character on POSIX hosts, so it can
		// reach here verbatim. Rewriting it to the path separator "/" would
		// manufacture a traversal sequence (e.g. "..\..\x" -> "../../x") that
		// escapes the extraction directory on the victim's machine, while
		// leaving it as "\" lets Windows extractors treat it as a separator.
		// Neutralize it to an inert character instead of turning it into one.
		nameInArchive = strings.ReplaceAll(nameInArchive, "\\", "_")

		// Defense in depth: never emit an archive entry whose path escapes the
		// archive root, regardless of how the name was produced.
		if cleaned := gopath.Clean("/" + nameInArchive); cleaned != "/"+nameInArchive {
			return nil, fmt.Errorf("refusing unsafe archive entry name: %q", nameInArchive)
		}

		archiveFiles = append(archiveFiles, archives.FileInfo{
			FileInfo:      info,
			NameInArchive: nameInArchive,
			Open: func() (fs.File, error) {
				return d.user.Fs.Open(path)
			},
		})
	}

	if info.IsDir() {
		f, err := d.user.Fs.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		names, err := f.Readdirnames(0)
		if err != nil {
			return nil, err
		}

		for _, name := range names {
			fPath := filepath.Join(path, name)
			subFiles, err := w.files(fPath)
			if err != nil {
				if w.ctx.Err() != nil {
					return nil, err
				}
				log.Printf("Failed to get files from %s: %v", fPath, err)
				continue
			}
			archiveFiles = append(archiveFiles, subFiles...)
		}
	}

	return archiveFiles, nil
}

func rawDirHandler(w http.ResponseWriter, r *http.Request, d *data, file *files.FileInfo) (int, error) {
	filenames, err := parseQueryFiles(r, file, d.user)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	extension, archiver, err := parseQueryAlgorithm(r)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	commonDir := fileutils.CommonPrefix(filepath.Separator, filenames...)

	walk := newArchiveWalk(r.Context(), d, commonDir)
	var allFiles []archives.FileInfo
	for _, fname := range filenames {
		archiveFiles, err := walk.files(fname)
		if err != nil {
			// Nobody is left to answer once the request is done.
			if r.Context().Err() != nil {
				return 0, err
			}
			log.Printf("Failed to get files from %s: %v", fname, err)
			continue
		}
		allFiles = append(allFiles, archiveFiles...)
	}

	name := filepath.Base(commonDir)
	if name == "." || name == "" || name == string(filepath.Separator) {
		if file.Name != "" {
			name = file.Name
		} else {
			actual, statErr := file.Fs.Stat(".")
			if statErr != nil {
				return http.StatusInternalServerError, statErr
			}
			name = actual.Name()
		}
	}
	if len(filenames) > 1 {
		name = "_" + name
	}
	name += extension
	w.Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))

	if err := archiver.Archive(r.Context(), w, allFiles); err != nil {
		return http.StatusInternalServerError, err
	}

	return 0, nil
}

func rawFileHandler(w http.ResponseWriter, r *http.Request, file *files.FileInfo) (int, error) {
	fd, err := file.Fs.Open(file.Path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer fd.Close()

	setContentDisposition(w, r, file)
	w.Header().Add("Content-Security-Policy", `script-src 'none';`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private")
	http.ServeContent(w, r, file.Name, file.ModTime, fd)
	return 0, nil
}
