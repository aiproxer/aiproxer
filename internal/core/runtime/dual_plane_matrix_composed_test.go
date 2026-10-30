package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	corestate "github.com/aiproxer/aiproxer/internal/core/state"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Composed Phase-1 dual-plane matrix: real open/recv/failover/parallel paths with
// deterministic backends and attempt/request coordinators (task 1.5).

func attachDualPlaneMatrixCoordinators(ex *Executor, att *recordingAttemptProvider, req *settleRecordingRequestProvider) {
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: att.id, Class: authoritycoord.AttemptPriorityHardSpend, Provider: att, Strength: authority.StrengthRequired,
		}},
	}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: req.id, Class: authoritycoord.PriorityQuotaBudgetRate, Provider: req, Strength: authority.StrengthRequired,
		}},
	}
}

type matrixFilterGate struct{}

func (matrixFilterGate) ID() string                   { return "matrix-filter-gate" }
func (matrixFilterGate) Order() int                   { return 0 }
func (matrixFilterGate) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (matrixFilterGate) Handle(_ context.Context, _ completion.Meta, buf completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	// Replace delivered text while re-emitting provider usage so operator settlement
	// retains provider output/cost (seenEvents only observes client-facing drain).
	var usage aipapi.Event
	for _, ev := range buf.Events() {
		if ev.Kind == aipapi.EventUsageDelta {
			usage = ev
			break
		}
	}
	out := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventTextDelta, Delta: "filtered"},
	}
	if usage.Kind != "" {
		out = append(out, usage)
	}
	out = append(out, aipapi.Event{Kind: aipapi.EventResponseFinished})
	return completion.ReplaceOutcome(out), nil
}

type matrixFilterRespHook struct{}

func (matrixFilterRespHook) ID() string                   { return "matrix-filter-resp" }
func (matrixFilterRespHook) Order() int                   { return 0 }
func (matrixFilterRespHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (matrixFilterRespHook) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdk.PartMeta) error {
	if ev != nil && ev.Kind == aipapi.EventTextDelta {
		ev.Delta = "filtered"
	}
	return nil
}

type textThenRecoverableErrStream struct {
	phase int
}

func (s *textThenRecoverableErrStream) Recv(context.Context) (aipapi.Event, error) {
	switch s.phase {
	case 0:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	case 1:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"}, nil
	default:
		return aipapi.Event{}, &aipapi.UpstreamFailureError{
			Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "late recoverable after output",
		}
	}
}

func (s *textThenRecoverableErrStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (s *textThenRecoverableErrStream) Close() error { return nil }

type matrixCompressHook struct {
	ran atomic.Bool
}

func (h *matrixCompressHook) ID() string                   { return "matrix-compress" }
func (h *matrixCompressHook) Order() int                   { return 1 }
func (h *matrixCompressHook) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (h *matrixCompressHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
	h.ran.Store(true)
	call.Messages = []aipapi.Message{{
		Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("compressed")},
	}}
	return nil
}

func TestDualPlaneMatrix_SequentialFailoverIncurredSettlesViaOpen(t *testing.T) {
	t.Parallel()

	att := &recordingAttemptProvider{id: "matrix-seq-att"}
	req := &settleRecordingRequestProvider{id: "matrix-seq-req"}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	attachDualPlaneMatrixCoordinators(ex, att, req)

	var opens atomic.Int32
	openFn := func(_ context.Context, _ aipapi.Call, c routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		n := opens.Add(1)
		if n == 1 {
			return nil, &aipapi.UpstreamFailureError{Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "first arm fail", CandidateKey: c.Key}
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "ok"},
			{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	tcaps := parallelTransportCaps()
	ex.Backends["backend-1"] = execbackend.Backend{Caps: caps, TransportCaps: tcaps, Open: openFn}
	ex.Backends["backend-2"] = execbackend.Backend{Caps: caps, TransportCaps: tcaps, Open: openFn}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-seq", aLegID, "trace-seq", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	sel, err := routing.Parse("backend-1:model-1|backend-2:model-2")
	if err != nil {
		t.Fatal(err)
	}
	budget := &attemptBudget{max: 5}
	openReq := authorityOpenRequest(t, aLegID, budget)
	openReq.routeFacts.sel = sel
	openReq.reqFacts.baseline.Route.Selector = "backend-1:model-1|backend-2:model-2"
	openReq.reqFacts.baseline.ID = "req-seq"

	var out openedAttempt
	for range 4 {
		var openErr error
		out, openErr = ex.openNext(ctx, openReq)
		if openErr != nil {
			t.Fatalf("openNext: %v", openErr)
		}
		if out.ready != nil && out.ready.session != nil {
			break
		}
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected second candidate to open after recoverable open failure")
	}
	if opens.Load() != 2 {
		t.Fatalf("backend opens=%d want 2", opens.Load())
	}
	if att.settleCalls.Load() != 1 {
		t.Fatalf("operator SettleAttempt after failed open=%d want 1", att.settleCalls.Load())
	}
	if att.releaseCalls.Load() != 0 {
		t.Fatalf("failed incurred open must settle, not release; releaseCalls=%d", att.releaseCalls.Load())
	}

	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	life := ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate())
	if !life.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, false) {
		t.Fatal("winner settle must apply")
	}
	_ = ex.settleRequestAuthority(ctx, nil)

	if att.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (failed+winner)", att.settleCalls.Load())
	}
	if req.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest=%d want 1", req.settleCalls.Load())
	}
}

