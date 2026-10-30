package terminalwork

import "time"

// Clock is an injectable time source for retry/lease decisions.
type Clock interface {
	Now() time.Time
}

// ClaimLease coordinates workers, not users.
type ClaimLease struct {
	OwnerID   string
	ExpiresAt time.Time
}

// HeldAt reports whether the lease is still held at now.
func (l ClaimLease) HeldAt(now time.Time) bool {
	return l.OwnerID != "" && now.Before(l.ExpiresAt)
}

// BoundedError is a content-safe work error classification.
type BoundedError struct {
	Code      string
	Permanent bool
	Message   string
}

// RetrySchedule computes deterministic backoff without jitter in the domain.
type RetrySchedule struct {
	Initial    time.Duration
	Multiplier int
	Max        time.Duration
}

// Delay returns the backoff for the given 1-based attempt number.
func (s RetrySchedule) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	mult := max(s.Multiplier, 1)
	d := s.Initial
	for i := 1; i < attempt; i++ {
		next := d * time.Duration(mult)
		if s.Max > 0 && next > s.Max {
			return s.Max
		}
		d = next
	}
	if s.Max > 0 && d > s.Max {
		return s.Max
	}
	return d
}
