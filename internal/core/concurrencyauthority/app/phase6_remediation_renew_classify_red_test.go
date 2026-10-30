package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

type countingUncertainStore struct {
	*memoryStore
	uncertainCalls int
	renewSetErr    error
}

func (s *countingUncertainStore) RenewSet(ctx context.Context, cmd app.RenewSetCommand) (app.RenewSetResult, error) {
	if s.renewSetErr != nil {
		return app.RenewSetResult{}, s.renewSetErr
	}
	return s.memoryStore.RenewSet(ctx, cmd)
}

func (s *countingUncertainStore) MarkSetUncertain(ctx context.Context, setID string, now time.Time) error {
	s.uncertainCalls++
	return s.memoryStore.MarkSetUncertain(ctx, setID, now)
}

func TestPhase6Remediation_RenewSetAmbiguousMarksUncertain(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 18, 0, 0, 0, time.UTC)
	base := newMemoryStore()
	store := &countingUncertainStore{memoryStore: base, renewSetErr: context.DeadlineExceeded}
	svc := newService(t, []domain.Rule{strictRule(5)}, store, now)
	_, err := svc.Renew(context.Background(), app.RenewInput{
		SetID: "set-1", RequestID: "req-1", ExpectedGeneration: 1,
		TTL: time.Minute, RenewBefore: 15 * time.Second,
	})
	if err == nil {
		t.Fatal("want renew error")
	}
	if store.uncertainCalls != 1 {
		t.Fatalf("uncertainCalls=%d want 1 for ambiguous timeout", store.uncertainCalls)
	}
}

func TestPhase6Remediation_RenewSetGenerationMismatchDoesNotStrand(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 18, 0, 0, 0, time.UTC)
	base := newMemoryStore()
	store := &countingUncertainStore{memoryStore: base, renewSetErr: domain.ErrGenerationMismatch}
	svc := newService(t, []domain.Rule{strictRule(5)}, store, now)
	_, err := svc.Renew(context.Background(), app.RenewInput{
		SetID: "set-1", RequestID: "req-1", ExpectedGeneration: 1,
		TTL: time.Minute, RenewBefore: 15 * time.Second,
	})
	if !errors.Is(err, domain.ErrGenerationMismatch) {
		t.Fatalf("err=%v want generation mismatch", err)
	}
	if store.uncertainCalls != 0 {
		t.Fatalf("deterministic mismatch must not MarkSetUncertain, calls=%d", store.uncertainCalls)
	}
}

func TestPhase6Remediation_RenewSetNotFoundDoesNotStrand(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 18, 0, 0, 0, time.UTC)
	base := newMemoryStore()
	store := &countingUncertainStore{memoryStore: base, renewSetErr: app.ErrNotFound}
	svc := newService(t, []domain.Rule{strictRule(5)}, store, now)
	_, err := svc.Renew(context.Background(), app.RenewInput{
		SetID: "missing", RequestID: "req-1", ExpectedGeneration: 1,
		TTL: time.Minute, RenewBefore: 15 * time.Second,
	})
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err=%v want not found", err)
	}
	if store.uncertainCalls != 0 {
		t.Fatalf("not-found must not MarkSetUncertain, calls=%d", store.uncertainCalls)
	}
}
