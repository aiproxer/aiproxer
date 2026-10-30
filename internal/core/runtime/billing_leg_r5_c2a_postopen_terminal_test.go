package runtime

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// r5c2aCheckpointCapacity is the attempt-owned bounded checkpoint queue depth.
// A destructive drain past this bound is an irreversible capture loss.
const r5c2aCheckpointCapacity = maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred

// r5c2aProviderDraft builds one native provider draft carrying a directional
// image media component and an aggregate provider charge. Delta semantics keep
// every revision a distinct economic identity, so a destructive single drain
// cannot coalesce the prefix.
func r5c2aProviderDraft(sourceKey string, revision uint64) coremetering.ProviderEvidenceDraft {
	media := metering.Decimal{Coefficient: strconv.FormatUint(revision, 10), Scale: 0}
	money := metering.Decimal{Coefficient: strconv.FormatUint(revision*100, 10), Scale: 0}
	return coremetering.ProviderEvidenceDraft{
		SourceEventKey: sourceKey, StreamID: "provider.r5c2a.v2",
		Semantics: metering.SemanticsDelta,
		Measures: []metering.Measure{{
			Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "r5c2a.provider.schema"},
			Value: &media, Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: fmt.Sprintf("charge:%s:%d", sourceKey, revision),
			Amount:       &money, Currency: "USD", Kind: metering.ChargeKindAggregate,
		}},
	}
}

// R5-C2a characterization: the executor's post-open assembly-failure route
// (executor.go Execute -> appendPostOpenFallback -> appendPostOpenTerminalLeg,
// and the wire/large-body twin, which share appendPostOpenFallback) must never
// become a second terminal owner when the opened B-leg already owns
// provider-native economic evidence.
//
// The post-open fallback is reachable while Prepare is in flight. The A-leg
// lifecycle is registered with ready.lifecycleHandle() (commitLaunchOrRegister:
// launch-permit Commit or aScope.RegisterBLeg), not with the attemptSession, so
// an A-leg cancellation during Prepare calls readyAttempt.cancelViaLifecycle.
// That path arms a pending invalidation and, before this fix, left
// readyAttempt.session attached until the cancellation waiter resumed, so a
// competing Execute read of out.ready.BLeg() could observe the still-live,
// evidence-owning session and append a second, loss-less leg while the waiter
// also terminalized the original leg.
//
// The fix (readyAttempt.armPendingInvalidationLocked) detaches the session under
// the readyAttempt mutex at the moment the pending invalidation is recorded,
// before waiting for the in-flight publication op. Any competing caller then
// observes an empty BLeg/Candidate and appends nothing, leaving the lifecycle
// cancellation path as the single terminal owner.
//
// TestR5C2aPostOpenFallbackLifecycleInvalidationSingleOwner forces that exact
// schedule (blocked observer Open during Prepare + A-leg cancel), asserts the
// detached-ownership boundary deterministically, and then asserts one terminal
// leg carrying the reserved checkpoint-capacity capture-loss marker and a
// single backend open (the retail refusal over the truncated leg is Enterprise;
// see the synchronous fail-closed tests below for the abortPreReturn path and
// the healthy no-false-loss case).

type r5c2aProviderStream struct {
	buffer    *coremetering.ProviderEvidenceBuffer
	revisions int
	bound     atomic.Bool
	drained   atomic.Int32
}

func (s *r5c2aProviderStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, nil }
func (s *r5c2aProviderStream) Send(aipapi.Event) error                    { return nil }
func (s *r5c2aProviderStream) Close() error                               { return nil }
func (s *r5c2aProviderStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *r5c2aProviderStream) DrainEconomicObservations() []metering.Observation {
	if s == nil || s.buffer == nil {
		return nil
	}
	out := s.buffer.DrainEconomicObservations()
	s.drained.Add(int32(len(out)))
	return out
}

// BindEconomicEvidence lets the runtime attribute the preloaded native drafts
// to the actual opened B-leg, exactly as the stock provider buffer adapter does.
// Drafts are seeded only after the trusted binding exists, matching production
// adapter ordering where a draft cannot be drained without a B-leg subject.
func (s *r5c2aProviderStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil || s.buffer == nil || s.bound.Swap(true) {
		return
	}
	s.buffer.BindEconomicEvidence(identity)
	for revision := uint64(1); revision <= uint64(s.revisions); revision++ {
		s.buffer.Add(r5c2aProviderDraft("provider.r5c2a.postopen", revision))
	}
}

func r5c2aExecutorWithProviderStream(t *testing.T, capture *abortJoinCapture, newStream func() aipapi.ManagedEventStream, opens *atomic.Int32) *Executor {
	t.Helper()
	return r5c2aExecutorWithObserver(t, capture, newStream, opens, failClosedStreamObserverFactory{})
}

