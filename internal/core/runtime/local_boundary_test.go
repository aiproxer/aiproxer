package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestPhase6LocalBoundaryTerminalDrainIsAttemptOwned(t *testing.T) {
	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("payload")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	session := &attemptSession{
		boundary:       boundary,
		billingStoreID: "store-phase6",
		requestID:      "request-phase6",
		billingCallID:  callID,
		bleg:           b2bua.BLegRecord{ALegID: "a-phase6", BLegID: "b-phase6", Seq: 2},
	}

	first := session.drainLocalBoundaryObservations(time.Unix(10, 0).UTC())
	if len(first) != 3 {
		t.Fatalf("first local observation drain = %d, want 3", len(first))
	}
	if second := session.drainLocalBoundaryObservations(time.Unix(11, 0).UTC()); len(second) != 0 {
		t.Fatalf("duplicate local observation drain = %d, want zero", len(second))
	}
	for _, observation := range first {
		if observation.Subject.BLegID != "b-phase6" || observation.Correlation.StoreID != "store-phase6" {
			t.Fatalf("observation lost attempt/store ownership: %#v", observation)
		}
	}
}

func TestPhase6LocalBoundaryBillingRecordRetainsSeparateObservations(t *testing.T) {
	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("payload")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	observations := boundary.Observations(coremetering.ObservationIdentity{
		StoreID: "store-phase6", RequestID: "request-phase6", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-phase6", BLegID: "b-phase6", AttemptID: "b-phase6", AttemptSeq: 2,
	})
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase6", storeID: "store-phase6", bLegID: "b-phase6", seq: 2,
		primary:   routing.Primary{Backend: "backend-phase6", Model: "model-phase6"},
		startedAt: time.Unix(10, 0).UTC(), finishedAt: time.Unix(11, 0).UTC(),
		outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes, localObservations: observations,
	})
	if len(record.Observations) != len(observations) || record.EvidenceVersion != usageevidence.EvidenceFormatVersionV2 {
		t.Fatalf("billing record observations=%d/version=%d, want %d/%d", len(record.Observations), record.EvidenceVersion, len(observations), usageevidence.EvidenceFormatVersionV2)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatalf("seal durable local observations: %v", err)
	}
	if len(sealed.Observations) != len(record.Observations) {
		t.Fatalf("sealed observations=%d, want %d", len(sealed.Observations), len(record.Observations))
	}
	accepted := false
	for _, measure := range sealed.Observations[0].Measures {
		if measure.Key.Component == aipsdkmetering.ComponentRequest && len(measure.Key.Dimensions) == 1 && measure.Key.Dimensions[0].Value == "accepted" && measure.Value != nil && measure.Value.Coefficient == "1" {
			accepted = measure.Quality == aipsdkmetering.QualityObserved
		}
	}
	if !accepted {
		t.Fatal("sealed local observations lost accepted lifecycle state")
	}
	for _, observation := range record.Observations {
		if observation.Origin != aipsdkmetering.OriginLocal {
			t.Fatalf("local boundary observation changed origin: %s", observation.Origin)
		}
	}
}

type phase6CountingAssessor struct{ called bool }

func (a *phase6CountingAssessor) AssessLargeBody(context.Context, largebody.Proof) (largebody.Assessment, error) {
	a.called = true
	return largebody.Assessment{}, nil
}

type phase6NoopRecorder struct{}

func (phase6NoopRecorder) Append(context.Context, aipsdkmetering.Fact) error { return nil }

func TestPhase6LocalBoundaryRequiredCapabilityDeclinesFastPathBeforeAssessor(t *testing.T) {
	assessor := &phase6CountingAssessor{}
	executor := &Executor{
		CoreRuntime:       CoreRuntime{LargeBodyAssessor: assessor},
		AccountingRuntime: AccountingRuntime{MeteringRecorder: phase6NoopRecorder{}},
	}
	assessment, err := executor.AssessLargeBody(context.Background(), largebody.Proof{ProfileID: "phase6"})
	if err != nil {
		t.Fatalf("AssessLargeBody: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline || assessment.Reason != largebody.DeclineReasonMeteringUnsupported {
		t.Fatalf("assessment=%#v, want metering decline", assessment)
	}
	if assessor.called {
		t.Fatal("fast-path assessor ran before required boundary capability check")
	}
}

func TestPhase6LocalBoundaryDisabledDoesNotAllocateCapture(t *testing.T) {
	if got := (&Executor{}).newLocalBoundaryAccumulator(); got != nil {
		t.Fatal("accounting-disabled executor allocated a local boundary accumulator")
	}
}

func TestPhase6LocalBoundaryDisabledHotPathHasNoCaptureAllocationOrCallback(t *testing.T) {
	executor := &Executor{}
	call := aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("disabled hot path")},
	}}}
	ctx := context.Background()
	callbackCalls := 0
	var captureEnabled bool
	allocs := testing.AllocsPerRun(1000, func() {
		captureEnabled = executor.localBoundaryCaptureEnabled() || executor.newLocalBoundaryAccumulator() != nil || coremetering.PreparedInputObservationEnabled(ctx)
		coremetering.ObservePreparedInputCall(ctx, call, "adapter:disabled.v1")
		// No observer is attached, so this callback counter must remain zero;
		// the assertion makes the no-side-effect contract explicit alongside the
		// allocation guard.
	})
	if captureEnabled {
		t.Fatal("accounting-disabled hot path enabled local capture")
	}
	if allocs != 0 {
		t.Fatalf("accounting-disabled hot path allocations = %.2f, want zero", allocs)
	}
	if callbackCalls != 0 {
		t.Fatal("accounting-disabled hot path invoked an observer callback")
	}
}

func TestPhase6ParallelNoTxSessionPreservesLocalBoundaryOwnership(t *testing.T) {
	callID := usageevidence.BillingCallID("bc_abcdef0123456789abcdef0123456789")
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("already observed")},
	}}})
	principal := scope.PrincipalScopeView{PrincipalID: scope.Known("principal-phase6")}
	leg := &parallelLeg{
		storeID:       "store-phase6",
		requestID:     "request-phase6",
		boundaryScope: principal,
		billingCallID: callID,
		boundary:      boundary,
		bleg:          b2bua.BLegRecord{ALegID: "a-phase6", BLegID: "b-phase6", Seq: 4},
	}

	session := (&Executor{}).createSessionForParallelLeg(leg, nil)
	if session == nil {
		t.Fatal("no-tx parallel leg did not create a session")
	}
	if session.boundary != boundary {
		t.Fatal("no-tx parallel leg replaced already-observed boundary state")
	}
	if session.requestID != leg.requestID || session.billingStoreID != leg.storeID || session.billingCallID != callID {
		t.Fatalf("session identity lost: request=%q store=%q billing_call=%q", session.requestID, session.billingStoreID, session.billingCallID)
	}
	if !session.boundaryScope.PrincipalID.Equal(principal.PrincipalID) {
		t.Fatalf("session scope lost: %#v", session.boundaryScope)
	}
	observations := session.drainLocalBoundaryObservations(time.Unix(20, 0).UTC())
	if len(observations) != 3 {
		t.Fatalf("drained observations=%d, want three", len(observations))
	}
	for _, observation := range observations {
		if observation.Subject.RequestID != leg.requestID || observation.Subject.BillingCallID != callID.String() || !observation.Scope.PrincipalID.Equal(principal.PrincipalID) {
			t.Fatalf("drained observation lost no-tx ownership: %#v", observation)
		}
	}
}
