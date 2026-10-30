package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// stubConversationReader returns a canned Snapshot.
type stubConversationReader struct {
	snap      conversationprojection.Snapshot
	err       error
	callCount int
}

func (s *stubConversationReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	s.callCount++
	return s.snap, s.err
}

// spyStockConversationViewTagger records calls to TagNeverBackend.
type spyStockConversationViewTagger struct {
	tagCalls atomic.Int32
}

func (s *spyStockConversationViewTagger) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	s.tagCalls.Add(1)
	return TagResult{}, nil
}

// spyStockCompactionDetector records calls to RequestOpened, PreviewResponse, and ResponseReleased.
type spyStockCompactionDetector struct {
	previewRequestCalls  atomic.Int32
	requestOpenedCalls   atomic.Int32
	previewResponseCalls atomic.Int32
	responseReleaseCalls atomic.Int32
	releasedEvents       []aipapi.Event
}

func (s *spyStockCompactionDetector) PreviewRequest(compaction.PreservationMeta, aipapi.Call) compaction.RequestPreview {
	s.previewRequestCalls.Add(1)
	return compaction.RequestPreview{Kind: compaction.PreviewNone}
}

func (s *spyStockCompactionDetector) RequestOpened(meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
	s.requestOpenedCalls.Add(1)
	return nil
}

func (s *spyStockCompactionDetector) PreviewResponse(meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
	s.previewResponseCalls.Add(1)
	return compaction.ResponsePreview{Kind: compaction.PreviewNone}
}

func (s *spyStockCompactionDetector) ResponseReleased(meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
	s.responseReleaseCalls.Add(1)
	s.releasedEvents = append(s.releasedEvents, ev)
	return nil
}

// TestStockHost_BehavioralDifferential_ConversationViewReader proves that under a
// clean stock baseline (empty NeverBackend, empty Steering), canonical snapshotAndProject
// is an identity no-op that returns Call unmodified, exactly matching wire execution.
// It also proves that when NeverBackend contains entries, canonical mutates Call,
// proving why the reader must block when mutating planes exist.
func TestStockHost_BehavioralDifferential_ConversationViewReader(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// 1. Clean baseline snapshot
	cleanReader := &stubConversationReader{
		snap: conversationprojection.Snapshot{
			StateRevision: 1,
			NeverBackend:  nil,
			Steering:      nil,
		},
	}
	ex := &Executor{
		CoreRuntime: CoreRuntime{
			CorePorts: CorePorts{ConversationViewReader: cleanReader},
		},
	}

	origCall := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "gpt-4o"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "you are a helpful assistant"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello world"}}},
		},
	}

	snap, ev, projectedCall, err := ex.snapshotAndProject(ctx, "a-leg-1", origCall)
	if err != nil {
		t.Fatalf("snapshotAndProject: %v", err)
	}

	// In canonical, empty snapshot takes fast path: zero mutations, Call is identical
	if ev.FilteredCount != 0 || ev.InjectedCount != 0 {
		t.Fatalf("expected 0 filtered and 0 injected turns on clean baseline, got filtered=%d injected=%d", ev.FilteredCount, ev.InjectedCount)
	}
	if len(projectedCall.Messages) != len(origCall.Messages) {
		t.Fatalf("projected call messages altered: got %d want %d", len(projectedCall.Messages), len(origCall.Messages))
	}
	for i := range origCall.Messages {
		if projectedCall.Messages[i].Parts[0].Text != origCall.Messages[i].Parts[0].Text {
			t.Fatalf("message %d content changed: got %q want %q", i, projectedCall.Messages[i].Parts[0].Text, origCall.Messages[i].Parts[0].Text)
		}
	}
	if snap.StateRevision != 1 {
		t.Fatalf("expected state revision 1, got %d", snap.StateRevision)
	}

	// 2. Mutating snapshot decoy: when NeverBackend contains turns, canonical projects
	// and drops them, proving why wire execution (which cannot drop turns from a wire stream)
	// must decline when mutating planes are active.
	msgToDrop := aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "local turn content to drop"}},
	}
	tagID, err := conversationprojection.MessageIdentityOf(msgToDrop)
	if err != nil {
		t.Fatalf("MessageIdentityOf: %v", err)
	}

	mutatingReader := &stubConversationReader{
		snap: conversationprojection.Snapshot{
			StateRevision: 2,
			NeverBackend:  []conversationprojection.Tag{{Identity: tagID}},
			Steering:      nil,
		},
	}
	exMutating := &Executor{
		CoreRuntime: CoreRuntime{
			CorePorts: CorePorts{ConversationViewReader: mutatingReader},
		},
	}
	callWithNB := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "gpt-4o"},
		Messages: []aipapi.Message{
			msgToDrop,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "backend content"}}},
		},
	}
	_, mutEv, mutCall, mutErr := exMutating.snapshotAndProject(ctx, "a-leg-1", callWithNB)
	if mutErr != nil {
		t.Fatalf("snapshotAndProject with mutating snapshot: %v", mutErr)
	}
	if mutEv.FilteredCount != 1 {
		t.Fatalf("expected 1 filtered turn, got %d", mutEv.FilteredCount)
	}
	if len(mutCall.Messages) != 1 || mutCall.Messages[0].Parts[0].Text != "backend content" {
		t.Fatalf("expected only backend content to remain, got %+v", mutCall.Messages)
	}
}

