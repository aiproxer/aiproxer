package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type timingReqPartHook struct {
	id    string
	order int
	fn    func(context.Context, *aipapi.Call, sdk.PartMeta) error
}

func (s *timingReqPartHook) ID() string                   { return s.id }
func (s *timingReqPartHook) Order() int                   { return s.order }
func (s *timingReqPartHook) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *timingReqPartHook) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

// Phase 1.1 characterization (dual-plane-economics-and-concurrency-foundation):
// lock today's attempt-grain usage-authority timing. These tests document current
// behavior (per B-leg admit, mutation after reserve, loser release after Open).
// Phase 6+ will split customer logical-request vs operator attempt lifecycles.

// sequencingAuthorityRecorder assigns a unique reservation ID per authoritative
// (non-estimate) Admit so characterization tests can prove per-attempt grain.
type sequencingAuthorityRecorder struct {
	mu             sync.Mutex
	admitInputsV   []usagelimit.AdmissionInput
	releaseInputsV []usagelimit.ReleaseInput
	settleInputsV  []usagelimit.SettleInput
	admitCalls     atomic.Int64
	releaseCalls   atomic.Int64
	settleCalls    atomic.Int64
	seq            atomic.Int64
}

func (s *sequencingAuthorityRecorder) Admit(_ context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	s.admitCalls.Add(1)
	s.mu.Lock()
	s.admitInputsV = append(s.admitInputsV, in)
	s.mu.Unlock()
	result := usagelimit.AdmissionResult{
		Allowed:        true,
		Reserved:       !in.EstimateOnly,
		ReservedAmount: authorityInputAmount(7),
		PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
	}
	if !in.EstimateOnly {
		n := s.seq.Add(1)
		result.ReservationID = fmt.Sprintf("res-%d", n)
	}
	return result, nil
}

func (s *sequencingAuthorityRecorder) Settle(_ context.Context, in usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	s.settleCalls.Add(1)
	s.mu.Lock()
	s.settleInputsV = append(s.settleInputsV, in)
	s.mu.Unlock()
	return usagelimit.SettleResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *sequencingAuthorityRecorder) Release(_ context.Context, in usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.mu.Lock()
	s.releaseInputsV = append(s.releaseInputsV, in)
	s.mu.Unlock()
	return usagelimit.ReleaseResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *sequencingAuthorityRecorder) ApplyUsage(_ context.Context, cmd usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *sequencingAuthorityRecorder) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	return controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}, nil
}

func (s *sequencingAuthorityRecorder) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (s *sequencingAuthorityRecorder) Decisions(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (s *sequencingAuthorityRecorder) authoritativeAdmits() []usagelimit.AdmissionInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]usagelimit.AdmissionInput, 0, len(s.admitInputsV))
	for _, in := range s.admitInputsV {
		if !in.EstimateOnly {
			out = append(out, in)
		}
	}
	return out
}

func (s *sequencingAuthorityRecorder) releases() []usagelimit.ReleaseInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]usagelimit.ReleaseInput, len(s.releaseInputsV))
	copy(out, s.releaseInputsV)
	return out
}

func (s *sequencingAuthorityRecorder) settles() []usagelimit.SettleInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]usagelimit.SettleInput, len(s.settleInputsV))
	copy(out, s.settleInputsV)
	return out
}

// TestAuthorityTiming_failoverIssuesAuthoritativeAdmitPerAttempt proves that a
// logical request currently runs authoritative Admit once per backend attempt
// under recv-phase failover (initial open + replacement), with distinct B-leg IDs.
func TestAuthorityTiming_failoverIssuesAuthoritativeAdmitPerAttempt(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	budget := &attemptBudget{max: 5}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	out, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
	if err != nil {
		t.Fatalf("initial evaluateAndOpenCandidate: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected initial attempt to open")
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	var priorState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		priorState = out.ready.session.authority.control.state
	}
	priorCand := out.ready.Candidate()
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: req.reqFacts.baseline,
			aLegID:   aLegID,
			traceID:  "trace-1",
		}),
		recovery: &recoveryController{budget: budget, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, attempt: testAttemptSlot(out.ready.BLeg(), priorCand, testAuthorityLifecycle(ex, priorState, priorCand)),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)

	testRetirePriorAttempt(rs)

	iterRes, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
	opened := iterRes.opened
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement attempt to open")
	}

	authoritative := auth.authoritativeAdmits()
	if got, want := len(authoritative), 2; got != want {
		t.Fatalf("authoritative admits = %d, want %d (one per backend attempt under failover)", got, want)
	}
	if authoritative[0].ReservationKey.BLegID == "" || authoritative[1].ReservationKey.BLegID == "" {
		t.Fatal("expected both authoritative admits to carry B-leg IDs")
	}
	if authoritative[0].ReservationKey.BLegID == authoritative[1].ReservationKey.BLegID {
		t.Fatalf("failover attempts shared BLegID %q; rules currently execute per backend attempt", authoritative[0].ReservationKey.BLegID)
	}
	if authoritative[0].ReservationKey.LogicalRequestID != authoritative[1].ReservationKey.LogicalRequestID {
		t.Fatalf("logical request IDs diverged across failover attempts: %q vs %q",
			authoritative[0].ReservationKey.LogicalRequestID, authoritative[1].ReservationKey.LogicalRequestID)
	}
}

