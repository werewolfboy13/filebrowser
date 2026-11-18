package fbhttp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bruteForce fails key until it has to wait, with n failures past the free
// ones on it, and returns how long it then waits.
func bruteForce(t *testing.T, l *failureLimiter, clock *fakeClock, group string, key [32]byte, n int) time.Duration {
	t.Helper()
	for i := 0; i < limiterFreeFailures; i++ {
		if allowed, _ := failOnceFrom(l, group, key); !allowed {
			t.Fatalf("free failure %d was refused", i+1)
		}
	}
	wait := limiterBaseDelay
	for i := 1; i < n; i++ {
		clock.Advance(wait)
		if allowed, _ := failOnceFrom(l, group, key); !allowed {
			t.Fatalf("failure %d past the free ones was refused after its wait", i)
		}
		wait = min(wait*2, limiterMaxDelay)
	}
	allowed, retry := failOnceFrom(l, group, key)
	if allowed || retry != wait {
		t.Fatalf("after %d failures past the free ones: allowed = %v, wait %s; want a wait of %s", n, allowed, retry, wait)
	}
	return wait
}

// When the limiter was full, a key that was not in it yet was refused: the
// limiter failed closed. Filling it took one failed login under each of 200
// names from each of 50 addresses, and from then on nobody whose address and
// name had no key could log in, with the right password or not, for as long
// as the keys were kept alive with a failure every half hour. A single
// address that everyone shares (a proxy that does not pass the client's on)
// was filled with 200.
//
// Room is made instead, by forgetting the key that is worth the least, in the
// group that holds the most. A key that waits is worth the most, and the
// account that is being guessed keeps waiting through all of it.
func TestFailureLimiterFloodDoesNotLockAnyoneOut(t *testing.T) {
	l, clock := testLimiter()
	address := func(g int) string { return fmt.Sprintf("2001:db8:0:%x::/64", g) }
	junk := func(g, u int) [32]byte { return limiterKey("login", address(g), fmt.Sprintf("junk-%d-%d", g, u)) }

	// An account that is being guessed, from one of the flooding addresses.
	target := limiterKey("login", address(0), "admin")
	wait := bruteForce(t, l, clock, address(0), target, 4)

	// The flood: every key fails once.
	const groups = limiterMaxKeys / limiterMaxGroupKeys
	for g := 0; g < groups; g++ {
		for u := 0; u < limiterMaxGroupKeys; u++ {
			failOnceFrom(l, address(g), junk(g, u))
		}
	}
	if len(l.keys) > limiterMaxKeys {
		t.Errorf("the limiter holds %d keys, limit %d", len(l.keys), limiterMaxKeys)
	}

	// Someone who has not failed at anything, from an address of their own.
	const elsewhere = "203.0.113.50"
	done, retry := l.begin(elsewhere, limiterKey("login", elsewhere, "admin"))
	if done == nil {
		t.Fatalf("a login from an address without a key was refused when the limiter was full (wait %s)", retry)
	}
	done(attemptSucceeded)

	// And from an address that is full of somebody else's failures.
	done, retry = l.begin(address(7), limiterKey("login", address(7), "bob"))
	if done == nil {
		t.Fatalf("a login from an address whose share of the limiter is full was refused (wait %s)", retry)
	}
	done(attemptSucceeded)

	// It goes on: more failures under new names, from new addresses and old.
	for g := groups - 5; g < groups+5; g++ {
		for u := limiterMaxGroupKeys; u < limiterMaxGroupKeys+20; u++ {
			failOnceFrom(l, address(g), junk(g, u))
		}
	}
	if len(l.keys) > limiterMaxKeys {
		t.Errorf("the limiter holds %d keys after more of the flood, limit %d", len(l.keys), limiterMaxKeys)
	}
	if n := len(l.groups); n > limiterMaxKeys {
		t.Errorf("the limiter holds %d groups", n)
	}

	// The account that is being guessed still waits, and for as long as it
	// would have without the flood.
	if allowed, retry := failOnceFrom(l, address(0), target); allowed || retry != wait {
		t.Errorf("the account that was being guessed, after the flood: allowed = %v, wait %s; want a wait of %s", allowed, retry, wait)
	}
	clock.Advance(wait)
	if allowed, _ := failOnceFrom(l, address(0), target); !allowed {
		t.Error("no attempt for it once its wait was over")
	}
	if allowed, retry := failOnceFrom(l, address(0), target); allowed || retry != 2*wait {
		t.Errorf("after one more failure: allowed = %v, wait %s; want %s (its count was dropped)", allowed, retry, 2*wait)
	}
}

