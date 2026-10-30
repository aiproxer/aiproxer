package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type staticRules struct{ snap app.RuleSnapshot }

func (s staticRules) Snapshot(context.Context) (app.RuleSnapshot, error) { return s.snap, nil }

// memoryStore is a test-only LeaseStore fake (production dialects are task 8.2).
type memoryStore struct {
	mu     sync.Mutex
	leases map[string]domain.Lease
	ready  domain.Readiness
}

type acquireErrorStore struct {
	*memoryStore
	ruleID string
	err    error
}

func (s *acquireErrorStore) Acquire(ctx context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	if cmd.RuleID == s.ruleID {
		return app.AcquireResult{}, s.err
	}
	return s.memoryStore.Acquire(ctx, cmd)
}

func (s *acquireErrorStore) AcquireSet(ctx context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	for _, m := range cmd.Members {
		if m.RuleID == s.ruleID {
			return app.AcquireSetResult{}, s.err
		}
	}
	return s.memoryStore.AcquireSet(ctx, cmd)
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		leases: make(map[string]domain.Lease),
		ready:  domain.Readiness{State: domain.ReadinessStateReady},
	}
}

func (s *memoryStore) Acquire(_ context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.leases[cmd.Lease.LeaseID]; ok {
		if existing.IsLive(cmd.Now) {
			return app.AcquireResult{
				Lease:          existing,
				Replayed:       true,
				RemainingSlots: remaining(s, cmd, cmd.Now),
			}, nil
		}
		// Expired/released rows do not occupy capacity; overwrite on re-acquire.
	}

	live := 0
	for _, l := range s.leases {
		if l.RuleID == cmd.RuleID && l.Dimensions.Key() == cmd.Dimensions.Key() && l.IsLive(cmd.Now) {
			live++
		}
	}
	if live >= cmd.Limit {
		return app.AcquireResult{
			CapacityExceeded: true,
			RemainingSlots:   0,
		}, nil
	}

	lease := cmd.Lease
	s.leases[lease.LeaseID] = lease
	return app.AcquireResult{
		Lease:          lease,
		RemainingSlots: cmd.Limit - live - 1,
	}, nil
}

func remaining(s *memoryStore, cmd app.AcquireCommand, now time.Time) int {
	live := 0
	for _, l := range s.leases {
		if l.RuleID == cmd.RuleID && l.Dimensions.Key() == cmd.Dimensions.Key() && l.IsLive(now) {
			live++
		}
	}
	left := cmd.Limit - live
	if left < 0 {
		return 0
	}
	return left
}

func (s *memoryStore) Renew(_ context.Context, cmd app.RenewCommand) (app.RenewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[cmd.LeaseID]
	if !ok {
		return app.RenewResult{}, app.ErrNotFound
	}
	if err := lease.Renew(cmd.Now, cmd.ExpectedGeneration, cmd.TTL); err != nil {
		return app.RenewResult{}, err
	}
	s.leases[cmd.LeaseID] = lease
	return app.RenewResult{Lease: lease}, nil
}

func (s *memoryStore) Release(_ context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[cmd.LeaseID]
	if !ok {
		return app.ReleaseResult{Applied: false}, nil
	}
	lease.Release(cmd.Now)
	s.leases[cmd.LeaseID] = lease
	return app.ReleaseResult{Applied: true, Lease: lease}, nil
}

func (s *memoryStore) Query(_ context.Context, q app.QueryCommand) (app.QueryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Lease
	for _, l := range s.leases {
		if q.LeaseID != "" && l.LeaseID != q.LeaseID {
			continue
		}
		if q.RequestID != "" && l.LogicalID != q.RequestID {
			continue
		}
		if q.RuleID != "" && l.RuleID != q.RuleID {
			continue
		}
		state := l.EffectiveState(q.Now)
		if state == domain.LeaseStateActive && !l.ExpiresAt.IsZero() && !q.Now.Before(l.ExpiresAt.Add(-15*time.Second)) {
			state = domain.LeaseStateExpiring
		}
		if q.State != "" && state != q.State {
			continue
		}
		cp := l
		cp.State = state
		out = append(out, cp)
	}
	return app.QueryResult{Leases: out}, nil
}

