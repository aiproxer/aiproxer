package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

var errAuthorityLifecycleSettleFailed = errors.New("settle failed")

// TestAuthorityLifecycle exercises the authorityLifecycle owner in isolation:
// the settle/release/reset contract and the settled idempotency guard. No
// Executor or retryRecvStream is involved, so this pins the owner's behavior
// before Phase 4b/4c migrate scattered call sites onto it.
func TestAuthorityLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// newActiveLifecycle builds an owner over a reserved admission state
	// (TraceID set, Reserved=true) so Settle/Release are active.
	newActiveLifecycle := func(svc UsageAuthorityService) authorityLifecycle {
		state := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(100),
			admissionResult: usagelimit.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
			},
		}
		return newAuthorityLifecycle(svc, nil, state, authorityCandidate())
	}

	// newRecorder builds a recordingAuthorityService pre-loaded with a reserved
	// admission result so an owner built over it is active.
	newRecorder := func() *recordingAuthorityService {
		return &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
			},
		}
	}

	t.Run("Settle success settles once and is idempotent", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); !applied {
			t.Fatalf("Settle on success = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after first Settle = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after successful Settle = %d, want 0", got)
		}
		if in := rec.lastSettle(); in.Kind != usagelimit.SettlementKindFinal {
			t.Fatalf("settle kind = %q, want %q", in.Kind, usagelimit.SettlementKindFinal)
		}

		// Second Settle must be a no-op: the settled guard is already true.
		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); applied {
			t.Fatalf("second Settle = true, want false (idempotent)")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after second Settle = %d, want 1 (no further service calls)", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after second Settle = %d, want 0", got)
		}
	})

	t.Run("Settle forwards BoundVersion from admission", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		state := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(100),
			admissionResult: usagelimit.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r1",
				ReservedAmount: authorityInputAmount(100),
				BoundVersion: economics.PolicySnapshotRef{
					VersionRef: economics.VersionRef{ID: "usage_authority", Version: "v1"},
					PolicyID:   "usage_authority",
				},
			},
		}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); !applied {
			t.Fatal("Settle = false, want true")
		}
		got := rec.lastSettle().BoundVersion
		if got.Version != "v1" || got.PolicyID != "usage_authority" {
			t.Fatalf("Bound version = %+v, want v1/usage_authority", got)
		}
	})

	t.Run("Settle failure triggers losing release and is idempotent", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		rec.settleErr = errAuthorityLifecycleSettleFailed
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); applied {
			t.Fatalf("Settle on failure = true, want false")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after failing Settle = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after failing Settle = %d, want 1 (losing fallback)", got)
		}
		if rel := rec.lastRelease(); rel.Kind != usagelimit.ReleaseKindLosing {
			t.Fatalf("fallback release kind = %q, want %q", rel.Kind, usagelimit.ReleaseKindLosing)
		}

		// A following Settle and Release must both be no-ops (settled guard true).
		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); applied {
			t.Fatalf("Settle after failure = true, want false (idempotent)")
		}
		l.Release(ctx, usagelimit.ReleaseKindSwallowed)
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after follow-up = %d, want 1", got)
		}
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after follow-up = %d, want 1", got)
		}
	})

	t.Run("Settle not-applied without error triggers losing release", func(t *testing.T) {
		t.Parallel()
		svc := &notAppliedAuthorityService{}
		l := newActiveLifecycle(svc)

		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); applied {
			t.Fatalf("Settle not-applied = true, want false")
		}
		if got := svc.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1", got)
		}
		if got := svc.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls = %d, want 1 (losing fallback for not-applied)", got)
		}
		if rel := svc.lastReleaseInput(); rel.Kind != usagelimit.ReleaseKindLosing {
			t.Fatalf("fallback release kind = %q, want %q", rel.Kind, usagelimit.ReleaseKindLosing)
		}
	})

	t.Run("Release releases once with the right kind and is idempotent", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		l.Release(ctx, usagelimit.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after first Release = %d, want 1", got)
		}
		if rel := rec.lastRelease(); rel.Kind != usagelimit.ReleaseKindSwallowed {
			t.Fatalf("release kind = %q, want %q", rel.Kind, usagelimit.ReleaseKindSwallowed)
		}

		l.Release(ctx, usagelimit.ReleaseKindLosing)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls after second Release = %d, want 1 (idempotent)", got)
		}
	})

	t.Run("Release after Settle is a no-op", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); !applied {
			t.Fatalf("Settle = false, want true")
		}
		l.Release(ctx, usagelimit.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after Settle = %d, want 0 (Settle already settled)", got)
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1", got)
		}
	})

	t.Run("Reset clears the settled guard for a fresh settle", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder()
		l := newActiveLifecycle(rec)

		if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); !applied {
			t.Fatalf("first Settle = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls after first Settle = %d, want 1", got)
		}

		state2 := attemptAuthorityState{
			admissionInput: testAuthorityAdmissionInput(200),
			admissionResult: usagelimit.AdmissionResult{
				Reserved:       true,
				ReservationID:  "r2",
				ReservedAmount: authorityInputAmount(200),
			},
		}
		cand2 := routing.AttemptCandidate{
			Primary: routing.Primary{Backend: "backend-2", Model: "model-2"},
			Key:     "backend-2:model-2",
		}
		l.Reset(state2, cand2)

		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); !applied {
			t.Fatalf("Settle after Reset = false, want true (guard cleared)")
		}
		if got := rec.settleCalls.Load(); got != 2 {
			t.Fatalf("settle calls after Reset+Settle = %d, want 2 (fresh settle)", got)
		}
		if got := rec.releaseCalls.Load(); got != 0 {
			t.Fatalf("release calls after Reset+Settle = %d, want 0", got)
		}
		if in := rec.lastSettle(); in.ReservationID != "r2" {
			t.Fatalf("settle ReservationID after Reset = %q, want r2", in.ReservationID)
		}
		if in := rec.lastSettle(); in.Kind != usagelimit.SettlementKindPartial {
			t.Fatalf("settle kind after Reset = %q, want %q", in.Kind, usagelimit.SettlementKindPartial)
		}
	})

	t.Run("Inactive owner no-ops Settle and Release", func(t *testing.T) {
		t.Parallel()
		t.Run("empty state", func(t *testing.T) {
			t.Parallel()
			rec := newRecorder()
			l := newAuthorityLifecycle(rec, nil, attemptAuthorityState{}, authorityCandidate())
			if l.IsActive() {
				t.Fatalf("IsActive on empty state = true, want false")
			}
			if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); applied {
				t.Fatalf("Settle on inactive owner = true, want false")
			}
			l.Release(ctx, usagelimit.ReleaseKindLosing)
			if got := rec.settleCalls.Load(); got != 0 {
				t.Fatalf("settle calls on inactive owner = %d, want 0", got)
			}
			if got := rec.releaseCalls.Load(); got != 0 {
				t.Fatalf("release calls on inactive owner = %d, want 0", got)
			}
		})

		t.Run("reserved false", func(t *testing.T) {
			t.Parallel()
			rec := newRecorder()
			state := attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(100),
				admissionResult: usagelimit.AdmissionResult{Reserved: false},
			}
			l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
			if l.IsActive() {
				t.Fatalf("IsActive with Reserved=false = true, want false")
			}
			if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); applied {
				t.Fatalf("Settle on inactive (Reserved=false) = true, want false")
			}
			l.Release(ctx, usagelimit.ReleaseKindLosing)
			if got := rec.settleCalls.Load(); got != 0 {
				t.Fatalf("settle calls on inactive (Reserved=false) = %d, want 0", got)
			}
			if got := rec.releaseCalls.Load(); got != 0 {
				t.Fatalf("release calls on inactive (Reserved=false) = %d, want 0", got)
			}
		})
	})
}

