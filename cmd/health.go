package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// healthTimeout bounds the whole check, so that it ends even when the server
// accepts the connection and then says nothing.
//
// It is shorter than the timeout of the Docker image's HEALTHCHECK, so that
// the command ends by itself and says what went wrong. Docker kills a check
// that runs into its timeout, and then has nothing to show for it.
const healthTimeout = 2 * time.Second

const (
	// healthAnswerStatus is the status in the answer of a healthy server:
	// {"status":"OK"} (healthHandler in the http package).
	healthAnswerStatus = "OK"
	// healthAnswerMaxSize is how much of an answer is read.
	healthAnswerMaxSize = 1024
)

func init() {
	rootCmd.AddCommand(healthCmd)
	addListenFlags(healthCmd.Flags())
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check that a running File Browser answers",
	Long: `Requests /health from a running File Browser and fails when the
answer is not 200 OK with the status "OK" in it. It is meant for health checks, such as the one of the
Docker image, where there is no shell and no HTTP client to do it with.

Where the server listens is looked up in this order:

- The flags below, and "--config". When any of them is given, nothing else
  than the flags, the FB_ environment variables and the configuration file
  is used, as the server itself reads them.
- The file that the FB_HEALTH_FILE environment variable names. When the
  variable is set, the server writes there where it listens once it has
  started, and removes the file when it stops. That is where it really
  listens, whether that came from its flags, the environment, the
  configuration file or the database. The Docker image sets the variable.
- The FB_ environment variables and the configuration file.

The database is not read, because the running server holds it locked. So
without FB_HEALTH_FILE, a value that only the database has, or that was given
to the server as a flag, has to be given to this command as a flag too.`,
	Args: cobra.NoArgs,
	RunE: runHealth,
}

func runHealth(cmd *cobra.Command, _ []string) error {
	target, err := resolveHealthTarget(cmd)
	if err != nil {
		return err
	}

	return checkHealth(cmd.Context(), target, healthTimeout)
}

// healthFlags are the flags that say where to look for the server.
var healthFlags = []string{"config", "address", "port", "cert", "key", "socket", "baseURL"}

// resolveHealthTarget works out where the server listens: from the command's
// own flags when it has any, or else from what the running server wrote down
// (see listenRecord), or else from the environment and the configuration
// file.
//
// The record comes before the environment, although the server reads the
// environment too. In a container, the health check gets the same environment
// as the server, but not the server's flags, which override it. A check that
// trusted FB_PORT would miss a server started with --port.
func resolveHealthTarget(cmd *cobra.Command) (healthTarget, error) {
	explicit := false
	for _, name := range healthFlags {
		if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
			explicit = true
			break
		}
	}

	if !explicit {
		if record, ok := readListenRecord(os.Getenv(envHealthFile)); ok {
			return healthTargetFor(record.listen()), nil
		}
	}

	v, err := newViper(cmd)
	if err != nil {
		return healthTarget{}, err
	}

	// With no configuration file, the flags and the environment decide.
	if err := v.ReadInConfig(); err != nil && errors.As(err, &viper.ConfigParseError{}) {
		return healthTarget{}, err
	}

	return healthTargetFrom(v), nil
}

// healthTarget says how to reach the server's health endpoint.
type healthTarget struct {
	// socket is the unix socket to connect to. When it is empty, the host in
	// url is connected to over TCP.
	socket string
	url    string
}

// listenSettings is where a server listens, as it is configured.
type listenSettings struct {
	address string
	port    string
	socket  string
	tls     bool
	baseURL string
}

func healthTargetFrom(v *viper.Viper) healthTarget {
	return healthTargetFor(listenSettings{
		address: v.GetString("address"),
		port:    v.GetString("port"),
		socket:  v.GetString("socket"),
		tls:     v.GetString("cert") != "" && v.GetString("key") != "",
		baseURL: v.GetString("baseURL"),
	})
}

func healthTargetFor(listen listenSettings) healthTarget {
	// The server takes the base URL without its trailing slash
	// (settings.Server.Clean) and strips it from the request path.
	path := strings.TrimSuffix(listen.baseURL, "/") + "/health"
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	if listen.socket != "" {
		// The host is only a placeholder: the connection goes to the socket.
		return healthTarget{socket: listen.socket, url: "http://localhost" + path}
	}

	scheme := "http"
	if listen.tls {
		scheme = "https"
	}

	// The server joins address and port itself, so an IPv6 address is
	// configured in brackets.
	host := strings.TrimSuffix(strings.TrimPrefix(listen.address, "["), "]")
	switch host {
	case "":
		// The server listens on every address.
		host = "localhost"
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}

	return healthTarget{url: scheme + "://" + net.JoinHostPort(host, listen.port) + path}
}

func checkHealth(ctx context.Context, target healthTarget, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		DisableKeepAlives: true,
		// The check talks to its own server on the loopback address, where
		// the certificate, issued for the public name, cannot match.
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec
		},
	}
	if target.socket != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", target.socket)
		}
	}

	client := &http.Client{
		Transport: transport,
		// A redirect means that the request did not reach the health endpoint.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url, http.NoBody)
	if err != nil {
		return err
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", target.url, res.Status)
	}

	// The server answers 200 with the web client's page for a path it does
	// not know, so the status alone does not say that this is the health
	// endpoint. Its answer is short: more than this is something else.
	answer := struct {
		Status string `json:"status"`
	}{}
	body, err := io.ReadAll(io.LimitReader(res.Body, healthAnswerMaxSize))
	if err != nil {
		return fmt.Errorf("%s answered %s, and reading the answer failed: %w", target.url, res.Status, err)
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Status != healthAnswerStatus {
		return fmt.Errorf("%s answered %s, but not with the status %q of File Browser's health endpoint",
			target.url, res.Status, healthAnswerStatus)
	}

	return nil
}