func TestDualPlaneMatrix_ParallelLoserIncurredSettlesViaRace(t *testing.T) {
	t.Parallel()

	att := &recordingAttemptProvider{id: "matrix-par-att"}
	req := &settleRecordingRequestProvider{id: "matrix-par-req"}
	rec := &recordingMeter{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, nil)
	ex.AccountingRuntime = AccountingRuntime{MeteringRecorder: rec}
	ex.Now = func() time.Time { return time.Unix(200, 0).UTC() }
	attachDualPlaneMatrixCoordinators(ex, att, req)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	leg2OpenedCh := make(chan struct{}, 1)
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	tcaps := parallelTransportCaps()
	ex.Backends["backend-1"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: leg2OpenedCh,
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "winner"},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 6, TotalTokens: 10,
						CostNanoUnits: 99, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
					},
					{Kind: aipapi.EventResponseFinished},
				},
			}, nil
		},
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &emitThenBlockStream{
				openedCh: leg2OpenedCh,
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: 8, OutputTokens: 1, TotalTokens: 9,
						CostNanoUnits: 44, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
						Accounting: aipapi.UsageAccountingMetadata{
							Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
						},
					},
				},
			}, nil
		},
	}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-par", aLegID, "trace-par", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	holder := &checkpoint.RequestHolder{}
	_, err = holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-par"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx = withMeteringHolder(ctx, holder)
	budget := &attemptBudget{max: 10}
	openReq := authorityOpenRequest(t, aLegID, budget)
	openReq.reqFacts.metering = holder
	openReq.reqFacts.requestAuth = requestAuthorityFrom(ctx)
	openReq.reqFacts.aScope = aScope
	openReq.reqFacts.baseline.Route.Selector = "backend-1:model-1!backend-2:model-2"
	openReq.reqFacts.baseline.ID = "req-par"
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(ctx, openReq, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel race winner")
	}
	if out.ready.session.inner != nil {
		_ = out.ready.session.inner.Close()
	}

	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	life := ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate())
	usage := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 6, TotalTokens: 10,
		CostNanoUnits: 99, Currency: "USD", CostPresent: true, CostSource: string(aipapi.UsageSourceProviderReported),
	}
	if !life.Settle(ctx, usagelimit.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}
	f := openReq.reqFacts.recvTurnFacts
	f.traceID = "trace-par"
	f.metering = holder
	f.requestAuth = requestAuthorityFrom(ctx)
	stream := &retryRecvStream{facts: testRecvTurnFacts(f), attempt: testAttemptSlot(out.ready.BLeg(), out.ready.Candidate(), life)}
	bindTestRuntimeOwners(stream, ex)
	_ = stream.terminal.settleRequestAuthorityWithFrontendEgress(ctx, usage, stream.facts.terminalFacts(), stream.responsePipeline)

	if att.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (winner+loser)", att.settleCalls.Load())
	}
	if att.releaseCalls.Load() != 0 {
		t.Fatalf("incurred parallel loser must settle; releaseCalls=%d", att.releaseCalls.Load())
	}
	if req.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest=%d want 1", req.settleCalls.Load())
	}
	facts := rec.Facts()
	var loserBE int
	var feMoney *metering.MoneyObservation
	for _, f := range facts {
		if f.Boundary == metering.BoundaryBackendEgress && f.AttemptOutcome == metering.AttemptOutcomeLoser {
			loserBE++
		}
		if f.Boundary == metering.BoundaryFrontendEgress {
			feMoney = f.Money
		}
	}
	if loserBE != 1 {
		t.Fatalf("loser BE egress facts=%d want 1", loserBE)
	}
	var loserFact *metering.Fact
	for i := range facts {
		f := &facts[i]
		if f.Boundary == metering.BoundaryBackendEgress && f.AttemptOutcome == metering.AttemptOutcomeLoser {
			loserFact = f
			break
		}
	}
	if loserFact == nil {
		t.Fatal("loser BE fact missing")
	}
	in, ok := checkpoint.QuantityComponentValue(loserFact.Quantities, metering.ComponentInputToken)
	if !ok || in != 8 {
		t.Fatalf("loser BE input_token=%d ok=%v want observed 8", in, ok)
	}
	if loserFact.Money == nil || loserFact.Money.NanoUnits != 44 {
		t.Fatalf("loser BE money=%+v want observed 44", loserFact.Money)
	}
	if feMoney != nil {
		t.Fatalf("customer FE must not inherit provider money; got %+v", feMoney)
	}
}

