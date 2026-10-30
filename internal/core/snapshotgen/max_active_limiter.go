package snapshotgen

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// maxActiveLimiter enforces MaxActiveRequests for one executable generation.
type maxActiveLimiter struct {
	limit int
	inner authority.ConcurrencyProvider
	desc  authority.ProviderDescriptor

	mu     sync.Mutex
	active map[string]leaseSlot
	seq    atomic.Int64
}

type leaseSlot struct {
	leaseID    string
	generation int64
	expiresAt  time.Time
}

func newMaxActiveLimiter(limit int, inner authority.ConcurrencyProvider, desc authority.ProviderDescriptor) *maxActiveLimiter {
	if limit <= 0 {
		return nil
	}
	if desc.ID == "" {
		desc.ID = "generation-concurrency"
		desc.Kind = authority.ProviderKindAuthority
		desc.Postures = []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}}
	}
	return &maxActiveLimiter{
		limit:  limit,
		inner:  inner,
		desc:   desc,
		active: make(map[string]leaseSlot),
	}
}

func (l *maxActiveLimiter) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	if l == nil {
		return authority.LeaseDecision{}, fmt.Errorf("snapshotgen: nil max-active limiter")
	}
	reqID := strings.TrimSpace(in.RequestID)
	if reqID == "" {
		return authority.LeaseDecision{}, fmt.Errorf("snapshotgen: empty request id")
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reapLocked(now)
	if slot, ok := l.active[reqID]; ok {
		return authority.LeaseDecision{
			Kind:            authority.LeaseAllow,
			LeaseID:         slot.leaseID,
			Generation:      slot.generation,
			ExpiresAt:       slot.expiresAt,
			RemainingSlots:  l.limit - len(l.active),
			RenewBefore:     ttl / 3,
			TTL:             ttl,
			FailureBehavior: authority.FailureFailClosed,
		}, nil
	}
	if len(l.active) >= l.limit {
		return authority.LeaseDecision{
			Kind:           authority.LeaseDeny,
			RemainingSlots: 0,
			Readiness:      authority.ReadinessReady,
		}, nil
	}
	if l.inner != nil {
		ld, err := l.inner.AdmitLease(ctx, in)
		if err != nil {
			return ld, err
		}
		if ld.Kind == authority.LeaseDeny {
			return ld, nil
		}
		if ld.LeaseID == "" {
			ld.LeaseID = fmt.Sprintf("gen-lease-%d", l.seq.Add(1))
		}
		if ld.Generation == 0 {
			ld.Generation = 1
		}
		if ld.ExpiresAt.IsZero() {
			ld.ExpiresAt = now.Add(ttl)
		}
		if ld.TTL <= 0 {
			ld.TTL = ttl
		}
		if ld.RenewBefore <= 0 {
			ld.RenewBefore = ttl / 3
		}
		if ld.FailureBehavior == "" {
			ld.FailureBehavior = authority.FailureFailClosed
		}
		l.active[reqID] = leaseSlot{leaseID: ld.LeaseID, generation: ld.Generation, expiresAt: ld.ExpiresAt}
		ld.RemainingSlots = l.limit - len(l.active)
		return ld, nil
	}
	leaseID := fmt.Sprintf("gen-lease-%d", l.seq.Add(1))
	exp := now.Add(ttl)
	l.active[reqID] = leaseSlot{leaseID: leaseID, generation: 1, expiresAt: exp}
	return authority.LeaseDecision{
		Kind:            authority.LeaseAllow,
		LeaseID:         leaseID,
		Generation:      1,
		ExpiresAt:       exp,
		RemainingSlots:  l.limit - len(l.active),
		RenewBefore:     ttl / 3,
		TTL:             ttl,
		FailureBehavior: authority.FailureFailClosed,
	}, nil
}

func (l *maxActiveLimiter) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if l == nil {
		return authority.LeaseDecision{}, fmt.Errorf("snapshotgen: nil max-active limiter")
	}
	if l.inner != nil {
		return l.inner.RenewLease(ctx, in)
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for reqID, slot := range l.active {
		if slot.leaseID != in.LeaseID {
			continue
		}
		if in.ExpectedGeneration != 0 && slot.generation != in.ExpectedGeneration {
			return authority.LeaseDecision{Kind: authority.LeaseDeny}, nil
		}
		slot.generation++
		slot.expiresAt = now.Add(ttl)
		l.active[reqID] = slot
		return authority.LeaseDecision{
			Kind:            authority.LeaseAllow,
			LeaseID:         slot.leaseID,
			Generation:      slot.generation,
			ExpiresAt:       slot.expiresAt,
			RemainingSlots:  l.limit - len(l.active),
			RenewBefore:     ttl / 3,
			TTL:             ttl,
			FailureBehavior: authority.FailureFailClosed,
		}, nil
	}
	return authority.LeaseDecision{Kind: authority.LeaseDeny}, nil
}

func (l *maxActiveLimiter) ReleaseLease(ctx context.Context, in authority.LeaseRelease) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for reqID, slot := range l.active {
		if slot.leaseID == in.LeaseID || (in.RequestID != "" && reqID == in.RequestID) {
			delete(l.active, reqID)
			break
		}
	}
	if l.inner != nil {
		// Request-coordinator and compensation callers hand release a
		// detached, deadline-bounded cleanup context. Keep that bound instead
		// of substituting context.Background(): an unbounded inner release
		// would block this cleanup and every admission waiting on l.mu past
		// every configured cleanup deadline.
		return l.inner.ReleaseLease(ctx, in)
	}
	return nil
}

func (l *maxActiveLimiter) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func (l *maxActiveLimiter) reapLocked(now time.Time) {
	for reqID, slot := range l.active {
		if !slot.expiresAt.IsZero() && !slot.expiresAt.After(now) {
			delete(l.active, reqID)
		}
	}
}
