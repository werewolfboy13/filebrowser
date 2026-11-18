package fbhttp

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// doTraced is do for a request whose connection matters: it also reports
// whether the request went out on a connection that was used before.
func (e *fullEnv) doTraced(method, path, token string, body io.Reader, hdr map[string]string) (code int, reused bool, text string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}))
	if token != "" {
		req.Header.Set("X-Auth", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		return -1, reused, err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, reused, string(b)
}

// The server reads what is left of a request body before it sends its
// answer. That wait had a limit for requests that were refused, and none for
// requests that were carried out without their body being read: a delete, a
// copy, a new directory, the creation or the cancellation of an upload, a
// renewal. A client that announced a body with such a request and never sent
// it kept the connection, and the goroutine serving it, for as long as it
// liked; so did the body of a rejected chunk that stopped arriving. The
// request itself had long been carried out.
func TestStalledBodiesOfAnsweredRequestsAreCutOff(t *testing.T) {
	shortBodyTimeouts(t)
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)
	e.writeBobFile("gone.txt", "x")
	e.writeBobFile("copied.txt", "x")
	e.writeBobFile("moved.txt", "x")
	e.writeBobFile("shared.txt", "x")
	share := e.createShare(tok, "/shared.txt", "{}")
	for _, name := range []string{"cancelled.bin", "rejected.bin"} {
		if code, _, body := e.do(http.MethodPost, "/api/tus/"+name, tok, nil, map[string]string{"Upload-Length": "100"}); code != http.StatusCreated {
			t.Fatalf("tus create of %s = %d %q", name, code, body)
		}
	}

	// Each request announces 64 bytes and sends four.
	head := func(method, path, extra string) string {
		return method + " " + path + " HTTP/1.1\r\nHost: x\r\nX-Auth: " + tok + "\r\n" + extra + "Content-Length: 64\r\n\r\n"
	}
	exists := func(rel string) bool { return pathExists(filepath.Join(e.bobDir, rel)) }

	requests := []struct {
		name, head string
		want       string
		done       func() bool // the request was carried out
	}{
		{"delete", head("DELETE", "/api/resources/gone.txt", ""), " 204 ", func() bool { return !exists("gone.txt") }},
		{"copy", head("PATCH", "/api/resources/copied.txt?action=copy&destination=%2Fcopy.txt", ""), " 200 ", func() bool { return exists("copy.txt") }},
		{"rename", head("PATCH", "/api/resources/moved.txt?action=rename&destination=%2Frenamed.txt", ""), " 200 ", func() bool { return exists("renamed.txt") }},
		{"new directory", head("POST", "/api/resources/newdir/", ""), " 200 ", func() bool { return exists("newdir") }},
		{"upload creation", head("POST", "/api/tus/created.bin", "Upload-Length: 100\r\n"), " 201 ", func() bool { return len(uploadTempsIn(t, e.bobDir)) == 3 }},
		{"upload cancellation", head("DELETE", "/api/tus/cancelled.bin", ""), " 204 ", func() bool { return len(uploadTempsIn(t, e.bobDir)) == 2 }},
		{"share removal", head("DELETE", "/api/share/"+share, ""), " 200 ", func() bool { return true }},
		{"renewal", head("POST", "/api/renew", ""), " 200 ", func() bool { return true }},
		{"rejected chunk", head("PATCH", "/api/tus/rejected.bin", "Content-Type: application/offset+octet-stream\r\nUpload-Offset: 50\r\n"), " 409 ", func() bool { return true }},
	}
	for _, rq := range requests {
		t.Run(rq.name, func(t *testing.T) {
			start := time.Now()
			status, closed := stalledRequest(t, e, rq.head, "abcd", 5*time.Second)
			if status == "" {
				t.Fatalf("no answer within %s (the request was carried out: %v)", time.Since(start).Round(time.Millisecond), rq.done())
			}
			if !strings.Contains(status, rq.want) {
				t.Errorf("answered %q, want%s", status, rq.want)
			}
			if !closed {
				t.Errorf("answered %q but kept the stalled connection open", status)
			}
			if !rq.done() {
				t.Error("the request was not carried out")
			}
		})
	}
}

