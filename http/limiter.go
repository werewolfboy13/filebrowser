package fbhttp

import (
	"crypto/sha256"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults of the failure limiters.
const (
	// limiterFreeFailures is the number of failed attempts that cost nothing:
	// a mistyped password or two must not get anyone locked out.
	limiterFreeFailures = 5

	// limiterBaseDelay is the wait imposed by the first failure past the free
	// ones. It doubles with every further failure, up to limiterMaxDelay.
	limiterBaseDelay = time.Second

	// limiterMaxDelay caps the wait. It bounds how long someone who shares an
	// address with an attacker (see clientAddr) can be kept out, while still
	// leaving the attacker a few hundred guesses a day.
	limiterMaxDelay = 5 * time.Minute

	// limiterForget is how long after the last failure a key is forgotten.
	limiterForget = 30 * time.Minute

	// limiterMaxKeys bounds the memory of a limiter. See makeRoom for what
	// happens when it is full.
	limiterMaxKeys = 10000

	// limiterMaxGroupKeys bounds the keys one group (one client address, for
	// the login) can take up, so that it takes many of them to fill a
	// limiter.
	limiterMaxGroupKeys = 200
)

// failureLimiter slows down repeated failed attempts at guessing a secret (a
// login password, the password of a share). It counts failures per key and
// makes the key wait after each one past the first few, twice as long each
// time. A success forgets the key.
//
// While a key waits, attempts for it are refused outright, without checking
// the secret: a correct guess gets no further than a wrong one, and no
// password hash is computed for either.
//
// The state is in memory: it is per process and does not survive a restart.
type failureLimiter struct {
	mu   sync.Mutex
	keys map[[sha256.Size]byte]*failureState

	// groups counts the keys of each group.
	groups map[string]int

	free         int
	base         time.Duration
	maxDelay     time.Duration
	forget       time.Duration
	maxKeys      int
	maxGroupKeys int
	now          func() time.Time
}

type failureState struct {
	group        string
	failures     int
	inFlight     int
	lastFailure  time.Time
	blockedUntil time.Time
}

func newFailureLimiter() *failureLimiter {
	return &failureLimiter{
		keys:         map[[sha256.Size]byte]*failureState{},
		groups:       map[string]int{},
		free:         limiterFreeFailures,
		base:         limiterBaseDelay,
		maxDelay:     limiterMaxDelay,
		forget:       limiterForget,
		maxKeys:      limiterMaxKeys,
		maxGroupKeys: limiterMaxGroupKeys,
		now:          time.Now,
	}
}

// limiterKey builds a key from its parts. Hashing gives every key the same
// size, whatever a client sends as a username.
func limiterKey(parts ...string) [sha256.Size]byte {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

// attemptOutcome is how an attempt ended.
type attemptOutcome int

const (
	// attemptFailed: the secret was wrong.
	attemptFailed attemptOutcome = iota
	// attemptSucceeded: the secret was right; the key is forgotten.
	attemptSucceeded
	// attemptAborted: the attempt ended without a verdict on the secret (a
	// server error). It counts neither way.
	attemptAborted
)

// begin announces an attempt for key. When the key has to wait, it returns a
// nil function and how long, and the attempt must not be made. Otherwise it
// returns a function that must be called, once, with the outcome.
//
// group names who the key belongs to as far as the room in the limiter goes:
// the client address for a login. See makeRoom.
//
// Attempts in flight count as failures until they report back, so firing many
// guesses at once gets no more of them checked than sending them in turn.
func (l *failureLimiter) begin(group string, key [sha256.Size]byte) (done func(attemptOutcome), retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	state := l.keys[key]
	if state != nil && l.forgotten(state, now) {
		l.drop(key, state)
		state = nil
	}

	if state != nil {
		if wait := state.blockedUntil.Sub(now); wait > 0 {
			return nil, wait
		}
		// Past the free failures, one attempt at a time; before that, as many
		// as are left of them.
		if allowed := max(1, l.free-state.failures); state.inFlight >= allowed {
			return nil, l.base
		}
	} else {
		if wait := l.makeRoom(group, now); wait > 0 {
			return nil, wait
		}
		state = &failureState{group: group, lastFailure: now}
		l.keys[key] = state
		l.groups[group]++
	}
	state.inFlight++

	var once sync.Once
	return func(outcome attemptOutcome) {
		once.Do(func() { l.end(key, state, outcome) })
	}, 0
}

// forgotten reports whether a key has been quiet for long enough to be
// dropped.
func (l *failureLimiter) forgotten(state *failureState, now time.Time) bool {
	return state.inFlight == 0 && now.Sub(state.lastFailure) > l.forget
}

// drop removes a key.
func (l *failureLimiter) drop(key [sha256.Size]byte, state *failureState) {
	delete(l.keys, key)
	if l.groups[state.group]--; l.groups[state.group] <= 0 {
		delete(l.groups, state.group)
	}
}

func (l *failureLimiter) end(key [sha256.Size]byte, state *failureState, outcome attemptOutcome) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state.inFlight--
	if l.keys[key] != state {
		// Dropped in the meantime, by a success of another attempt.
		return
	}

	switch outcome {
	case attemptSucceeded:
		l.drop(key, state)
		return
	case attemptAborted:
		// A key that was only created for this attempt has nothing to keep.
		if state.failures == 0 && state.inFlight == 0 {
			l.drop(key, state)
		}
		return
	}

	now := l.now()
	state.failures++
	state.lastFailure = now
	if over := state.failures - l.free; over >= 0 {
		delay := l.maxDelay
		// The shift is bounded so that it cannot overflow.
		if over < 32 {
			delay = min(l.base<<over, l.maxDelay)
		}
		state.blockedUntil = now.Add(delay)
	}
}

// makeRoom makes sure a key of group can be added without the limiter growing
// past maxKeys, or the group past maxGroupKeys. It returns 0 when it can, and
// otherwise how long the attempt has to wait for room.
//
// Room is only ever made by dropping keys that are forgotten anyway. A key
// that has a failure on it stays until then, however full the limiter is:
// dropping one to let a new key in forgets its count, and that is something a
// client can cause at will, by failing under one throw-away name after the
// other. It would get the free failures of the key it is really after back,
// and the wait of that key would start over.
//
// So when there is no room, it is the new key that is refused. That is a
// wait for someone who has done nothing, which is why a group has a bound of
// its own, well below that of the limiter: a single address fills its own
// share and is then the only one refused, and it takes many addresses
// (maxKeys/maxGroupKeys of them, each failing under maxGroupKeys names) to
// fill the limiter for everyone. While that lasts, logins from an address
// with no key of its own yet are refused: the limiter fails closed. Users
// who share an address with such a client (see clientAddr) share its bound.
func (l *failureLimiter) makeRoom(group string, now time.Time) time.Duration {
	full := func() bool {
		return len(l.keys) >= l.maxKeys || l.groups[group] >= l.maxGroupKeys
	}
	if !full() {
		return 0
	}

	// soonest is when the next key that counts against this one is forgotten.
	var soonest time.Duration
	limiterFull := len(l.keys) >= l.maxKeys
	for key, state := range l.keys {
		if l.forgotten(state, now) {
			l.drop(key, state)
			continue
		}
		if !limiterFull && state.group != group {
			continue
		}
		if left := l.forget - now.Sub(state.lastFailure); soonest == 0 || left < soonest {
			soonest = left
		}
	}
	if !full() {
		return 0
	}

	// Not longer than any other wait, and not nothing.
	return min(max(soonest, l.base), l.maxDelay)
}

// limits holds the failure limiters of one handler.
type limits struct {
	login *failureLimiter
	share *failureLimiter

	// password counts wrong answers to "what is your current password", the
	// question the users API asks before it changes an account: see
	// confirmPassword.
	password *failureLimiter
}

func newLimits() *limits {
	return &limits{login: newFailureLimiter(), share: newFailureLimiter(), password: newFailureLimiter()}
}

// tooManyAttempts answers a request that has to wait.
func tooManyAttempts(w http.ResponseWriter, retryAfter time.Duration) (int, error) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.FormatInt(max(1, seconds), 10))
	return http.StatusTooManyRequests, nil
}

