package img

import (
	"context"
	"sync"
)

// weighted is a semaphore whose holders take up different amounts of it. It
// hands out its capacity in the order it was asked for: a large request at
// the head of the queue is not overtaken by smaller ones behind it, so it
// gets its turn however busy the queue is.
type weighted struct {
	mu      sync.Mutex
	size    int64
	used    int64
	waiters []*weightedWaiter
}

type weightedWaiter struct {
	n     int64
	ready chan struct{}
}

func newWeighted(size int64) *weighted {
	return &weighted{size: size}
}

// acquire takes n of the capacity, waiting for it if it has to. It returns
// the error of ctx, and takes nothing, when ctx is done first. A request for
// more than the whole capacity is one for all of it.
func (w *weighted) acquire(ctx context.Context, n int64) error {
	n = max(0, min(n, w.size))

	w.mu.Lock()
	if w.size-w.used >= n && len(w.waiters) == 0 {
		w.used += n
		w.mu.Unlock()
		return nil
	}

	waiter := &weightedWaiter{n: n, ready: make(chan struct{})}
	w.waiters = append(w.waiters, waiter)
	w.mu.Unlock()

	select {
	case <-waiter.ready:
		return nil
	case <-ctx.Done():
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	select {
	case <-waiter.ready:
		// It was granted just as ctx ended: hand it back.
		w.used -= n
	default:
		for i, other := range w.waiters {
			if other == waiter {
				w.waiters = append(w.waiters[:i], w.waiters[i+1:]...)
				break
			}
		}
	}
	// Either way those behind may fit now.
	w.grant()

	return ctx.Err()
}

// release gives back n of the capacity.
func (w *weighted) release(n int64) {
	n = max(0, min(n, w.size))

	w.mu.Lock()
	w.used -= n
	w.grant()
	w.mu.Unlock()
}

// grant lets the waiters at the head of the queue go for as long as they fit.
// It is called with the lock held.
func (w *weighted) grant() {
	for len(w.waiters) > 0 {
		next := w.waiters[0]
		if w.size-w.used < next.n {
			// The rest wait behind it, also those that would fit: letting
			// them pass is what would keep a large request waiting for ever.
			return
		}
		w.used += next.n
		w.waiters = w.waiters[1:]
		close(next.ready)
	}
}
