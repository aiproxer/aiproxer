package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestRetryRecvStreamRegisterBLegFailureReleasesNewAuthority reproduces L7: when
// tryReplacementIteration reserves a NEW out.authority for a recv-phase
// replacement attempt and RegisterBLeg then fails (the A-leg is canceled
// mid-flight during backend.Open, after out.authority was admitted), the freshly
// admitted reservation must be released. Before the fix the RegisterBLeg error
// branch returned without releasing out.authority, leaking the reservation.
// tryReplacementIteration also releases the prior swallowed s.authority before
// opening the replacement (so the same logical request does not double-count
// capacity under strict enforcement), so on RegisterBLeg failure both the prior
// and the NEW reservation are released; the last release must be the NEW one.
func TestRetryRecvStreamRegisterBLegFailureReleasesNewAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-new",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	ex.ALegLifecycle = coord

	// Cancel the A-leg inside backend.Open so RegisterBLeg fails with
	// ErrALegCanceled after out.authority was reserved. A real fixed stream is
	// returned so the coordinator's cancelAndClose cleanup in RegisterBLeg has a
	// B-leg attempt to tear down.
	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
			return nil, err
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	// priorAuthority is the still-reserved capacity from the swallowed prior
	// attempt; it is distinct from the NEW out.authority and is released by
	// tryReplacementIteration before the replacement is opened.
	priorAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	priorAuthority.admissionResult.ReservationID = "reservation-prior"
	priorAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:    "request-1",
				Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeStreaming,
				},
				Messages: testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-1",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3, used: 0}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1}, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}, testAuthorityLifecycle(ex, priorAuthority, routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}})),
		terminal: newTurnTerminalWithALeg(aScope, aLegEndBase),
	}
	bindTestRuntimeOwners(rs, ex)

	testRetirePriorAttempt(rs)

	plan, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
	if err == nil {
		err = rs.terminal.registerReplacement(context.Background(), plan.open, plan.next)
	}
	if err == nil {
		t.Fatal("expected tryReplacementIteration to surface the RegisterBLeg failure")
	}
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("tryReplacementIteration error = %v, want ErrALegCanceled", err)
	}

	// The prior swallowed reservation is settled before the replacement is opened,
	// and the NEW out.authority is settled on RegisterBLeg failure: 2 settles.
	if got, want := auth.releaseCalls.Load(), int64(0); got != want {
		t.Fatalf("release calls = %d, want %d (incurred attempts must settle)", got, want)
	}
	if got, want := auth.settleCalls.Load(), int64(2); got != want {
		t.Fatalf("settle calls = %d, want %d (prior settled before admit + NEW out.authority settled on RegisterBLeg failure)", got, want)
	}
	settle := auth.lastSettle()
	if settle.ReservationID != "reservation-new" {
		t.Fatalf("settled reservation ID = %q, want reservation-new (the NEW out.authority, settled last on RegisterBLeg failure)", settle.ReservationID)
	}
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
}
