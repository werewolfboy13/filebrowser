package fbhttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

func dialCommand(t *testing.T, srv *httptest.Server, token string) (*websocket.Conn, int) {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("X-Auth", token)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/command/", hdr)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err != nil {
		return nil, status
	}
	return conn, status
}

// The command endpoint upgraded every authenticated request to a WebSocket and
// read a whole message into memory before it looked at whether commands are
// enabled and whether the user may run them. Any user, on a server with
// commands disabled, could make it buffer messages of any size
// (GHSA-39cx-23x9-5c8p). The upgrade is now refused up front.
func TestCommandSocketIsRefusedWithoutPermission(t *testing.T) {
	key := []byte("test-signing-key")

	cases := map[string]struct {
		enableExec bool
		perm       users.Permissions
	}{
		"commands disabled, user may not execute": {false, users.Permissions{}},
		"commands disabled, user may execute":     {false, users.Permissions{Execute: true}},
		"commands enabled, user may not execute":  {true, users.Permissions{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := scopedUserStorage(t, t.TempDir(), tc.perm, key)
			srv := httptest.NewServer(handle(commandsHandler, "/api/command", st, &settings.Server{EnableExec: tc.enableExec}))
			defer srv.Close()

			conn, status := dialCommand(t, srv, signToken(t, tc.perm, key))
			if conn != nil {
				conn.Close()
				t.Fatalf("the WebSocket was opened (status %d)", status)
			}
			if status != http.StatusForbidden {
				t.Errorf("handshake answered %d, want 403", status)
			}
		})
	}
}

// A user who may run commands still cannot make the server buffer an
// arbitrarily large message: a command line is short.
func TestCommandSocketLimitsMessageSize(t *testing.T) {
	key := []byte("test-signing-key")
	perm := users.Permissions{Execute: true}
	st := scopedUserStorage(t, t.TempDir(), perm, key)
	srv := httptest.NewServer(handle(commandsHandler, "/api/command", st, &settings.Server{EnableExec: true}))
	defer srv.Close()

	conn, status := dialCommand(t, srv, signToken(t, perm, key))
	if conn == nil {
		t.Fatalf("the WebSocket was refused for a user who may execute (status %d)", status)
	}
	defer conn.Close()

	// Sent in the background: the server stops reading once the limit is
	// exceeded, so the write may not complete.
	go func() {
		_ = conn.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("A"), 4<<20))
	}()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("a 4 MiB message was read in full and answered with %q", msg)
	}
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) && !strings.Contains(err.Error(), "close") && !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "reset") {
		t.Errorf("unexpected error after an oversized message: %v", err)
	}

	// A short command is still read and answered.
	conn2, _ := dialCommand(t, srv, signToken(t, perm, key))
	if conn2 == nil {
		t.Fatal("second connection refused")
	}
	defer conn2.Close()
	if err := conn2.WriteMessage(websocket.TextMessage, []byte("not-an-allowed-command")); err != nil {
		t.Fatal(err)
	}
	_ = conn2.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err = conn2.ReadMessage()
	if err != nil || !bytes.Equal(msg, cmdNotAllowed) {
		t.Errorf("a short command was answered with %q, %v; want %q", msg, err, cmdNotAllowed)
	}
}