// The limit is on a body nobody is waiting for. It must not close the
// connection of a request that sent its body, nor reach the request that
// follows on the same connection, however long that one takes.
func TestBodyLimitLeavesOrdinaryRequestsAlone(t *testing.T) {
	shortBodyTimeouts(t)
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)

	// A request that is answered without its body being read, with the body
	// sent in full: the server throws it away and keeps the connection.
	e.writeBobFile("gone.txt", "x")
	if code, reused, body := e.doTraced(http.MethodDelete, "/api/resources/gone.txt", tok, strings.NewReader(strings.Repeat("x", 4096)), nil); code != http.StatusNoContent || !reused {
		t.Fatalf("delete with a body = %d %q, on the connection of the login: %v", code, body, reused)
	}

	// The connection is still good after the limit of that request has
	// passed...
	time.Sleep(2 * leftoverBodyTimeout)
	code, reused, body := e.doTraced(http.MethodPost, "/api/resources/dir/", tok, strings.NewReader("ignored"), nil)
	if code != http.StatusOK || !reused {
		t.Errorf("request after a request with an unread body = %d %q, on the same connection: %v", code, body, reused)
	}

	// ...and an upload that follows on it takes as long as it takes.
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 10; i++ {
				_, _ = pw.Write([]byte("0123456789"))
				time.Sleep(100 * time.Millisecond) // a second in all
			}
			pw.Close()
		}()
		code, reused, body := e.doTraced(method, "/api/resources/slow.bin?override=true", tok, pr, nil)
		if code != http.StatusOK || !reused {
			t.Errorf("slow %s after a request with an unread body = %d %q, on the same connection: %v", method, code, body, reused)
		}
		if data, _ := os.ReadFile(filepath.Join(e.bobDir, "slow.bin")); len(data) != 100 {
			t.Errorf("slow %s stored %d bytes, want 100", method, len(data))
		}
	}

	// A download that follows takes as long as it takes, too.
	e.writeBobFile("big.bin", strings.Repeat("0123456789abcdef", 1<<16))
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/raw/big.bin", http.NoBody)
	req.Header.Set("X-Auth", tok)
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	buf := make([]byte, 64<<10)
	for {
		n, err := res.Body.Read(buf)
		got += n
		if err != nil {
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	res.Body.Close()
	if got != 1<<20 {
		t.Errorf("slow download received %d bytes, want %d", got, 1<<20)
	}
}

// A chunk that is rejected is still being sent. What is left of it is read
// so that the answer reaches the client on a connection that stays usable;
// the limit is on how long nothing arrives, not on how long the rest takes.
func TestRejectedChunkSentSlowlyIsStillAnswered(t *testing.T) {
	shortBodyTimeouts(t)
	e := newFullEnv(t)
	tok := e.login("bob", fullEnvUserPassword)

	code, h, _ := e.do(http.MethodPost, "/api/tus/slow.bin", tok, nil, map[string]string{"Upload-Length": "200"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}

	// The offset is wrong, and the chunk takes a second to arrive: three
	// times the limit.
	pr, pw := io.Pipe()
	go func() {
		// The body ends with its last piece: a client that is answered
		// before it has finished sending does not use the connection again,
		// whatever the server does.
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			_, _ = pw.Write([]byte("0123456789"))
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPatch, e.srv.URL+h.Get("Location"), pr)
	req.ContentLength = 100
	req.Header.Set("X-Auth", tok)
	req.Header.Set("Upload-Offset", "50")
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("the rejected chunk got no answer: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("chunk at the wrong offset = %d, want 409", res.StatusCode)
	}

	code, reused, body := e.doTraced(http.MethodPatch, h.Get("Location"), tok, strings.NewReader(strings.Repeat("x", 200)), map[string]string{
		"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream",
	})
	if code != http.StatusNoContent || !reused {
		t.Errorf("chunk after the rejected one = %d %q, on the same connection: %v", code, body, reused)
	}
}