func TestAuthorityLifecycleSerializesConcurrentTerminalOperations(t *testing.T) {
	t.Parallel()
	rec := &recordingAuthorityService{admitResult: usagelimit.AdmissionResult{
		Reserved: true, ReservationID: "r-race", ReservedAmount: authorityInputAmount(10),
	}}
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: rec.admitResult,
	}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(settle bool) {
			defer wg.Done()
			<-start
			if settle {
				lifecycle.Settle(context.Background(), usagelimit.SettlementKindFinal, aipapi.Event{}, false)
				return
			}
			lifecycle.Release(context.Background(), usagelimit.ReleaseKindLosing)
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	if calls := rec.settleCalls.Load() + rec.releaseCalls.Load(); calls != 1 {
		t.Fatalf("terminal service calls = %d, want exactly 1", calls)
	}
}

type resetRaceAuthorityService struct {
	entered  chan struct{}
	unblock  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	settles  []usagelimit.SettleInput
	releases []usagelimit.ReleaseInput
}

func (s *resetRaceAuthorityService) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{}, nil
}

func (s *resetRaceAuthorityService) Settle(_ context.Context, in usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	s.mu.Lock()
	s.settles = append(s.settles, in)
	s.mu.Unlock()
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.unblock:
	case <-time.After(time.Second):
		return usagelimit.SettleResult{}, context.DeadlineExceeded
	}
	return usagelimit.SettleResult{Applied: true}, nil
}

