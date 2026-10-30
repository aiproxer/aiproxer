package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// LeaseSetState is the terminal/occupancy state of one atomic lease set.
type LeaseSetState string

const (
	LeaseSetStateActive    LeaseSetState = "active"
	LeaseSetStateUncertain LeaseSetState = "uncertain"
	LeaseSetStateExpiring  LeaseSetState = "expiring"
	LeaseSetStateReleased  LeaseSetState = "released"
	LeaseSetStateFailed    LeaseSetState = "failed"
)

// IsKnown reports whether s is a documented lease-set state.
func (s LeaseSetState) IsKnown() bool {
	switch s {
	case LeaseSetStateActive, LeaseSetStateUncertain, LeaseSetStateExpiring,
		LeaseSetStateReleased, LeaseSetStateFailed:
		return true
	default:
		return false
	}
}

// IdentityVersionLeaseSet marks rows that participate in atomic lease sets.
const IdentityVersionLeaseSet = 2

// ErrInvalidTiming is returned when renew_before/lease_ttl violate 10.1.
var ErrInvalidTiming = errors.New("concurrencyauthority: invalid lease timing")

// ErrIncompleteSet is returned when a set mutation does not cover all members.
var ErrIncompleteSet = errors.New("concurrencyauthority: incomplete lease set")

// ErrUncertainOccupancy marks conservatively occupied capacity after ambiguous renew.
var ErrUncertainOccupancy = errors.New("concurrencyauthority: uncertain occupancy")

// LeaseSet is one atomic multi-rule occupancy for a logical request.
type LeaseSet struct {
	SetID       string
	RequestID   string
	Generation  int64
	State       LeaseSetState
	Members     []Lease
	AcquiredAt  time.Time
	RenewedAt   time.Time
	ExpiresAt   time.Time
	ReleasedAt  time.Time
	RenewBefore time.Duration
	TTL         time.Duration
}

// OccupiesCapacity reports whether the set still consumes slots at now.
func (s LeaseSet) OccupiesCapacity(now time.Time) bool {
	switch s.State {
	case LeaseSetStateReleased, LeaseSetStateFailed:
		return false
	case LeaseSetStateUncertain:
		return true
	default:
		if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) && s.State != LeaseSetStateUncertain {
			return false
		}
		return s.State == LeaseSetStateActive || s.State == LeaseSetStateExpiring || s.State == ""
	}
}

// MemberRuleIDs returns sorted unique member rule IDs.
func (s LeaseSet) MemberRuleIDs() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(s.Members))
	for _, m := range s.Members {
		id := strings.TrimSpace(m.RuleID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// StableSetID returns set_id = stable(namespace, request, sorted rule ids).
func StableSetID(namespace, requestID string, ruleIDs []string) string {
	ids := append([]string(nil), ruleIDs...)
	sort.Strings(ids)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s", namespace, requestID, strings.Join(ids, ",")))
	return "cset_" + hex.EncodeToString(sum[:16])
}

// SortedRuleIDs returns a deterministically ordered copy of rule IDs.
func SortedRuleIDs(ruleIDs []string) []string {
	out := append([]string(nil), ruleIDs...)
	sort.Strings(out)
	return out
}

// ValidateTiming enforces 0 < renew_before < lease_ttl with bounded practical values (10.1).
func ValidateTiming(leaseTTL, renewBefore time.Duration) error {
	if leaseTTL <= 0 {
		return fmt.Errorf("%w: lease_ttl must be > 0", ErrInvalidTiming)
	}
	if renewBefore <= 0 {
		return fmt.Errorf("%w: renew_before must be > 0", ErrInvalidTiming)
	}
	if renewBefore >= leaseTTL {
		return fmt.Errorf("%w: renew_before must be < lease_ttl", ErrInvalidTiming)
	}
	const maxTTL = 24 * time.Hour
	const maxRenew = 12 * time.Hour
	if leaseTTL > maxTTL {
		return fmt.Errorf("%w: lease_ttl exceeds %s", ErrInvalidTiming, maxTTL)
	}
	if renewBefore > maxRenew {
		return fmt.Errorf("%w: renew_before exceeds %s", ErrInvalidTiming, maxRenew)
	}
	return nil
}

// ValidateSetDecisionShape checks external set admit/renew occupancy (10.2).
func ValidateSetDecisionShape(setID string, generation int64, expiresAt time.Time, ttl, renewBefore time.Duration, memberCount int) error {
	if strings.TrimSpace(setID) == "" {
		return fmt.Errorf("concurrencyauthority: set_id required")
	}
	if generation <= 0 {
		return fmt.Errorf("concurrencyauthority: set generation required")
	}
	if expiresAt.IsZero() {
		return fmt.Errorf("concurrencyauthority: set expires_at required")
	}
	if memberCount <= 0 {
		return fmt.Errorf("%w: empty set members", ErrIncompleteSet)
	}
	return ValidateTiming(ttl, renewBefore)
}

// MarkUncertain transitions an active/expiring set to uncertain (still occupies capacity).
func (s *LeaseSet) MarkUncertain(now time.Time) error {
	if s == nil {
		return ErrLeaseNotActive
	}
	switch s.State {
	case LeaseSetStateReleased, LeaseSetStateFailed:
		return ErrLeaseReleased
	}
	s.State = LeaseSetStateUncertain
	s.RenewedAt = now
	for i := range s.Members {
		s.Members[i].State = LeaseStateExpiring
	}
	return nil
}
