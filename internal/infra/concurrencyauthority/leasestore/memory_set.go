package leasestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// AcquireSet atomically acquires or replays a complete multi-rule lease set.
func (s *MemoryStore) AcquireSet(ctx context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	if err := ctx.Err(); err != nil {
		return app.AcquireSetResult{}, err
	}
	if strings.TrimSpace(cmd.SetID) == "" || strings.TrimSpace(cmd.RequestID) == "" || len(cmd.Members) == 0 {
		return app.AcquireSetResult{}, fmt.Errorf("leasestore: invalid acquire set command")
	}
	if err := domain.ValidateTiming(cmd.TTL, cmd.RenewBefore); err != nil {
		return app.AcquireSetResult{}, err
	}
	lockOrder := make([]string, 0, len(cmd.Members))
	for _, m := range cmd.Members {
		lockOrder = append(lockOrder, strings.TrimSpace(m.RuleID))
	}
	lockOrder = domain.SortedRuleIDs(lockOrder)

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	// Replay complete prior set when any member is live under this set id.
	if existing := s.loadSetLocked(cmd.SetID, cmd.Now); existing != nil && existing.OccupiesCapacity(cmd.Now) {
		minRemain := -1
		for _, m := range cmd.Members {
			dimKey := string(m.Dimensions.Key())
			rem := remainingLocked(s, m.RuleID, dimKey, m.Limit, cmd.Now)
			if minRemain < 0 || rem < minRemain {
				minRemain = rem
			}
		}
		if minRemain < 0 {
			minRemain = 0
		}
		return app.AcquireSetResult{Set: *existing, Replayed: true, RemainingSlots: minRemain, LockOrder: lockOrder}, nil
	}

	selfIDs := map[string]struct{}{}
	for _, m := range cmd.Members {
		selfIDs[strings.TrimSpace(m.Lease.LeaseID)] = struct{}{}
	}
	// Capacity check for every member in lock order before mutate.
	for _, ruleID := range lockOrder {
		var member app.AcquireSetMember
		for _, m := range cmd.Members {
			if strings.TrimSpace(m.RuleID) == ruleID {
				member = m
				break
			}
		}
		dimKey := string(member.Dimensions.Key())
		s.reclaimLocked(member.RuleID, dimKey, cmd.Now)
		live := countLiveExcludingLocked(s, member.RuleID, dimKey, cmd.Now, selfIDs)
		if live >= member.Limit {
			if member.Mode == domain.RuleModeAdvisory {
				continue
			}
			return app.AcquireSetResult{
				CapacityExceeded: true, RemainingSlots: 0, LockOrder: lockOrder, DenyingRuleID: ruleID,
			}, nil
		}
	}

	members := make([]domain.Lease, 0, len(cmd.Members))
	exp := cmd.Now.Add(cmd.TTL)
	for _, ruleID := range lockOrder {
		var member app.AcquireSetMember
		for _, m := range cmd.Members {
			if strings.TrimSpace(m.RuleID) == ruleID {
				member = m
				break
			}
		}
		lease := member.Lease
		if lease.LeaseID == "" {
			return app.AcquireSetResult{}, fmt.Errorf("leasestore: empty member lease id")
		}
		lease.IdentityVersion = domain.IdentityVersionLeaseSet
		lease.SetID = cmd.SetID
		lease.SetGeneration = 1
		lease.SetState = domain.LeaseSetStateActive
		lease.State = domain.LeaseStateActive
		lease.AcquiredAt = cmd.Now
		lease.RenewedAt = cmd.Now
		lease.ExpiresAt = exp
		lease.Generation = 1
		lease.LogicalID = cmd.RequestID
		if lease.RuleID == "" {
			lease.RuleID = member.RuleID
		}
		s.state.leases[s.key(lease.LeaseID)] = lease
		members = append(members, lease)
	}

	minRemain := -1
	for _, m := range cmd.Members {
		dimKey := string(m.Dimensions.Key())
		rem := remainingLocked(s, m.RuleID, dimKey, m.Limit, cmd.Now)
		if minRemain < 0 || rem < minRemain {
			minRemain = rem
		}
	}
	if minRemain < 0 {
		minRemain = 0
	}
	set := domain.LeaseSet{
		SetID: cmd.SetID, RequestID: cmd.RequestID, Generation: 1,
		State: domain.LeaseSetStateActive, Members: members,
		AcquiredAt: cmd.Now, RenewedAt: cmd.Now, ExpiresAt: exp,
		RenewBefore: cmd.RenewBefore, TTL: cmd.TTL,
	}
	return app.AcquireSetResult{Set: set, RemainingSlots: minRemain, LockOrder: lockOrder}, nil
}

