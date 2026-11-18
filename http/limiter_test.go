package fbhttp

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock tests move by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_800_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func testLimiter() (*failureLimiter, *fakeClock) {
	clock := newFakeClock()
	l := newFailureLimiter()
	l.now = clock.Now
	return l, clock
}

// failOnce makes one failed attempt and reports whether it was let through.
func failOnce(l *failureLimiter, key [32]byte) (allowed bool, retryAfter time.Duration) {
	return failOnceFrom(l, "203.0.113.7", key)
}

// failOnceFrom is failOnce for a key of the given group.
func failOnceFrom(l *failureLimiter, group string, key [32]byte) (allowed bool, retryAfter time.Duration) {
	done, retry := l.begin(group, key)
	if done == nil {
		return false, retry
	}
	done(attemptFailed)
	return true, 0
}

func TestFailureLimiterBacksOffExponentially(t *testing.T) {
	l, clock := testLimiter()
	key := limiterKey("login", "203.0.113.7", "admin")

	for i := 1; i <= limiterFreeFailures; i++ {
		if allowed, _ := failOnce(l, key); !allowed {
			t.Fatalf("failure %d of the free ones was refused", i)
		}
	}

	// From here on every failure doubles the wait.
	want := limiterBaseDelay
	for i := 0; i < 12; i++ {
		allowed, retry := failOnce(l, key)
		if allowed {
			t.Fatalf("an attempt was let through while the key had to wait %s", want)
		}
		if retry != want {
			t.Fatalf("after %d failures past the free ones: wait %s, want %s", i, retry, want)
		}

		// A moment before the wait is over it still holds, also for a
		// success-to-be: the attempt is not made at all.
		clock.Advance(want - time.Millisecond)
		if done, _ := l.begin("203.0.113.7", key); done != nil {
			t.Fatal("an attempt was let through before the wait was over")
		}
		clock.Advance(time.Millisecond)

		if allowed, _ := failOnce(l, key); !allowed {
			t.Fatalf("no attempt was let through after waiting %s", want)
		}
		// That attempt failed too, so the next round starts out waiting,
		// twice as long.
		want = min(want*2, limiterMaxDelay)
	}
	if want != limiterMaxDelay {
		t.Fatalf("the wait never reached its cap (%s)", want)
	}
}

func TestFailureLimiterForgets(t *testing.T) {
	l, clock := testLimiter()
	key := limiterKey("login", "203.0.113.7", "admin")
	other := limiterKey("login", "203.0.113.7", "bob")

	for i := 0; i < limiterFreeFailures; i++ {
		failOnce(l, key)
	}
	if allowed, _ := failOnce(l, key); allowed {
		t.Fatal("the key does not wait after its free failures")
	}

	// Another key is not affected.
	if allowed, _ := failOnce(l, other); !allowed {
		t.Error("a failure for one key made another one wait")
	}

	// A success clears the key: the free failures are available again.
	clock.Advance(limiterBaseDelay)
	done, _ := l.begin("203.0.113.7", key)
	if done == nil {
		t.Fatal("no attempt after the wait")
	}
	done(attemptSucceeded)
	for i := 0; i < limiterFreeFailures; i++ {
		if allowed, _ := failOnce(l, key); !allowed {
			t.Fatalf("failure %d after a success was refused", i+1)
		}
	}

	// So does a long enough silence.
	clock.Advance(limiterForget + time.Second)
	for i := 0; i < limiterFreeFailures; i++ {
		if allowed, _ := failOnce(l, key); !allowed {
			t.Fatalf("failure %d after the key was forgotten was refused", i+1)
		}
	}
}

// Sending guesses in parallel must not get more of them checked than sending
// them one after the other.
func TestFailureLimiterCountsAttemptsInFlight(t *testing.T) {
	l, _ := testLimiter()
	key := limiterKey("login", "203.0.113.7", "admin")

	var pending []func(attemptOutcome)
	for i := 0; i < 50; i++ {
		if done, _ := l.begin("203.0.113.7", key); done != nil {
			pending = append(pending, done)
		}
	}
	if len(pending) != limiterFreeFailures {
		t.Fatalf("%d attempts were let through at once, want %d", len(pending), limiterFreeFailures)
	}
	for _, done := range pending {
		done(attemptFailed)
		done(attemptFailed) // reporting twice changes nothing
	}

	// Past the free failures, one at a time.
	l.now = func() time.Time { return time.Unix(1_800_000_000, 0).Add(time.Hour / 4) }
	first, _ := l.begin("203.0.113.7", key)
	if first == nil {
		t.Fatal("no attempt after the wait")
	}
	if second, _ := l.begin("203.0.113.7", key); second != nil {
		t.Error("two attempts in flight for a key that used up its free failures")
	}
	first(attemptAborted)

	// An attempt that ended without a verdict counts neither way: the key
	// neither waits longer nor is forgotten.
	if done, _ := l.begin("203.0.113.7", key); done == nil {
		t.Error("an aborted attempt made the key wait")
	} else {
		done(attemptAborted)
	}
	if state := l.keys[key]; state == nil || state.failures != limiterFreeFailures || state.inFlight != 0 {
		t.Errorf("state after aborted attempts: %+v", state)
	}
	fresh := limiterKey("login", "203.0.113.7", "nobody")
	if done, _ := l.begin("203.0.113.7", fresh); done != nil {
		done(attemptAborted)
	}
	if _, kept := l.keys[fresh]; kept {
		t.Error("a key with nothing but an aborted attempt was kept")
	}
}