func (s *memoryStore) CheckReadiness(context.Context) (domain.Readiness, error) {
	return s.ready, nil
}

func (s *memoryStore) AcquireSet(_ context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := domain.ValidateTiming(cmd.TTL, cmd.RenewBefore); err != nil {
		return app.AcquireSetResult{}, err
	}
	lockOrder := domain.SortedRuleIDs(func() []string {
		ids := make([]string, 0, len(cmd.Members))
		for _, m := range cmd.Members {
			ids = append(ids, m.RuleID)
		}
		return ids
	}())
	for _, l := range s.leases {
		if l.SetID == cmd.SetID && l.IsLive(cmd.Now) {
			members := make([]domain.Lease, 0)
			for _, x := range s.leases {
				if x.SetID == cmd.SetID {
					members = append(members, x)
				}
			}
			return app.AcquireSetResult{
				Set: domain.LeaseSet{
					SetID: cmd.SetID, RequestID: cmd.RequestID, Generation: l.SetGeneration,
					State: l.SetState, Members: members, ExpiresAt: l.ExpiresAt,
				},
				Replayed: true, LockOrder: lockOrder,
			}, nil
		}
	}
	selfIDs := map[string]struct{}{}
	for _, m := range cmd.Members {
		selfIDs[m.Lease.LeaseID] = struct{}{}
	}
	for _, ruleID := range lockOrder {
		var m app.AcquireSetMember
		for _, cand := range cmd.Members {
			if cand.RuleID == ruleID {
				m = cand
				break
			}
		}
		live := 0
		for _, l := range s.leases {
			if _, skip := selfIDs[l.LeaseID]; skip {
				continue
			}
			if l.RuleID == m.RuleID && l.Dimensions.Key() == m.Dimensions.Key() && l.IsLive(cmd.Now) {
				live++
			}
		}
		if live >= m.Limit && m.Mode != domain.RuleModeAdvisory {
			return app.AcquireSetResult{CapacityExceeded: true, LockOrder: lockOrder, DenyingRuleID: ruleID}, nil
		}
	}
	exp := cmd.Now.Add(cmd.TTL)
	members := make([]domain.Lease, 0, len(cmd.Members))
	for _, ruleID := range lockOrder {
		var m app.AcquireSetMember
		for _, cand := range cmd.Members {
			if cand.RuleID == ruleID {
				m = cand
				break
			}
		}
		lease := m.Lease
		lease.IdentityVersion = domain.IdentityVersionLeaseSet
		lease.SetID = cmd.SetID
		lease.SetGeneration = 1
		lease.SetState = domain.LeaseSetStateActive
		lease.State = domain.LeaseStateActive
		lease.ExpiresAt = exp
		lease.AcquiredAt = cmd.Now
		lease.RenewedAt = cmd.Now
		lease.Generation = 1
		lease.LogicalID = cmd.RequestID
		s.leases[lease.LeaseID] = lease
		members = append(members, lease)
	}
	return app.AcquireSetResult{
		Set: domain.LeaseSet{
			SetID: cmd.SetID, RequestID: cmd.RequestID, Generation: 1,
			State: domain.LeaseSetStateActive, Members: members,
			AcquiredAt: cmd.Now, RenewedAt: cmd.Now, ExpiresAt: exp,
			TTL: cmd.TTL, RenewBefore: cmd.RenewBefore,
		},
		LockOrder: lockOrder,
	}, nil
}

