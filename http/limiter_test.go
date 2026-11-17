package fbhttp

import (
	"net/http"
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
	done, retry := l.begin(key)
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
		if done, _ := l.begin(key); done != nil {
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
	done, _ := l.begin(key)
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
		if done, _ := l.begin(key); done != nil {
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
	first, _ := l.begin(key)
	if first == nil {
		t.Fatal("no attempt after the wait")
	}
	if second, _ := l.begin(key); second != nil {
		t.Error("two attempts in flight for a key that used up its free failures")
	}
	first(attemptAborted)

	// An attempt that ended without a verdict counts neither way: the key
	// neither waits longer nor is forgotten.
	if done, _ := l.begin(key); done == nil {
		t.Error("an aborted attempt made the key wait")
	} else {
		done(attemptAborted)
	}
	if state := l.keys[key]; state == nil || state.failures != limiterFreeFailures || state.inFlight != 0 {
		t.Errorf("state after aborted attempts: %+v", state)
	}
	fresh := limiterKey("login", "203.0.113.7", "nobody")
	if done, _ := l.begin(fresh); done != nil {
		done(attemptAborted)
	}
	if _, kept := l.keys[fresh]; kept {
		t.Error("a key with nothing but an aborted attempt was kept")
	}
}

func TestFailureLimiterIsBounded(t *testing.T) {
	l, clock := testLimiter()
	l.maxKeys = 100

	for i := 0; i < 1000; i++ {
		failOnce(l, limiterKey("login", "203.0.113.7", string(rune('a'+i%26))+string(rune(i))))
		clock.Advance(time.Millisecond)
	}
	if len(l.keys) > l.maxKeys {
		t.Errorf("the limiter holds %d keys, limit %d", len(l.keys), l.maxKeys)
	}

	// The most recent keys are the ones that were kept.
	recent := limiterKey("login", "203.0.113.7", string(rune('a'+999%26))+string(rune(999)))
	if _, ok := l.keys[recent]; !ok {
		t.Error("the most recent key was evicted")
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
