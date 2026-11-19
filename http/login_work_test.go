package fbhttp

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// setWait changes how long work waits for its turn.
func (g *workGate) setWait(wait time.Duration) {
	g.mu.Lock()
	g.wait = wait
	g.mu.Unlock()
}

// size is the number of groups the gate holds.
func (g *workGate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.groups)
}

func TestWorkGateBoundsTheWorkOfAGroup(t *testing.T) {
	g := newWorkGate(2, 50*time.Millisecond)
	ctx := context.Background()

	first, second := g.enter(ctx, "a"), g.enter(ctx, "a")
	if first == nil || second == nil {
		t.Fatal("work within the bound was turned away")
	}

	// A third has to wait, and is turned away when no turn comes.
	start := time.Now()
	if leave := g.enter(ctx, "a"); leave != nil {
		t.Fatal("a third piece of work was let in while two were under way")
	}
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Errorf("it was turned away after %v, before the wait was over", waited)
	}

	// Another group is not concerned.
	other := g.enter(ctx, "b")
	if other == nil {
		t.Fatal("work of another group was turned away")
	}
	other()

	// Work that waits is let in as soon as there is room: delayed, not
	// refused.
	g.setWait(time.Minute)
	turn := make(chan func(), 1)
	go func() { turn <- g.enter(ctx, "a") }()
	select {
	case <-turn:
		t.Fatal("a third piece of work did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	first() // ending a turn twice gives one place back, not two
	var third func()
	select {
	case third = <-turn:
	case <-time.After(10 * time.Second):
		t.Fatal("work that waited was not let in when there was room")
	}
	if third == nil {
		t.Fatal("work that waited was turned away although there was room in time")
	}
	if leave := func() func() {
		g.setWait(20 * time.Millisecond)
		defer g.setWait(time.Minute)
		return g.enter(ctx, "a")
	}(); leave != nil {
		t.Fatal("ending one turn twice made room for two")
	}

	// Work whose request is gone does not go on waiting.
	cancelled, cancel := context.WithCancel(ctx)
	gone := make(chan func(), 1)
	go func() { gone <- g.enter(cancelled, "a") }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case leave := <-gone:
		if leave != nil {
			t.Error("work whose request ended was let in")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("work whose request ended went on waiting")
	}

	// Nothing is kept of a group once its work is done.
	second()
	third()
	if n := g.size(); n != 0 {
		t.Errorf("the gate holds %d groups with no work under way", n)
	}
}

// One client address could have about a thousand password hashes computed
// at the same time: the failure limiter lets it try five passwords at once
// for each of two hundred names. Each hash costs the better part of a tenth
// of a second of a processor.
//
// Only a few logins of one address are now worked on at a time. The others
// wait their turn, and are turned away, with 429, when it does not come in
// time. Other addresses are not kept waiting, and neither is a login of the
// same address for longer than it takes the ones before it.
func TestLoginsOfOneAddressAreWorkedOnAFewAtATime(t *testing.T) {
	e := newFullEnv(t)
	w := e.watchUsers()
	const flooding, elsewhere = "198.51.100.7", "203.0.113.9"

	// Logins as bob are held where the account has been read and the
	// password is about to be compared: at work.
	var atWork, most atomic.Int32
	release := make(chan struct{})
	var released sync.Once
	letGo := func() { released.Do(func() { close(release) }) }
	defer letGo() // also when the test gives up: the server waits for its requests
	after := func(id interface{}) {
		if id != interface{}("bob") {
			return
		}
		n := atWork.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		atWork.Add(-1)
	}
	w.afterGet.Store(&after)

	// As many as the failure limiter lets one name have under way at once.
	const logins = limiterFreeFailures
	done := make(chan int, logins)
	for i := 0; i < logins; i++ {
		go func() {
			code, _ := e.loginFrom(flooding, "bob", fullEnvUserPassword)
			done <- code
		}()
	}
	deadline := time.Now().Add(30 * time.Second)
	for atWork.Load() < loginWorkPerAddress {
		if time.Now().After(deadline) {
			t.Fatalf("%d logins are at work, want %d", atWork.Load(), loginWorkPerAddress)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Another address is served while they are.
	if code, _ := e.loginFrom(elsewhere, "admin", fullEnvAdminPassword); code != http.StatusOK {
		t.Errorf("a login from another address while one address is at its bound = %d, want 200", code)
	}

	// One more from the address that is at its bound waits, and is turned
	// away when its turn does not come: with the right password too.
	e.limits.loginWork.setWait(100 * time.Millisecond)
	code, h := e.loginFrom(flooding, "admin", fullEnvAdminPassword)
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" {
		t.Errorf("a login beyond the bound of its address = %d (Retry-After %q), want 429", code, h.Get("Retry-After"))
	}

	// It was not counted as a wrong password, and with time to wait it is
	// served as soon as the ones before it are done.
	e.limits.loginWork.setWait(time.Minute)
	waiting := make(chan int, 1)
	go func() {
		code, _ := e.loginFrom(flooding, "admin", fullEnvAdminPassword)
		waiting <- code
	}()
	select {
	case code := <-waiting:
		t.Errorf("a login beyond the bound of its address was answered (%d) without waiting", code)
		waiting <- code
	case <-time.After(150 * time.Millisecond):
	}

	letGo()
	select {
	case code := <-waiting:
		if code != http.StatusOK {
			t.Errorf("the login that waited = %d, want 200", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the login that waited was never answered")
	}
	for i := 0; i < logins; i++ {
		if code := <-done; code != http.StatusOK {
			t.Errorf("a login that was held = %d, want 200", code)
		}
	}

	if got := most.Load(); got != loginWorkPerAddress {
		t.Errorf("%d logins of one address were at work at the same time, want %d", got, loginWorkPerAddress)
	}
	if n := e.limits.loginWork.size(); n != 0 {
		t.Errorf("the gate holds %d addresses with no login under way", n)
	}
}