func (s *memoryStore) RenewSet(_ context.Context, cmd app.RenewSetCommand) (app.RenewSetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var members []domain.Lease
	var gen int64
	for _, l := range s.leases {
		if l.SetID != cmd.SetID {
			continue
		}
		if gen == 0 {
			gen = l.SetGeneration
		}
		members = append(members, l)
	}
	if len(members) == 0 {
		return app.RenewSetResult{}, app.ErrNotFound
	}
	if gen != cmd.ExpectedGeneration {
		return app.RenewSetResult{}, domain.ErrGenerationMismatch
	}
	exp := cmd.Now.Add(cmd.TTL)
	next := gen + 1
	for i := range members {
		m := members[i]
		m.ExpiresAt = exp
		m.RenewedAt = cmd.Now
		m.Generation++
		m.SetGeneration = next
		m.SetState = domain.LeaseSetStateActive
		m.State = domain.LeaseStateActive
		s.leases[m.LeaseID] = m
		members[i] = m
	}
	return app.RenewSetResult{Set: domain.LeaseSet{
		SetID: cmd.SetID, RequestID: cmd.RequestID, Generation: next,
		State: domain.LeaseSetStateActive, Members: members, ExpiresAt: exp,
	}}, nil
}

func (s *memoryStore) ReleaseSet(_ context.Context, cmd app.ReleaseSetCommand) (app.ReleaseSetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var members []domain.Lease
	for _, l := range s.leases {
		if l.SetID == cmd.SetID {
			members = append(members, l)
		}
	}
	if len(members) == 0 {
		return app.ReleaseSetResult{Applied: false}, nil
	}
	for i := range members {
		m := members[i]
		m.Release(cmd.Now)
		m.SetState = domain.LeaseSetStateReleased
		s.leases[m.LeaseID] = m
		members[i] = m
	}
	return app.ReleaseSetResult{Applied: true, Set: domain.LeaseSet{
		SetID: cmd.SetID, RequestID: cmd.RequestID, State: domain.LeaseSetStateReleased, Members: members,
	}}, nil
}

func (s *memoryStore) QuerySets(_ context.Context, q app.QuerySetsCommand) (app.QuerySetsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]*domain.LeaseSet{}
	for _, l := range s.leases {
		if l.SetID == "" {
			continue
		}
		if q.SetID != "" && l.SetID != q.SetID {
			continue
		}
		if q.RequestID != "" && l.LogicalID != q.RequestID {
			continue
		}
		set := by[l.SetID]
		if set == nil {
			set = &domain.LeaseSet{SetID: l.SetID, RequestID: l.LogicalID, Generation: l.SetGeneration, State: l.SetState}
			by[l.SetID] = set
		}
		set.Members = append(set.Members, l)
	}
	out := make([]domain.LeaseSet, 0, len(by))
	for _, set := range by {
		if q.State != "" && set.State != q.State {
			continue
		}
		out = append(out, *set)
	}
	return app.QuerySetsResult{Sets: out}, nil
}

func (s *memoryStore) MarkSetUncertain(_ context.Context, setID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.leases {
		if l.SetID != setID {
			continue
		}
		l.SetState = domain.LeaseSetStateUncertain
		l.RenewedAt = now
		s.leases[id] = l
	}
	return nil
}

func principalScope(id string) scope.PrincipalScopeView {
	return scope.PrincipalScopeView{PrincipalID: scope.Known(id), Origin: scope.OriginClient}
}

func strictRule(limit int) domain.Rule {
	return domain.Rule{
		ID:        "max-active",
		Namespace: "default",
		Version:   "v1",
		Mode:      domain.RuleModeStrict,
		Limit:     limit,
		LeaseTTL:  time.Minute,
		Match: domain.DimensionsMatcher{
			Principal: domain.DimensionMatcher{Value: scope.Known("alice")},
		},
	}
}

func advisoryRule(limit int) domain.Rule {
	r := strictRule(limit)
	r.ID = "max-active-adv"
	r.Mode = domain.RuleModeAdvisory
	return r
}

func newService(t *testing.T, rules []domain.Rule, store app.LeaseStore, now time.Time) *app.Service {
	t.Helper()
	return app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     rules,
	}}, store, fixedClock{t: now})
}

