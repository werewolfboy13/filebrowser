package fbhttp

import (
	"context"
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

	// limiterMaxKeys bounds the memory of a limiter. See add for what
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

	// groups holds the keys of each group, for as long as it has any.
	groups map[string]*limiterGroup

	// noRoomUntil is set when the limiter was full and looking through it
	// found no key to forget: it is not looked through again before then.
	noRoomUntil time.Time

	free         int
	base         time.Duration
	maxDelay     time.Duration
	forget       time.Duration
	maxKeys      int
	maxGroupKeys int
	now          func() time.Time
}

// limiterGroup is the keys that count against one bound: those of one client
// address, for the login.
type limiterGroup struct {
	name string
	keys map[[sha256.Size]byte]*failureState

	// failures, blockedUntil and until are what is left of the keys that had
	// to make room: the highest count among them, the latest wait, and when
	// the last of them would have been forgotten anyway. A new key of the
	// group starts out with them. See evict.
	failures     int
	blockedUntil time.Time
	until        time.Time
}

type failureState struct {
	group    *limiterGroup
	failures int
	inFlight int

	// failed tells that an attempt for the key itself has failed. Until
	// then, all it has on it is what its group had left: see evict.
	failed bool

	lastFailure  time.Time
	blockedUntil time.Time
}