func r5c2aExecutorWithObserver(t *testing.T, capture *abortJoinCapture, newStream func() aipapi.ManagedEventStream, opens *atomic.Int32, observer response.StreamObserverFactory) *Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	// The stock provider evidence buffer refuses to drain without a trusted
	// store and B-leg subject; give the test the same durable store identity a
	// production host would inject.
	ex.BillingIdentity.StoreID = func(context.Context) string { return "store-r5c2a" }
	// A configured observation sink is required for bounded checkpoint
	// admission: without a durable checkpoint consumer the runtime correctly
	// treats evidence as terminal-owned and never exercises queue capacity.
	ex.MeteringObservationSink = &refinement41ObservationSink{}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{observer},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return newStream(), nil
			},
		},
	}
	return ex
}

func r5c2aCall() *aipapi.Call {
	return &aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-r5c2a-postopen", ContinuityKey: "sess-r5c2a-postopen"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
}

func r5c2aProviderBearingStream(revisions int) *r5c2aProviderStream {
	return &r5c2aProviderStream{buffer: coremetering.NewProviderEvidenceBuffer(), revisions: revisions}
}

// TestR5C2aPostOpenAssemblyFailureSingleOwnerCarriesCaptureLoss forces a
// provider-native checkpoint-capacity overflow before the opened B-leg fails
// assembly. The real executor must produce exactly one terminal leg, owned by
// the attempt session, carrying the reserved capture-loss marker, and the
// backend must be opened exactly once (no upstream retry or failover after
// open).
func TestR5C2aPostOpenAssemblyFailureSingleOwnerCarriesCaptureLoss(t *testing.T) {
	t.Parallel()

	capture := &abortJoinCapture{}
	var opens atomic.Int32
	stream := r5c2aProviderBearingStream(r5c2aCheckpointCapacity + 32)
	ex := r5c2aExecutorWithProviderStream(t, capture, func() aipapi.ManagedEventStream {
		return stream
	}, &opens)

	if _, err := ex.Execute(context.Background(), r5c2aCall()); err == nil {
		t.Fatal("expected post-open assembly failure from fail-closed stream observer")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1 (no retry/failover after open)", got)
	}
	if stream.drained.Load() == 0 {
		t.Fatal("provider-native evidence was never drained into the attempt")
	}

	calls, legs := capture.snapshot()
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(calls))
	}
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly 1 (single terminal owner)", len(legs))
	}
	leg := legs[0]
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	markerVisible := false
	for _, conflict := range leg.EvidenceConflicts {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == wantReason {
			markerVisible = true
		}
	}
	if !markerVisible {
		t.Fatalf("post-open failure leg dropped the reserved capture-loss marker: %+v", leg.EvidenceConflicts)
	}
	// The reserved marker is runtime-owned provenance; the downstream retail
	// refusal over the truncated post-open leg is Enterprise (5.2 preservation:
	// TestEnterprisePreservationRuntimePostOpenFailureRefusesRetail).
}

// TestR5C2aHealthyPostOpenAssemblyFailureDoesNotFalselyAcquireLoss proves the
// same real executor route does not manufacture capture loss for a healthy
// bounded provider progression: the single terminal leg has no reserved loss
// marker even though the opened B-leg ultimately failed assembly.
func TestR5C2aHealthyPostOpenAssemblyFailureDoesNotFalselyAcquireLoss(t *testing.T) {
	t.Parallel()

	capture := &abortJoinCapture{}
	var opens atomic.Int32
	ex := r5c2aExecutorWithProviderStream(t, capture, func() aipapi.ManagedEventStream {
		return r5c2aProviderBearingStream(4)
	}, &opens)

	if _, err := ex.Execute(context.Background(), r5c2aCall()); err == nil {
		t.Fatal("expected post-open assembly failure from fail-closed stream observer")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1", got)
	}

	_, legs := capture.snapshot()
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly 1", len(legs))
	}
	leg := legs[0]
	for _, conflict := range leg.EvidenceConflicts {
		if strings.HasPrefix(conflict.IncomingCoverageReason, evidenceCaptureLossConflictReasonPrefix) {
			t.Fatalf("healthy post-open failure falsely acquired capture loss: %+v", conflict)
		}
	}
}

// r5c2aBlockingObserver blocks the response-stream observer Open so a test can
// hold the fallible post-open Prepare step in flight (readyAttempt.opInFlight)
// and drive A-leg cancellation exactly during that window.
func r5c2aBlockingObserver(entered, allowFinish chan struct{}) *countingObserverFactory {
	return &countingObserverFactory{
		onOpen: func(context.Context) {
			close(entered)
			<-allowFinish
		},
	}
}

// r5c2aWaitState spins without sleeping until cond holds or a wall-clock guard
// expires. It is state observation rather than sleep-based synchronization, so
// the interleaving is forced by the blocked observer, not by timing.
func r5c2aWaitState(t *testing.T, label string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		runtime.Gosched()
	}
}

func r5c2aLegIDs(legs []usageevidence.CallLegUsageRecord) []string {
	ids := make([]string, 0, len(legs))
	for _, leg := range legs {
		ids = append(ids, leg.BLegID+"/"+string(leg.Outcome))
	}
	return ids
}