// The limiter keeps a bounded number of keys. It used to make room for a new
// one by dropping the key that had been quiet the longest, whatever that key
// had on it. Failed attempts under throw-away names pushed out the key of the
// account being guessed, and with it its count: the free failures were
// available again, and the wait started over.
//
// Room is still made by forgetting a key (see add), but never a count: no key
// is better off after a flood than it was before.
func TestFailureLimiterIsBoundedWithoutForgettingFailures(t *testing.T) {
	l, clock := testLimiter()
	l.maxKeys = 30
	const addr = "203.0.113.7"
	throwaway := func(i int) [32]byte { return limiterKey("login", addr, "nobody-"+strconv.Itoa(i)) }

	// One key that used up all but one of its free failures, one that is
	// waiting.
	counting := limiterKey("login", addr, "bob")
	for i := 0; i < limiterFreeFailures-1; i++ {
		failOnce(l, counting)
	}
	blocked := limiterKey("login", addr, "admin")
	for i := 0; i < limiterFreeFailures+2; i++ {
		failOnce(l, blocked)
		clock.Advance(limiterBaseDelay * 2)
	}
	if allowed, _ := failOnce(l, blocked); allowed {
		t.Fatal("control: the key is not waiting")
	}

	// The flood.
	for i := 0; i < 1000; i++ {
		if allowed, retry := failOnce(l, throwaway(i)); !allowed && retry <= 0 {
			t.Fatalf("a refused attempt was given no time to wait (%s)", retry)
		}
		if len(l.keys) > l.maxKeys {
			t.Fatalf("the limiter holds %d keys, limit %d", len(l.keys), l.maxKeys)
		}
		if allowed, _ := failOnce(l, blocked); allowed {
			t.Fatalf("the waiting key was let through after %d throw-away names", i+1)
		}
		clock.Advance(time.Millisecond)
	}

	// Neither key has more left than it had. The first had one free failure:
	// after one more it waits. The second, whose wait is over by now, waits
	// at least twice as long as the last time instead of starting over.
	clock.Advance(limiterMaxDelay)
	failOnce(l, counting)
	if allowed, retry := failOnce(l, counting); allowed || retry < limiterBaseDelay {
		t.Errorf("after the flood the key that had used %d free failures: allowed = %v, wait %s; want a wait of at least %s",
			limiterFreeFailures-1, allowed, retry, limiterBaseDelay)
	}
	if allowed, _ := failOnce(l, blocked); !allowed {
		t.Fatal("no attempt for the waiting key once its wait was over")
	}
	if allowed, retry := failOnce(l, blocked); allowed || retry < limiterBaseDelay<<3 {
		t.Errorf("after the flood the waiting key: allowed = %v, wait %s; want at least %s (its count was dropped)", allowed, retry, limiterBaseDelay<<3)
	}

	// A success gives its place back at once.
	clock.Advance(limiterMaxDelay)
	done, _ := l.begin(addr, counting)
	if done == nil {
		t.Fatal("no attempt after the wait")
	}
	done(attemptSucceeded)
	if len(l.keys) >= l.maxKeys {
		t.Errorf("the limiter still holds %d keys after a success", len(l.keys))
	}

	// And everything is forgotten after a long enough silence: also what
	// the keys that had to make room left behind.
	clock.Advance(limiterForget + time.Second)
	for i := 0; i < l.maxKeys; i++ {
		for n := 0; n < limiterFreeFailures; n++ {
			if allowed, _ := failOnce(l, throwaway(6000+i)); !allowed {
				t.Fatalf("free failure %d of new key %d was refused after the old ones were forgotten", n+1, i)
			}
		}
	}
	if len(l.keys) > l.maxKeys || len(l.groups) != 1 || len(l.groups[addr].keys) != len(l.keys) {
		t.Errorf("%d keys, groups %v", len(l.keys), l.groups)
	}
}