func newFailureLimiter() *failureLimiter {
	return &failureLimiter{
		keys:         map[[sha256.Size]byte]*failureState{},
		groups:       map[string]*limiterGroup{},
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
// the client address for a login. See add.
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

	if state == nil {
		var wait time.Duration
		if state, wait = l.add(group, key, now); state == nil {
			return nil, wait
		}
	}

	if wait := state.blockedUntil.Sub(now); wait > 0 {
		return nil, wait
	}
	// Past the free failures, one attempt at a time; before that, as many
	// as are left of them.
	if allowed := max(1, l.free-state.failures); state.inFlight >= allowed {
		return nil, l.base
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

// drop removes a key, and its group with the last of its keys.
func (l *failureLimiter) drop(key [sha256.Size]byte, state *failureState) {
	delete(l.keys, key)
	delete(state.group.keys, key)
	if len(state.group.keys) == 0 && l.groups[state.group.name] == state.group {
		delete(l.groups, state.group.name)
	}
}

func (l *failureLimiter) end(key [sha256.Size]byte, state *failureState, outcome attemptOutcome) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state.inFlight--
	if l.keys[key] != state {
		// Dropped in the meantime, by a success of another attempt. (Never to
		// make room: a key with an attempt in flight stays.)
		return
	}

	switch outcome {
	case attemptSucceeded:
		l.drop(key, state)
		return
	case attemptAborted:
		// A key that was only created for this attempt has nothing to keep.
		if !state.failed && state.inFlight == 0 {
			l.drop(key, state)
		}
		return
	}

	now := l.now()
	state.failed = true
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

// add makes a new key of group, and room for it when the limiter holds
// maxKeys keys already, or the group maxGroupKeys. It returns nil, and how
// long the attempt has to wait, when there is none to be made.
//
// Room is made by forgetting a key before its time: see evict for what that
// costs, and why it is not a way to start a count over. Which key goes is
// chosen so that it costs the least:
//
//   - a key that is waiting stays for as long as one is left that is not: a
//     wait is what the limiter is for;
//   - of those that are not waiting, the one with the fewest failures goes
//     first, and of those with as many, the one that failed the longest ago;
//   - a key with an attempt in flight stays.
//
// When the group is full, the key that goes is one of its own, and when all
// of them are waiting (or in flight), the new key is refused: a single
// address that is full of failures only ever costs that address. Users who
// share an address with such a client (see clientAddr) share that.
//
// When the limiter is full, the key that goes is one of the group that holds
// the most, whoever is asking: filling the limiter takes many keys, and they
// are the first to go. A group is never refused because of the keys of the
// others. It used to be: the limiter failed closed, and failing under 200
// names from each of 50 addresses kept everybody else from logging in, with
// the right password too, for as long as it was kept up. Only when every
// other key in the limiter is in flight is there nothing to forget.
func (l *failureLimiter) add(group string, key [sha256.Size]byte, now time.Time) (*failureState, time.Duration) {
	g := l.groups[group]
	if g != nil {
		if !now.Before(g.until) {
			// What its evicted keys left behind is forgotten by now, as
			// they would have been.
			g.failures, g.blockedUntil = 0, time.Time{}
		}
		// A key of this group that was waiting had to make room. Its wait
		// goes for every key the group has not got: one of them is that key.
		if wait := g.blockedUntil.Sub(now); wait > 0 {
			return nil, wait
		}
		if len(g.keys) >= l.maxGroupKeys {
			if wait := l.makeRoomIn(g, now); wait > 0 {
				return nil, wait
			}
		}
	}
	if len(l.keys) >= l.maxKeys {
		if wait := l.makeRoom(now); wait > 0 {
			return nil, wait
		}
	}

	if g == nil {
		g = &limiterGroup{name: group, keys: map[[sha256.Size]byte]*failureState{}}
	}
	// Also when making room has just removed the last key of the group, and
	// with it the group: it keeps what it remembers.
	l.groups[group] = g

	state := &failureState{group: g, failures: g.failures, lastFailure: now}
	l.keys[key] = state
	g.keys[key] = state
	return state, 0
}

// evictable reports whether a key may be forgotten to make room, and whether
// it is waiting.
func (l *failureLimiter) evictable(state *failureState, now time.Time) (ok, waiting bool) {
	return state.inFlight == 0, state.blockedUntil.After(now)
}

// makeRoomIn makes room for one more key in a group that is full. It returns
// 0 when there is room, and otherwise how long until a key of the group can
// be forgotten.
func (l *failureLimiter) makeRoomIn(g *limiterGroup, now time.Time) time.Duration {
	var (
		victimKey [sha256.Size]byte
		victim    *failureState
		soonest   time.Duration
	)
	for key, state := range g.keys {
		if l.forgotten(state, now) {
			l.drop(key, state)
			continue
		}
		ok, waiting := l.evictable(state, now)
		switch {
		case !ok:
		case waiting:
			if left := state.blockedUntil.Sub(now); soonest == 0 || left < soonest {
				soonest = left
			}
		case victim == nil || state.failures < victim.failures ||
			(state.failures == victim.failures && state.lastFailure.Before(victim.lastFailure)):
			victimKey, victim = key, state
		}
	}
	switch {
	case len(g.keys) < l.maxGroupKeys:
		return 0
	case victim != nil:
		l.evict(victimKey, victim)
		return 0
	}

	// Not longer than any other wait, and not nothing.
	return min(max(soonest, l.base), l.maxDelay)
}

// makeRoom makes room for one more key in a limiter that is full. It returns
// 0 when there is room, and otherwise how long to wait.
//
// It looks at every key, which is only worth it for an attempt that is going
// to be made. When that found nothing to forget, the attempts that follow are
// refused without looking again, for a moment.
func (l *failureLimiter) makeRoom(now time.Time) time.Duration {
	if wait := l.noRoomUntil.Sub(now); wait > 0 {
		return wait
	}

	var (
		victimKey     [sha256.Size]byte
		victim        *failureState
		victimWaiting bool
	)
	for key, state := range l.keys {
		if l.forgotten(state, now) {
			l.drop(key, state)
			continue
		}
		ok, waiting := l.evictable(state, now)
		if !ok {
			continue
		}
		if victim != nil {
			// The group with the most keys first; in it, a key that is not
			// waiting before one that is; then the fewest failures, and
			// the oldest.
			switch a, b := len(state.group.keys), len(victim.group.keys); {
			case a != b:
				if a < b {
					continue
				}
			case waiting != victimWaiting:
				if waiting {
					continue
				}
			case state.failures != victim.failures:
				if state.failures > victim.failures {
					continue
				}
			case !state.lastFailure.Before(victim.lastFailure):
				continue
			}
		}
		victimKey, victim, victimWaiting = key, state, waiting
	}
	switch {
	case len(l.keys) < l.maxKeys:
		return 0
	case victim != nil:
		l.evict(victimKey, victim)
		return 0
	}

	l.noRoomUntil = now.Add(l.base)
	return l.base
}

// evict forgets a key before its time, to make room.
//
// Forgetting a key forgets its count, and that is something a client can
// bring about: by failing under one throw-away name after the other until the
// key it is really after has to go. The key would then start over, with its
// free failures back and its wait gone. A single address could guess at an
// account without ever waiting longer than the first, shortest wait.
//
// So the count is not forgotten with the key. The group keeps the highest
// count of the keys it lost, and, when one of them was waiting, until when:
// a new key of the group starts out with that count, and none is made before
// that wait is over. The key that had to go is back where it was as soon as
// it is used again, and so, at worst, is every other new key of that group,
// which is the address the failures came from. It is kept for as long as the
// key itself would have been: until forget after its last failure, or until
// the group has no key left.
func (l *failureLimiter) evict(key [sha256.Size]byte, state *failureState) {
	g := state.group
	g.failures = max(g.failures, state.failures)
	if state.blockedUntil.After(g.blockedUntil) {
		g.blockedUntil = state.blockedUntil
	}
	if until := state.lastFailure.Add(l.forget); until.After(g.until) {
		g.until = until
	}
	l.drop(key, state)
}

// Defaults of the gate in front of the work a login costs.
const (
	// loginWorkPerAddress is how many logins of one client address are
	// worked on at a time: compared with a password hash, or handed to the
	// hook. A hash takes the better part of a tenth of a second of one
	// processor, so this is also how many processors one address can keep
	// busy.
	loginWorkPerAddress = 4

	// loginWorkWait is how long a login waits for its turn before it is
	// turned away. It is long next to the time a login takes, so that the
	// users behind one address (an office, a proxy that hides its clients)
	// who log in at the same moment are served one after the other and
	// never notice, and short enough not to hold a connection for long.
	loginWorkWait = 10 * time.Second
)

// workGate bounds how much work is under way at a time for each group: for
// each client address, in front of the login.
//
// The failure limiter does not do that. It lets an address try a few
// passwords at once for each of a couple of hundred user names, which is
// about a thousand password hashes under way at the same time, for as long
// as the address keeps them coming. They are cheap to ask for and costly to
// compute.
//
// Work beyond the bound waits for its turn, in the order it came, and is
// only turned away when the turn does not come within wait. The wait is on
// the address that causes it: whoever shares an address with a flood of
// logins waits in the same line, and is delayed, but the line moves, and no
// other address is kept waiting.
//
// What it holds is in proportion to the requests that are under way: a
// group is forgotten when the last of them leaves.
type workGate struct {
	mu     sync.Mutex
	groups map[string]*workGroup
	slots  int
	wait   time.Duration
}

type workGroup struct {
	// slots holds one value for each piece of work that is under way.
	slots chan struct{}

	// users counts who is in the group: at work or waiting.
	users int
}

func newWorkGate(slots int, wait time.Duration) *workGate {
	return &workGate{groups: map[string]*workGroup{}, slots: slots, wait: wait}
}

// enter waits for a turn in group. It returns the function that ends the
// turn, which must be called, once; or nil when the turn did not come in
// time or ctx ended first, and the work must not be done.
func (g *workGate) enter(ctx context.Context, group string) (leave func()) {
	g.mu.Lock()
	gr := g.groups[group]
	if gr == nil {
		gr = &workGroup{slots: make(chan struct{}, g.slots)}
		g.groups[group] = gr
	}
	gr.users++
	wait := g.wait
	g.mu.Unlock()

	forget := func() {
		g.mu.Lock()
		gr.users--
		if gr.users == 0 {
			delete(g.groups, group)
		}
		g.mu.Unlock()
	}

	select {
	case gr.slots <- struct{}{}:
	default:
		// Those who wait on a channel get their turn in the order they
		// came.
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case gr.slots <- struct{}{}:
		case <-timer.C:
			forget()
			return nil
		case <-ctx.Done():
			forget()
			return nil
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-gr.slots
			forget()
		})
	}
}

// limits holds the failure limiters of one handler.
type limits struct {
	login *failureLimiter
	share *failureLimiter

	// password counts wrong answers to "what is your current password", the
	// question the users API asks before it changes an account: see
	// confirmPassword.
	password *failureLimiter

	// loginWork bounds the logins of one client address that are worked on
	// at a time.
	loginWork *workGate
}

func newLimits() *limits {
	return &limits{
		login:     newFailureLimiter(),
		share:     newFailureLimiter(),
		password:  newFailureLimiter(),
		loginWork: newWorkGate(loginWorkPerAddress, loginWorkWait),
	}
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
