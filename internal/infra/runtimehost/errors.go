package runtimehost

import (
	"errors"
	"sync"
	"time"
)

// Sentinel errors for generation publication and lifecycle (req 10.1, 10.8-10.9).
var (
	ErrRetentionBlocked         = errors.New("runtimehost: retained-generation budget exhausted")
	ErrHostShuttingDown         = errors.New("runtimehost: host is shutting down")
	ErrNotPrepared              = errors.New("runtimehost: candidate not prepared")
	ErrAlreadyPublished         = errors.New("runtimehost: generation already published")
	ErrAlreadyClosed            = errors.New("runtimehost: generation already closed")
	ErrIllegalTransition        = errors.New("runtimehost: illegal generation lifecycle transition")
	ErrOwnedAlreadyBound        = errors.New("runtimehost: generation owned resources already bound")
	ErrRequestPlaneAlreadyBound = errors.New("runtimehost: generation request plane already bound")
)

// ManualClock is a fake clock for tests (no timing sleeps).
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the fake clock forward by d.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
