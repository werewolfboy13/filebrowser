package fbhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	gopath "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/afero"

	libErrors "github.com/werewolfboy13/filebrowser/v2/errors"
	imgErrors "github.com/werewolfboy13/filebrowser/v2/img"
)

// slashClean canonicalizes a virtual path to the absolute, "/"-separated,
// lexically cleaned form that both the rule checker and the scoped filesystem
// expect.
//
// Paths reach us either verbatim from a request or built by the OS — afero.Walk
// and filepath.Join use "\" on Windows. There the filesystem resolves "\" as a
// separator while the rule checker treated it as an ordinary character, so
// "/allow\..\Secret.txt" evaded a rule for "/Secret.txt" and still opened it.
// Normalizing with the host's own separator makes both layers agree on which
// file a path names, and is a no-op on POSIX, where "\" is a legal character in
// a filename and must stay one.
func slashClean(name string) string {
	return cleanSeparators(name, string(filepath.Separator))
}

// canonicalizeRequestPath rewrites the request path to its canonical virtual
// form, so that the filesystem operation, the hook, the stored share record and
// the thumbnail cache key all use the same string the rule check approved.
//
// A trailing separator is preserved: handlers distinguish "/dir/" from "/dir"
// to decide whether to create a directory, and gopath.Clean would drop it.
func canonicalizeRequestPath(r *http.Request) {
	p := slashClean(r.URL.Path)
	trailing := strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(r.URL.Path, string(filepath.Separator))
	if p != "/" && trailing {
		p += "/"
	}
	r.URL.Path = p
}

// cleanSeparators is slashClean with the host separator passed in explicitly,
// so the Windows behaviour can be exercised on any platform.
func cleanSeparators(name, sep string) string {
	if sep != "/" {
		name = strings.ReplaceAll(name, sep, "/")
	}
	if name == "" || name[0] != '/' {
		name = "/" + name
	}
	return gopath.Clean(name)
}

func renderJSON(w http.ResponseWriter, _ *http.Request, data interface{}) (int, error) {
	marsh, err := json.Marshal(data)

	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, err := w.Write(marsh); err != nil {
		// The status line is already on the wire, so there is no error status
		// left to send: a failed write here means the client hung up. Report it
		// for the log without asking the caller to write a second header.
		return 0, err
	}

	return 0, nil
}

// lstatPath stats name without following a final symlink, where the
// filesystem can.
func lstatPath(afs afero.Fs, name string) (os.FileInfo, error) {
	if lstater, ok := afs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(name)
		return info, err
	}
	return afs.Stat(name)
}

// Limits on how long the server waits for a request body. They are variables
// so that tests can shorten them.
//
// The server itself only limits the time a request header may take
// (ReadHeaderTimeout): a limit on whole requests would cut off uploads, and a
// connection deadline that is still set while a handler runs cancels the
// request, which would cut off downloads. So the wait is limited where a body
// is read that has no reason to be slow, and lifted again once it is in.
var (
	// requestBodyTimeout is the time a small request body (credentials, a
	// JSON document) may take to arrive.
	requestBodyTimeout = 30 * time.Second

	// leftoverBodyTimeout is the time the server waits for the rest of a body
	// nobody is going to use: that of a refused request, or of a request that
	// should not have one.
	leftoverBodyTimeout = 10 * time.Second
)

// limitBodyWait gives the connection of w the time d to deliver the request
// body. It does nothing for a writer that is not a server connection.
func limitBodyWait(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
}

// liftBodyWait removes the limit set by limitBodyWait. It has to be called as
// soon as the body is in: the limit is a deadline on the connection, and when
// it passes while the handler is still at work the request is cancelled.
func liftBodyWait(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
}

