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

// ctxCheckingStore mirrors production lease stores: Release and ReleaseSet reject
// cancelled contexts. Advisory Acquire cancels the parent ctx before checking it,
// simulating client cancellation racing the advisory admission path.
type ctxCheckingStore struct {
	*memoryStore
	recMu  sync.Mutex
	cancel context.CancelFunc
}

func (s *ctxCheckingStore) Acquire(ctx context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if err := ctx.Err(); err != nil {
		return app.AcquireResult{}, err
	}
	return s.memoryStore.Acquire(ctx, cmd)
}

func (s *ctxCheckingStore) AcquireSet(ctx context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	if err := ctx.Err(); err != nil {
		return app.AcquireSetResult{}, err
	}
	return s.memoryStore.AcquireSet(ctx, cmd)
}

func (s *ctxCheckingStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	return s.memoryStore.Release(ctx, cmd)
}

func (s *ctxCheckingStore) ReleaseSet(ctx context.Context, cmd app.ReleaseSetCommand) (app.ReleaseSetResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseSetResult{}, err
	}
	return s.memoryStore.ReleaseSet(ctx, cmd)
}

func TestAdmit_AdvisoryContextCanceledRollsBackStrictSetLeases(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	ruleA := strictRule(5)
	ruleA.ID = "rule-a"
	ruleB := strictRule(5)
	ruleB.ID = "rule-b"
	adv := advisoryRule(5)
	adv.ID = "rule-adv"

	store := &ctxCheckingStore{memoryStore: newMemoryStore(), cancel: cancel}
	svc := newService(t, []domain.Rule{ruleA, ruleB, adv}, store, now)

	res, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-cancel-rollback", Scope: principalScope("alice"), Namespace: "default",
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if len(res.Leases) != 0 || res.LeaseID != "" {
		t.Fatalf("error result must carry no leases: %+v", res)
	}
	if live := countLiveLeases(t, svc, "req-cancel-rollback", now); live != 0 {
		t.Fatalf("live leases=%d want 0 after cancelled-advisory rollback", live)
	}
}
