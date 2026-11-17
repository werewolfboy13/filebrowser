package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// healthTimeout bounds the whole check, so that it ends even when the server
// accepts the connection and then says nothing.
const healthTimeout = 5 * time.Second

func init() {
	rootCmd.AddCommand(healthCmd)
	addListenFlags(healthCmd.Flags())
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check that a running File Browser answers",
	Long: `Requests /health from a running File Browser and fails when the
answer is not 200 OK. It is meant for health checks, such as the one of the
Docker image, where there is no shell and no HTTP client to do it with.

Where the server listens is read the same way the server reads it: from the
flags below, then the FB_ environment variables, then the configuration file.
The database is not read, because the running server holds it locked. So a
value that only the database has, or that was given to the server as a flag,
has to be given to this command as a flag too.`,
	Args: cobra.NoArgs,
	RunE: runHealth,
}

func runHealth(cmd *cobra.Command, _ []string) error {
	v, err := newViper(cmd)
	if err != nil {
		return err
	}

	// With no configuration file, the flags and the environment decide.
	if err := v.ReadInConfig(); err != nil && errors.As(err, &viper.ConfigParseError{}) {
		return err
	}

	return checkHealth(cmd.Context(), healthTargetFrom(v))
}

// healthTarget says how to reach the server's health endpoint.
type healthTarget struct {
	// socket is the unix socket to connect to. When it is empty, the host in
	// url is connected to over TCP.
	socket string
	url    string
}

func healthTargetFrom(v *viper.Viper) healthTarget {
	// The server takes the base URL without its trailing slash
	// (settings.Server.Clean) and strips it from the request path.
	path := strings.TrimSuffix(v.GetString("baseURL"), "/") + "/health"
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	if socket := v.GetString("socket"); socket != "" {
		// The host is only a placeholder: the connection goes to the socket.
		return healthTarget{socket: socket, url: "http://localhost" + path}
	}

	scheme := "http"
	if v.GetString("cert") != "" && v.GetString("key") != "" {
		scheme = "https"
	}

	// The server joins address and port itself, so an IPv6 address is
	// configured in brackets.
	host := strings.TrimSuffix(strings.TrimPrefix(v.GetString("address"), "["), "]")
	switch host {
	case "":
		// The server listens on every address.
		host = "localhost"
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}

	return healthTarget{url: scheme + "://" + net.JoinHostPort(host, v.GetString("port")) + path}
}

func checkHealth(ctx context.Context, target healthTarget) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
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
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", target.url, res.Status)
	}

	return nil
}