func TestAdmit_StrictDenyVsAdvisory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	t.Run("strict denies at capacity", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore()
		svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

		first, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-1",
			Scope:     principalScope("alice"),
			Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if first.Kind != domain.DecisionAllow || first.LeaseID == "" {
			t.Fatalf("first=%+v", first)
		}

		second, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-2",
			Scope:     principalScope("alice"),
			Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.Kind != domain.DecisionDeny {
			t.Fatalf("kind=%s want deny", second.Kind)
		}
		if second.Evidence.Category != domain.EvidenceCategoryConcurrencyLimit {
			t.Fatalf("evidence=%+v", second.Evidence)
		}
		if second.Evidence.Message != "" && (strings.Contains(second.Evidence.Message, first.LeaseID) || strings.Contains(second.Evidence.Message, "alice")) {
			t.Fatalf("unsafe evidence message: %q", second.Evidence.Message)
		}
	})

	t.Run("advisory allows when over limit", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore()
		svc := newService(t, []domain.Rule{advisoryRule(1)}, store, now)

		first, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if first.Kind != domain.DecisionAllow {
			t.Fatalf("first=%+v", first)
		}
		second, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-2", Scope: principalScope("alice"), Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.Kind != domain.DecisionAdvisory {
			t.Fatalf("kind=%s want advisory", second.Kind)
		}
	})
}

func TestAdmit_SafeScopeMatching(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	bob, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-bob", Scope: principalScope("bob"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bob.Kind != domain.DecisionAllow || bob.LeaseID != "" {
		t.Fatalf("unmatched principal should skip rules: %+v", bob)
	}
}

func TestAdmit_DistinctLeasesPerMatchingRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(1)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	svc := newService(t, []domain.Rule{ruleA, ruleB}, store, now)

	first, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-shared", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != domain.DecisionAllow {
		t.Fatalf("first admit=%+v", first)
	}

	q, err := svc.Query(ctx, app.QueryCommand{RequestID: "req-shared", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Leases) != 2 {
		t.Fatalf("expected 2 leases (one per matching rule), got %d: %+v", len(q.Leases), q.Leases)
	}
	byRule := make(map[string]domain.Lease, 2)
	for _, l := range q.Leases {
		if l.LeaseID == "" {
			t.Fatalf("empty lease id: %+v", l)
		}
		if _, dup := byRule[l.RuleID]; dup {
			t.Fatalf("duplicate RuleID in leases: %+v", q.Leases)
		}
		byRule[l.RuleID] = l
	}
	la, okA := byRule["rule-a"]
	lb, okB := byRule["rule-b"]
	if !okA || !okB {
		t.Fatalf("missing rule leases: %+v", byRule)
	}
	if la.LeaseID == lb.LeaseID {
		t.Fatalf("distinct rules must not share lease id: %q", la.LeaseID)
	}

	// rule-b is at capacity (limit 1); a new logical request must be denied by rule-b
	// even though rule-a still has free slots. Regression: shared lease id would
	// replay rule-a into rule-b and leave rule-b capacity unused.
	second, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-next", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny from rule-b capacity, got %+v", second)
	}
	if second.Evidence.RuleID != "rule-b" {
		t.Fatalf("deny evidence rule_id=%q want rule-b", second.Evidence.RuleID)
	}
	if len(second.Leases) != 0 {
		t.Fatalf("deny must not expose live leases: %+v", second.Leases)
	}
	// Mid-loop rule-a acquire must be rolled back; req-next must leave zero live leases.
	nextLive := countLiveLeases(t, svc, "req-next", now)
	if nextLive != 0 {
		t.Fatalf("req-next live leases=%d want 0 after multi-rule deny rollback", nextLive)
	}
}

