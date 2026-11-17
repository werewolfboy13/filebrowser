package cmd

import (
	"bufio"
	"net"
	"net/http"
	"testing"
	"time"
)

// The server only set a timeout for reading request headers, so an idle
// keep-alive connection was kept for ever. It must give them up after a
// while, without limiting how long a request or a response may take: uploads
// and downloads have no upper bound.
func TestServerTimeouts(t *testing.T) {
	srv := newServer(http.NotFoundHandler())

	if srv.ReadHeaderTimeout <= 0 {
		t.Error("no ReadHeaderTimeout")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("no IdleTimeout: idle connections are kept for ever")
	}
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %s: it would cut off uploads", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %s: it would cut off downloads", srv.WriteTimeout)
	}
}

func TestServerClosesIdleConnections(t *testing.T) {
	srv := newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	if srv.IdleTimeout <= 0 {
		t.Fatal("no IdleTimeout")
	}
	srv.IdleTimeout = 200 * time.Millisecond

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := reader.Read(buf); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	// Nothing more is sent: the server has to hang up.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("unexpected data on an idle connection")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Error("the idle connection was still open after 5s")
	}
}