func (s *resetRaceAuthorityService) Release(_ context.Context, in usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	s.mu.Lock()
	s.releases = append(s.releases, in)
	s.mu.Unlock()
	return usagelimit.ReleaseResult{Applied: true}, nil
}

func (s *resetRaceAuthorityService) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

func TestAuthorityLifecycleResetSharesPayloadSynchronization(t *testing.T) {
	t.Parallel()
	svc := &resetRaceAuthorityService{entered: make(chan struct{}), unblock: make(chan struct{})}
	oldState := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(1),
		admissionResult: usagelimit.AdmissionResult{Reserved: true, ReservationID: "old-reservation", ReservedAmount: authorityInputAmount(1)},
	}
	lifecycle := newAuthorityLifecycle(svc, nil, oldState, authorityCandidate())
	settled := make(chan bool, 1)
	go func() {
		settled <- lifecycle.Settle(context.Background(), usagelimit.SettlementKindFinal, aipapi.Event{}, false)
	}()
	<-svc.entered

	newState := oldState
	newState.admissionResult.ReservationID = "new-reservation"
	resetDone := make(chan struct{})
	go func() {
		lifecycle.Reset(newState, routing.AttemptCandidate{Key: "new-candidate"})
		close(resetDone)
	}()
	select {
	case <-resetDone:
		t.Fatal("Reset completed while Settle still owned the lifecycle mutex")
	case <-time.After(20 * time.Millisecond):
	}
	close(svc.unblock)
	if !<-settled {
		t.Fatal("old settlement was not applied")
	}
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not complete after settlement released the mutex")
	}

	svc.mu.Lock()
	if len(svc.settles) != 1 || svc.settles[0].ReservationID != "old-reservation" {
		t.Fatalf("settlement payload = %#v, want old reservation only", svc.settles)
	}
	svc.mu.Unlock()
	if got := lifecycle.stateSnapshot().admissionResult.ReservationID; got != "new-reservation" {
		t.Fatalf("lifecycle state after Reset = %q, want new-reservation", got)
	}
	if lifecycle.Settled() {
		t.Fatal("Reset must reopen the replacement lifecycle")
	}
	lifecycle.Release(context.Background(), usagelimit.ReleaseKindLosing)
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.releases) != 1 || svc.releases[0].ReservationID != "new-reservation" {
		t.Fatalf("release payload = %#v, want replacement reservation", svc.releases)
	}
}

