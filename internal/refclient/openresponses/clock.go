package openresponses

import (
	"fmt"
	"sync"
	"time"
)

// Clock provides deterministic time for the emulator.
type Clock interface {
	Now() time.Time
}

// VirtualClock is a deterministic, goroutine-safe clock for client emulator tests.
type VirtualClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewClock returns a VirtualClock seeded to a fixed reference instant.
func NewClock(initial time.Time) *VirtualClock {
	if initial.IsZero() {
		initial = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	}
	return &VirtualClock{now: initial}
}

// Now returns the current virtual time.
func (c *VirtualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the virtual clock forward deterministically.
func (c *VirtualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set pins the virtual clock to an exact instant.
func (c *VirtualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// IDGenerator deterministically issues client-owned identifiers.
type IDGenerator struct {
	mu     sync.Mutex
	clock  Clock
	seq    int64
	prefix string
}

// NewIDGenerator returns a deterministic ID generator.
func NewIDGenerator(prefix string, clock Clock) *IDGenerator {
	if clock == nil {
		clock = NewClock(time.Time{})
	}
	return &IDGenerator{clock: clock, prefix: prefix}
}

// Next returns a deterministic identifier derived from virtual time and a counter.
func (g *IDGenerator) Next() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	return fmt.Sprintf("%s_%d_%d", g.prefix, g.clock.Now().UnixNano(), g.seq)
}