func TestAdmit_MultiRuleDenyReleasesPriorAcquires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(1)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	svc := newService(t, []domain.Rule{ruleA, ruleB}, store, now)

	// Fill rule-b (and acquire rule-a) with req-1.
	first, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != domain.DecisionAllow {
		t.Fatalf("first admit=%+v", first)
	}
	if countLiveLeases(t, svc, "req-1", now) != 2 {
		t.Fatalf("req-1 should hold 2 live leases")
	}

	// req-2: rule-a acquires then rule-b denies → prior acquire must be released.
	second, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-2", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny, got %+v", second)
	}
	if second.Evidence.RuleID != "rule-b" {
		t.Fatalf("deny evidence rule_id=%q want rule-b", second.Evidence.RuleID)
	}
	if len(second.Leases) != 0 {
		t.Fatalf("deny Leases must be empty, got %+v", second.Leases)
	}
	if countLiveLeases(t, svc, "req-2", now) != 0 {
		t.Fatalf("req-2 must have zero live leases after deny rollback")
	}

	// rule-a capacity must not remain consumed by an orphan from req-2.
	ruleALive := 0
	q, err := svc.Query(ctx, app.QueryCommand{RuleID: "rule-a", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range q.Leases {
		if l.IsLive(now) {
			ruleALive++
		}
	}
	if ruleALive != 1 {
		t.Fatalf("rule-a live=%d want 1 (only req-1); orphan from req-2 not released", ruleALive)
	}
}

func TestAdmit_MultiRuleDenyDoesNotReleaseReplayedLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(1)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	serviceA := newService(t, []domain.Rule{ruleA}, store, now)
	serviceB := newService(t, []domain.Rule{ruleB}, store, now)
	serviceBoth := newService(t, []domain.Rule{ruleA, ruleB}, store, now)

	replayed, err := serviceA.Admit(ctx, app.AdmitInput{
		RequestID: "req-replayed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || replayed.Kind != domain.DecisionAllow {
		t.Fatalf("seed replayed lease: result=%+v err=%v", replayed, err)
	}
	blocker, err := serviceB.Admit(ctx, app.AdmitInput{
		RequestID: "req-blocker", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || blocker.Kind != domain.DecisionAllow {
		t.Fatalf("seed rule-b capacity: result=%+v err=%v", blocker, err)
	}

	denied, err := serviceBoth.Admit(ctx, app.AdmitInput{
		RequestID: "req-replayed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Kind != domain.DecisionDeny || denied.Evidence.RuleID != "rule-b" {
		t.Fatalf("deny=%+v", denied)
	}
	if countLiveLeases(t, serviceBoth, "req-replayed", now) != 1 {
		t.Fatal("deny rollback released the pre-existing replayed rule-a lease")
	}
}

func TestAdmit_MultiRuleErrorDoesNotReleaseReplayedLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	serviceA := newService(t, []domain.Rule{ruleA}, store, now)
	seed, err := serviceA.Admit(ctx, app.AdmitInput{
		RequestID: "req-replayed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || seed.Kind != domain.DecisionAllow {
		t.Fatalf("seed replayed lease: result=%+v err=%v", seed, err)
	}

	wantErr := errors.New("rule-b acquire failed")
	failingStore := &acquireErrorStore{memoryStore: store, ruleID: "rule-b", err: wantErr}
	serviceBoth := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{ruleA, ruleB},
	}}, failingStore, fixedClock{t: now})

	_, err = serviceBoth.Admit(ctx, app.AdmitInput{
		RequestID: "req-replayed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want wrapped %v", err, wantErr)
	}
	if countLiveLeases(t, serviceA, "req-replayed", now) != 1 {
		t.Fatal("error rollback released the pre-existing replayed rule-a lease")
	}
}

func TestAdmit_MultiRuleReplayThenAcquireReportsOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	serviceBoth := newService(t, []domain.Rule{ruleA, ruleB}, store, now)
	first, err := serviceBoth.Admit(ctx, app.AdmitInput{
		RequestID: "req-mixed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != domain.DecisionAllow || first.Replayed || !first.Acquired || len(first.Leases) != 2 {
		t.Fatalf("first set acquire=%+v", first)
	}

	got, err := serviceBoth.Admit(ctx, app.AdmitInput{
		RequestID: "req-mixed", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != domain.DecisionAllow || len(got.Leases) != 2 {
		t.Fatalf("admit=%+v", got)
	}
	if !got.Replayed || got.Acquired {
		t.Fatalf("set replay scalar ownership=%+v", got)
	}
	for _, l := range got.Leases {
		if !l.Replayed || l.Acquired {
			t.Fatalf("set replay member ownership=%+v", l)
		}
	}
}

func TestAdmit_MultiRuleAllowReturnsAllLeases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"

	store := newMemoryStore()
	svc := newService(t, []domain.Rule{ruleA, ruleB}, store, now)

	got, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-multi", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != domain.DecisionAllow {
		t.Fatalf("admit=%+v", got)
	}
	if got.LeaseID == "" {
		t.Fatal("scalar LeaseID must remain set (primary = lastAllow)")
	}
	if len(got.Leases) != 2 {
		t.Fatalf("Leases len=%d want 2: %+v", len(got.Leases), got.Leases)
	}
	ids := map[string]string{}
	for _, occ := range got.Leases {
		if occ.LeaseID == "" || occ.RuleID == "" {
			t.Fatalf("incomplete occupancy: %+v", occ)
		}
		if _, dup := ids[occ.LeaseID]; dup {
			t.Fatalf("duplicate LeaseID in Leases: %+v", got.Leases)
		}
		ids[occ.LeaseID] = occ.RuleID
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 distinct lease ids, got %v", ids)
	}
	// Primary scalar tracks lastAllow (last successfully acquired).
	last := got.Leases[len(got.Leases)-1]
	if got.LeaseID != last.LeaseID || got.RuleID != last.RuleID {
		t.Fatalf("scalar primary=%s/%s want lastAllow %s/%s", got.LeaseID, got.RuleID, last.LeaseID, last.RuleID)
	}
}

func countLiveLeases(t *testing.T, svc *app.Service, requestID string, now time.Time) int {
	t.Helper()
	q, err := svc.Query(context.Background(), app.QueryCommand{RequestID: requestID, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range q.Leases {
		if l.IsLive(now) {
			n++
		}
	}
	return n
}

func TestAdmit_IdempotentReplaySameLogicalRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	first, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-same", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Retry / parallel leg: same logical request id must replay one lease.
	retry, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-same", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseID == "" || first.LeaseID != retry.LeaseID {
		t.Fatalf("replay lease mismatch: %q vs %q", first.LeaseID, retry.LeaseID)
	}
	if first.Generation != retry.Generation {
		t.Fatalf("generation drift on replay: %d vs %d", first.Generation, retry.Generation)
	}
	// Capacity still one: a different request must deny.
	other, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-other", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny for second distinct request, got %+v", other)
	}
}