// Through the API. The requests that filled the limiter did not even cost a
// password hash: a body the login refuses before it looks at the password
// (a password that is not a string, for one) still left a key behind.
func TestLoginFloodDoesNotLockOutLogins(t *testing.T) {
	post := func(e *fullEnv, addr, body string) int {
		var hdr map[string]string
		if addr != "" {
			hdr = map[string]string{"X-Forwarded-For": addr}
		}
		code, _, _ := e.do(http.MethodPost, "/api/login", "", strings.NewReader(body), hdr)
		return code
	}

	t.Run("bodies that are refused before the password is looked at", func(t *testing.T) {
		e := newFullEnv(t)
		e.useFakeClock()

		for i, template := range []string{
			`{"username":"junk-%d","password":0}`,
			`{"username":"junk-%d","password":["x"]}`,
			`{"username":"junk-%d","password":"x","recaptcha":0}`,
			`{"username":%d,"password":"x"}`,
			`{"username":"","password":"x-%d"}`,
			`{"password":"x-%d"}`,
			`{"username":"junk-%d","password":"x"`,
			`junk-%d`,
			``,
		} {
			for n := 0; n < 30; n++ {
				body := template
				if strings.Contains(template, "%d") {
					body = fmt.Sprintf(template, n)
				}
				if code := post(e, fmt.Sprintf("2001:db8:0:%x::1", i), body); code != http.StatusForbidden {
					t.Fatalf("login with the body %s = %d, want 403", body, code)
				}
			}
		}
		e.limits.login.mu.Lock()
		kept := len(e.limits.login.keys)
		e.limits.login.mu.Unlock()
		if kept != 0 {
			t.Errorf("%d keys were kept for logins that were refused before the password was looked at", kept)
		}
	})

	// As it was done: 200 names from each of 50 addresses, then the same
	// through one address that everyone shares.
	t.Run("the limiter is filled", func(t *testing.T) {
		e := newFullEnv(t)
		e.useFakeClock()
		for g := 0; g < limiterMaxKeys/limiterMaxGroupKeys; g++ {
			for u := 0; u < limiterMaxGroupKeys; u++ {
				post(e, fmt.Sprintf("2001:db8:0:%x::1", g), fmt.Sprintf(`{"username":"junk-%d-%d","password":0}`, g, u))
			}
		}
		if code, h := e.loginFrom("203.0.113.50", "admin", fullEnvAdminPassword); code != http.StatusOK {
			t.Errorf("the right password from an address of its own, after the flood = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
		}
		for u := 0; u < limiterMaxGroupKeys; u++ {
			post(e, "", fmt.Sprintf(`{"username":"junk-%d","password":0}`, u))
		}
		if code, h, _ := e.tryLogin("bob", fullEnvUserPassword); code != http.StatusOK {
			t.Errorf("the right password from the address everyone shares, after the flood = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
		}
	})

	// With failures that are real ones (a wrong password, which costs a hash
	// each), and a limiter small enough to fill that way in a test.
	t.Run("real failures", func(t *testing.T) {
		e := newFullEnv(t)
		clock := e.useFakeClock()
		e.limits.login.maxKeys = 24
		e.limits.login.maxGroupKeys = 6
		const attacker, shared, elsewhere = "198.51.100.7", "198.51.100.8", "203.0.113.50"

		// An account that is being guessed.
		for i := 0; i <= limiterFreeFailures; i++ {
			e.loginFrom(attacker, "admin", "wrong-"+strconv.Itoa(i))
			clock.Advance(limiterBaseDelay)
		}
		code, h := e.loginFrom(attacker, "admin", fullEnvAdminPassword)
		if code != http.StatusTooManyRequests {
			t.Fatalf("control: the account that is being guessed does not wait: %d", code)
		}
		wait, _ := strconv.Atoi(h.Get("Retry-After"))

		// The flood: from the attacker's own address, from one that others
		// share, and from enough further ones to fill the limiter.
		for _, addr := range []string{attacker, shared, "198.51.100.9", "198.51.100.10", "198.51.100.11"} {
			for u := 0; u < 6; u++ {
				if code, _ := e.loginFrom(addr, "nobody-"+strconv.Itoa(u), "wrong"); code != http.StatusForbidden {
					t.Fatalf("a wrong password under a new name from %s = %d, want 403", addr, code)
				}
			}
		}

		if code, h := e.loginFrom(elsewhere, "bob", fullEnvUserPassword); code != http.StatusOK {
			t.Errorf("the right password from an address of its own, after the flood = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
		}
		if code, h := e.loginFrom(shared, "bob", fullEnvUserPassword); code != http.StatusOK {
			t.Errorf("the right password from an address that is full of failures = %d (Retry-After %q), want 200", code, h.Get("Retry-After"))
		}
		e.limits.login.mu.Lock()
		kept := len(e.limits.login.keys)
		e.limits.login.mu.Unlock()
		if kept > 24 {
			t.Errorf("the limiter holds %d keys, limit 24", kept)
		}

		// The account that is being guessed still waits, right password or
		// not, and goes on waiting longer.
		if code, _ := e.loginFrom(attacker, "admin", fullEnvAdminPassword); code != http.StatusTooManyRequests {
			t.Errorf("the right password for the account that is being guessed, after the flood = %d, want 429", code)
		}
		clock.Advance(time.Duration(wait) * time.Second)
		if code, _ := e.loginFrom(attacker, "admin", "wrong-after-the-wait"); code != http.StatusForbidden {
			t.Fatalf("a guess after the wait = %d, want 403", code)
		}
		code, h = e.loginFrom(attacker, "admin", fullEnvAdminPassword)
		if next, _ := strconv.Atoi(h.Get("Retry-After")); code != http.StatusTooManyRequests || next < 2*wait {
			t.Errorf("the guess after that = %d with Retry-After %d, want 429 and at least %d", code, next, 2*wait)
		}
	})
}

// guessesLetThrough plays an attacker who is after one key for the given
// number of seconds: every second it does whatever trick is (nothing, when
// it is nil), and then guesses at the key for as long as it is let.
func guessesLetThrough(l *failureLimiter, clock *fakeClock, group string, key [32]byte, seconds int, trick func(second int)) int {
	guesses := 0
	for second := 0; second < seconds; second++ {
		clock.Advance(time.Second)
		if trick != nil {
			trick(second)
		}
		for n := 0; n < 10; n++ {
			if allowed, _ := failOnceFrom(l, group, key); !allowed {
				break
			}
			guesses++
		}
	}
	return guesses
}

// Making room forgets a key, and a client can make that happen to the key it
// is after: with the rest of its address's share filled with keys that have
// more failures on them, one failure under a new name pushes that key out as
// soon as its wait is over. Were its count forgotten with it, it would be
// back with all its free failures, every second: from a single address, at an
// account, without ever waiting more than that. The same from the outside,
// by filling the limiter and making the key's address the one with the most
// keys.
//
// What a key had on it stays with its group (see evict), so being pushed out
// gets the key nothing: it is let through no more often than a key that is
// simply guessed at for as long.
func TestFailureLimiterMakingRoomDoesNotStartACountOver(t *testing.T) {
	const address, seconds = "198.51.100.7", 600
	target := limiterKey("login", address, "admin")

	// prepare gives the key the attacker is after all its free failures, and
	// junk keys of the same address one more than that, all of them done
	// waiting.
	prepare := func(l *failureLimiter, clock *fakeClock, junk int) {
		for i := 0; i < junk; i++ {
			for n := 0; n < limiterFreeFailures; n++ {
				failOnceFrom(l, address, limiterKey("login", address, "junk-"+strconv.Itoa(i)))
			}
		}
		clock.Advance(limiterBaseDelay)
		for i := 0; i < junk; i++ {
			failOnceFrom(l, address, limiterKey("login", address, "junk-"+strconv.Itoa(i)))
		}
		for n := 0; n < limiterFreeFailures; n++ {
			failOnceFrom(l, address, target)
		}
		clock.Advance(2 * limiterBaseDelay)
	}

	// What guessing at the key gets without any trick.
	plain, clock := testLimiter()
	prepare(plain, clock, 0)
	honest := guessesLetThrough(plain, clock, address, target, seconds, nil)
	if honest < 5 || honest > 20 {
		t.Fatalf("control: %d guesses in %d seconds without a trick", honest, seconds)
	}

	t.Run("from its own address", func(t *testing.T) {
		l, clock := testLimiter()
		l.maxGroupKeys = 20
		prepare(l, clock, l.maxGroupKeys-1)

		got := guessesLetThrough(l, clock, address, target, seconds, func(second int) {
			failOnceFrom(l, address, limiterKey("login", address, "new-name-"+strconv.Itoa(second)))
		})
		if got > honest {
			t.Errorf("%d guesses at one key in %d seconds by pushing it out again and again, %d without: its count starts over", got, seconds, honest)
		}
		if n := len(l.groups[address].keys); n > l.maxGroupKeys {
			t.Errorf("the address holds %d keys, limit %d", n, l.maxGroupKeys)
		}
	})

	t.Run("from other addresses, with the limiter full", func(t *testing.T) {
		l, clock := testLimiter()
		l.maxGroupKeys = 20
		l.maxKeys = 60
		prepare(l, clock, l.maxGroupKeys-1)
		// The limiter is full, and the key's address is the one with the
		// most keys.
		for i := 0; len(l.keys) < l.maxKeys; i++ {
			other := "203.0.113." + strconv.Itoa(i%4)
			failOnceFrom(l, other, limiterKey("login", other, "fill-"+strconv.Itoa(i)))
		}

		got := guessesLetThrough(l, clock, address, target, seconds, func(second int) {
			other := "192.0.2." + strconv.Itoa(second%200)
			failOnceFrom(l, other, limiterKey("login", other, "new-name-"+strconv.Itoa(second)))
		})
		if got > honest {
			t.Errorf("%d guesses at one key in %d seconds by pushing it out from other addresses, %d without: its count starts over", got, seconds, honest)
		}
		if len(l.keys) > l.maxKeys {
			t.Errorf("the limiter holds %d keys, limit %d", len(l.keys), l.maxKeys)
		}
	})
}

// What a group remembers of the keys that had to make room is kept for as
// long as those keys would have been, and no longer.
func TestFailureLimiterForgetsWhatMakingRoomLeftBehind(t *testing.T) {
	const address = "198.51.100.7"
	name := func(s string) [32]byte { return limiterKey("login", address, s) }
	freeFailures := func(l *failureLimiter, key [32]byte) int {
		n := 0
		for ; n < 2*limiterFreeFailures; n++ {
			if allowed, _ := failOnceFrom(l, address, key); !allowed {
				break
			}
		}
		return n
	}
	// full is a limiter in which the address has all the keys it may have,
	// each with all but one of its free failures used.
	full := func() (*failureLimiter, *fakeClock) {
		l, clock := testLimiter()
		l.maxGroupKeys = 3
		for _, s := range []string{"a", "b", "c"} {
			for n := 0; n < limiterFreeFailures-1; n++ {
				failOnceFrom(l, address, name(s))
			}
		}
		return l, clock
	}

	// A new key starts where the one that made room for it was.
	l, _ := full()
	if n := freeFailures(l, name("d")); n != 1 {
		t.Errorf("a new key had %d free failures where a key with one left made room for it, want 1", n)
	}

	// After the time a key is forgotten in, so is what it left behind, also
	// while the group has other keys that are not.
	l, clock := full()
	failOnceFrom(l, address, name("d")) // "a", "b" or "c" makes room
	clock.Advance(limiterForget - time.Minute)
	if n := freeFailures(l, name("e")); n != 1 {
		t.Errorf("a new key had %d free failures a while later, want 1", n)
	}
	clock.Advance(2 * time.Minute)
	if n := freeFailures(l, name("f")); n != limiterFreeFailures {
		t.Errorf("a new key had %d free failures once the key that made room would have been forgotten, want %d", n, limiterFreeFailures)
	}

	// A key that has nothing on it but what was left behind is not kept
	// when its only attempt ends without a verdict: such an attempt leaves
	// no key, with or without something to start from.
	l, _ = full()
	failOnceFrom(l, address, name("d"))
	done, _ := l.begin(address, name("e"))
	if done == nil {
		t.Fatal("no attempt for a new key")
	}
	done(attemptAborted)
	if _, kept := l.keys[name("e")]; kept || len(l.groups[address].keys) != l.maxGroupKeys-1 {
		t.Errorf("a key with nothing but an aborted attempt was kept: %d keys", len(l.groups[address].keys))
	}

	// And it goes with the last key of the group.
	l, _ = full()
	failOnceFrom(l, address, name("d"))
	for _, state := range l.groups[address].keys {
		state.failures = 0 // so that every one of them can still succeed
		state.blockedUntil = time.Time{}
	}
	for key := range l.groups[address].keys {
		done, _ := l.begin(address, key)
		if done == nil {
			t.Fatal("no attempt for a key that is not waiting")
		}
		done(attemptSucceeded)
	}
	if len(l.groups) != 0 || len(l.keys) != 0 {
		t.Fatalf("%d groups and %d keys after every key succeeded", len(l.groups), len(l.keys))
	}
	if n := freeFailures(l, name("g")); n != limiterFreeFailures {
		t.Errorf("a new key had %d free failures after the group was gone, want %d", n, limiterFreeFailures)
	}
}

// A key that is waiting only ever makes room for another address, and its
// wait is not lost when it does.
func TestFailureLimiterKeepsWaitsWhenMakingRoom(t *testing.T) {
	const first, second, newcomer = "198.51.100.7", "198.51.100.8", "203.0.113.50"
	l, clock := testLimiter()
	l.maxGroupKeys = 3
	l.maxKeys = 6
	key := func(addr string, i int) [32]byte { return limiterKey("login", addr, "name-"+strconv.Itoa(i)) }
	for _, addr := range []string{first, second} {
		for i := 0; i < 3; i++ {
			for n := 0; n < limiterFreeFailures; n++ {
				failOnceFrom(l, addr, key(addr, i))
			}
		}
	}
	clock.Advance(300 * time.Millisecond)
	left := limiterBaseDelay - 300*time.Millisecond

	// Its own address is full of keys that wait: a new name waits with them.
	if allowed, retry := failOnceFrom(l, first, key(first, 99)); allowed || retry != limiterBaseDelay {
		t.Errorf("a new name from an address whose keys all wait: allowed = %v, wait %s; want %s", allowed, retry, limiterBaseDelay)
	}
	if len(l.keys) != 6 {
		t.Fatalf("%d keys", len(l.keys))
	}

	// Somebody else is not refused for it, although every key in the limiter
	// waits.
	done, retry := l.begin(newcomer, limiterKey("login", newcomer, "admin"))
	if done == nil {
		t.Fatalf("a login from another address was refused because the limiter is full of waiting keys (wait %s)", retry)
	}
	done(attemptSucceeded)

	// One of the six keys made room. It waits as before, and so does every
	// name its address has no key for; the rest is untouched.
	waiting := 0
	for _, addr := range []string{first, second} {
		for i := 0; i < 3; i++ {
			if allowed, retry := failOnceFrom(l, addr, key(addr, i)); !allowed && retry == left {
				waiting++
			}
		}
	}
	if waiting != 6 {
		t.Errorf("%d of the 6 keys still wait for %s after one of them made room, want all", waiting, left)
	}
	lost := first
	if len(l.groups[first].keys) == 3 {
		lost = second
	}
	if allowed, retry := failOnceFrom(l, lost, key(lost, 99)); allowed || retry != left {
		t.Errorf("a new name from the address that lost a waiting key: allowed = %v, wait %s; want %s", allowed, retry, left)
	}
	if len(l.keys) > l.maxKeys {
		t.Errorf("%d keys, limit %d", len(l.keys), l.maxKeys)
	}

	// Once the wait is over, each of them is where it was: one attempt, then
	// twice the wait.
	clock.Advance(left)
	for _, addr := range []string{first, second} {
		for i := 0; i < 3; i++ {
			if allowed, _ := failOnceFrom(l, addr, key(addr, i)); !allowed {
				t.Errorf("no attempt for key %d of %s once its wait was over", i, addr)
			}
			if allowed, retry := failOnceFrom(l, addr, key(addr, i)); allowed || retry != 2*limiterBaseDelay {
				t.Errorf("key %d of %s after one more failure: allowed = %v, wait %s; want %s", i, addr, allowed, retry, 2*limiterBaseDelay)
			}
		}
	}

	// With an attempt in flight for every key there is nothing to forget,
	// and that is not looked for again with every request.
	l, clock = testLimiter()
	l.maxKeys = 2
	var pending []func(attemptOutcome)
	for i := 0; i < 2; i++ {
		done, _ := l.begin(first, key(first, i))
		pending = append(pending, done)
	}
	for i := 0; i < 3; i++ {
		if done, retry := l.begin(newcomer, limiterKey("login", newcomer, "admin")); done != nil || retry <= 0 || retry > limiterBaseDelay {
			t.Errorf("with every key in flight: allowed = %v, wait %s", done != nil, retry)
		}
	}
	if !l.noRoomUntil.After(clock.Now()) {
		t.Error("the limiter does not remember that there was no room")
	}
	pending[0](attemptSucceeded)
	pending[1](attemptFailed)
	if done, _ := l.begin(newcomer, limiterKey("login", newcomer, "admin")); done == nil {
		t.Error("a new key was refused although a place had become free")
	}
}
