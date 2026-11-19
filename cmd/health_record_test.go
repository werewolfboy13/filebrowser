package cmd

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/settings"
)

// closedPort returns a loopback port that nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(healthyHandler("/health"))
	_, port := hostAndPort(t, srv)
	srv.Close()

	return port
}

// recordFor writes the record of a server that listens on listener with the
// given settings, to a file that FB_HEALTH_FILE names, as the server does
// when it starts. It returns the file and the function that removes it.
func recordFor(t *testing.T, server *settings.Server, listener net.Listener) (path string, remove func()) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "health.json")
	t.Setenv(envHealthFile, path)
	remove = recordListenAddress(server, listener)
	assertFileExists(t, path)

	return path, remove
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func readRecordFile(t *testing.T, path string) listenRecord {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record := listenRecord{}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("%s is not a record: %v", path, err)
	}

	return record
}

func writeRecordFile(t *testing.T, path string, record listenRecord) {
	t.Helper()

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(data))
}

// otherRunningProcess returns the record of a server in another process
// that is running: the process that started the tests. It skips the test
// where the start time of a process cannot be read, because nothing tells a
// process that runs from one that is gone there.
func otherRunningProcess(t *testing.T) listenRecord {
	t.Helper()

	parent := os.Getppid()
	startTime := processStartTime(parent)
	if startTime == "" {
		t.Skip("the start time of the parent process cannot be read on this system")
	}

	return listenRecord{PID: parent, StartTime: startTime, Address: "127.0.0.1", Port: "1"}
}

