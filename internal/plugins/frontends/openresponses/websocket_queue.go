package openresponses

import "sync"

// wsByteBudget is the per-session queued-byte gate. The session read pump
// reserves the byte size of each buffered turn envelope before it can place the
// message, and the session pump releases the bytes after consuming it, so the
// total turn payload buffered in the session queue never exceeds maxBytes.
//
// The budget is owned by the session: Run consumes it, the read pump produces
// it, and close() broadcasts so a producer waiting on a full budget is always
// released when the session terminates — no goroutine or timer outlives Run.
type wsByteBudget struct {
	mu        sync.Mutex
	cond      *sync.Cond
	maxBytes  int64
	usedBytes int64
	closed    bool
}

// newWSByteBudget creates a byte budget. The config validator rejects a bound
// below the one-message floor and the handler clamps programmatic overrides, so
// the bound passed here is already sane; reserve()'s empty-queue guard keeps an
// oversized envelope from deadlocking the read pump on any programmatic path.
func newWSByteBudget(maxBytes int64) *wsByteBudget {
	b := &wsByteBudget{maxBytes: maxBytes}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// reserve blocks until size bytes fit the budget or the budget closes. It
// returns false when the budget is closed. A single in-hand message always fits
// an empty queue, so an oversized envelope can never deadlock the read pump.
func (b *wsByteBudget) reserve(size int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.usedBytes > 0 && b.usedBytes+size > b.maxBytes && !b.closed {
		b.cond.Wait()
	}
	if b.closed {
		return false
	}
	b.usedBytes += size
	return true
}

// release returns size bytes to the budget and wakes one waiting producer.
func (b *wsByteBudget) release(size int64) {
	b.mu.Lock()
	b.usedBytes -= size
	if b.usedBytes < 0 {
		b.usedBytes = 0
	}
	b.cond.Signal()
	b.mu.Unlock()
}

// close marks the budget closed and releases every blocked producer.
func (b *wsByteBudget) close() {
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// buffered reports the currently reserved bytes. It is observability for tests.
func (b *wsByteBudget) buffered() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usedBytes
}
