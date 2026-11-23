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

// healthTimeout bounds the request of the check, so that it ends even when
// the server accepts the connection and then says nothing. Before that, the
// command can spend up to hostLookupTimeout on the name of a host.
//
// The two together are shorter than the timeout of the Docker image's
// HEALTHCHECK, so that the command ends by itself and says what went wrong.
// Docker kills a check that runs into its timeout, and then has nothing to
// show for it.
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
answer is not 200 OK with the status "OK" in it. It is meant for health
checks, such as the one of the Docker image, where there is no shell and no
HTTP client to do it with. It gives up after 2 seconds, or after 2.5 when
the server listens on a host name, which is looked up first.

Where the server listens is looked up in this order:

- The flags below, and "--config". When any of them is given, nothing else
  than the flags, the FB_ environment variables and the configuration file
  is used, as the server itself reads them.
- The file that the FB_HEALTH_FILE environment variable names. When the
  variable is set, the server writes there where it listens once it has
  started, and removes the file when it stops. That is where it really
  listens, whether that came from its flags, the environment, the
  configuration file or the database. The Docker image sets the variable.
  The file is only used when it belongs to the user who runs this command
  or to the user the server runs as, and when it names this machine or the
  address in the configuration. When it names this machine by a host name,
  only the addresses of that name that are this machine's are connected to.
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

	// The record is taken at its word for a server on this machine: a socket,
	// or an address that is one of this machine's. For any other address
	// only if the configuration names the same one. The check is made from
	// inside the container, of the server next to it, and a record is not a
	// way to have it sent somewhere else.
	var elsewhere *listenRecord
	if !explicit {
		if record, ok := readListenRecord(os.Getenv(envHealthFile)); ok {
			if record.Socket != "" {
				return healthTargetFor(record.listen()), nil
			}
			if ofName, local := localAddresses(record.Address); local {
				target := healthTargetFor(record.listen())
				target.addresses = strings.Join(ofName, " ")
				return target, nil
			}
			elsewhere = record
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

	if elsewhere != nil && elsewhere.Address == v.GetString("address") {
		return healthTargetFor(elsewhere.listen()), nil
	}

	return healthTargetFrom(v), nil
}

// healthTarget says how to reach the server's health endpoint.
type healthTarget struct {
	// socket is the unix socket to connect to. When it is empty, the host in
	// url is connected to over TCP.
	socket string
	url    string
	// addresses are the IP addresses to connect to in place of the host in
	// url, separated by spaces, or empty to connect to that host.
	//
	// It is set for a host name that comes from the server's record. Such a
	// name was looked up to see that it is this machine's, and it can stand
	// for another machine as well: connecting to the name would look it up
	// again and could reach that one. So the check connects to the addresses
	// that were found to be this machine's, and to no other.
	//
	// It is one string, and not a list, so that two targets can be compared.
	addresses string
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

// dialHealthAddress connects to one of the addresses of a recorded host name
// (healthTarget.addresses). It is a variable so that the tests can have an
// address that never answers, which no machine they run on is sure to have.
var dialHealthAddress = func(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// dialFirst connects to all of addresses at once, and returns the first
// connection that is made. It fails when no address takes one, with the
// errors of all of them, or when ctx ends before that.
//
// The server listens on one of the addresses. The others mostly refuse the
// connection at once, but one can also not answer at all: an address whose
// packets a firewall drops, or ::1 where IPv6 is set up and does not work.
// Tried one after the other, such an address used up the time of the whole
// check, and a server that was up on the next address was reported as
// unhealthy. So no address waits for another one.
func dialFirst(
	ctx context.Context,
	dial func(ctx context.Context, network, address string) (net.Conn, error),
	network string,
	addresses []string,
) (net.Conn, error) {
	// Not a connection that is nil without an error.
	if len(addresses) == 0 {
		return nil, errors.New("no address to connect to")
	}

	type attempt struct {
		index int
		conn  net.Conn
		err   error
	}

	// With room for every attempt, so that none is kept from ending by a
	// result that is not waited for.
	attempts := make(chan attempt, len(addresses))
	ctx, cancel := context.WithCancel(ctx)
	for i, address := range addresses {
		go func() {
			conn, err := dial(ctx, network, address)
			attempts <- attempt{index: i, conn: conn, err: err}
		}()
	}

	// When this returns, the attempts that are still under way are given up,
	// and a connection that one of them makes all the same is closed.
	waiting := len(addresses)
	defer func() {
		cancel()
		go func() {
			for ; waiting > 0; waiting-- {
				if late := <-attempts; late.conn != nil {
					late.conn.Close()
				}
			}
		}()
	}()

	errs := make([]error, len(addresses))
	for waiting > 0 {
		select {
		case result := <-attempts:
			waiting--
			if result.err == nil {
				return result.conn, nil
			}
			errs[result.index] = result.err
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}

	// In the order of the addresses, whichever failed first.
	return nil, errors.Join(errs...)
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
	switch {
	case target.socket != "":
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", target.socket)
		}
	case target.addresses != "":
		// The check's own context, and not the one the transport hands over,
		// which does not end with the request: the attempts must not outlast
		// the check.
		dial := dialHealthAddress
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses := strings.Fields(target.addresses)
			for i, ip := range addresses {
				addresses[i] = net.JoinHostPort(ip, port)
			}
			return dialFirst(ctx, dial, network, addresses)
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