// RenewSet renews every member under one set generation CAS.
func (s *MemoryStore) RenewSet(ctx context.Context, cmd app.RenewSetCommand) (app.RenewSetResult, error) {
	if err := ctx.Err(); err != nil {
		return app.RenewSetResult{}, err
	}
	if strings.TrimSpace(cmd.SetID) == "" {
		return app.RenewSetResult{}, fmt.Errorf("leasestore: empty set id")
	}
	if err := domain.ValidateTiming(cmd.TTL, cmd.RenewBefore); err != nil {
		return app.RenewSetResult{}, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	set := s.loadSetLocked(cmd.SetID, cmd.Now)
	if set == nil {
		return app.RenewSetResult{}, app.ErrNotFound
	}
	if set.State == domain.LeaseSetStateReleased || set.State == domain.LeaseSetStateFailed {
		return app.RenewSetResult{}, domain.ErrLeaseReleased
	}
	if set.Generation != cmd.ExpectedGeneration {
		return app.RenewSetResult{}, domain.ErrGenerationMismatch
	}
	exp := cmd.Now.Add(cmd.TTL)
	nextGen := set.Generation + 1
	for i := range set.Members {
		m := set.Members[i]
		m.RenewedAt = cmd.Now
		m.ExpiresAt = exp
		m.Generation++
		m.SetGeneration = nextGen
		m.SetState = domain.LeaseSetStateActive
		m.State = domain.LeaseStateActive
		s.state.leases[s.key(m.LeaseID)] = m
		set.Members[i] = m
	}
	set.Generation = nextGen
	set.RenewedAt = cmd.Now
	set.ExpiresAt = exp
	set.State = domain.LeaseSetStateActive
	set.TTL = cmd.TTL
	set.RenewBefore = cmd.RenewBefore
	return app.RenewSetResult{Set: *set}, nil
}

// ReleaseSet releases every member of a set idempotently.
func (s *MemoryStore) ReleaseSet(ctx context.Context, cmd app.ReleaseSetCommand) (app.ReleaseSetResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseSetResult{}, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	set := s.loadSetLocked(cmd.SetID, cmd.Now)
	if set == nil {
		return app.ReleaseSetResult{Applied: false}, nil
	}
	if set.State == domain.LeaseSetStateReleased {
		return app.ReleaseSetResult{Applied: false, Set: *set}, nil
	}
	for i := range set.Members {
		m := set.Members[i]
		m.Release(cmd.Now)
		m.SetState = domain.LeaseSetStateReleased
		s.state.leases[s.key(m.LeaseID)] = m
		set.Members[i] = m
	}
	set.State = domain.LeaseSetStateReleased
	set.ReleasedAt = cmd.Now
	return app.ReleaseSetResult{Applied: true, Set: *set}, nil
}

// QuerySets returns a bounded page of lease sets.
func (s *MemoryStore) QuerySets(ctx context.Context, q app.QuerySetsCommand) (app.QuerySetsResult, error) {
	if err := ctx.Err(); err != nil {
		return app.QuerySetsResult{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	bySet := map[string]*domain.LeaseSet{}
	prefix := s.cfg.StoreID + "\x00"
	for k, lease := range s.state.leases {
		if !strings.HasPrefix(k, prefix) || lease.SetID == "" {
			continue
		}
		if q.SetID != "" && lease.SetID != q.SetID {
			continue
		}
		if q.RequestID != "" && lease.LogicalID != q.RequestID {
			continue
		}
		set := bySet[lease.SetID]
		if set == nil {
			set = &domain.LeaseSet{
				SetID: lease.SetID, RequestID: lease.LogicalID,
				Generation: lease.SetGeneration, State: lease.SetState,
				ExpiresAt: lease.ExpiresAt, AcquiredAt: lease.AcquiredAt, RenewedAt: lease.RenewedAt,
			}
			bySet[lease.SetID] = set
		}
		set.Members = append(set.Members, lease)
	}
	ids := make([]string, 0, len(bySet))
	for id := range bySet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]domain.LeaseSet, 0, limit)
	for _, id := range ids {
		set := *bySet[id]
		if q.State != "" && set.State != q.State {
			continue
		}
		out = append(out, set)
		if len(out) >= limit {
			break
		}
	}
	return app.QuerySetsResult{Sets: out}, nil
}

func (s *MemoryStore) loadSetLocked(setID string, now time.Time) *domain.LeaseSet {
	_ = now
	prefix := s.cfg.StoreID + "\x00"
	var set *domain.LeaseSet
	for k, lease := range s.state.leases {
		if !strings.HasPrefix(k, prefix) || lease.SetID != setID {
			continue
		}
		if set == nil {
			set = &domain.LeaseSet{
				SetID: lease.SetID, RequestID: lease.LogicalID,
				Generation: lease.SetGeneration, State: lease.SetState,
				ExpiresAt: lease.ExpiresAt, AcquiredAt: lease.AcquiredAt, RenewedAt: lease.RenewedAt,
			}
		}
		set.Members = append(set.Members, lease)
	}
	if set == nil {
		return nil
	}
	sort.SliceStable(set.Members, func(i, j int) bool {
		return set.Members[i].RuleID < set.Members[j].RuleID
	})
	return set
}

// MarkSetUncertain is used by heartbeat ambiguous-renew paths (task 6.3).
func (s *MemoryStore) MarkSetUncertain(ctx context.Context, setID string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	set := s.loadSetLocked(setID, now)
	if set == nil {
		return app.ErrNotFound
	}
	if err := set.MarkUncertain(now); err != nil {
		return err
	}
	for _, m := range set.Members {
		m.SetState = domain.LeaseSetStateUncertain
		m.State = domain.LeaseStateExpiring
		s.state.leases[s.key(m.LeaseID)] = m
	}
	return nil
}
