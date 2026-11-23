package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// What a stubbed connection attempt does with an address.
const (
	// dialHangs never connects and never fails: the attempt lasts until it is
	// given up. That is an address whose packets are dropped.
	dialHangs = "hangs"
	// dialRefuses fails at once: nothing listens there.
	dialRefuses = "refuses"
)

// healthDials is what a stubbed connection attempt was asked.
type healthDials struct {
	mu    sync.Mutex
	hosts []string
	// givenUp gets a value for every attempt that hung and was given up.
	givenUp chan struct{}
}

func (d *healthDials) dialled() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.hosts)
}

// stubHealthDial decides, for the rest of the test, what becomes of the
// connections that the health check makes to the addresses of a recorded
// name. An address in behaviour hangs or refuses, as it says there. The
// address server is connected to. Any other address fails the test, and is
// not connected to: nothing here reaches another machine.
func stubHealthDial(t *testing.T, server string, behaviour map[string]string) *healthDials {
	t.Helper()

	dials := &healthDials{givenUp: make(chan struct{}, 16)}
	original := dialHealthAddress
	t.Cleanup(func() { dialHealthAddress = original })
	dialHealthAddress = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		dials.mu.Lock()
		dials.hosts = append(dials.hosts, host)
		dials.mu.Unlock()

		switch {
		case behaviour[host] == dialHangs:
			<-ctx.Done()
			dials.givenUp <- struct{}{}
			return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
		case behaviour[host] == dialRefuses:
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		case host == server:
			return original(ctx, network, address)
		default:
			t.Errorf("the check connected to %s, which is not an address it was given", address)
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ENETUNREACH}
		}
	}

	return dials
}

// The health check connected to the addresses of a recorded name one after
// the other, within the one time limit of the whole check, and took for
// granted that an address without the server refuses the connection at once.
// An address can also not answer at all: where a firewall drops its packets,
// and in the build of the Docker image, where connections to ::1 are not
// refused but time out. Such an address before the server's own used up the
// whole limit, and a server that was up was reported as unhealthy.
func TestHealthIsNotHeldUpByAnAddressThatDoesNotAnswer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	// An address of this machine that the server does not listen on, and one
	// that is another machine's.
	const (
		silent    = "172.17.0.2"
		elsewhere = "192.0.2.1"
	)

	for name, order := range map[string]func(host string) []string{
		"before the address of the server": func(host string) []string { return []string{elsewhere, silent, host} },
		"after the address of the server":  func(host string) []string { return []string{host, elsewhere, silent} },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(healthyHandler("/health"))
			defer srv.Close()
			host, port := hostAndPort(t, srv)

			stubInterfaceAddrs(t, silent+"/16")
			stubHostLookup(t, map[string][]string{"here-and-there.invalid": order(host)})
			dials := stubHealthDial(t, host, map[string]string{silent: dialHangs})

			path := filepath.Join(t.TempDir(), "health.json")
			t.Setenv(envHealthFile, path)
			writeRecordFile(t, path, listenRecord{
				PID:       os.Getpid(),
				StartTime: processStartTime(os.Getpid()),
				Address:   "here-and-there.invalid",
				Port:      port,
			})

			begin := time.Now()
			if err := runHealthTestCmd(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			// The server answers at once. Half of the limit is far more than
			// that takes, and less than waiting for the silent address.
			if elapsed := time.Since(begin); elapsed > healthTimeout/2 {
				t.Errorf("the check took %s of its %s", elapsed, healthTimeout)
			}

			// Only the addresses that were found to be this machine's.
			for _, dialled := range dials.dialled() {
				if dialled != silent && dialled != host {
					t.Errorf("the check connected to %s, want only %s and %s", dialled, silent, host)
				}
			}
			if !slices.Contains(dials.dialled(), host) {
				t.Errorf("the check connected to %q, and not to %s", dials.dialled(), host)
			}

			// With its answer, the check gives up the attempt that hangs.
			if slices.Contains(dials.dialled(), silent) {
				select {
				case <-dials.givenUp:
				case <-time.After(10 * time.Second):
					t.Error("the attempt to connect to the silent address is still waiting")
				}
			}
		})
	}
}