func TestAuthorityLifecycleMultiReservationSettleAndReleaseEachReservation(t *testing.T) {
	t.Parallel()

	secondaryAmount := usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 125}
	state := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(8),
		admissionResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "reservation-primary",
			ReservedAmount: authorityInputAmount(8),
			RuleIDs:        []string{"tenant.requests", "tenant.spend"},
			Reservations: []usagelimit.AdmissionReservation{
				{
					ReservationID:  "reservation-primary",
					RuleID:         "tenant.requests",
					ReservedAmount: authorityInputAmount(8),
				},
				{
					ReservationID:  "reservation-secondary",
					RuleID:         "tenant.spend",
					ReservedAmount: secondaryAmount,
				},
			},
		},
	}
	state.admissionInput.ReservationKey.RuleID = "tenant.requests"
	state.admissionInput.Request = secondaryAmount

	usageEv := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   5,
		TotalTokens:   5,
		CostNanoUnits: 40,
		Currency:      "USD",
	}

	t.Run("Settle targets every reservation", func(t *testing.T) {
		t.Parallel()

		rec := &recordingAuthorityService{}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

		if applied := l.Settle(context.Background(), usagelimit.SettlementKindFinal, usageEv, false); !applied {
			t.Fatal("Settle = false, want true")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1 atomic set", got)
		}
		inputs := rec.settleInputs()
		if len(inputs) != 1 || len(inputs[0].Reservations) != 2 {
			t.Fatalf("settle inputs = %#v, want one input with two reservations", inputs)
		}
		reservations := inputs[0].Reservations
		if reservations[0].Reservation.ReservationID != "reservation-primary" {
			t.Fatalf("primary settle reservation id = %q, want reservation-primary", reservations[0].Reservation.ReservationID)
		}
		if reservations[0].Reservation.RuleID != "tenant.requests" {
			t.Fatalf("primary settle rule id = %q, want tenant.requests", reservations[0].Reservation.RuleID)
		}
		if reservations[0].Reservation.ReservationKey.RuleID != "tenant.requests" {
			t.Fatalf("primary settle reservation key rule id = %q, want tenant.requests", reservations[0].Reservation.ReservationKey.RuleID)
		}
		if reservations[0].Reservation.Amount.Unit != usagelimit.AmountUnitInputTokens || reservations[0].Reservation.Amount.Value != 8 {
			t.Fatalf("primary settle reserved usage = %#v, want 8 input_tokens", reservations[0].Reservation.Amount)
		}
		if reservations[0].FinalUsage.Unit != usagelimit.AmountUnitInputTokens || reservations[0].FinalUsage.Value != 5 {
			t.Fatalf("primary settle final usage = %#v, want 5 input_tokens", reservations[0].FinalUsage)
		}
		if reservations[1].Reservation.ReservationID != "reservation-secondary" {
			t.Fatalf("secondary settle reservation id = %q, want reservation-secondary", reservations[1].Reservation.ReservationID)
		}
		if reservations[1].Reservation.RuleID != "tenant.spend" {
			t.Fatalf("secondary settle rule id = %q, want tenant.spend", reservations[1].Reservation.RuleID)
		}
		if reservations[1].Reservation.ReservationKey.RuleID != "tenant.spend" {
			t.Fatalf("secondary settle reservation key rule id = %q, want tenant.spend", reservations[1].Reservation.ReservationKey.RuleID)
		}
		if reservations[1].Reservation.Amount != secondaryAmount {
			t.Fatalf("secondary settle reserved usage = %#v, want %#v", reservations[1].Reservation.Amount, secondaryAmount)
		}
		if reservations[1].EstimatedUsage != secondaryAmount {
			t.Fatalf("secondary settle estimated usage = %#v, want %#v", reservations[1].EstimatedUsage, secondaryAmount)
		}
		if reservations[1].FinalUsage.Unit != usagelimit.AmountUnitInputTokens || reservations[1].FinalUsage.Value != 5 {
			t.Fatalf("secondary settle final usage = %#v, want observed 5 input_tokens", reservations[1].FinalUsage)
		}
		if !l.Settled() {
			t.Fatal("expected lifecycle settled after settling every reservation")
		}
	})

	t.Run("Release targets every reservation", func(t *testing.T) {
		t.Parallel()

		rec := &recordingAuthorityService{}
		l := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

		l.Release(context.Background(), usagelimit.ReleaseKindSwallowed)
		if got := rec.releaseCalls.Load(); got != 1 {
			t.Fatalf("release calls = %d, want 1 atomic set", got)
		}
		inputs := rec.releaseInputs()
		if len(inputs) != 1 || len(inputs[0].Reservations) != 2 {
			t.Fatalf("release inputs = %#v, want one input with two reservations", inputs)
		}
		reservations := inputs[0].Reservations
		if reservations[0].Reservation.ReservationID != "reservation-primary" {
			t.Fatalf("primary release reservation id = %q, want reservation-primary", reservations[0].Reservation.ReservationID)
		}
		if reservations[0].Reservation.RuleID != "tenant.requests" {
			t.Fatalf("primary release rule id = %q, want tenant.requests", reservations[0].Reservation.RuleID)
		}
		if reservations[0].Reservation.ReservationKey.RuleID != "tenant.requests" {
			t.Fatalf("primary release reservation key rule id = %q, want tenant.requests", reservations[0].Reservation.ReservationKey.RuleID)
		}
		if reservations[0].Reservation.Amount != authorityInputAmount(8) {
			t.Fatalf("primary release amount = %#v, want 8 requests", reservations[0].Reservation.Amount)
		}
		if reservations[1].Reservation.ReservationID != "reservation-secondary" {
			t.Fatalf("secondary release reservation id = %q, want reservation-secondary", reservations[1].Reservation.ReservationID)
		}
		if reservations[1].Reservation.RuleID != "tenant.spend" {
			t.Fatalf("secondary release rule id = %q, want tenant.spend", reservations[1].Reservation.RuleID)
		}
		if reservations[1].Reservation.ReservationKey.RuleID != "tenant.spend" {
			t.Fatalf("secondary release reservation key rule id = %q, want tenant.spend", reservations[1].Reservation.ReservationKey.RuleID)
		}
		if reservations[1].Reservation.Amount != secondaryAmount {
			t.Fatalf("secondary release amount = %#v, want %#v", reservations[1].Reservation.Amount, secondaryAmount)
		}
		if !l.Settled() {
			t.Fatal("expected lifecycle settled after releasing every reservation")
		}
	})
}