func TestDualPlaneMatrix_NoRetryAfterClientVisibleOutput(t *testing.T) {
	t.Parallel()

	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	var opens1, opens2 atomic.Int32
	backend.openFn = func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		switch cand.Primary.Backend {
		case "backend-2":
			opens2.Add(1)
		default:
			opens1.Add(1)
		}
		return &textThenRecoverableErrStream{}, nil
	}

	budget := &attemptBudget{max: 5}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	out, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
	if err != nil || out.ready.session == nil {
		t.Fatalf("open: err=%v opened=%v", err, out.ready.session != nil)
	}
	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: req.reqFacts.baseline,
			aLegID:   aLegID,
			traceID:  "trace-no-retry",
		}),
		recovery:         &recoveryController{budget: budget, sel: mustParseSelector(t, "backend-1:model-1|backend-2:model-2"), session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)},
		attempt:          testAttemptSlot(out.ready.BLeg(), out.ready.Candidate(), ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)
	testStoreInner(rs, out.ready.session.inner)

	var lastErr error
	for range 8 {
		_, lastErr = rs.Recv(context.Background())
		if lastErr != nil {
			break
		}
	}
	if lastErr == nil {
		t.Fatal("expected recoverable-after-output error from Recv")
	}
	if opens1.Load() != 1 {
		t.Fatalf("backend-1 opens=%d want 1 (no retry after client-visible output)", opens1.Load())
	}
	if opens2.Load() != 0 {
		t.Fatalf("backend-2 opens=%d want 0 (fallback must not run after client-visible output)", opens2.Load())
	}
}

func TestDualPlaneMatrix_CancellationSettlesIncurredAttempt(t *testing.T) {
	t.Parallel()

	att := &recordingAttemptProvider{id: "matrix-cancel-att"}
	req := &settleRecordingRequestProvider{id: "matrix-cancel-req"}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	attachDualPlaneMatrixCoordinators(ex, att, req)
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return blockingRecvStream{}, nil
	}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-cancel", aLegID, "trace-cancel", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	budget := &attemptBudget{max: 3}
	openReq := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	out, err := ex.evaluateAndOpenCandidate(ctx, openReq, plan)
	if err != nil || out.ready.session == nil {
		t.Fatalf("open: err=%v opened=%v", err, out.ready.session != nil)
	}

	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	f := openReq.reqFacts.recvTurnFacts
	f.traceID = "trace-cancel"
	f.requestAuth = requestAuthorityFrom(ctx)
	rs := &retryRecvStream{
		facts:    testRecvTurnFacts(f),
		recovery: &recoveryController{budget: budget, sel: mustParseSelector(t, "backend-1:model-1"), session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)},
		attempt:  testAttemptSlot(out.ready.BLeg(), out.ready.Candidate(), ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	bindTestRuntimeOwners(rs, ex)
	testStoreInner(rs, out.ready.session.inner)

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = rs.Recv(cancelCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err=%v want context.Canceled", err)
	}
	_ = rs.Close()

	if att.settleCalls.Load() != 1 {
		t.Fatalf("operator SettleAttempt=%d want 1 for incurred cancel path", att.settleCalls.Load())
	}
	if att.releaseCalls.Load() != 0 {
		t.Fatalf("incurred cancel must settle; releaseCalls=%d", att.releaseCalls.Load())
	}
	// Pre-output cancel terminals customer via ReleaseRequest (not SettleRequest).
	if req.settleCalls.Load() != 0 {
		t.Fatalf("customer SettleRequest=%d want 0 before committed output", req.settleCalls.Load())
	}
	if req.releaseCalls.Load() != 1 {
		t.Fatalf("customer ReleaseRequest=%d want 1 (pre-output cancel terminal)", req.releaseCalls.Load())
	}
	if !testAttemptSession(rs).authority.Settled() {
		t.Fatal("attempt authority must be terminal after cancel Recv")
	}
}