// When no address of the name answers, the check fails, and within its time
// limit: at once when every address refuses, and at the limit when one of
// them hangs.
func TestHealthFailsInTimeWhenNoAddressOfTheNameAnswers(t *testing.T) {
	const (
		first  = "172.17.0.2"
		second = "fd00::2"
	)
	target := healthTarget{url: "http://fbhost:" + closedPort(t) + "/health", addresses: first + " " + second}

	tests := map[string]struct {
		behaviour map[string]string
		timeout   time.Duration
		// atOnce is whether the check fails without waiting for its limit.
		atOnce bool
	}{
		"every address refuses": {
			behaviour: map[string]string{first: dialRefuses, second: dialRefuses},
			timeout:   time.Minute,
			atOnce:    true,
		},
		"every address hangs": {
			behaviour: map[string]string{first: dialHangs, second: dialHangs},
			timeout:   200 * time.Millisecond,
		},
		"the first address hangs and the second refuses": {
			behaviour: map[string]string{first: dialHangs, second: dialRefuses},
			timeout:   200 * time.Millisecond,
		},
		"the first address refuses and the second hangs": {
			behaviour: map[string]string{first: dialRefuses, second: dialHangs},
			timeout:   200 * time.Millisecond,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dials := stubHealthDial(t, "", tt.behaviour)

			begin := time.Now()
			result := make(chan error, 1)
			go func() { result <- checkHealth(context.Background(), target, tt.timeout) }()

			select {
			case err := <-result:
				if err == nil {
					t.Fatal("expected an error: no address answers")
				}
				elapsed := time.Since(begin)
				if tt.atOnce && elapsed > 10*time.Second {
					t.Errorf("failed after %s, want at once: every address refused", elapsed)
				}
				if !tt.atOnce && elapsed < tt.timeout {
					t.Errorf("gave up after %s, before its %s", elapsed, tt.timeout)
				}
				if tt.atOnce && errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("error = %v, want the refusals", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the check is still waiting for an address")
			}

			// Each address was tried, so the failure is that of all of them.
			dialled := dials.dialled()
			slices.Sort(dialled)
			if want := []string{first, second}; !slices.Equal(dialled, want) {
				t.Errorf("the check connected to %q, want %q", dialled, want)
			}

			// No attempt outlasts the check.
			for _, behaviour := range tt.behaviour {
				if behaviour != dialHangs {
					continue
				}
				select {
				case <-dials.givenUp:
				case <-time.After(10 * time.Second):
					t.Fatal("an attempt to connect is still waiting after the check has failed")
				}
			}
		})
	}
}

// A connection that is made after another address has answered is of no use
// to the check, and is closed: the server is not left with a connection that
// nobody speaks on.
func TestHealthClosesAConnectionThatComesTooLate(t *testing.T) {
	srv := httptest.NewServer(healthyHandler("/health"))
	defer srv.Close()
	host, port := hostAndPort(t, srv)

	// A second listener, for the address whose connection comes late.
	late, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()

	const slow = "172.17.0.2"
	answered := make(chan struct{})

	original := dialHealthAddress
	t.Cleanup(func() { dialHealthAddress = original })
	dialHealthAddress = func(ctx context.Context, network, address string) (net.Conn, error) {
		if dialHost, _, _ := net.SplitHostPort(address); dialHost != slow {
			return original(ctx, network, address)
		}
		// The connection is made all the same, once the check has its answer.
		<-answered
		return original(context.Background(), network, late.Addr().String())
	}

	target := healthTarget{url: "http://fbhost:" + port + "/health", addresses: slow + " " + host}
	if err := checkHealth(context.Background(), target, time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(answered)

	closed := make(chan error, 1)
	go func() {
		conn, err := late.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		// Nothing is sent on it, so reading ends when the other side closes.
		_, err = conn.Read(make([]byte, 1))
		closed <- err
	}()

	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Errorf("reading the late connection ended with %v, want it closed", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the late connection is still open")
	}
}

// Without an address there is nothing to connect to, which is an error and
// not a connection.
func TestDialFirstFailsWithoutAnAddress(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("connected, with no address to connect to")
		return nil, syscall.ECONNREFUSED
	}

	conn, err := dialFirst(context.Background(), dial, "tcp", nil)
	if err == nil {
		t.Error("expected an error")
	}
	if conn != nil {
		t.Errorf("got the connection %v, want none", conn)
		conn.Close()
	}
}