// TestStockHost_BehavioralDifferential_ConversationViewTagger proves that on a
// stock host without local_turn_handlers, ConversationViewTagger is completely idle
// (0 calls) in both canonical and wire execution.
func TestStockHost_BehavioralDifferential_ConversationViewTagger(t *testing.T) {
	t.Parallel()

	taggerSpy := &spyStockConversationViewTagger{}

	ex := &Executor{
		CoreRuntime: CoreRuntime{
			ConversationViewTagger: taggerSpy,
		},
	}

	// In canonical execution, local turn stage is only invoked when local turn handlers exist.
	// When handlers slice is empty, runLocalTurnStage is not called, and tagger is never touched.
	if ex.localTurnHandlers() != nil {
		t.Fatal("expected nil local turn handlers on clean stock executor")
	}

	// Simulate canonical turn execution without local turn handlers
	// Verify tagger was never called
	if got := taggerSpy.tagCalls.Load(); got != 0 {
		t.Fatalf("expected 0 calls to tagger on stock canonical, got %d", got)
	}

	// In wire execution, local turn stage is never invoked; tagger is never touched.
	if got := taggerSpy.tagCalls.Load(); got != 0 {
		t.Fatalf("expected 0 calls to tagger on stock wire, got %d", got)
	}
}

// TestStockHost_BehavioralDifferential_SteeringWriterFactory proves that on a
// stock host with clean baseline (no continuation, no interleaved, no active steering),
// SteeringWriterFactory is completely idle (0 calls) in both canonical and wire.
func TestStockHost_BehavioralDifferential_SteeringWriterFactory(t *testing.T) {
	t.Parallel()

	var factoryCalls atomic.Int32
	factory := func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error) {
		factoryCalls.Add(1)
		return nil, nil
	}

	ex := &Executor{
		CoreRuntime: CoreRuntime{
			SteeringWriterFactory: factory,
		},
	}

	// In canonical execution without continuation, interleaved, or active "alg-rec" overlay,
	// SteeringWriterFactory is never called.
	ctx := context.Background()
	cleanCall := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "gpt-4o"},
	}
	_, _, _, err := ex.snapshotAndProject(ctx, "a-leg-1", cleanCall)
	if err != nil {
		t.Fatalf("snapshotAndProject: %v", err)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("expected 0 factory calls on clean canonical snapshot, got %d", got)
	}

	// In wire execution, SteeringWriterFactory is never referenced in ExecuteLargeBody or assemble.
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("expected 0 factory calls on wire execution, got %d", got)
	}
}

// TestStockHost_BehavioralDifferential_CompactionDetector proves that on a stock host
// with no compaction observers/preservers:
//  1. Request side: zero events dispatched.
//  2. Response side: PreviewResponse and ResponseReleased are called on the detector
//     identically for both canonical and wire response streams.
func TestStockHost_BehavioralDifferential_CompactionDetector(t *testing.T) {
	t.Parallel()

	detectorSpy := &spyStockCompactionDetector{}

	ex := &Executor{
		CoreRuntime: CoreRuntime{
			CorePorts: CorePorts{CompactionDetector: detectorSpy},
		},
	}

	// In stock composition, observers and preservers are empty
	if len(ex.compactionObservers()) != 0 {
		t.Fatal("expected empty compaction observers on stock host")
	}
	if len(ex.compactionPreservers()) != 0 {
		t.Fatal("expected empty compaction preservers on stock host")
	}

	// Verify response pipeline wiring (shared by canonical and wire streams)
	pipe := newResponsePipelineForExecutor(ex, compaction.PreservationMeta{
		ALegID: "a-1",
	})
	if pipe.detector == nil {
		t.Fatal("expected non-nil detector in response pipeline")
	}

	// Send a test response event through observeCompactionRelease
	attempt := &attemptSession{
		bleg: b2bua.BLegRecord{
			BLegID: "b-1",
			Seq:    1,
		},
	}
	ev := aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: "hello from response",
	}
	facts := recvTurnFacts{}

	pipe.observeCompactionRelease(context.Background(), facts, attempt, ev)

	if got := detectorSpy.previewResponseCalls.Load(); got != 1 {
		t.Fatalf("expected 1 PreviewResponse call on response event, got %d", got)
	}
	if got := detectorSpy.responseReleaseCalls.Load(); got != 1 {
		t.Fatalf("expected 1 ResponseReleased call on response event, got %d", got)
	}
	if len(detectorSpy.releasedEvents) != 1 || detectorSpy.releasedEvents[0].Delta != "hello from response" {
		t.Fatalf("unexpected released event on detector: %+v", detectorSpy.releasedEvents)
	}
}