func TestRenew_TTLGenerationCASAndNoResurrect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(2)}, store, now)

	admitted, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	renewed, err := svc.Renew(ctx, app.RenewInput{
		LeaseID:            admitted.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: admitted.Generation,
		TTL:                time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Generation != admitted.Generation+1 {
		t.Fatalf("generation=%d want %d", renewed.Generation, admitted.Generation+1)
	}
	if !renewed.ExpiresAt.After(now) {
		t.Fatalf("expires_at not extended: %v", renewed.ExpiresAt)
	}

	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Renew(ctx, app.RenewInput{
		LeaseID:            admitted.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: renewed.Generation,
		TTL:                time.Minute,
	})
	if err == nil {
		t.Fatal("renew after release must fail")
	}
}

func TestReleaseAndExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	clock := &mutableClock{t: now}
	svc := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{strictRule(1)},
	}}, store, clock)

	admitted, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	// Idempotent release.
	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	// Slot free after release.
	again, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-2", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != domain.DecisionAllow {
		t.Fatalf("expected allow after release, got %+v", again)
	}

	// Expiry frees capacity without explicit release.
	store2 := newMemoryStore()
	clock2 := &mutableClock{t: now}
	svc2 := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{strictRule(1)},
	}}, store2, clock2)
	expiring, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-exp", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || expiring.Kind != domain.DecisionAllow {
		t.Fatalf("admit: %+v err=%v", expiring, err)
	}
	clock2.t = now.Add(2 * time.Minute)
	next, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-after-exp", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Kind != domain.DecisionAllow {
		t.Fatalf("expired lease must free capacity: %+v", next)
	}
}

