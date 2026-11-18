package cmd

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// hostLookups is what a stubbed lookup of host names was asked.
type hostLookups struct {
	hosts []string
	// timeouts holds, for each lookup, how long it was given, or 0 when it
	// was given no limit.
	timeouts []time.Duration
}

// stubHostLookup makes the names in hosts resolve to the addresses next to
// them, and every other name to nothing, for the rest of the test. Nothing is
// looked up on the network.
func stubHostLookup(t *testing.T, hosts map[string][]string) *hostLookups {
	t.Helper()

	lookups := &hostLookups{}
	original := lookupHostIPs
	t.Cleanup(func() { lookupHostIPs = original })
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.hosts = append(lookups.hosts, host)
		timeout := time.Duration(0)
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
		}
		lookups.timeouts = append(lookups.timeouts, timeout)

		addresses, ok := hosts[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		ips := make([]net.IP, 0, len(addresses))
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip == nil {
				t.Fatalf("%q is not an IP address", address)
			}
			ips = append(ips, ip)
		}
		return ips, nil
	}

	return lookups
}

// stubInterfaceAddrs gives this machine the interface addresses in cidrs, and
// no other, for the rest of the test.
func stubInterfaceAddrs(t *testing.T, cidrs ...string) {
	t.Helper()

	addrs := make([]net.Addr, 0, len(cidrs))
	for _, cidr := range cidrs {
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		addrs = append(addrs, &net.IPNet{IP: ip, Mask: ipNet.Mask})
	}

	original := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = original })
	interfaceAddrs = func() ([]net.Addr, error) { return addrs, nil }
}

// A server started with `--address <name>`, such as the name of its own
// container, records that name. No name counted as an address of this
// machine, so the record was put aside, and the health command looked for
// the server on localhost and the port of the configuration: the container
// was reported as unhealthy for as long as it ran.
func TestLocalAddressesResolvesAHostName(t *testing.T) {
	stubInterfaceAddrs(t, "172.17.0.2/16", "fd00::2/64")
	lookups := stubHostLookup(t, map[string][]string{
		"fbhost":            {"172.17.0.2"},
		"fbhost6":           {"fd00::2"},
		"loopback":          {"127.0.1.1"},
		"loopback6":         {"::1"},
		"both":              {"fd00::2", "172.17.0.2"},
		"here-and-there":    {"192.0.2.1", "172.17.0.2", "2001:db8::1"},
		"files.example.net": {"192.0.2.1", "2001:db8::1"},
		"neighbour":         {"172.17.0.3"},
		"no-addresses":      {},
	})

	tests := []struct {
		address string
		want    bool
		lookup  bool
		// ofName is which addresses of a name are this machine's.
		ofName []string
	}{
		// A name is looked up, and is this machine's when one of its
		// addresses is.
		{address: "fbhost", want: true, lookup: true, ofName: []string{"172.17.0.2"}},
		{address: "fbhost6", want: true, lookup: true, ofName: []string{"fd00::2"}},
		{address: "loopback", want: true, lookup: true, ofName: []string{"127.0.1.1"}},
		{address: "loopback6", want: true, lookup: true, ofName: []string{"::1"}},
		{address: "both", want: true, lookup: true, ofName: []string{"fd00::2", "172.17.0.2"}},
		// Not the address of the name that is another machine's.
		{address: "here-and-there", want: true, lookup: true, ofName: []string{"172.17.0.2"}},

		// A name of another machine, or of none, is not.
		{address: "files.example.net", want: false, lookup: true},
		{address: "neighbour", want: false, lookup: true},
		{address: "no-addresses", want: false, lookup: true},
		{address: "unknown.invalid", want: false, lookup: true},

		// What is not a name needs no lookup, and is judged as before.
		{address: "", want: true},
		{address: "localhost", want: true},
		{address: "LOCALHOST", want: true},
		{address: "0.0.0.0", want: true},
		{address: "[::]", want: true},
		{address: "127.0.0.1", want: true},
		{address: "[::1]", want: true},
		{address: "172.17.0.2", want: true},
		{address: "[fd00::2]", want: true},
		{address: "172.17.0.3", want: false},
		{address: "192.0.2.1", want: false},
		{address: "[2001:db8::1]", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			lookups.hosts, lookups.timeouts = nil, nil

			ofName, got := localAddresses(tt.address)
			if got != tt.want {
				t.Errorf("localAddresses(%q) = %v, want %v", tt.address, got, tt.want)
			}
			if !slices.Equal(ofName, tt.ofName) {
				t.Errorf("localAddresses(%q) = %q, want the addresses %q", tt.address, ofName, tt.ofName)
			}

			if !tt.lookup {
				if len(lookups.hosts) != 0 {
					t.Errorf("looked up %q, want no lookup", lookups.hosts)
				}
				return
			}
			if len(lookups.hosts) != 1 || lookups.hosts[0] != tt.address {
				t.Fatalf("looked up %q, want %q alone", lookups.hosts, tt.address)
			}
			// The lookup must end by itself: a name server that does not
			// answer would otherwise hold the health check until Docker
			// kills it.
			if timeout := lookups.timeouts[0]; timeout <= 0 || timeout > hostLookupTimeout {
				t.Errorf("the lookup was given %s, want a limit of at most %s", timeout, hostLookupTimeout)
			}
		})
	}
}