func TestDualPlaneMatrix_FilteringProviderVsDeliveredViaExecute(t *testing.T) {
	t.Parallel()

	att := &recordingAttemptProvider{id: "matrix-filter-att"}
	req := &settleRecordingRequestProvider{id: "matrix-filter-req"}
	rec := &recordingMeter{}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{matrixFilterRespHook{}}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			CompletionGates: []completion.Gate{matrixFilterGate{}},
		}),
	})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(400, 0).UTC() }
	ex.Rand = routing.NewSeededRng(1)
	callCount, outCount := clientVisibleCount(5, len("filtered"))
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: callCount, output: outCount}, accountingstream.Config{})
	attachDualPlaneMatrixCoordinators(ex, att, req)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "provider-secret"},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 40, TotalTokens: 45,
						CostNanoUnits: 77, Currency: "USD", CostPresent: true,
						CostSource: string(aipapi.UsageSourceProviderReported),
						Accounting: aipapi.UsageAccountingMetadata{
							Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
						},
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &aipapi.Call{
		ID:    "req-filter",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("Recv: %v", rerr)
			}
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	_ = stream.Close()
	if text.String() != "filtered" {
		t.Fatalf("delivered text=%q want filtered", text.String())
	}

	var beMoney *metering.MoneyObservation
	var beOut int64
	var beOutOK bool
	var feMoney *metering.MoneyObservation
	var feOut int64
	var feOK bool
	for _, f := range rec.facts {
		if f.Boundary == metering.BoundaryBackendEgress {
			if f.Money != nil {
				beMoney = f.Money
			}
			beOut, beOutOK = checkpoint.QuantityComponentValue(f.Quantities, metering.ComponentOutputToken)
		}
		if f.Boundary == metering.BoundaryFrontendEgress {
			feMoney = f.Money
			feOut, feOK = checkpoint.QuantityComponentValue(f.Quantities, metering.ComponentOutputToken)
		}
	}
	if beMoney == nil || beMoney.NanoUnits != 77 {
		t.Fatalf("operator BE must retain provider cost; got %+v", beMoney)
	}
	if !beOutOK || beOut != 40 {
		t.Fatalf("operator BE output_token=%d ok=%v want provider 40", beOut, beOutOK)
	}
	if feMoney != nil {
		t.Fatalf("customer FE must omit provider money; got %+v", feMoney)
	}
	if feOK && feOut == 40 {
		t.Fatalf("customer FE output_token=%d must not equal provider output 40", feOut)
	}
	if att.settleCalls.Load() != 1 {
		t.Fatalf("operator SettleAttempt=%d want 1", att.settleCalls.Load())
	}
	if req.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest=%d want 1", req.settleCalls.Load())
	}
}

func TestDualPlaneMatrix_AuxiliaryCallParentScopeSeparatesPlanes(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	var ex *Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, st)
	var openScope atomic.Value
	ex = TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(2000, 0).UTC() }
	callCount, outCount := clientVisibleCount(2, len("aux-out"))
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: callCount, output: outCount}, accountingstream.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if sc, ok := scope.ScopeFromContext(ctx); ok {
					openScope.Store(sc)
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "aux-out"},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 1, TotalTokens: 3,
						CostNanoUnits: 99, Currency: "USD", CostPresent: true,
						CostSource: string(aipapi.UsageSourceProviderReported),
						Accounting: aipapi.UsageAccountingMetadata{
							Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
						},
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)

	parent := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("parent-user"),
		Origin:      scope.OriginClient,
	}
	ctx := scope.WithScope(context.Background(), parent)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("aux")},
		}},
	}
	stream, err := snap.Aux().Stream(ctx, auxiliary.Request{
		ParentTraceID: "trace-parent",
		Call:          call,
	})
	if err != nil {
		t.Fatalf("aux Stream: %v", err)
	}
	for {
		_, err := stream.Recv(ctx)
		if err != nil {
			break
		}
	}
	_ = stream.Close()

	got, ok := openScope.Load().(scope.PrincipalScopeView)
	if !ok {
		t.Fatal("expected auxiliary Open to observe parent-derived scope")
	}
	if got.Origin != scope.OriginInternal {
		t.Fatalf("aux origin=%q want internal", got.Origin)
	}
	if got.ParentTraceID.String() != "trace-parent" {
		t.Fatalf("ParentTraceID=%q want trace-parent", got.ParentTraceID.String())
	}

	var fe *metering.Fact
	var beMoney *metering.MoneyObservation
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress &&
			rec.facts[i].Perspective == metering.PerspectiveCustomer {
			fe = &rec.facts[i]
		}
		if rec.facts[i].Boundary == metering.BoundaryBackendEgress && rec.facts[i].Money != nil {
			beMoney = rec.facts[i].Money
		}
	}
	if fe == nil {
		t.Fatal("auxiliary child ResponseFinished terminalization must record customer FE egress fact")
	}
	if fe.Money != nil {
		t.Fatalf("auxiliary customer FE fact must omit provider money; got %+v", fe.Money)
	}
	if beMoney == nil || beMoney.NanoUnits != 99 {
		t.Fatalf("auxiliary operator BE must retain provider cost; got %+v", beMoney)
	}
}