type mutableClock struct{ t time.Time }

func (c *mutableClock) Now() time.Time { return c.t }

func TestReadinessReporting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	store.ready = domain.Readiness{State: domain.ReadinessStateDegraded, Reason: "backing_degraded"}
	svc := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateDegraded, Reason: "backing_degraded"},
		Rules:     []domain.Rule{strictRule(5)},
	}}, store, fixedClock{t: now})

	got, err := svc.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != authority.ReadinessDegraded {
		t.Fatalf("readiness=%s", got)
	}
	decision, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Readiness.State != domain.ReadinessStateDegraded {
		t.Fatalf("decision readiness=%s", decision.Readiness.State)
	}
}

func TestAuxInheritsParentNoSecondLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	parent, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-parent", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	aux, err := svc.Admit(ctx, app.AdmitInput{
		RequestID:     "req-aux",
		Scope:         principalScope("alice"),
		Namespace:     "default",
		Lifecycle:     metering.LifecycleAuxiliaryRequest,
		ParentLeaseID: parent.LeaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aux.Kind != domain.DecisionAllow {
		t.Fatalf("aux=%+v", aux)
	}
	if aux.LeaseID != parent.LeaseID {
		t.Fatalf("aux should inherit parent lease: got %q want %q", aux.LeaseID, parent.LeaseID)
	}
	if aux.Acquired {
		t.Fatal("aux must not acquire a second top-level lease by default")
	}

	// Capacity still held by parent only — another top-level request denies.
	other, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-other", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny, got %+v", other)
	}

	// Configurable: acquire own lease for aux.
	store2 := newMemoryStore()
	svc2 := newService(t, []domain.Rule{strictRule(2)}, store2, now)
	p2, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-parent", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	own, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID:     "req-aux-own",
		Scope:         principalScope("alice"),
		Namespace:     "default",
		Lifecycle:     metering.LifecycleAuxiliaryRequest,
		ParentLeaseID: p2.LeaseID,
		AuxPolicy:     domain.AuxPolicyAcquireOwn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !own.Acquired || own.LeaseID == "" || own.LeaseID == p2.LeaseID {
		t.Fatalf("expected distinct aux lease: %+v parent=%q", own, p2.LeaseID)
	}
}

func TestProviderAdapter_ImplementsConcurrencyProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(2)}, store, now)
	var provider authority.ConcurrencyProvider = app.NewProvider(svc)

	dec, err := provider.AdmitLease(ctx, authority.LeaseAdmission{
		RequestID: "req-1",
		Scope:     principalScope("alice"),
		Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("%+v", dec)
	}
	renewed, err := provider.RenewLease(ctx, authority.LeaseRenew{
		LeaseID:            dec.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: dec.Generation,
		TTL:                time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Generation != dec.Generation+1 {
		t.Fatalf("generation=%d", renewed.Generation)
	}
	if err := provider.ReleaseLease(ctx, authority.LeaseRelease{LeaseID: dec.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	page, err := provider.QueryLeases(ctx, authority.LeaseQuery{LeaseID: dec.LeaseID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Leases) != 1 || page.Leases[0].State != authority.LeaseStateReleased {
		t.Fatalf("query=%+v", page)
	}
}
