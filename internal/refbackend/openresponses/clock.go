package openresponses

import (
	"sync"
	"time"
)

// Clock provides deterministic time for the emulator server.
type Clock interface {
	Now() time.Time
}

// VirtualClock is a deterministic, goroutine-safe clock for emulator tests.
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
