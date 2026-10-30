package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestOpenInitialAttempt_RegisterBLegFailureReleasesAuthority reproduces L6: when
// RegisterBLeg fails on the first attempt, the freshly-admitted out.authority
// reservation is orphaned because the error branch closes out.stream but never
// releases out.authority (which is not stored into any holder field on this path).
//
// The seam cancels the A-leg inside backend.Open (the same seam used by
// TestExecutor_RegisterBLegFailureDoesNotDoubleCancelStream) so the backend opens
// (out.opened=true, authority carried in out.authority) and the subsequent
// RegisterBLeg fails with ErrALegCanceled. The freshly-admitted reservation must
// then be released with ReleaseKindSwallowed, mirroring the swallowed-authority
// release sites in executor_recv_loop.go.
func TestOpenInitialAttempt_RegisterBLegFailureReleasesAuthority(t *testing.T) {
	t.Parallel()

	const reservationID = "reservation-bleg-leak"
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  reservationID,
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	backend.openFn = func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
			return nil, err
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	rng := routing.NewSeededRng(1)
	progress := newRecoveryController(recoveryControllerInput{
		budget:   &attemptBudget{max: 3},
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      rng,
	})
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: rng,
		},
		progress: progress,
	}
	call := &aipapi.Call{
		ID:    "request-leak",
		Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Session: aipapi.SessionRef{
			ALegID: aLegID,
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: testMinimalUserMessages(),
	}
	preSession := session.SessionView{
		ALegID: aLegID,
	}
	ibt, err := newIdentityBoundTurn(
		"trace-leak",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: aLegID},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		bus:      hooks.New(hooks.Config{}),
		identity: ibt,
		call:     ibt.call,
		aScope:   aScope,
	}

	_, err = ex.openInitialAttempt(context.Background(), prep, plan)
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("openInitialAttempt err = %v, want ErrALegCanceled", err)
	}
	if auth.admitCalls.Load() != 3 {
		t.Fatalf("admit calls = %d, want 3 (estimate-only precheck + clamp preview + authoritative admit)", auth.admitCalls.Load())
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 (incurred out.authority must settle when RegisterBLeg fails)", auth.releaseCalls.Load())
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1 (out.authority must settle when RegisterBLeg fails)", auth.settleCalls.Load())
	}
	settle := auth.lastSettle()
	if settle.ReservationID != reservationID {
		t.Fatalf("settle reservation ID = %q, want %q", settle.ReservationID, reservationID)
	}
	if settle.Kind != usagelimit.SettlementKindSwallowed {
		t.Fatalf("settle kind = %q, want swallowed", settle.Kind)
	}
}
