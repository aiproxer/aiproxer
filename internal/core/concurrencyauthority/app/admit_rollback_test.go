package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// rollbackRecordingStore wraps memoryStore to record which leases were newly
// acquired and which were released, and to fail Acquire for one rule.
type rollbackRecordingStore struct {
	*memoryStore
	recMu      sync.Mutex
	acquired   []string
	released   []string
	failRuleID string
	err        error
	releaseErr error
}

func (s *rollbackRecordingStore) Acquire(ctx context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	if cmd.RuleID == s.failRuleID {
		return app.AcquireResult{}, s.err
	}
	res, err := s.memoryStore.Acquire(ctx, cmd)
	if err == nil && !res.Replayed && !res.CapacityExceeded && !res.Rejected {
		s.recMu.Lock()
		s.acquired = append(s.acquired, res.Lease.LeaseID)
		s.recMu.Unlock()
	}
	return res, err
}

func (s *rollbackRecordingStore) AcquireSet(ctx context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	res, err := s.memoryStore.AcquireSet(ctx, cmd)
	if err == nil && !res.Replayed && !res.CapacityExceeded && !res.Rejected {
		s.recMu.Lock()
		for _, m := range res.Set.Members {
			s.acquired = append(s.acquired, m.LeaseID)
		}
		s.recMu.Unlock()
	}
	return res, err
}

func (s *rollbackRecordingStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	s.recMu.Lock()
	s.released = append(s.released, cmd.LeaseID)
	s.recMu.Unlock()
	if s.releaseErr != nil {
		return app.ReleaseResult{}, s.releaseErr
	}
	return s.memoryStore.Release(ctx, cmd)
}

func (s *rollbackRecordingStore) recordings() (acquired, released []string) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return append([]string(nil), s.acquired...), append([]string(nil), s.released...)
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		counts[id]--
		if counts[id] < 0 {
			return false
		}
	}
	return true
}

func TestAdmit_AdvisoryStoreErrorRollsBackStrictSetLeases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"
	adv := advisoryRule(5)
	adv.ID = "rule-adv"

	wantErr := errors.New("advisory acquire store fault")
	store := &rollbackRecordingStore{memoryStore: newMemoryStore(), failRuleID: "rule-adv", err: wantErr}
	svc := newService(t, []domain.Rule{ruleA, ruleB, adv}, store, now)

	res, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-rollback", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want wrapped %v", err, wantErr)
	}
	if len(res.Leases) != 0 || res.LeaseID != "" {
		t.Fatalf("error result must carry no leases: %+v", res)
	}

	acquired, released := store.recordings()
	if len(acquired) != 2 {
		t.Fatalf("strict set acquired=%v want 2 members", acquired)
	}
	if !sameIDs(acquired, released) {
		t.Fatalf("rollback released=%v want every acquired strict lease %v", released, acquired)
	}
	if live := countLiveLeases(t, svc, "req-rollback", now); live != 0 {
		t.Fatalf("live leases=%d want 0 after error rollback", live)
	}
}

func TestAdmit_AdvisoryStoreErrorRollsBackEarlierAdvisoryLeases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	advOK := advisoryRule(5)
	advOK.ID = "rule-adv-ok"
	advFail := advisoryRule(5)
	advFail.ID = "rule-adv-fail"

	wantErr := errors.New("second advisory acquire store fault")
	store := &rollbackRecordingStore{memoryStore: newMemoryStore(), failRuleID: "rule-adv-fail", err: wantErr}
	svc := newService(t, []domain.Rule{advOK, advFail}, store, now)

	res, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-rollback-adv", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want wrapped %v", err, wantErr)
	}
	if len(res.Leases) != 0 || res.LeaseID != "" {
		t.Fatalf("error result must carry no leases: %+v", res)
	}

	acquired, released := store.recordings()
	if len(acquired) != 1 {
		t.Fatalf("advisory acquired=%v want 1 lease from rule-adv-ok", acquired)
	}
	if !sameIDs(acquired, released) {
		t.Fatalf("rollback released=%v want earlier advisory lease %v", released, acquired)
	}
	if live := countLiveLeases(t, svc, "req-rollback-adv", now); live != 0 {
		t.Fatalf("live leases=%d want 0 after error rollback", live)
	}
}

func TestAdmit_AdvisoryStoreErrorDoesNotReleaseReplayedSetLeases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	adv := advisoryRule(5)
	adv.ID = "rule-adv"
	rules := []domain.Rule{ruleA, adv}

	base := newMemoryStore()
	seedSvc := newService(t, rules, base, now)
	seed, err := seedSvc.Admit(ctx, app.AdmitInput{
		RequestID: "req-replay", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || seed.Kind != domain.DecisionAllow {
		t.Fatalf("seed admit=%+v err=%v", seed, err)
	}

	wantErr := errors.New("advisory acquire store fault")
	store := &rollbackRecordingStore{memoryStore: base, failRuleID: "rule-adv", err: wantErr}
	svc := newService(t, rules, store, now)

	_, err = svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-replay", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want wrapped %v", err, wantErr)
	}

	acquired, released := store.recordings()
	if len(acquired) != 0 {
		t.Fatalf("replayed set must record no new acquisitions, got %v", acquired)
	}
	if len(released) != 0 {
		t.Fatalf("error rollback must not release replayed pre-existing leases, released=%v", released)
	}
	if live := countLiveLeases(t, svc, "req-replay", now); live != 2 {
		t.Fatalf("live leases=%d want 2 (seed strict + advisory leases survive)", live)
	}
}
