package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Service owns concurrency-lease orchestration for admit, renew, release,
// query, and readiness. Persistence dialects implement LeaseStore (task 8.2).
type Service struct {
	rules RuleSource
	store LeaseStore
	clock Clock
}

// NewService constructs a service with explicit dependencies. A nil clock uses
// the system wall clock.
func NewService(rules RuleSource, store LeaseStore, clock Clock) *Service {
	return &Service{rules: rules, store: store, clock: clock}
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// Admit acquires or replays occupancy for matching rules. Strict multi-rule
// matches use one atomic lease set; advisory rules still use per-lease Acquire.
// If admission fails after occupancy was acquired, the leases newly acquired by
// this call are rolled back (replayed pre-existing leases are left untouched).
func (s *Service) Admit(ctx context.Context, in AdmitInput) (AdmitResult, error) {
	if s == nil || s.store == nil || s.rules == nil {
		return AdmitResult{}, WrapError("admit", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return AdmitResult{}, err
	}
	requestID := strings.TrimSpace(in.RequestID)
	if requestID == "" {
		return AdmitResult{}, WrapError("admit", ErrInvalidInput)
	}

	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		return AdmitResult{}, WrapError("admit", err)
	}
	ready := mergeReadiness(snap.Readiness, nil)
	if storeReady, storeErr := s.store.CheckReadiness(ctx); storeErr == nil {
		ready = mergeReadiness(ready, &storeReady)
	}

	// Auxiliary requests inherit the parent lease by default (requirement 10.10).
	if in.Lifecycle == metering.LifecycleAuxiliaryRequest && in.AuxPolicy.InheritsParent() {
		return AdmitResult{
			Kind:      domain.DecisionAllow,
			LeaseID:   strings.TrimSpace(in.ParentLeaseID),
			Readiness: ready,
			Acquired:  false,
		}, nil
	}

	dims := domain.DimensionsFromScope(in.Scope)
	namespace := strings.TrimSpace(in.Namespace)
	now := s.now()

	matched := matchRules(snap.Rules, dims, namespace, in.RuleID)
	if len(matched) == 0 {
		return AdmitResult{Kind: domain.DecisionAllow, Readiness: ready}, nil
	}

	strict := make([]domain.Rule, 0, len(matched))
	advisory := make([]domain.Rule, 0, len(matched))
	for _, rule := range matched {
		if err := rule.ValidateTiming(); err != nil {
			return AdmitResult{}, WrapError("admit", err)
		}
		if rule.Mode == domain.RuleModeAdvisory {
			advisory = append(advisory, rule)
			continue
		}
		strict = append(strict, rule)
	}

	var acquiredLeases []AdmittedLease
	var lastAllow AdmitResult
	haveAllow := false
	bound := snap.PolicyRef()
	if in.BoundVersion.Version != "" {
		bound = in.BoundVersion
	}

	if len(strict) > 0 {
		ttl := strict[0].EffectiveTTL()
		if in.TTL > 0 {
			ttl = in.TTL
		}
		renewBefore := strict[0].EffectiveRenewBefore()
		for _, rule := range strict[1:] {
			if rb := rule.EffectiveRenewBefore(); rb < renewBefore {
				renewBefore = rb
			}
			if t := rule.EffectiveTTL(); t < ttl {
				ttl = t
			}
		}
		ruleIDs := make([]string, 0, len(strict))
		members := make([]AcquireSetMember, 0, len(strict))
		for _, rule := range strict {
			ns := rule.Namespace
			if ns == "" {
				ns = namespace
			}
			ruleIDs = append(ruleIDs, rule.ID)
			leaseID := domain.StableLeaseID(ns, rule.ID, rule.Version, requestID, dims)
			proposed := domain.NewLease(domain.NewLeaseParams{
				LeaseID: leaseID, RuleID: rule.ID, RuleVersion: rule.Version,
				LogicalID: requestID, Namespace: ns, Dimensions: dims, Now: now, TTL: ttl,
			})
			members = append(members, AcquireSetMember{
				Lease: proposed, RuleID: rule.ID, Dimensions: dims, Limit: rule.Limit, Mode: rule.Mode,
			})
		}
		setID := domain.StableSetID(namespace, requestID, ruleIDs)
		acq, err := s.store.AcquireSet(ctx, AcquireSetCommand{
			SetID: setID, RequestID: requestID, Members: members,
			TTL: ttl, RenewBefore: renewBefore, Now: now,
		})
		if err != nil {
			return AdmitResult{}, WrapError("admit", err)
		}
		if acq.CapacityExceeded {
			denyRule := strings.TrimSpace(acq.DenyingRuleID)
			if denyRule == "" {
				denyRule = strict[0].ID
			}
			return AdmitResult{
				Kind: domain.DecisionDeny, RemainingSlots: 0, Readiness: ready,
				Evidence: domain.DenialEvidence(denyRule, 0), RuleID: denyRule, BoundVersion: bound,
			}, nil
		}
		byRule := map[string]domain.Rule{}
		for _, rule := range strict {
			byRule[rule.ID] = rule
		}
		for _, member := range acq.Set.Members {
			rule := byRule[member.RuleID]
			acquiredLeases = append(acquiredLeases, AdmittedLease{
				LeaseID: member.LeaseID, RuleID: member.RuleID, Generation: member.Generation,
				ExpiresAt: member.ExpiresAt, RenewBefore: rule.EffectiveRenewBefore(), TTL: ttl,
				FailureBehavior: rule.FailureBehavior, Acquired: !acq.Replayed, Replayed: acq.Replayed,
			})
		}
		primary := acq.Set.Members[len(acq.Set.Members)-1]
		lastAllow = AdmitResult{
			Kind: domain.DecisionAllow, LeaseID: primary.LeaseID, Generation: acq.Set.Generation,
			ExpiresAt: acq.Set.ExpiresAt, RemainingSlots: acq.RemainingSlots, Readiness: ready,
			BoundVersion: bound, Acquired: !acq.Replayed, Replayed: acq.Replayed,
			RuleID: primary.RuleID, RenewBefore: renewBefore, TTL: ttl,
			FailureBehavior: byRule[primary.RuleID].FailureBehavior,
			Leases:          append([]AdmittedLease(nil), acquiredLeases...),
			SetID:           acq.Set.SetID,
		}
		haveAllow = true
	}

	sawAdvise := false
	var advise AdmitResult
	for _, rule := range advisory {
		ttl := rule.EffectiveTTL()
		if in.TTL > 0 {
			ttl = in.TTL
		}
		ns := rule.Namespace
		if ns == "" {
			ns = namespace
		}
		leaseID := domain.StableLeaseID(ns, rule.ID, rule.Version, requestID, dims)
		proposed := domain.NewLease(domain.NewLeaseParams{
			LeaseID: leaseID, RuleID: rule.ID, RuleVersion: rule.Version,
			LogicalID: requestID, Namespace: ns, Dimensions: dims, Now: now, TTL: ttl,
		})
		acq, err := s.store.Acquire(ctx, AcquireCommand{
			Lease: proposed, RuleID: rule.ID, Dimensions: dims, Limit: rule.Limit, Mode: rule.Mode, Now: now,
		})
		if err != nil {
			return AdmitResult{}, errors.Join(
				WrapError("admit", err),
				s.rollbackAcquired(ctx, acquiredLeaseIDs(acquiredLeases), requestID, now),
			)
		}
		if acq.CapacityExceeded {
			sawAdvise = true
			advise = AdmitResult{
				Kind: domain.DecisionAdvisory, RemainingSlots: 0, Readiness: ready,
				Evidence: domain.DenialEvidence(rule.ID, 0), RuleID: rule.ID, BoundVersion: bound,
			}
			continue
		}
		if acq.Rejected {
			continue
		}
		acquiredLeases = append(acquiredLeases, AdmittedLease{
			LeaseID: acq.Lease.LeaseID, RuleID: rule.ID, Generation: acq.Lease.Generation,
			ExpiresAt: acq.Lease.ExpiresAt, RenewBefore: rule.EffectiveRenewBefore(), TTL: ttl,
			FailureBehavior: rule.FailureBehavior, Acquired: !acq.Replayed, Replayed: acq.Replayed,
		})
		lastAllow = AdmitResult{
			Kind: domain.DecisionAllow, LeaseID: acq.Lease.LeaseID, Generation: acq.Lease.Generation,
			ExpiresAt: acq.Lease.ExpiresAt, RemainingSlots: acq.RemainingSlots, Readiness: ready,
			BoundVersion: bound, Acquired: !acq.Replayed, Replayed: acq.Replayed,
			RuleID: rule.ID, RenewBefore: rule.EffectiveRenewBefore(), TTL: ttl,
			FailureBehavior: rule.FailureBehavior,
			Leases:          append([]AdmittedLease(nil), acquiredLeases...),
		}
		haveAllow = true
	}

	if haveAllow {
		lastAllow.Leases = append([]AdmittedLease(nil), acquiredLeases...)
		return lastAllow, nil
	}
	if sawAdvise {
		return advise, nil
	}
	return AdmitResult{Kind: domain.DecisionAllow, Readiness: ready}, nil
}

// acquiredLeaseIDs returns the IDs of leases newly acquired by this Admit call.
// Replayed occupancies are excluded so rollback never frees pre-existing leases.
func acquiredLeaseIDs(leases []AdmittedLease) []string {
	ids := make([]string, 0, len(leases))
	for _, l := range leases {
		if l.Acquired {
			ids = append(ids, l.LeaseID)
		}
	}
	return ids
}

// rollbackAcquired idempotently releases leases acquired earlier in the same Admit.
// Rollback must complete even when the trigger is client cancellation. A release
// failure is returned rather than dropped because occupancy may still be held;
// the caller reports it alongside the admission failure that triggered rollback.
func (s *Service) rollbackAcquired(ctx context.Context, leaseIDs []string, requestID string, now time.Time) error {
	if s == nil || s.store == nil {
		return nil
	}
	releaseCtx := context.WithoutCancel(ctx)
	var rollbackErr error
	for _, id := range leaseIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := s.store.Release(releaseCtx, ReleaseCommand{
			LeaseID:   id,
			RequestID: requestID,
			Reason:    "admit_deny_rollback",
			Now:       now,
		}); err != nil {
			rollbackErr = errors.Join(rollbackErr, WrapError("rollback", err))
		}
	}
	return rollbackErr
}