// The same, through the health command: where it sends its request.
func TestHealthUsesARecordWithANameOfThisMachine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	// Where the health command looks without a record it can use: what the
	// environment says, not where the server was told to listen.
	t.Setenv("FB_ADDRESS", "127.0.0.1")
	t.Setenv("FB_PORT", "8080")
	const fromEnvironment = "http://127.0.0.1:8080/health"

	stubInterfaceAddrs(t, "172.17.0.2/16")
	stubHostLookup(t, map[string][]string{
		"fbhost":            {"172.17.0.2"},
		"loopback":          {"127.0.1.1"},
		"here-and-there":    {"192.0.2.1", "172.17.0.2"},
		"files.example.net": {"192.0.2.1"},
	})

	for address, want := range map[string]healthTarget{
		"fbhost":   {url: "http://fbhost:9000/health", addresses: "172.17.0.2"},
		"loopback": {url: "http://loopback:9000/health", addresses: "127.0.1.1"},
		// The name is looked up once. Connecting to it would look it up
		// again, and could reach the address that is another machine's.
		"here-and-there": {url: "http://here-and-there:9000/health", addresses: "172.17.0.2"},

		// A record is not a way to have the check sent to another machine.
		"files.example.net": {url: fromEnvironment},
		"unknown.invalid":   {url: fromEnvironment},
	} {
		t.Run(address, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "health.json")
			t.Setenv(envHealthFile, path)
			writeRecordFile(t, path, listenRecord{
				PID:       os.Getpid(),
				StartTime: processStartTime(os.Getpid()),
				Address:   address,
				Port:      "9000",
			})

			if got := resolvedHealthTarget(t); got != want {
				t.Errorf("target = %+v, want %+v", got, want)
			}
		})
	}
}

// The same again, with a server to connect to. The name stands for this
// machine and for another one, and nothing but the stub knows it: a check
// that connected to the name, and not to the address that was found to be
// this machine's, would not find the server.
func TestHealthConnectsToTheLocalAddressesOfARecordedName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConfig, "")

	srv := httptest.NewServer(healthyHandler("/health"))
	defer srv.Close()
	host, port := hostAndPort(t, srv)

	// The address that is another machine's comes first, and nothing
	// listens on the second one that is this machine's.
	stubInterfaceAddrs(t, "172.17.0.2/16")
	stubHostLookup(t, map[string][]string{"here-and-there.invalid": {"192.0.2.1", "::1", host}})

	path := filepath.Join(t.TempDir(), "health.json")
	t.Setenv(envHealthFile, path)
	writeRecordFile(t, path, listenRecord{
		PID:       os.Getpid(),
		StartTime: processStartTime(os.Getpid()),
		Address:   "here-and-there.invalid",
		Port:      port,
	})

	want := healthTarget{url: "http://here-and-there.invalid:" + port + "/health", addresses: "::1 " + host}
	if got := resolvedHealthTarget(t); got != want {
		t.Fatalf("target = %+v, want %+v", got, want)
	}
	if err := runHealthTestCmd(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	srv.Close()
	if err := runHealthTestCmd(); err == nil {
		t.Error("expected an error: the server no longer listens")
	}
}