// readSmallBody reads the whole request body, which may be at most
// maxAuthBodySize bytes and has requestBodyTimeout to arrive. It returns the
// status to answer with when it cannot be read: 413 when it is too large, 408
// when it did not arrive in time, 400 otherwise.
func readSmallBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	if r.Body == nil {
		return nil, http.StatusBadRequest, libErrors.ErrEmptyRequest
	}

	limitBodyWait(w, requestBodyTimeout)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBodySize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		var netErr net.Error
		switch {
		case errors.As(err, &tooLarge):
			return nil, http.StatusRequestEntityTooLarge, err
		case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, os.ErrDeadlineExceeded):
			return nil, http.StatusRequestTimeout, err
		}
		return nil, http.StatusBadRequest, err
	}
	liftBodyWait(w)

	return body, 0, nil
}

// decodeJSONBody decodes the JSON request body into v. See readSmallBody for
// its limits and for the status returned when the body cannot be read; a body
// that is not valid JSON is a 400.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v interface{}) (int, error) {
	body, status, err := readSmallBody(w, r)
	if err != nil {
		return status, err
	}

	if err := json.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		return http.StatusBadRequest, err
	}

	return 0, nil
}

// maxUnexpectedBody is how much of a body that should not be there is read
// and thrown away. It is what the server itself is prepared to discard.
const maxUnexpectedBody = 256 << 10

// discardUnexpectedBody deals with the body of requests that have no use for
// one: everything but POST, PUT, PATCH and DELETE. No browser sends one, but
// nothing stops a client from announcing it and then never sending it, and the
// server waits for a body before it answers, on whatever route, logged in or
// not. Here the body is read and dropped within leftoverBodyTimeout, before
// the request is handled; a client that does not deliver it is answered and
// disconnected.
//
// Requests whose method can carry a body are left alone: uploads may take as
// long as they need. Those are covered where the body is read or refused (see
// readSmallBody and handle).
func discardUnexpectedBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			next.ServeHTTP(w, r)
			return
		}

		if r.Body != nil && r.Body != http.NoBody {
			limitBodyWait(w, leftoverBodyTimeout)
			n, err := io.Copy(io.Discard, io.LimitReader(r.Body, maxUnexpectedBody+1))
			if err != nil || n > maxUnexpectedBody {
				w.Header().Set("Connection", "close")
				status := http.StatusRequestTimeout
				if err == nil {
					status = http.StatusRequestEntityTooLarge
				}
				http.Error(w, strconv.Itoa(status)+" "+http.StatusText(status), status)
				return
			}
			liftBodyWait(w)
		}

		next.ServeHTTP(w, r)
	})
}

func errToStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case os.IsPermission(err):
		return http.StatusForbidden
	case os.IsNotExist(err), errors.Is(err, libErrors.ErrNotExist):
		return http.StatusNotFound
	case os.IsExist(err), errors.Is(err, libErrors.ErrExist):
		return http.StatusConflict
	case errors.Is(err, libErrors.ErrPermissionDenied):
		return http.StatusForbidden
	case errors.Is(err, libErrors.ErrInvalidRequestParams):
		return http.StatusBadRequest
	case errors.Is(err, libErrors.ErrRootUserDeletion):
		return http.StatusForbidden
	case errors.Is(err, imgErrors.ErrImageTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

// This is an adaptation if http.StripPrefix in which we don't
// return 404 if the page doesn't have the needed prefix.
func stripPrefix(prefix string, h http.Handler) http.Handler {
	if prefix == "" || prefix == "/" {
		return h
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		rp := strings.TrimPrefix(r.URL.RawPath, prefix)

		// If the path is exactly the prefix (no trailing slash), redirect to
		// the prefix with a trailing slash so the router receives "/" instead
		// of "", which would otherwise cause a redirect to the site root.
		if p == "" {
			http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
			return
		}

		r2 := new(http.Request)
		*r2 = *r
		r2.URL = new(url.URL)
		*r2.URL = *r.URL
		r2.URL.Path = p
		r2.URL.RawPath = rp
		h.ServeHTTP(w, r2)
	})
}
