package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type blockingAuthorityService struct {
	settleCalls          atomic.Int64
	releaseCalls         atomic.Int64
	applyCalls           atomic.Int64
	releaseSucceeds      atomic.Bool
	releaseHadLiveBudget atomic.Bool
	lastErr              atomic.Value
}

func (s *blockingAuthorityService) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{}, nil
}

func (s *blockingAuthorityService) wait(ctx context.Context) error {
	<-ctx.Done()
	err := ctx.Err()
	s.lastErr.Store(err)
	return err
}

func (s *blockingAuthorityService) Settle(ctx context.Context, _ usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	s.settleCalls.Add(1)
	return usagelimit.SettleResult{}, s.wait(ctx)
}

func (s *blockingAuthorityService) Release(ctx context.Context, _ usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	if s.releaseSucceeds.Load() {
		deadline, ok := ctx.Deadline()
		s.releaseHadLiveBudget.Store(ctx.Err() == nil && ok && time.Until(deadline) > 0)
		return usagelimit.ReleaseResult{Applied: true}, nil
	}
	return usagelimit.ReleaseResult{}, s.wait(ctx)
}

func (s *blockingAuthorityService) ApplyUsage(ctx context.Context, _ usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	s.applyCalls.Add(1)
	return usagelimit.ApplyUsageResult{}, s.wait(ctx)
}

func boundedAuthorityLifecycle(svc UsageAuthorityService) authorityLifecycle {
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(1),
		admissionResult: usagelimit.AdmissionResult{Reserved: true, ReservationID: "reservation", ReservedAmount: authorityInputAmount(1), RuleIDs: []string{"rule"}, UnreservedRuleIDs: []string{"advisory"}},
		cleanupTimeout:  500 * time.Millisecond,
	}
	return newAuthorityLifecycle(svc, nil, state, authorityCandidate())
}

func assertBoundedAuthorityCall(t *testing.T, call func()) {
	t.Helper()
	started := time.Now()
	call()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("authority cleanup elapsed %v, want bounded", elapsed)
	}
}

func TestAuthorityLifecycleDetachedOperationsAreBounded(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("fallback release gets a fresh deadline", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		svc.releaseSucceeds.Store(true)
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() { lifecycle.Settle(parent, usagelimit.SettlementKindCancellation, aipapi.Event{}, true) })
		if svc.settleCalls.Load() != 1 || svc.releaseCalls.Load() != 1 {
			t.Fatalf("calls settle=%d release=%d, want one each", svc.settleCalls.Load(), svc.releaseCalls.Load())
		}
		if err, _ := svc.lastErr.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("context error = %v, want deadline exceeded", err)
		}
		if !svc.releaseHadLiveBudget.Load() || !lifecycle.Settled() {
			t.Fatal("fallback release must receive a fresh live budget and settle the lifecycle")
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() { lifecycle.Release(parent, usagelimit.ReleaseKindLosing) })
		if svc.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d", svc.releaseCalls.Load())
		}
	})

	t.Run("unreserved usage", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() {
			lifecycle.ApplyUnreservedUsage(parent, usagelimit.SettlementKindCancellation, aipapi.Event{})
		})
		if svc.applyCalls.Load() != 1 {
			t.Fatalf("apply calls = %d", svc.applyCalls.Load())
		}
	})

	t.Run("authoritative reconciliation", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		lifecycle.control.mu.Lock()
		lifecycle.control.terminal = authorityTerminalSettled
		lifecycle.control.mu.Unlock()
		event := aipapi.Event{Kind: aipapi.EventUsageDelta, Accounting: aipapi.UsageAccountingMetadata{Authority: aipapi.UsageAuthorityAuthoritative, Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported}}
		assertBoundedAuthorityCall(t, func() { lifecycle.ReconcileAuthoritative(parent, event) })
		if svc.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d", svc.settleCalls.Load())
		}
	})
}
