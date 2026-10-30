package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func attachPhase6Coordinators(ex *Executor) {
	if ex == nil || ex.UsageAuthority == nil {
		return
	}
	req, att := BuildAuthorityCoordinators(ex.UsageAuthority, ex.ConcurrencyProvider)
	ex.RequestCoordinator = req
	ex.AttemptCoordinator = att
}

// TestPhase6_requestAdmitOnceZerosAttemptRequestCount proves prepare-path request
// admission reserves request-count once; subsequent B-leg admits use RequestCount=0.
func TestPhase6_requestAdmitOnceZerosAttemptRequestCount(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	attachPhase6Coordinators(ex)
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	ctx := context.Background()
	ctx, err := ex.admitRequestAuthorityOnce(ctx, "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admitRequestAuthorityOnce: %v", err)
	}
	if requestAuthorityFrom(ctx) == nil {
		t.Fatal("expected request authority state after admit")
	}

	budget := &attemptBudget{max: 5}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	out, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err != nil {
		t.Fatalf("evaluateAndOpenCandidate: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected open")
	}

	authoritative := auth.authoritativeAdmits()
	if got, want := len(authoritative), 2; got != want {
		t.Fatalf("authoritative admits = %d, want %d (1 request + 1 attempt)", got, want)
	}
	reqAdmit, attAdmit := authoritative[0], authoritative[1]
	if reqAdmit.ReservationKey.BLegID != "" {
		t.Fatalf("request admit must not carry BLegID, got %q", reqAdmit.ReservationKey.BLegID)
	}
	if reqAdmit.RequestCount.Unit != usagelimit.AmountUnitRequests || reqAdmit.RequestCount.Value != 1 {
		t.Fatalf("request admit RequestCount = %v, want 1 requests", reqAdmit.RequestCount)
	}
	if attAdmit.ReservationKey.BLegID == "" {
		t.Fatal("attempt admit must carry BLegID")
	}
	if attAdmit.RequestCount.Value != 0 {
		t.Fatalf("attempt admit RequestCount = %v, want 0 (already reserved at request stage)", attAdmit.RequestCount)
	}
}

// TestPhase6_parallelRaceDoesNotReReserveCustomerRequestCount proves parallel
// B-legs each admit attempt authority without re-running request-count.
func TestPhase6_parallelRaceDoesNotReReserveCustomerRequestCount(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
	attachPhase6Coordinators(ex)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	leg2OpenedCh := make(chan struct{}, 1)
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	ex.Backends["backend-1"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: leg2OpenedCh,
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "winner"},
					{Kind: aipapi.EventResponseFinished},
				},
			}, nil
		},
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: leg2OpenedCh}, nil
		},
	}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admitRequestAuthorityOnce: %v", err)
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	req.reqFacts.baseline.Route.Selector = "backend-1:model-1!backend-2:model-2"
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(ctx, req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel race to open a backend")
	}

	authoritative := auth.authoritativeAdmits()
	var requestCountOnes, attemptAdmits int
	for _, in := range authoritative {
		if in.ReservationKey.BLegID == "" && in.RequestCount.Value == 1 {
			requestCountOnes++
			continue
		}
		if in.ReservationKey.BLegID != "" {
			attemptAdmits++
			if in.RequestCount.Value != 0 {
				t.Fatalf("attempt admit RequestCount=%v want 0; key=%s", in.RequestCount, in.ReservationKey.String())
			}
		}
	}
	if requestCountOnes != 1 {
		t.Fatalf("customer request-count admits = %d, want 1", requestCountOnes)
	}
	if attemptAdmits != 2 {
		t.Fatalf("attempt admits = %d, want 2 (one per parallel B-leg)", attemptAdmits)
	}
}

// TestPhase6_settleRequestAuthorityOnce ensures request settle is idempotent.
func TestPhase6_settleRequestAuthorityOnce(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	attachPhase6Coordinators(ex)

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	_ = ex.settleRequestAuthority(ctx, nil)
	_ = ex.settleRequestAuthority(ctx, nil)

	// Re-admit must no-op when state already present.
	ctx2, err := ex.admitRequestAuthorityOnce(ctx, "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if requestAuthorityFrom(ctx2) != requestAuthorityFrom(ctx) {
		t.Fatal("second admit must keep existing request authority state")
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || !st.Settled {
		t.Fatal("expected settled request authority state")
	}
}

func TestPhase6_releaseRequestAuthority(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	attachPhase6Coordinators(ex)

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "request-1", aLegID, "trace-1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	_ = ex.releaseRequestAuthority(ctx)
	_ = ex.releaseRequestAuthority(ctx) // idempotent

	if got := auth.releaseCalls.Load(); got < 1 {
		t.Fatalf("releaseCalls=%d want >=1", got)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || !st.Released {
		t.Fatal("expected released flag")
	}
}