func matchRules(rules []domain.Rule, dims domain.Dimensions, namespace, ruleID string) []domain.Rule {
	out := make([]domain.Rule, 0, len(rules))
	wantRule := strings.TrimSpace(ruleID)
	for _, rule := range rules {
		if wantRule != "" && rule.ID != wantRule {
			continue
		}
		if namespace != "" && rule.Namespace != "" && rule.Namespace != namespace {
			continue
		}
		if rule.Limit <= 0 {
			continue
		}
		if !rule.Mode.IsKnown() {
			continue
		}
		if !rule.Matches(dims) {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func mergeReadiness(a domain.Readiness, b *domain.Readiness) domain.Readiness {
	if b == nil {
		if a.State == "" {
			return domain.Readiness{State: domain.ReadinessStateReady}
		}
		return a
	}
	return worseReadiness(a, *b)
}

func worseReadiness(a, b domain.Readiness) domain.Readiness {
	rank := func(s domain.ReadinessState) int {
		switch s {
		case domain.ReadinessStateReady:
			return 0
		case domain.ReadinessStateDegraded:
			return 1
		case domain.ReadinessStateUnavailable:
			return 2
		case domain.ReadinessStateDisabled:
			return 3
		default:
			return 0
		}
	}
	if rank(b.State) > rank(a.State) {
		return b
	}
	if a.State == "" {
		return b
	}
	return a
}

// Renew extends an active lease or lease set with generation CAS.
func (s *Service) Renew(ctx context.Context, in RenewInput) (AdmitResult, error) {
	if s == nil || s.store == nil {
		return AdmitResult{}, WrapError("renew", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return AdmitResult{}, err
	}
	ready, _ := s.ReadinessDomain(ctx)
	if setID := strings.TrimSpace(in.SetID); setID != "" {
		ttl := in.TTL
		if ttl <= 0 {
			ttl = time.Minute
		}
		renewBefore := in.RenewBefore
		if renewBefore <= 0 {
			renewBefore = 15 * time.Second
		}
		if err := domain.ValidateTiming(ttl, renewBefore); err != nil {
			return AdmitResult{}, WrapError("renew", err)
		}
		res, err := s.store.RenewSet(ctx, RenewSetCommand{
			SetID: setID, RequestID: in.RequestID, ExpectedGeneration: in.ExpectedGeneration,
			TTL: ttl, RenewBefore: renewBefore, Now: s.now(),
		})
		if err != nil {
			if IsAmbiguousRenewError(err) {
				if markErr := s.store.MarkSetUncertain(ctx, setID, s.now()); markErr != nil {
					return AdmitResult{}, errors.Join(WrapError("renew", err), WrapError("mark_uncertain", markErr))
				}
			}
			return AdmitResult{}, WrapError("renew", err)
		}
		primaryID := in.LeaseID
		if primaryID == "" && len(res.Set.Members) > 0 {
			primaryID = res.Set.Members[len(res.Set.Members)-1].LeaseID
		}
		out := AdmitResult{
			Kind: domain.DecisionAllow, LeaseID: primaryID, Generation: res.Set.Generation,
			ExpiresAt: res.Set.ExpiresAt, Readiness: ready, SetID: res.Set.SetID,
			TTL: ttl, RenewBefore: renewBefore,
		}
		for _, m := range res.Set.Members {
			out.Leases = append(out.Leases, AdmittedLease{
				LeaseID: m.LeaseID, RuleID: m.RuleID, Generation: m.Generation,
				ExpiresAt: m.ExpiresAt, RenewBefore: renewBefore, TTL: ttl,
			})
		}
		return out, nil
	}
	if strings.TrimSpace(in.LeaseID) == "" {
		return AdmitResult{}, WrapError("renew", ErrInvalidInput)
	}
	res, err := s.store.Renew(ctx, RenewCommand{
		LeaseID:            in.LeaseID,
		RequestID:          in.RequestID,
		ExpectedGeneration: in.ExpectedGeneration,
		TTL:                in.TTL,
		Now:                s.now(),
	})
	if err != nil {
		return AdmitResult{}, WrapError("renew", err)
	}
	return AdmitResult{
		Kind:       domain.DecisionAllow,
		LeaseID:    res.Lease.LeaseID,
		Generation: res.Lease.Generation,
		ExpiresAt:  res.Lease.ExpiresAt,
		Readiness:  ready,
	}, nil
}

// Release releases a lease or lease set idempotently.
func (s *Service) Release(ctx context.Context, in ReleaseInput) error {
	if s == nil || s.store == nil {
		return WrapError("release", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if setID := strings.TrimSpace(in.SetID); setID != "" {
		_, err := s.store.ReleaseSet(ctx, ReleaseSetCommand{
			SetID: setID, RequestID: in.RequestID, Reason: in.Reason, Now: s.now(),
		})
		if err != nil {
			return WrapError("release", err)
		}
		return nil
	}
	if strings.TrimSpace(in.LeaseID) == "" {
		return WrapError("release", ErrInvalidInput)
	}
	_, err := s.store.Release(ctx, ReleaseCommand{
		LeaseID:   in.LeaseID,
		RequestID: in.RequestID,
		Reason:    in.Reason,
		Now:       s.now(),
	})
	if err != nil {
		return WrapError("release", err)
	}
	return nil
}

// RulesSnapshot returns the current immutable rule set for capacity queries.
func (s *Service) RulesSnapshot(ctx context.Context) (RuleSnapshot, error) {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}, WrapError("rules", ErrUnavailable)
	}
	return s.rules.Snapshot(ctx)
}

// Query returns a bounded lease page.
func (s *Service) Query(ctx context.Context, q QueryCommand) (QueryResult, error) {
	if s == nil || s.store == nil {
		return QueryResult{}, WrapError("query", ErrUnavailable)
	}
	if q.Now.IsZero() {
		q.Now = s.now()
	}
	res, err := s.store.Query(ctx, q)
	if err != nil {
		return QueryResult{}, WrapError("query", err)
	}
	return res, nil
}

// ReadinessDomain returns domain readiness from store (and optional snapshot).
// Uncertain or failed lease-set occupancy degrades readiness (task 6.4 remediation).
func (s *Service) ReadinessDomain(ctx context.Context) (domain.Readiness, error) {
	if s == nil || s.store == nil {
		return domain.Readiness{State: domain.ReadinessStateUnavailable}, WrapError("readiness", ErrUnavailable)
	}
	ready, err := s.store.CheckReadiness(ctx)
	if err != nil {
		return domain.Readiness{State: domain.ReadinessStateUnavailable}, WrapError("readiness", err)
	}
	if s.rules != nil {
		if snap, snapErr := s.rules.Snapshot(ctx); snapErr == nil {
			ready = mergeReadiness(snap.Readiness, &ready)
		}
	}
	if ready.State == "" {
		ready.State = domain.ReadinessStateReady
	}
	counts, countErr := s.LeaseSetOccupancyCounts(ctx)
	if countErr == nil && (counts.Uncertain > 0 || counts.Failed > 0 || counts.Expiring > 0) {
		ready = mergeReadiness(ready, &domain.Readiness{
			State:  domain.ReadinessStateDegraded,
			Reason: "lease_set_uncertain_failed_or_expiring",
		})
	}
	return ready, nil
}

// LeaseSetOccupancyCounts returns bounded occupancy counts by set state.
func (s *Service) LeaseSetOccupancyCounts(ctx context.Context) (LeaseSetOccupancyCounts, error) {
	if s == nil || s.store == nil {
		return LeaseSetOccupancyCounts{}, WrapError("query_sets", ErrUnavailable)
	}
	res, err := s.store.QuerySets(ctx, QuerySetsCommand{Now: s.now(), Limit: 500})
	if err != nil {
		return LeaseSetOccupancyCounts{}, WrapError("query_sets", err)
	}
	var out LeaseSetOccupancyCounts
	for _, set := range res.Sets {
		switch set.State {
		case domain.LeaseSetStateActive:
			out.Active++
		case domain.LeaseSetStateUncertain:
			out.Uncertain++
		case domain.LeaseSetStateExpiring:
			out.Expiring++
		case domain.LeaseSetStateReleased:
			out.Released++
		case domain.LeaseSetStateFailed:
			out.Failed++
		}
	}
	return out, nil
}

// ReconcileUncertainSets conservatively keeps uncertain sets occupied and returns
// their IDs for durable release-work resumption (startup reconciliation).
func (s *Service) ReconcileUncertainSets(ctx context.Context) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, WrapError("reconcile", ErrUnavailable)
	}
	res, err := s.store.QuerySets(ctx, QuerySetsCommand{
		State: domain.LeaseSetStateUncertain, Now: s.now(), Limit: 500,
	})
	if err != nil {
		return nil, WrapError("reconcile", err)
	}
	ids := make([]string, 0, len(res.Sets))
	for _, set := range res.Sets {
		if set.SetID == "" {
			continue
		}
		if err := s.store.MarkSetUncertain(ctx, set.SetID, s.now()); err != nil {
			return ids, WrapError("reconcile", err)
		}
		ids = append(ids, set.SetID)
	}
	return ids, nil
}

// Readiness maps domain readiness onto the public authority readiness enum.
func (s *Service) Readiness(ctx context.Context) (authority.Readiness, error) {
	ready, err := s.ReadinessDomain(ctx)
	if err != nil {
		return authority.ReadinessUnavailable, err
	}
	return mapReadiness(ready), nil
}

func mapReadiness(r domain.Readiness) authority.Readiness {
	switch r.State {
	case domain.ReadinessStateReady:
		return authority.ReadinessReady
	case domain.ReadinessStateDegraded:
		return authority.ReadinessDegraded
	case domain.ReadinessStateUnavailable:
		return authority.ReadinessUnavailable
	case domain.ReadinessStateDisabled:
		return authority.ReadinessDisabled
	default:
		return authority.ReadinessReady
	}
}
