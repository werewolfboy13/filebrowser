package fbhttp

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/asticode/go-astisub"

	"github.com/filebrowser/filebrowser/v2/files"
)

var srtLineBreakTag = regexp.MustCompile(`(?i)<br(?:\s+[^>]*)?\s*/?>`)

// maxSubtitleSize is the largest subtitle file that is converted to WebVTT.
// The conversion holds the whole file in memory several times over (the raw
// bytes, the parsed cues and the rendered output), so the limit is what bounds
// the memory one request can use. Real subtitle files are far smaller.
const maxSubtitleSize = 5 << 20 // 5 MiB

var subtitleHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
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

	if file.IsDir {
		return http.StatusBadRequest, nil
	}

	return subtitleFileHandler(w, r, file)
})

func subtitleFileHandler(w http.ResponseWriter, r *http.Request, file *files.FileInfo) (int, error) {
	// if its not a subtitle file, reject
	if !files.IsSupportedSubtitle(file.Name) {
		return http.StatusBadRequest, nil
	}

	// WebVTT is streamed to the client as it is; every other format is
	// converted in memory and therefore limited in size. Stat follows a
	// symlink, so the size is that of the file that will be read.
	converted := !strings.HasSuffix(file.Name, ".vtt")
	if converted {
		info, err := file.Fs.Stat(file.Path)
		if err != nil {
			return errToStatus(err), err
		}
		if info.Size() > maxSubtitleSize {
			return http.StatusRequestEntityTooLarge, nil
		}
	}

	fd, err := file.Fs.Open(file.Path)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer fd.Close()

	// The size was checked above, but the file can grow before it is read.
	limited := io.LimitReader(fd, maxSubtitleSize)

	// load subtitle for conversion to vtt
	var sub *astisub.Subtitles
	if strings.HasSuffix(file.Name, ".srt") {
		content, readErr := io.ReadAll(limited)
		if readErr != nil {
			return http.StatusInternalServerError, readErr
		}
		sub, err = astisub.ReadFromSRT(bytes.NewReader(normalizeSRTLineBreaks(content)))
	} else if strings.HasSuffix(file.Name, ".ass") || strings.HasSuffix(file.Name, ".ssa") {
		sub, err = astisub.ReadFromSSA(limited)
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}

	setContentDisposition(w, r, file)
	w.Header().Add("Content-Security-Policy", `script-src 'none';`)
	w.Header().Set("Cache-Control", "private")
	// force type to text/vtt
	w.Header().Set("Content-Type", "text/vtt")

	// serve vtt file directly
	if sub == nil {
		http.ServeContent(w, r, file.Name, file.ModTime, fd)
		return 0, nil
	}

	// convert others to vtt and serve from buffer
	var buf = &bytes.Buffer{}
	err = sub.WriteToWebVTT(buf)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	http.ServeContent(w, r, file.Name, file.ModTime, bytes.NewReader(buf.Bytes()))
	return 0, nil
}

func normalizeSRTLineBreaks(content []byte) []byte {
	return srtLineBreakTag.ReplaceAll(content, []byte("\n"))
}