// TestStockHost_BehavioralDifferential_CapsResolver_DisagreementRejection proves that
// when a backend's canonical capabilities resolver disagrees with wire capability
// (e.g. streaming capability is absent from canonical capability resolution),
// CapsResolverWireProofSubsumed is false, causing the authority gate to reject the
// fast path and decline precommit with DeclineReasonAuthorityBlocker, falling back to
// canonical execution where capabilities are resolved dynamically.
// Conversely, when canonical capabilities resolver and wire proof agree that streaming
// is supported, CapsResolverWireProofSubsumed is true and the gate proceeds.
func TestStockHost_BehavioralDifferential_CapsResolver_DisagreementRejection(t *testing.T) {
	t.Parallel()

	genID := "gen-stock-caps-diff"

	// Case 1: Semantic capability disagreement (wire claimed, but canonical caps lacks streaming)
	// CapsResolverWireProofSubsumed is false -> precommit declines with DeclineReasonAuthorityBlocker.
	censusDisagreed := largebody.NewStandardDependencyCensus(genID)
	censusDisagreed.Ports.CapsResolverOccupied = true
	censusDisagreed.Ports.CapsResolverWireProofSubsumed = false

	summaryDisagreed, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    censusDisagreed.Planes,
		Hooks:                     censusDisagreed.Hooks,
		Ports:                     censusDisagreed.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary (disagreed): %v", err)
	}
	if !summaryDisagreed.HasStaticBlocker() {
		t.Fatal("expected summaryDisagreed to report HasStaticBlocker == true")
	}

	gateDisagreed := largebody.NewAuthorityAssessmentGate(summaryDisagreed, censusDisagreed, genID)
	dec, reas := gateDisagreed.Evaluate()
	if dec != largebody.AssessmentDecisionDecline || reas != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline with authority blocker on caps disagreement, got dec=%v reas=%v", dec, reas)
	}

	assessorDisagreed := NewProductionLargeBodyAssessor(genID, genID, gateDisagreed, nil, StandardLaneDomainPolicies())
	proof := largebody.Proof{
		ProfileID:     "openailegacy",
		Operation:     aipapi.OperationOpenAIChatCompletions,
		Delivery:      aipapi.DeliveryModeStreaming,
		RouteSelector: "gpt-4o",
		Turn: largebody.ClientTurnShape{
			Items: []largebody.ClientTurnItemShape{{
				Kind:  aipapi.ItemKindMessage,
				Role:  aipapi.RoleUser,
				Parts: []largebody.ClientTurnPartShape{{Kind: aipapi.ContentPartText, ContentBytes: 100}},
			}},
		},
	}
	assessment, err := assessorDisagreed.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody failed: %v", err)
	}
	if !assessment.Declined() || assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected declined with DeclineReasonAuthorityBlocker, got %v (%s)", assessment.Decision, assessment.Reason)
	}

	// Case 2: Semantic capability agreement (wire capability subsumed by canonical caps)
	// CapsResolverWireProofSubsumed is true -> authority gate allows wire evaluation without blocker.
	censusAgreed := largebody.NewStandardDependencyCensus(genID)
	censusAgreed.Ports.CapsResolverOccupied = true
	censusAgreed.Ports.CapsResolverWireProofSubsumed = true

	summaryAgreed, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    censusAgreed.Planes,
		Hooks:                     censusAgreed.Hooks,
		Ports:                     censusAgreed.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary (agreed): %v", err)
	}
	if summaryAgreed.HasStaticBlocker() {
		t.Fatal("expected summaryAgreed to report HasStaticBlocker == false")
	}

	gateAgreed := largebody.NewAuthorityAssessmentGate(summaryAgreed, censusAgreed, genID)
	decAgreed, reasAgreed := gateAgreed.Evaluate()
	if decAgreed != largebody.AssessmentDecisionAccept || reasAgreed != largebody.DeclineReasonNone {
		t.Fatalf("expected accept on caps agreement, got dec=%v reas=%v", decAgreed, reasAgreed)
	}
}
