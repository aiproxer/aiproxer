package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// LeaseState is the durable occupancy state of a concurrency lease.
type LeaseState string

const (
	LeaseStateActive   LeaseState = "active"
	LeaseStateExpiring LeaseState = "expiring"
	LeaseStateExpired  LeaseState = "expired"
	LeaseStateReleased LeaseState = "released"
)

// IsKnown reports whether s is a documented lease state.
func (s LeaseState) IsKnown() bool {
	switch s {
	case LeaseStateActive, LeaseStateExpiring, LeaseStateExpired, LeaseStateReleased:
		return true
	default:
		return false
	}
}

// Sentinel domain errors for lease state transitions.
var (
	ErrGenerationMismatch = errors.New("concurrencyauthority: generation mismatch")
	ErrLeaseNotActive     = errors.New("concurrencyauthority: lease not active")
	ErrLeaseReleased      = errors.New("concurrencyauthority: lease released")
	ErrLeaseExpired       = errors.New("concurrencyauthority: lease expired")
)

// Lease is one logical-request occupancy record.
type Lease struct {
	LeaseID         string
	RuleID          string
	RuleVersion     string
	Namespace       string
	Dimensions      Dimensions
	LogicalID       string
	HolderID        string
	AcquiredAt      time.Time
	RenewedAt       time.Time
	ExpiresAt       time.Time
	ReleasedAt      time.Time
	Generation      int64
	State           LeaseState
	IdentityVersion int
	SetID           string
	SetGeneration   int64
	SetState        LeaseSetState
}

// NewLeaseParams constructs an active lease at Now with TTL.
type NewLeaseParams struct {
	LeaseID     string
	RuleID      string
	RuleVersion string
	LogicalID   string
	HolderID    string
	Namespace   string
	Dimensions  Dimensions
	Now         time.Time
	TTL         time.Duration
}

// NewLease creates an active lease with generation 1.
func NewLease(p NewLeaseParams) Lease {
	ttl := p.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	return Lease{
		LeaseID:     p.LeaseID,
		RuleID:      p.RuleID,
		RuleVersion: p.RuleVersion,
		Namespace:   p.Namespace,
		Dimensions:  p.Dimensions,
		LogicalID:   p.LogicalID,
		HolderID:    p.HolderID,
		AcquiredAt:  p.Now,
		RenewedAt:   p.Now,
		ExpiresAt:   p.Now.Add(ttl),
		Generation:  1,
		State:       LeaseStateActive,
	}
}

// StableLeaseID returns lease_id = stable(namespace, rule_id, rule_version, logical_request_id, principal/scope).
func StableLeaseID(namespace, ruleID, ruleVersion, logicalRequestID string, dims Dimensions) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s|%s", namespace, ruleID, ruleVersion, logicalRequestID, dims.Key()))
	return "cls_" + hex.EncodeToString(sum[:16])
}

// IsLive reports whether the lease still occupies capacity at now.
func (l Lease) IsLive(now time.Time) bool {
	switch l.State {
	case LeaseStateReleased, LeaseStateExpired:
		return false
	default:
		// Uncertain set members remain occupied until reconciliation (10.7).
		if l.SetState == LeaseSetStateUncertain {
			return true
		}
		if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
			return false
		}
		return l.State == LeaseStateActive || l.State == LeaseStateExpiring || l.State == ""
	}
}

// EffectiveState projects wall-clock expiry onto the stored state.
func (l Lease) EffectiveState(now time.Time) LeaseState {
	if l.State == LeaseStateReleased {
		return LeaseStateReleased
	}
	if l.State == LeaseStateExpired {
		return LeaseStateExpired
	}
	if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
		return LeaseStateExpired
	}
	if l.State == "" {
		return LeaseStateActive
	}
	return l.State
}

// Release marks the lease released. Idempotent.
func (l *Lease) Release(now time.Time) {
	if l == nil {
		return
	}
	if l.State == LeaseStateReleased {
		return
	}
	l.State = LeaseStateReleased
	l.ReleasedAt = now
}

// Expire marks the lease expired. No-op when already released/expired.
func (l *Lease) Expire(now time.Time) {
	if l == nil {
		return
	}
	if l.State == LeaseStateReleased || l.State == LeaseStateExpired {
		return
	}
	l.State = LeaseStateExpired
	if l.ExpiresAt.IsZero() || l.ExpiresAt.After(now) {
		l.ExpiresAt = now
	}
}