// One address can only take up so many keys, and gets only so many failures
// in before it waits, under however many names: what it does costs the
// others nothing.
func TestFailureLimiterBoundsTheKeysOfOneAddress(t *testing.T) {
	l, clock := testLimiter()
	l.maxKeys = 100
	l.maxGroupKeys = 5
	const flooder, other = "198.51.100.7", "203.0.113.9"

	// Every new name makes one of the old ones go, and starts where that
	// left off. So the address has as many failures as its keys have free
	// ones, and then all of its keys wait.
	allowed := 0
	for i := 0; i < 50; i++ {
		if ok, _ := failOnceFrom(l, flooder, limiterKey("login", flooder, "name-"+strconv.Itoa(i))); ok {
			allowed++
		}
		if n := len(l.groups[flooder].keys); n > l.maxGroupKeys {
			t.Fatalf("one address holds %d keys, limit %d", n, l.maxGroupKeys)
		}
	}
	if want := l.maxGroupKeys * limiterFreeFailures; allowed != want {
		t.Errorf("an address got %d failures in under 50 names before it had to wait, want %d", allowed, want)
	}
	if len(l.keys) != l.maxGroupKeys {
		t.Errorf("one address holds %d keys, limit %d", len(l.keys), l.maxGroupKeys)
	}
	if ok, retry := failOnceFrom(l, flooder, limiterKey("login", flooder, "one-more")); ok || retry != limiterBaseDelay {
		t.Errorf("one more name from the address: allowed = %v, wait %s; want a wait of %s", ok, retry, limiterBaseDelay)
	}

	// Nobody else is held up by it.
	for i := 0; i < 20; i++ {
		if ok, _ := failOnceFrom(l, other, limiterKey("login", other, "name-"+strconv.Itoa(i))); !ok {
			t.Fatalf("new key %d of another address was refused", i)
		}
	}

	// The address gets its places back as its keys are forgotten.
	clock.Advance(limiterForget + time.Second)
	for n := 0; n < limiterFreeFailures; n++ {
		if ok, _ := failOnceFrom(l, flooder, limiterKey("login", flooder, "later")); !ok {
			t.Errorf("free failure %d of a new key was refused after the old ones of the address were forgotten", n+1)
		}
	}
	total := 0
	for _, g := range l.groups {
		total += len(g.keys)
	}
	if total != len(l.keys) {
		t.Errorf("the groups hold %d keys between them, the limiter %d", total, len(l.keys))
	}
}

func TestClientAddr(t *testing.T) {
	cases := map[string]struct {
		remote    string
		forwarded []string
		want      string
	}{
		"direct connection":                         {"203.0.113.7:4711", nil, "203.0.113.7"},
		"public peer: the header is not trusted":    {"203.0.113.7:4711", []string{"198.51.100.1"}, "203.0.113.7"},
		"proxy on this machine":                     {"127.0.0.1:4711", []string{"198.51.100.1"}, "198.51.100.1"},
		"proxy on a private network":                {"172.18.0.2:4711", []string{"198.51.100.1"}, "198.51.100.1"},
		"only the entry the proxy added counts":     {"10.0.0.2:4711", []string{"1.2.3.4, 5.6.7.8, 198.51.100.1"}, "198.51.100.1"},
		"several header lines: the last one counts": {"10.0.0.2:4711", []string{"1.2.3.4", "198.51.100.1"}, "198.51.100.1"},
		"entry with a port":                         {"10.0.0.2:4711", []string{"198.51.100.1:5000"}, "198.51.100.1"},
		"private peer without the header":           {"192.168.1.20:4711", nil, "192.168.1.20"},
		"private peer with a useless header":        {"192.168.1.20:4711", []string{"unknown"}, "192.168.1.20"},
		"IPv6 client is cut to its /64":             {"[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:4711", nil, "2001:db8:1:2::/64"},
		"IPv6 client behind a proxy":                {"[::1]:4711", []string{"2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "2001:db8:1:2::/64"},
		"IPv4-mapped peer":                          {"[::ffff:203.0.113.7]:4711", []string{"198.51.100.1"}, "203.0.113.7"},
		"unix socket behind a proxy":                {"@", []string{"198.51.100.1"}, "198.51.100.1"},
		"unix socket without the header":            {"@", nil, "local"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodPost, "/api/login", http.NoBody)
			r.RemoteAddr = tc.remote
			for _, v := range tc.forwarded {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientAddr(r); got != tc.want {
				t.Errorf("clientAddr = %q, want %q", got, tc.want)
			}
		})
	}
}