func TestDualPlaneMatrix_CompressionPlanesSettleFromOwnEvidence(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(300, 0).UTC() }

	hook := &matrixCompressHook{}
	bus := hooks.New(hooks.Config{RequestPartHooks: []sdk.RequestPartHook{hook}})
	var openedCall aipapi.Call
	backend.openFn = func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		openedCall = call
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "ok"},
			{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}

	holder := &checkpoint.RequestHolder{}
	orig := aipapi.Call{
		ID: "req-comp",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("original long prompt")},
		}},
	}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: orig, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 800, Present: true},
	})

	ctx := withMeteringHolder(context.Background(), holder)
	budget := &attemptBudget{max: 3}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.metering = holder
	req.reqFacts.requestAuth = requestAuthorityFrom(ctx)
	req.reqFacts.bus = bus
	req.reqFacts.baseline = orig
	req.reqFacts.baseline.Route.Selector = "backend-1:model-1"
	req.reqFacts.baseline.Invocation = aipapi.Invocation{
		Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming,
	}
	plan := candidatePlan{
		cand: authorityCandidate(),
	}

	out, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
	if err != nil || out.ready.session == nil {
		t.Fatalf("open: err=%v opened=%v", err, out.ready.session != nil)
	}
	if !hook.ran.Load() {
		t.Fatal("expected compression request-part hook to run before Open")
	}
	if len(openedCall.Messages) != 1 || openedCall.Messages[0].Parts[0].Text != "compressed" {
		t.Fatalf("backend Open call must see compressed payload; got %+v", openedCall.Messages)
	}
	feMsg := holder.FrontendIngress.Call.Messages
	if len(feMsg) != 1 || feMsg[0].Parts[0].Text != "original long prompt" {
		t.Fatalf("FE ingress snapshot must remain original; got %+v", feMsg)
	}

	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	f := req.reqFacts.recvTurnFacts
	f.traceID = "trace-comp"
	f.metering = holder
	f.requestAuth = requestAuthorityFrom(ctx)
	rs := &retryRecvStream{
		facts:            testRecvTurnFacts(f),
		recovery:         &recoveryController{budget: budget, sel: mustParseSelector(t, "backend-1:model-1"), session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)},
		attempt:          testAttemptSlot(out.ready.BLeg(), out.ready.Candidate(), ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate()), newAttemptAccountingTracker(time.Unix(1, 0))),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	bindTestRuntimeOwners(rs, ex)
	testStoreInner(rs, out.ready.session.inner)
	for {
		_, rerr := rs.Recv(ctx)
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("Recv: %v", rerr)
			}
			break
		}
	}
	_ = rs.Close()

	feIn, _ := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	if feIn != 800 {
		t.Fatalf("FE ingress input=%d want 800", feIn)
	}
	var sawBE, sawFE bool
	for _, f := range rec.facts {
		if f.Boundary == metering.BoundaryBackendEgress {
			sawBE = true
		}
		if f.Boundary == metering.BoundaryFrontendEgress {
			sawFE = true
		}
	}
	if !sawBE || !sawFE {
		t.Fatalf("expected BE+FE egress facts after Recv terminal; be=%v fe=%v facts=%d", sawBE, sawFE, len(rec.facts))
	}
}

func mustParseSelector(t *testing.T, s string) *routing.Selector {
	t.Helper()
	sel, err := routing.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}
