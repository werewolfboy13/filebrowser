package img

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// waitersOf waits until the semaphore has n requests queued.
func waitersOf(t *testing.T, w *weighted, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		queued := len(w.waiters)
		w.mu.Unlock()
		if queued == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the semaphore never had %d requests waiting", n)
}

// acquireSoon is acquire for a request that has no reason to wait. A request
// that waits all the same fails the test here, instead of keeping it waiting
// until the whole test run is cut off.
func acquireSoon(t *testing.T, w *weighted, n int64) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- w.acquire(context.Background(), n) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a request for %d of %d waits although nothing stands in its way", n, w.size)
	}
}

func TestWeightedSemaphore(t *testing.T) {
	ctx := context.Background()
	w := newWeighted(100)

	// What fits is granted at once.
	for _, n := range []int64{60, 40} {
		if err := w.acquire(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	// What does not fit waits, in the order it came: the small request
	// behind the large one does not overtake it.
	order := make(chan string, 2)
	var wg sync.WaitGroup
	for i, rq := range []struct {
		name string
		n    int64
	}{{"large", 90}, {"small", 5}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.acquire(ctx, rq.n); err != nil {
				t.Error(err)
			}
			order <- rq.name
		}()
		waitersOf(t, w, i+1)
	}

	w.release(40) // room for the small one, not for the large one
	select {
	case name := <-order:
		t.Fatalf("%s was granted while the request ahead of it still waits", name)
	case <-time.After(50 * time.Millisecond):
	}
	w.release(60)
	wg.Wait()
	if w.used != 95 {
		t.Errorf("in use after both were granted: %d, want 95", w.used)
	}
	w.release(90)
	w.release(5)

	// A request for more than everything is one for everything. Taken as it
	// is, it could never be granted, and would wait for ever.
	acquireSoon(t, w, 1000)
	if w.used != 100 {
		t.Errorf("in use after a request for more than the capacity: %d", w.used)
	}

	// A request that gives up takes nothing, and lets those behind it go.
	cancelled, cancel := context.WithCancel(ctx)
	gaveUp := make(chan error, 1)
	go func() { gaveUp <- w.acquire(cancelled, 80) }()
	waitersOf(t, w, 1)
	behind := make(chan error, 1)
	go func() { behind <- w.acquire(ctx, 10) }()
	waitersOf(t, w, 2)

	w.release(1000)
	// Now 100 are free and the request for 80 has been granted; give it back
	// and start over with one that is cancelled while it waits.
	if err := <-gaveUp; err != nil {
		t.Fatal(err)
	}
	if err := <-behind; err != nil {
		t.Fatal(err)
	}
	go func() { gaveUp <- w.acquire(cancelled, 80) }() // 90 in use: waits
	waitersOf(t, w, 1)
	go func() { behind <- w.acquire(ctx, 10) }() // would fit, but is behind
	waitersOf(t, w, 2)
	cancel()
	if err := <-gaveUp; !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled request returned %v", err)
	}
	if err := <-behind; err != nil {
		t.Errorf("the request behind a cancelled one returned %v", err)
	}
	if w.used != 100 || len(w.waiters) != 0 {
		t.Errorf("in use %d with %d waiting, want 100 and none", w.used, len(w.waiters))
	}

	// A context that is already done gets nothing when it would have to wait.
	w.release(80)
	w.release(10)
	w.release(10)
	if w.used != 0 {
		t.Fatalf("in use after everything was released: %d", w.used)
	}
	if err := w.acquire(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := w.acquire(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("a request under a finished context returned %v", err)
	}
	if w.used != 100 || len(w.waiters) != 0 {
		t.Errorf("in use %d with %d waiting after a refused request", w.used, len(w.waiters))
	}
}

// Hammering it from many goroutines never hands out more than there is.
func TestWeightedSemaphoreNeverOvercommits(t *testing.T) {
	const size = 64
	w := newWeighted(size)

	var (
		mu      sync.Mutex
		inUse   int64
		highest int64
		wg      sync.WaitGroup
	)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				n := int64(1 + (g*7+i*13)%40)
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+i%5)*time.Millisecond)
				err := w.acquire(ctx, n)
				cancel()
				if err != nil {
					continue
				}
				mu.Lock()
				inUse += n
				highest = max(highest, inUse)
				mu.Unlock()

				mu.Lock()
				inUse -= n
				mu.Unlock()
				w.release(n)
			}
		}()
	}
	wg.Wait()

	if highest > size {
		t.Errorf("%d were in use at once, of %d", highest, size)
	}
	if w.used != 0 || len(w.waiters) != 0 {
		t.Errorf("after everything was released: in use %d, %d waiting", w.used, len(w.waiters))
	}
}

// A request that gives up just as it is granted its share has to hand it
// back: nobody is going to release what the caller was told it did not get.
// Kept, it is gone from the budget for good, and with enough of them every
// image waits for ever.
//
// Which of the two a waiting request sees first, its grant or the end of its
// context, is up to the scheduler, so the moment is made here: with the lock
// of the semaphore held, the context is ended, the request is given the time
// to notice, and its share is then granted the way release grants it.
func TestWeightedSemaphoreHandsBackAGrantItGaveUpOn(t *testing.T) {
	const size, rounds = 100, 200
	w := newWeighted(size)
	handedBack := 0

	for i := 0; i < rounds; i++ {
		acquireSoon(t, w, size)

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- w.acquire(ctx, 60) }()
		waitersOf(t, w, 1)

		w.mu.Lock()
		cancel()
		// Long enough for the request to wake up to its context and come to
		// a halt at the lock.
		time.Sleep(time.Millisecond)
		w.used -= size
		w.grant()
		granted := len(w.waiters) == 0
		w.mu.Unlock()
		if !granted {
			t.Fatal("the waiting request was not granted its share")
		}

		switch err := <-result; {
		case err == nil:
			// It saw the grant first, and holds its share like any other.
			w.release(60)
		case errors.Is(err, context.Canceled):
			handedBack++
		default:
			t.Fatal(err)
		}

		w.mu.Lock()
		used, waiting := w.used, len(w.waiters)
		w.mu.Unlock()
		if used != 0 || waiting != 0 {
			t.Fatalf("round %d: %d of %d in use with %d waiting after a request that gave up as it was granted; want nothing in use", i, used, size, waiting)
		}
	}

	if handedBack == 0 {
		t.Errorf("none of %d requests gave up after it was granted: the test did not get to the case it is for", rounds)
	}

	// Everything is still there to be had.
	acquireSoon(t, w, size)
}