// The Docker image's health check runs `filebrowser health` with the
// container's environment, but without the flags the server was started
// with. A server started with `--port` or `--config` was looked for on the
// port of the environment, the image's configuration file or the defaults,
// and reported as unhealthy for as long as it ran.
func TestHealthFindsAServerStartedWithFlags(t *testing.T) {
	// Not the machine's own configuration.
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(healthyHandler("/files/health"))
	defer srv.Close()
	host, port := hostAndPort(t, srv)

	// What the health check can see points somewhere else: the environment
	// and the image's configuration file.
	configPath := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, configPath, `{"port": `+closedPort(t)+`, "address": "", "baseURL": "/from-file"}`)
	t.Setenv(envConfig, configPath)
	t.Setenv("FB_PORT", closedPort(t))
	t.Setenv("FB_BASE_URL", "/from-env")

	t.Run("fails without the server's record", func(t *testing.T) {
		t.Setenv(envHealthFile, "")

		if err := runHealthTestCmd(); err == nil {
			t.Fatal("expected an error: nothing says where the server listens")
		}
	})

	t.Run("succeeds with it", func(t *testing.T) {
		// As if started with --address, --port and --baseURL.
		recordFor(t, &settings.Server{Address: host, Port: port, BaseURL: "/files"}, srv.Listener)

		if err := runHealthTestCmd(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("its own flags come first", func(t *testing.T) {
		recordFor(t, &settings.Server{Address: host, Port: port, BaseURL: "/files"}, srv.Listener)

		if err := runHealthTestCmd("--port", closedPort(t)); err == nil {
			t.Error("expected an error: --port names a port that nothing listens on")
		}
		if err := runHealthTestCmd("--address", host, "--port", port, "--baseURL", "/files"); err != nil {
			t.Errorf("unexpected error with the server's address as flags: %v", err)
		}
	})

	t.Run("--config comes first", func(t *testing.T) {
		recordFor(t, &settings.Server{Address: host, Port: port, BaseURL: "/files"}, srv.Listener)

		// The environment still names the closed port.
		if err := runHealthTestCmd("--config", configPath); err == nil {
			t.Error("expected an error: with --config, the record is not used")
		}
	})

	t.Run("fails again once the server has removed it", func(t *testing.T) {
		path, remove := recordFor(t, &settings.Server{Address: host, Port: port, BaseURL: "/files"}, srv.Listener)

		remove()

		assertNoFile(t, path)
		if err := runHealthTestCmd(); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestHealthFailsWhenTheRecordedServerIsGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	srv := httptest.NewServer(healthyHandler("/health"))
	host, port := hostAndPort(t, srv)
	recordFor(t, &settings.Server{Address: host, Port: port}, srv.Listener)
	srv.Close()

	if err := runHealthTestCmd(); err == nil {
		t.Error("expected an error: the server no longer listens")
	}
}

// A server that is killed cannot remove its record. The next one may listen
// somewhere else and be unable to write its own, and must not be looked for
// where the dead one was.
func TestHealthIgnoresTheRecordOfAnotherProcess(t *testing.T) {
	if processStartTime(os.Getpid()) == "" {
		t.Skip("the start time of a process cannot be read on this system")
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	srv := httptest.NewServer(healthyHandler("/health"))
	defer srv.Close()
	host, port := hostAndPort(t, srv)
	t.Setenv("FB_ADDRESS", host)
	t.Setenv("FB_PORT", port)

	stale := listenRecord{
		PID:       os.Getpid(),
		StartTime: processStartTime(os.Getpid()),
		Address:   "127.0.0.1",
		Port:      closedPort(t),
	}

	t.Run("a record of this process is used", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		writeRecordFile(t, path, stale)

		if err := runHealthTestCmd(); err == nil {
			t.Error("expected an error: the record names a port that nothing listens on")
		}
	})

	t.Run("a record of a process that started at another time is not", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		other := stale
		other.StartTime = "1"
		writeRecordFile(t, path, other)

		if err := runHealthTestCmd(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("a record of a process that no longer runs is not", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		other := stale
		// Above the highest process number that Linux gives out.
		other.PID = 1 << 30
		writeRecordFile(t, path, other)

		if err := runHealthTestCmd(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestHealthFallsBackWithoutAUsableRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	srv := httptest.NewServer(healthyHandler("/health"))
	defer srv.Close()
	host, port := hostAndPort(t, srv)
	t.Setenv("FB_ADDRESS", host)
	t.Setenv("FB_PORT", port)

	tests := map[string]string{
		"no file":              "",
		"not a record":         `{"port": `,
		"an empty file":        ``,
		"a record of nothing":  `{"pid": 1, "address": "127.0.0.1"}`,
		"something else there": `["127.0.0.1", 80]`,
	}

	// What was planted next to a server that could not replace it: the
	// first process of a container, and a port that nothing listens on.
	if processStartTime(os.Getpid()) != "" {
		tests["a record that does not say when its process started"] = `{"pid": 1, "address": "127.0.0.1", "port": "9"}`
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "health.json")
			t.Setenv(envHealthFile, path)
			if name != "no file" {
				writeTestFile(t, path, content)
			}

			if err := runHealthTestCmd(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRecordListenAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	actualPort := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	t.Run("does nothing when no file is named", func(t *testing.T) {
		t.Setenv(envHealthFile, "")

		remove := recordListenAddress(&settings.Server{Port: "80"}, listener)
		remove()
	})

	t.Run("records the port the server got, not the one it asked for", func(t *testing.T) {
		path, _ := recordFor(t, &settings.Server{Address: "", Port: "0", BaseURL: "/files"}, listener)

		got := readRecordFile(t, path)
		want := listenRecord{
			PID:       os.Getpid(),
			StartTime: processStartTime(os.Getpid()),
			Address:   "",
			Port:      actualPort,
			BaseURL:   "/files",
		}
		if got != want {
			t.Errorf("record = %+v, want %+v", got, want)
		}
	})

	t.Run("records TLS only with both a certificate and a key", func(t *testing.T) {
		path, _ := recordFor(t, &settings.Server{Port: "0", TLSCert: "cert.pem", TLSKey: "key.pem"}, listener)
		if !readRecordFile(t, path).TLS {
			t.Error("TLS is not recorded")
		}

		path, _ = recordFor(t, &settings.Server{Port: "0", TLSCert: "cert.pem"}, listener)
		if readRecordFile(t, path).TLS {
			t.Error("TLS is recorded for a server without a key")
		}
	})

	t.Run("records a socket instead of an address", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "fb.sock")
		unixListener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer unixListener.Close()

		// The database can still hold an address and a certificate from
		// before, which the server does not use next to a socket.
		server := &settings.Server{Socket: socket, Address: "0.0.0.0", Port: "80", TLSCert: "c", TLSKey: "k"}
		path, _ := recordFor(t, server, unixListener)

		got := readRecordFile(t, path)
		if got.Socket != socket || got.Address != "" || got.Port != "" || got.TLS {
			t.Errorf("record = %+v, want only the socket", got)
		}
	})

	t.Run("is readable by other users and leaves nothing else behind", func(t *testing.T) {
		path, _ := recordFor(t, &settings.Server{Port: "0"}, listener)

		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != listenRecordPermissions {
			t.Errorf("mode = %o, want %o", got, listenRecordPermissions)
		}

		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("%d files next to the record, want none", len(entries)-1)
		}
	})

	t.Run("replaces the record of an earlier server", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		writeRecordFile(t, path, listenRecord{PID: 1, Address: "192.0.2.1", Port: "1"})

		recordListenAddress(&settings.Server{Address: "127.0.0.1", Port: "0"}, listener)

		if got := readRecordFile(t, path); got.Address != "127.0.0.1" || got.Port != actualPort {
			t.Errorf("record = %+v, want this server's", got)
		}
	})

	t.Run("removes its record when the server stops", func(t *testing.T) {
		path, remove := recordFor(t, &settings.Server{Port: "0"}, listener)

		remove()

		assertNoFile(t, path)
	})

	// A second server in the same container, started with `docker exec`,
	// took over the record of the container's own server.
	t.Run("leaves the record of another running server as it is", func(t *testing.T) {
		other := otherRunningProcess(t)
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		writeRecordFile(t, path, other)

		remove := recordListenAddress(&settings.Server{Address: "127.0.0.1", Port: "0"}, listener)
		if got := readRecordFile(t, path); got != other {
			t.Errorf("record = %+v, want the other server's: %+v", got, other)
		}

		// And when it stops, it removes nothing that is not its own.
		remove()
		if got := readRecordFile(t, path); got != other {
			t.Errorf("record after the stop = %+v, want the other server's: %+v", got, other)
		}
	})

	t.Run("replaces the record of a server that no longer runs", func(t *testing.T) {
		other := otherRunningProcess(t)
		other.StartTime = "1"
		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		writeRecordFile(t, path, other)

		recordListenAddress(&settings.Server{Address: "127.0.0.1", Port: "0"}, listener)

		if got := readRecordFile(t, path); got.PID != os.Getpid() || got.Port != actualPort {
			t.Errorf("record = %+v, want this server's", got)
		}
	})

	// Where the start time of a process cannot be read, nothing tells a
	// server that runs from one that was killed. A record that is left there
	// is replaced, or the record of a killed server would stay for good, and
	// with it the place where the health command looks.
	t.Run("replaces the record of another process where no start time can be read", func(t *testing.T) {
		readable := processStartTime
		processStartTime = func(int) string { return "" }
		t.Cleanup(func() { processStartTime = readable })

		path := filepath.Join(t.TempDir(), "health.json")
		t.Setenv(envHealthFile, path)
		other := listenRecord{PID: os.Getppid(), Address: "127.0.0.1", Port: "1"}
		writeRecordFile(t, path, other)
		// The health command goes by this record, as it has nothing better:
		// it is not one that is passed over anyway.
		if _, ok := readListenRecord(path); !ok {
			t.Fatal("the record of another process is not read where no start time can be read")
		}

		recordListenAddress(&settings.Server{Address: "127.0.0.1", Port: "0"}, listener)

		if got := readRecordFile(t, path); got.PID != os.Getpid() || got.Port != actualPort {
			t.Errorf("record = %+v, want this server's", got)
		}
	})

	// Two containers that share the file: the server is process 1 in each
	// of them, and only the start time tells the two apart.
	t.Run("does not remove the record of another process with the same number", func(t *testing.T) {
		path, remove := recordFor(t, &settings.Server{Port: "0"}, listener)

		other := readRecordFile(t, path)
		if other.PID != os.Getpid() {
			t.Fatalf("the record names process %d, want this one", other.PID)
		}
		other.StartTime += "1"
		writeRecordFile(t, path, other)

		remove()

		if got := readRecordFile(t, path); got != other {
			t.Errorf("record after the stop = %+v, want the other server's: %+v", got, other)
		}
	})

	// The record is another server's by the time this one stops: this one
	// was killed and started again, or the file was replaced by hand.
	t.Run("does not remove a record that is no longer its own", func(t *testing.T) {
		path, remove := recordFor(t, &settings.Server{Port: "0"}, listener)

		other := readRecordFile(t, path)
		other.PID = os.Getppid()
		other.StartTime = processStartTime(other.PID)
		writeRecordFile(t, path, other)

		remove()

		if got := readRecordFile(t, path); got != other {
			t.Errorf("record after the stop = %+v, want the other server's: %+v", got, other)
		}
	})

	t.Run("does not write through a link that is in the way", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim")
		writeTestFile(t, victim, "untouched")
		path := filepath.Join(dir, "health.json")
		if err := os.Symlink(victim, path); err != nil {
			t.Skip("symbolic links are not available")
		}
		t.Setenv(envHealthFile, path)

		recordListenAddress(&settings.Server{Port: "0"}, listener)

		if data, err := os.ReadFile(victim); err != nil || string(data) != "untouched" {
			t.Errorf("the file the link points to holds %q (%v)", data, err)
		}
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Errorf("the record is not a regular file (%v)", err)
		}
	})

	t.Run("goes on when the file cannot be written", func(t *testing.T) {
		// A file where the directory should be, which no user can write into.
		notADir := filepath.Join(t.TempDir(), "file")
		writeTestFile(t, notADir, "")
		path := filepath.Join(notADir, "health.json")
		t.Setenv(envHealthFile, path)

		remove := recordListenAddress(&settings.Server{Port: "0"}, listener)
		remove()

		if _, ok := readListenRecord(path); ok {
			t.Error("a record was read from a file that cannot exist")
		}
	})
}

func TestHealthTargetForARecord(t *testing.T) {
	tests := map[string]struct {
		record listenRecord
		want   healthTarget
	}{
		"every address, as in the Docker image": {
			record: listenRecord{Address: "", Port: "80"},
			want:   healthTarget{url: "http://localhost:80/health"},
		},
		"every IPv4 address": {
			record: listenRecord{Address: "0.0.0.0", Port: "8080"},
			want:   healthTarget{url: "http://127.0.0.1:8080/health"},
		},
		"every IPv6 address": {
			record: listenRecord{Address: "[::]", Port: "8080"},
			want:   healthTarget{url: "http://[::1]:8080/health"},
		},
		"TLS and a base URL": {
			record: listenRecord{Address: "127.0.0.1", Port: "8443", TLS: true, BaseURL: "/files/"},
			want:   healthTarget{url: "https://127.0.0.1:8443/files/health"},
		},
		"a socket": {
			record: listenRecord{Socket: "/run/fb.sock", BaseURL: "/files"},
			want:   healthTarget{socket: "/run/fb.sock", url: "http://localhost/files/health"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := healthTargetFor(tt.record.listen()); got != tt.want {
				t.Errorf("target = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseProcStatStartTime(t *testing.T) {
	tests := map[string]struct {
		stat    string
		want    string
		wantErr bool
	}{
		"a plain name": {
			stat: "1 (filebrowser) S 0 1 1 0 -1 4194560 1 2 3 4 5 6 7 8 20 0 9 0 12345 100 200\n",
			want: "12345",
		},
		"a name with spaces and parentheses": {
			stat: "42 (a b) c (d) S 0 1 1 0 -1 4194560 1 2 3 4 5 6 7 8 20 0 9 0 67890 100 200\n",
			want: "67890",
		},
		"too short":    {stat: "1 (filebrowser) S 0 1", wantErr: true},
		"no name":      {stat: "1 filebrowser S 0 1", wantErr: true},
		"not a number": {stat: "1 (x) S 0 1 1 0 -1 4194560 1 2 3 4 5 6 7 8 20 0 9 0 soon 100", wantErr: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseProcStatStartTime([]byte(tt.stat))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want an error: %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("start time = %q, want %q", got, tt.want)
			}
		})
	}
}