// notAppliedAuthorityService is a UsageAuthorityService stub whose Settle always
// returns Applied=false with no error, to exercise the owner's not-applied
// fallback branch (distinct from the error branch covered via settleErr on
// recordingAuthorityService).
type notAppliedAuthorityService struct {
	settleCalls  atomic.Int64
	releaseCalls atomic.Int64
	lastRelease  atomic.Value
}

func (s *notAppliedAuthorityService) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{Reserved: true, ReservationID: "r1"}, nil
}

func (s *notAppliedAuthorityService) Settle(_ context.Context, _ usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	s.settleCalls.Add(1)
	return usagelimit.SettleResult{Applied: false}, nil
}

func (s *notAppliedAuthorityService) Release(_ context.Context, in usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.lastRelease.Store(in)
	return usagelimit.ReleaseResult{Applied: true}, nil
}

func (s *notAppliedAuthorityService) ApplyUsage(_ context.Context, cmd usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *notAppliedAuthorityService) lastReleaseInput() usagelimit.ReleaseInput {
	if v := s.lastRelease.Load(); v != nil {
		if in, ok := v.(usagelimit.ReleaseInput); ok {
			return in
		}
	}
	return usagelimit.ReleaseInput{}
}

// TestAuthorityLifecycleSettleReconcilesWhenReservationKeyRuleIDIsCandidateKey
// pins the runtime integration fix for the reservation-key rule-id mismatch
// leak reported by review.
//
// The runtime's attemptAuthorityReservationKey puts the routing candidate key
// (e.g. "backend-1:model-1") into ReservationKey.RuleID because it cannot know
// which authority rules will match until the injected port evaluates the
// request. The admission result reports the matched per-rule reservation via
// AdmissionReservation.RuleID; the authorityLifecycle owner then rebuilds the
// settle/release key using AdmissionReservation.RuleID (the real rule id).
//
// The owner must submit that rebuilt key, never the candidate key: the
// Enterprise store reconciles the reservation by the admitted key, so a
// candidate-key rebuild would leak the strict capacity. The reconciliating
// store side is Enterprise rule semantics
// (TestEnterpriseRuleSettleReconcilesWhenReservationKeyRuleIDIsCandidateKey).
func TestAuthorityLifecycleSettleReconcilesWhenReservationKeyRuleIDIsCandidateKey(t *testing.T) {
	t.Parallel()

	rec := &recordingAuthorityService{}
	rec.admitResult = usagelimit.AdmissionResult{
		Allowed:        true,
		Reserved:       true,
		ReservationID:  "reservation-candidate-key",
		ReservedAmount: authorityInputAmount(3),
		Reservations: []usagelimit.AdmissionReservation{{
			ReservationID:  "reservation-candidate-key",
			RuleID:         "tenant.requests",
			ReservedAmount: authorityInputAmount(3),
		}},
	}

	// Mimic attemptAuthorityReservationKey: the routing candidate key is NOT an
	// authority rule id, but the runtime cannot know the matched rule id until
	// the port evaluates the request.
	admissionInput := testAuthorityAdmissionInput(3)
	admissionInput.ReservationKey.RuleID = "backend-1:model-1"

	state := attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: rec.admitResult,
	}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 3, TotalTokens: 3}
	if applied := lifecycle.Settle(context.Background(), usagelimit.SettlementKindFinal, usageEv, false); !applied {
		t.Fatal("Settle = false, want true (reservation must be reconciled, not leaked)")
	}
	got := rec.lastSettle()
	if len(got.Reservations) != 1 {
		t.Fatalf("settle reservations = %#v, want the admitted reservation", got.Reservations)
	}
	reservation := got.Reservations[0]
	if reservation.Reservation.RuleID != "tenant.requests" {
		t.Fatalf("settle rule id = %q, want the admission-reported rule id tenant.requests", reservation.Reservation.RuleID)
	}
	if reservation.Reservation.ReservationKey.RuleID != "tenant.requests" {
		t.Fatalf("settle reservation key rule id = %q, want the matched rule id tenant.requests (rebuilding from the candidate key leaks the reservation)",
			reservation.Reservation.ReservationKey.RuleID)
	}
	if reservation.Reservation.Amount != authorityInputAmount(3) {
		t.Fatalf("settle reserved amount = %#v, want the admitted 3 input_tokens", reservation.Reservation.Amount)
	}
	if !lifecycle.Settled() {
		t.Fatal("expected lifecycle settled after Settle")
	}
}