// clientAddr returns the address failed attempts of the request are counted
// against.
//
// It is the address the connection comes from, with one exception. When that
// address is on this machine or on a private network (or the server listens
// on a Unix socket), the connection is taken to come from a reverse proxy,
// and the last entry of X-Forwarded-For is used: the one that proxy added,
// naming the client it saw. Without that, everyone behind the proxy would
// share one address, and a single attacker could keep a user's login waiting
// for all of them.
//
// Only the last entry is used. Earlier ones are whatever the client sent, so
// trusting them would let it pick a new address for every guess. For a
// connection from a public address the header is ignored altogether, for the
// same reason.
//
// What this cannot tell apart is a proxy from a client that reaches the
// server directly from a private address (the same LAN, or a Docker network).
// Such a client can forge the header, and so sidestep the limiter or make
// attempts count against another address. An installation that matters
// should not be reachable that way; one behind a second proxy (a CDN in front
// of the reverse proxy) sees the CDN's addresses here, shared by its users.
//
// An IPv6 address is cut to its /64: the rest is the client's to choose.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(strings.Trim(host, "[]"))

	if peer == nil || peer.IsLoopback() || peer.IsPrivate() || peer.IsLinkLocalUnicast() {
		if forwarded := lastForwardedFor(r); forwarded != nil {
			peer = forwarded
		}
	}

	switch {
	case peer == nil:
		// A Unix socket without a usable header: one bucket for everyone.
		return "local"
	case peer.To4() != nil:
		return peer.To4().String()
	default:
		return peer.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
}

// lastForwardedFor returns the last address in the X-Forwarded-For headers of
// the request, or nil when there is none or it is not an address.
func lastForwardedFor(r *http.Request) net.IP {
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return nil
	}

	entries := strings.Split(values[len(values)-1], ",")
	last := strings.TrimSpace(entries[len(entries)-1])

	// Some proxies add the port.
	if host, _, err := net.SplitHostPort(last); err == nil {
		last = host
	}

	return net.ParseIP(strings.Trim(last, "[]"))
}