// TestR5C2aPostOpenFallbackLifecycleInvalidationSingleOwner forces the exact
// reviewer schedule: Prepare drains provider-native evidence past the bounded
// checkpoint capacity, then blocks in the observer Open; the A-leg is canceled
// while Prepare is in flight, which arms a pending lifecycle invalidation and
// transfers terminal ownership to the cancellation path; the observer then
// returns and assembly fails.
//
// The ready capability must not expose its evidence-owning session to the
// post-open fallback once a lifecycle invalidation owns terminalization. The
// deterministic assertion below therefore fails on the pre-fix code (the live
// session is still visible and Execute would append a second, loss-less leg)
// and passes once the invalidation detaches the session at record time.
//
// After teardown exactly one terminal leg exists: the lifecycle-owned leg that
// carries the reserved checkpoint-capacity capture-loss marker, and the backend
// is opened exactly once (no retry/failover after open).
func TestR5C2aPostOpenFallbackLifecycleInvalidationSingleOwner(t *testing.T) {
	t.Parallel()

	capture := &abortJoinCapture{}
	var opens atomic.Int32
	entered := make(chan struct{})
	allowFinish := make(chan struct{})

	stream := r5c2aProviderBearingStream(r5c2aCheckpointCapacity + 32)
	ex := r5c2aExecutorWithObserver(t, capture, func() aipapi.ManagedEventStream {
		return stream
	}, &opens, r5c2aBlockingObserver(entered, allowFinish))

	prep, prepCtx, cleanup, err := ex.prepareRequest(context.Background(), r5c2aCall())
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()
	if err := ex.checkCheapCredit(prepCtx, prep); err != nil {
		t.Fatalf("checkCheapCredit: %v", err)
	}
	plan, err := ex.buildRoutePlan(prepCtx, prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if err := ex.authorizeBillingOnce(prepCtx, prep, plan); err != nil {
		t.Fatalf("authorizeBillingOnce: %v", err)
	}
	out, err := ex.openInitialAttempt(prepCtx, prep, plan)
	if err != nil {
		t.Fatalf("openInitialAttempt: %v", err)
	}
	if out.ready == nil {
		t.Fatal("openInitialAttempt returned nil ready")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1", got)
	}
	prep.compactionOpenMeta = ex.observeCompactionOpened(prepCtx, prep, out)

	assembleErr := make(chan error, 1)
	go func() {
		_, aerr := streamAssembler{ex}.assemble(prepCtx, prep, plan, out)
		assembleErr <- aerr
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for observer Open to be entered")
	}

	// A-leg cancellation while Prepare is in flight: it arms the pending
	// invalidation and blocks until the in-flight publication op ends.
	cancelDone := make(chan struct{})
	go func() {
		_ = prep.aScope.Cancel(context.Background(), leglifecycle.CancelCause{
			Kind:   leglifecycle.CancelExplicit,
			Detail: "cancel during prepare",
		})
		close(cancelDone)
	}()

	r5c2aWaitState(t, "pending lifecycle invalidation", out.ready.hasPendingInvalidation)

	// Deterministic single-owner assertion: while cancellation owns the
	// attempt, the ready capability exposes no session for a competing
	// post-open terminal fallback.
	if bleg := out.ready.BLeg(); strings.TrimSpace(bleg.BLegID) != "" {
		t.Fatalf("ready exposed B-leg %q to post-open fallback while lifecycle invalidation owned terminalization", bleg.BLegID)
	}
	if cand := out.ready.Candidate(); strings.TrimSpace(cand.Primary.Backend) != "" {
		t.Fatalf("ready exposed candidate backend %q to post-open fallback while lifecycle invalidation owned terminalization", cand.Primary.Backend)
	}
	select {
	case <-cancelDone:
		t.Fatal("A-leg cancellation returned before the in-flight Prepare finished")
	default:
	}

	close(allowFinish)

	select {
	case aerr := <-assembleErr:
		if aerr == nil {
			t.Fatal("expected assembly to fail after cancellation during Prepare")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for assembly to finish")
	}
	// Production post-open fallback decision, exactly as Execute and the
	// large-body path run it.
	ex.appendPostOpenFallback(prepCtx, prep.billingCallState, prep.identity.aLeg.ALegID, out)

	select {
	case <-cancelDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for A-leg cancellation to finish")
	}

	if stream.drained.Load() == 0 {
		t.Fatal("provider-native evidence was never drained into the attempt")
	}

	calls, legs := capture.snapshot()
	if len(calls) > 1 {
		t.Fatalf("call-closure appends = %d, want at most 1", len(calls))
	}
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly 1 (single terminal owner); got %v", len(legs), r5c2aLegIDs(legs))
	}
	leg := legs[0]
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	markerVisible := false
	for _, conflict := range leg.EvidenceConflicts {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == wantReason {
			markerVisible = true
		}
	}
	if !markerVisible {
		t.Fatalf("lifecycle-owned terminal leg lost the reserved capture-loss marker: %+v", leg.EvidenceConflicts)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1 (no retry/failover after open)", got)
	}
}
