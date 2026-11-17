package cmd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// newHealthTestCmd returns a command that runs the health check with its own
// flags, so no flag state is shared through the package's commands.
func newHealthTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "health",
		Args:          cobra.NoArgs,
		RunE:          runHealth,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringP("config", "c", "", "config file path")
	addListenFlags(cmd.Flags())

	return cmd
}

func runHealthTestCmd(args ...string) error {
	cmd := newHealthTestCmd()
	cmd.SetArgs(args)

	return cmd.ExecuteContext(context.Background())
}

func healthTargetForArgs(t *testing.T, args ...string) healthTarget {
	t.Helper()

	cmd := newHealthTestCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	v, err := newViper(cmd)
	if err != nil {
		t.Fatal(err)
	}
	// As in the command, a missing configuration file is fine.
	_ = v.ReadInConfig()

	return healthTargetFrom(v)
}

// healthyHandler answers like the server's health endpoint, at path.
func healthyHandler(path string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	})

	return mux
}

func hostAndPort(t *testing.T, srv *httptest.Server) (host, port string) {
	t.Helper()

	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	return host, port
}

func TestHealthTargetFrom(t *testing.T) {
	// Not the machine's own configuration.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	tests := map[string]struct {
		args []string
		want healthTarget
	}{
		"the server's defaults": {
			args: nil,
			want: healthTarget{url: "http://127.0.0.1:8080/health"},
		},
		"every address": {
			args: []string{"--address", "", "--port", "80"},
			want: healthTarget{url: "http://localhost:80/health"},
		},
		"every IPv4 address": {
			args: []string{"--address", "0.0.0.0", "--port", "80"},
			want: healthTarget{url: "http://127.0.0.1:80/health"},
		},
		"every IPv6 address": {
			args: []string{"--address", "[::]", "--port", "80"},
			want: healthTarget{url: "http://[::1]:80/health"},
		},
		"an IPv6 address": {
			args: []string{"--address", "[fd00::1]", "--port", "80"},
			want: healthTarget{url: "http://[fd00::1]:80/health"},
		},
		"a host name": {
			args: []string{"--address", "files.internal", "--port", "8443"},
			want: healthTarget{url: "http://files.internal:8443/health"},
		},
		"a base URL": {
			args: []string{"--baseURL", "/files"},
			want: healthTarget{url: "http://127.0.0.1:8080/files/health"},
		},
		"a base URL with a trailing slash": {
			args: []string{"--baseURL", "/files/"},
			want: healthTarget{url: "http://127.0.0.1:8080/files/health"},
		},
		"the root as base URL": {
			args: []string{"--baseURL", "/"},
			want: healthTarget{url: "http://127.0.0.1:8080/health"},
		},
		"TLS": {
			args: []string{"--cert", "cert.pem", "--key", "key.pem"},
			want: healthTarget{url: "https://127.0.0.1:8080/health"},
		},
		"a certificate without a key is not TLS": {
			args: []string{"--cert", "cert.pem"},
			want: healthTarget{url: "http://127.0.0.1:8080/health"},
		},
		"a socket": {
			args: []string{"--socket", "/run/fb.sock", "--baseURL", "/files"},
			want: healthTarget{socket: "/run/fb.sock", url: "http://localhost/files/health"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := healthTargetForArgs(t, tt.args...); got != tt.want {
				t.Errorf("target = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestHealthTargetPrecedence ensures the health check finds the server where
// the server itself listens: flags, then environment, then config file.
func TestHealthTargetPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.json")
	writeTestFile(t, configPath, `{"port": 1111, "address": "", "baseURL": "/from-file"}`)
	t.Setenv(envConfig, configPath)

	t.Run("config file", func(t *testing.T) {
		want := healthTarget{url: "http://localhost:1111/from-file/health"}
		if got := healthTargetForArgs(t); got != want {
			t.Errorf("target = %+v, want %+v", got, want)
		}
	})

	t.Run("environment over config file", func(t *testing.T) {
		t.Setenv("FB_PORT", "2222")
		t.Setenv("FB_ADDRESS", "0.0.0.0")
		t.Setenv("FB_BASE_URL", "/from-env")

		want := healthTarget{url: "http://127.0.0.1:2222/from-env/health"}
		if got := healthTargetForArgs(t); got != want {
			t.Errorf("target = %+v, want %+v", got, want)
		}
	})

	t.Run("flags over environment", func(t *testing.T) {
		t.Setenv("FB_PORT", "2222")
		t.Setenv("FB_ADDRESS", "0.0.0.0")
		t.Setenv("FB_BASE_URL", "/from-env")

		want := healthTarget{url: "http://192.0.2.1:3333/from-flag/health"}
		got := healthTargetForArgs(t, "--port", "3333", "--address", "192.0.2.1", "--baseURL", "/from-flag")
		if got != want {
			t.Errorf("target = %+v, want %+v", got, want)
		}
	})

	t.Run("--config over FB_CONFIG", func(t *testing.T) {
		flagPath := filepath.Join(t.TempDir(), "flag.json")
		writeTestFile(t, flagPath, `{"port": 4444}`)

		want := healthTarget{url: "http://127.0.0.1:4444/health"}
		if got := healthTargetForArgs(t, "--config", flagPath); got != want {
			t.Errorf("target = %+v, want %+v", got, want)
		}
	})
}

func TestHealthCommand(t *testing.T) {
	// Not the machine's own configuration, nor a server that runs on it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")
	t.Setenv(envHealthFile, "")

	t.Run("succeeds against a healthy server", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		if err := runHealthTestCmd("--address", host, "--port", port); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("finds the server through the config file", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		defer srv.Close()
		_, port := hostAndPort(t, srv)

		// As in the Docker image's default settings, the address is empty.
		configPath := filepath.Join(t.TempDir(), "settings.json")
		writeTestFile(t, configPath, `{"port": `+port+`, "address": ""}`)
		t.Setenv(envConfig, configPath)

		if err := runHealthTestCmd(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("finds the server through the environment", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)
		t.Setenv("FB_ADDRESS", host)
		t.Setenv("FB_PORT", port)

		if err := runHealthTestCmd(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("fails when nothing listens", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		host, port := hostAndPort(t, srv)
		srv.Close()

		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("fails when the answer is not 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("fails on a redirect, even to a page that answers 200", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
		})
		mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("requests the health endpoint under the base URL", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/files/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		if err := runHealthTestCmd("--address", host, "--port", port, "--baseURL", "/files"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error without the base URL")
		}
	})

	t.Run("speaks TLS when the server has a certificate and a key", func(t *testing.T) {
		srv := httptest.NewTLSServer(healthyHandler("/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		// The files are not read: the server's certificate is not checked.
		err := runHealthTestCmd("--address", host, "--port", port, "--cert", "cert.pem", "--key", "key.pem")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error without TLS")
		}
	})

	t.Run("connects to a unix socket", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "fb.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(healthyHandler("/health"))
		srv.Listener = listener
		srv.Start()
		defer srv.Close()

		if err := runHealthTestCmd("--socket", socket); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if err := runHealthTestCmd("--socket", socket+".missing"); err == nil {
			t.Error("expected an error for a missing socket")
		}
	})

	t.Run("fails on a config file that cannot be parsed", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		configPath := filepath.Join(t.TempDir(), "settings.json")
		writeTestFile(t, configPath, `{"port": `)
		t.Setenv(envConfig, configPath)

		if err := runHealthTestCmd("--address", host, "--port", port); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("does not create the config file", func(t *testing.T) {
		srv := httptest.NewServer(healthyHandler("/health"))
		defer srv.Close()
		host, port := hostAndPort(t, srv)

		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, `{"port": 80}`)
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		if err := runHealthTestCmd("--address", host, "--port", port); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		assertNoFile(t, configPath)
	})
}

// TestHealthCommandIsRegistered ensures the Docker image's health check,
// "filebrowser health", exists with the server's listen flags and defaults.
func TestHealthCommandIsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"health"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != healthCmd {
		t.Fatalf("health resolved to %q", cmd.Name())
	}

	for _, name := range []string{"address", "port", "cert", "key", "socket", "baseURL"} {
		flag := healthCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("health has no --%s flag", name)
			continue
		}
		if want := rootCmd.Flags().Lookup(name).DefValue; flag.DefValue != want {
			t.Errorf("--%s default = %q, want the server's %q", name, flag.DefValue, want)
		}
	}
}
