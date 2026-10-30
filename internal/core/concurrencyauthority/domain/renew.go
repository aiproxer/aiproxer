package domain

import (
	"errors"
	"time"
)

// Renew extends ExpiresAt using generation CAS. Released/expired leases cannot
// be resurrected (requirements 10.7, 10.8).
func (l *Lease) Renew(now time.Time, expectedGen int64, ttl time.Duration) error {
	if l == nil {
		return ErrLeaseNotActive
	}
	switch l.State {
	case LeaseStateReleased:
		return ErrLeaseReleased
	case LeaseStateExpired:
		return ErrLeaseExpired
	}
	if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
		return ErrLeaseExpired
	}
	if l.State != LeaseStateActive && l.State != LeaseStateExpiring && l.State != "" {
		return ErrLeaseNotActive
	}
	if l.Generation != expectedGen {
		return ErrGenerationMismatch
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	l.RenewedAt = now
	l.ExpiresAt = now.Add(ttl)
	l.Generation++
	l.State = LeaseStateActive
	return nil
}

// IsCASError reports whether err is a renew CAS / terminal-state rejection.
func IsCASError(err error) bool {
	return errors.Is(err, ErrGenerationMismatch) ||
		errors.Is(err, ErrLeaseReleased) ||
		errors.Is(err, ErrLeaseExpired) ||
		errors.Is(err, ErrLeaseNotActive)
}