// TestAuthorityTiming_parallelRaceIssuesAuthoritativeAdmitPerLeg proves that a
// parallel race currently issues one authoritative Admit per opened leg (not one
// per logical request), with distinct B-leg reservation keys.
func TestAuthorityTiming_parallelRaceIssuesAuthoritativeAdmitPerLeg(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	// Force both legs through Open (and therefore through authoritative Admit)
	// before either can win, so the characterization is not racy under -parallel load.
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

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	req.reqFacts.baseline.Route.Selector = "backend-1:model-1!backend-2:model-2"
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel race to open a backend")
	}

	authoritative := auth.authoritativeAdmits()
	if got, want := len(authoritative), 2; got != want {
		t.Fatalf("authoritative admits = %d, want %d (one per parallel leg)", got, want)
	}
	if authoritative[0].ReservationKey.BLegID == authoritative[1].ReservationKey.BLegID {
		t.Fatalf("parallel legs shared BLegID %q", authoritative[0].ReservationKey.BLegID)
	}
	keys := map[string]struct{}{}
	for _, in := range authoritative {
		keys[in.ReservationKey.String()] = struct{}{}
	}
	if len(keys) != 2 {
		t.Fatalf("distinct reservation keys = %d, want 2; keys=%v", len(keys), keys)
	}
}

// TestAuthorityTiming_loserReleaseAfterOpenSetsBackendAttempted proves that when a
// parallel loser has already opened (provider work begun), its Losing release reports
// BackendAttempted=true. Today cleanup is release-only (no settle of incurred cost).
func TestAuthorityTiming_loserReleaseAfterOpenSetsBackendAttempted(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	loserOpenedCh := make(chan struct{}, 1)
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	ex.Backends["loser"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: loserOpenedCh}, nil
		},
	}
	ex.Backends["winner"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: loserOpenedCh,
				events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "fast"}, {Kind: aipapi.EventResponseFinished}},
			}, nil
		},
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "loser", Model: "m"}, Key: "loser:m"},
		{Primary: routing.Primary{Backend: "winner", Model: "m"}, Key: "winner:m"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected winner to open")
	}
	if out.ready.Candidate().Primary.Backend != "winner" {
		t.Fatalf("winner backend = %q, want winner", out.ready.Candidate().Primary.Backend)
	}

	// Wait briefly for loser cancel/settle path to finish after winner election.
	deadline := time.Now().Add(2 * time.Second)
	var settles []usagelimit.SettleInput
	for time.Now().Before(deadline) {
		settles = auth.settles()
		if len(settles) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(settles) < 1 {
		t.Fatal("expected at least one Losing settle for the opened loser")
	}

	foundOpenedLoser := false
	for _, st := range settles {
		if st.Kind == usagelimit.SettlementKindLosing && st.BackendAttempted {
			foundOpenedLoser = true
			break
		}
	}
	if !foundOpenedLoser {
		t.Fatalf("expected a Losing settle with BackendAttempted=true after provider open; settles=%+v", settles)
	}
}

// TestAuthorityTiming_requestHookMutatesBeforeAuthoritativeAdmit locks the
// approved dual-plane Backend Ingress order: request-part hooks / route shaping
// run, BE freeze+count, then authoritative Admit, then Open sees the mutated
// call while the reservation remains held (design Lifecycle Placement).
func TestAuthorityTiming_requestHookMutatesBeforeAuthoritativeAdmit(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-mutate",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	var openedCall aipapi.Call
	backend.openFn = func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		openedCall = call
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	hookRan := atomic.Bool{}
	hookBeforeAuthoritativeAdmit := atomic.Bool{}
	mutatedMax := 4242
	bus := hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			&timingReqPartHook{
				id:    "mutate-before-admit",
				order: 0,
				fn: func(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
					// Estimate-only precheck may have run; authoritative admit (2nd) must not.
					if auth.admitCalls.Load() < 2 {
						hookBeforeAuthoritativeAdmit.Store(true)
					}
					hookRan.Store(true)
					call.Options.MaxOutputTokens = &mutatedMax
					call.Messages = append(call.Messages, aipapi.Message{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("pre-admit mutation")},
					})
					return nil
				},
			},
		},
	})

	budget := &attemptBudget{max: 3}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.bus = bus
	req.reqFacts.baseline.Messages = []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("hello")},
	}}
	plan := candidatePlan{
		cand: authorityCandidate(),
	}

	out, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
	if err != nil {
		t.Fatalf("evaluateAndOpenCandidate: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected backend to open")
	}
	if !hookRan.Load() {
		t.Fatal("expected request-part hook to run")
	}
	if !hookBeforeAuthoritativeAdmit.Load() {
		t.Fatal("expected request-part hook mutation before authoritative Admit")
	}
	if auth.admitCalls.Load() < 2 {
		t.Fatalf("admit calls = %d, want estimate + authoritative", auth.admitCalls.Load())
	}
	if openedCall.Options.MaxOutputTokens == nil || *openedCall.Options.MaxOutputTokens != mutatedMax {
		t.Fatalf("Open call MaxOutputTokens = %v, want %d (hooks mutate before reserve)", openedCall.Options.MaxOutputTokens, mutatedMax)
	}
	if len(openedCall.Messages) < 2 {
		t.Fatalf("Open call messages = %d, want >= 2 after pre-admit mutation", len(openedCall.Messages))
	}
	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	if authState.admissionResult.ReservationID != "reservation-mutate" {
		t.Fatalf("reservation ID = %q, want reservation-mutate still held through Open", authState.admissionResult.ReservationID)
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 while opened attempt still owns the reservation", auth.releaseCalls.Load())
	}
}
