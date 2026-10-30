package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wireTestSpyCompactionDetector struct {
	inner         *compactiondetect.Detector
	mu            sync.Mutex
	calls         []compactionfacts.RequestFacts
	metas         []compaction.PreservationMeta
	eventsEmitted []compaction.Event
}

func newSpyCompactionDetector() *wireTestSpyCompactionDetector {
	return &wireTestSpyCompactionDetector{
		inner: compactiondetect.New(compactiondetect.Config{}),
	}
}

func (s *wireTestSpyCompactionDetector) PreviewRequest(meta compaction.PreservationMeta, call aipapi.Call) compaction.RequestPreview {
	return s.inner.PreviewRequest(meta, call)
}

func (s *wireTestSpyCompactionDetector) RequestOpened(meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metas = append(s.metas, meta)
	evs := s.inner.RequestOpened(meta, call)
	s.eventsEmitted = append(s.eventsEmitted, evs...)
	return evs
}

func (s *wireTestSpyCompactionDetector) PreviewResponse(meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
	return s.inner.PreviewResponse(meta, ev)
}

func (s *wireTestSpyCompactionDetector) ResponseReleased(meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.inner.ResponseReleased(meta, ev)
	s.eventsEmitted = append(s.eventsEmitted, evs...)
	return evs
}

func (s *wireTestSpyCompactionDetector) RequestOpenedFacts(meta compaction.PreservationMeta, facts compactionfacts.RequestFacts) []compaction.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, facts)
	s.metas = append(s.metas, meta)
	evs := s.inner.RequestOpenedFacts(meta, facts)
	s.eventsEmitted = append(s.eventsEmitted, evs...)
	return evs
}

func (s *wireTestSpyCompactionDetector) Calls() []compactionfacts.RequestFacts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]compactionfacts.RequestFacts, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *wireTestSpyCompactionDetector) Events() []compaction.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]compaction.Event, len(s.eventsEmitted))
	copy(out, s.eventsEmitted)
	return out
}

func setupWireCompactionExecutor(t *testing.T, detector CompactionDetector) (*Executor, *int) {
	t.Helper()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)

	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)

	openWireCalls := 0

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1000, 0) }
	ex.LargeBodyGenerationID = "gen-1"
	ex.LargeBodyCandidateDomainGeneration = "dom-gen-1"
	ex.DefaultBackend = "default"
	ex.CompactionDetector = detector
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				openWireCalls++
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}

	return ex, &openWireCalls
}

func TestCompaction_WireAcceptedFacts_ReachRealDetectorOnce(t *testing.T) {
	t.Parallel()

	spy := newSpyCompactionDetector()
	ex, openWireCalls := setupWireCompactionExecutor(t, spy)

	// Create request with codex local checkpoint marker
	call := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "context checkpoint compaction for active session"}}},
		},
	}
	facts := compactionfacts.ExtractFactsFromCall(call)
	require.True(t, facts.StartRuleMatched)
	require.Equal(t, "codex.local_checkpoint.v1", facts.StartRuleID)

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"system","content":"context checkpoint compaction for active session"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openailegacy", src, true)
	acc.CompactionFacts = facts
	acc.CompactionComplete = true

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-compaction"})

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotEmpty(t, res.Facts.RequestID)
	assert.Equal(t, 1, *openWireCalls)

	// Real detector must have been called exactly once with exact facts
	calls := spy.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "codex.local_checkpoint.v1", calls[0].StartRuleID)
	assert.True(t, calls[0].StartRuleMatched)

	events := spy.Events()
	require.Len(t, events, 1)
	assert.Equal(t, compaction.PhaseStarted, events[0].Phase)
	assert.Equal(t, "codex.local_checkpoint.v1", events[0].RuleID)
	assert.Equal(t, compaction.EvidenceSignatureStrict, events[0].Evidence)
}

func TestCompaction_WireAcceptedFacts_IncompleteFacts_FailsInvariantBeforeBackendOpen(t *testing.T) {
	t.Parallel()

	spy := newSpyCompactionDetector()
	ex, openWireCalls := setupWireCompactionExecutor(t, spy)

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openailegacy", src, true)
	// Incomplete facts
	acc.CompactionFacts = compactionfacts.RequestFacts{}
	acc.CompactionComplete = false

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-compaction"})

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compaction facts are incomplete")

	// Backend OpenWire must NEVER have been called
	assert.Equal(t, 0, *openWireCalls)
	// Detector must have received zero calls
	assert.Empty(t, spy.Calls())
}

func TestCompaction_AssessLargeBody_ActiveDetectorDeclineWhenIncomplete(t *testing.T) {
	t.Parallel()

	genID := "gen-compaction-assess"
	census := largebody.NewStandardDependencyCensus(genID)
	census.Ports.CompactionDetectorOccupied = true
	census.Ports.CompactionDetectorWireSupported = true

	assessor := makeBlocker2Assessor(t, genID, genID, census)

	// 1. Proof with CompactionComplete: false -> DECLINE with AuthorityBlocker
	proofIncomplete := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proofIncomplete.CompactionComplete = false

	dec, err := assessor.AssessLargeBody(context.Background(), proofIncomplete)
	require.NoError(t, err)
	assert.True(t, dec.Declined())
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, dec.Reason)

	// 2. Proof with CompactionComplete: true -> ACCEPT with facts propagated
	proofComplete := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proofComplete.CompactionComplete = true
	proofComplete.CompactionFacts = compactionfacts.RequestFacts{
		Operation: aipapi.OperationOpenAIChatCompletions,
		ItemCount: 1,
	}

	acc, err := assessor.AssessLargeBody(context.Background(), proofComplete)
	require.NoError(t, err)
	assert.True(t, acc.Accepted())
	assert.True(t, acc.CompactionComplete)
	assert.Equal(t, 1, acc.CompactionFacts.ItemCount)
}

func TestCompaction_ProofSliceOwnership_IsolatedFromCallerMutations(t *testing.T) {
	t.Parallel()

	genID := "gen-compaction-ownership"
	census := largebody.NewStandardDependencyCensus(genID)
	assessor := makeBlocker2Assessor(t, genID, genID, census)

	originalHash := [32]byte{1, 2, 3}
	proof := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proof.CompactionComplete = true
	proof.CompactionFacts = compactionfacts.RequestFacts{
		Operation:  aipapi.OperationOpenAIChatCompletions,
		ItemCount:  1,
		ItemHashes: [][32]byte{originalHash},
	}

	acc, err := assessor.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.True(t, acc.Accepted())

	// Mutate caller's slice after assessment
	mutatedHash := [32]byte{99, 99, 99}
	proof.CompactionFacts.ItemHashes[0] = mutatedHash

	// Accepted assessment must retain its owned copy unchanged
	assert.Equal(t, originalHash, acc.CompactionFacts.ItemHashes[0], "accepted compaction facts must not be mutated by caller")
}

func TestCompaction_StampDigestBinding_RejectsMutatedFactsBeforeBackendOpen(t *testing.T) {
	t.Parallel()

	spy := newSpyCompactionDetector()
	ex, openWireCalls := setupWireCompactionExecutor(t, spy)

	genID := "gen-1"
	cGen := "dom-gen-1"
	census := largebody.NewStandardDependencyCensus(genID)
	census.Ports.CompactionDetectorOccupied = true
	census.Ports.CompactionDetectorWireSupported = true
	assessor := makeBlocker2Assessor(t, genID, cGen, census)

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	proof := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proof.Source = src.digest
	proof.BodyBytes = src.size
	proof.CompactionComplete = true
	proof.CompactionFacts = compactionfacts.RequestFacts{
		Operation:  aipapi.OperationOpenAIChatCompletions,
		ItemCount:  1,
		ItemHashes: [][32]byte{{1, 1, 1}},
	}

	acc, err := assessor.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.True(t, acc.Accepted())
	require.NotEqual(t, [32]byte{}, acc.Stamp.CompactionDigest(), "assessment stamp must bind non-zero compaction digest")

	// Case 1: Tamper with ItemHashes in accepted assessment
	tamperedAcc := acc
	tamperedAcc.CompactionFacts = acc.CompactionFacts.Clone()
	tamperedAcc.CompactionFacts.ItemHashes[0] = [32]byte{42, 42, 42}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-compaction"})
	_, err = ex.ExecuteLargeBody(ctx, tamperedAcc, src)
	require.Error(t, err)
	assert.True(t, errors.Is(err, largebody.ErrStampDisagreement), "must return ErrStampDisagreement when compaction facts mutated")
	assert.Equal(t, 0, *openWireCalls, "backend OpenWire must not be called when facts are tampered")

	// Case 2: Tamper with CompactionComplete
	tamperedAcc2 := acc
	tamperedAcc2.CompactionComplete = false
	_, err = ex.ExecuteLargeBody(ctx, tamperedAcc2, src)
	require.Error(t, err)
	assert.Equal(t, 0, *openWireCalls, "backend OpenWire must not be called")

	// Case 3: Tamper with Operation in compaction facts
	tamperedAcc3 := acc
	tamperedAcc3.CompactionFacts = acc.CompactionFacts.Clone()
	tamperedAcc3.CompactionFacts.Operation = aipapi.OperationOpenAIResponses
	_, err = ex.ExecuteLargeBody(ctx, tamperedAcc3, src)
	require.Error(t, err)
	assert.True(t, errors.Is(err, largebody.ErrStampDisagreement))
	assert.Equal(t, 0, *openWireCalls)

	// Untampered accepted assessment executes successfully
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotEmpty(t, res.Facts.RequestID)
	assert.Equal(t, 1, *openWireCalls)
}

func TestCompaction_AssessLargeBody_ConfiguredSemanticFactBudget(t *testing.T) {
	t.Parallel()

	genID := "gen-budget"
	census := largebody.NewStandardDependencyCensus(genID)
	assessor := makeBlocker2Assessor(t, genID, genID, census)

	proof := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proof.CompactionComplete = true
	// Create several item hashes so aggregate fact bytes exceeds 200 bytes
	hashes := make([][32]byte, 10)
	for i := range hashes {
		hashes[i] = [32]byte{byte(i + 1)}
	}
	proof.CompactionFacts = compactionfacts.RequestFacts{
		Operation:  aipapi.OperationOpenAIChatCompletions,
		ItemCount:  len(hashes),
		ItemHashes: hashes,
	}

	aggBytes := proof.AggregateFactBytes()
	require.Greater(t, aggBytes, int64(200), "aggregate fact bytes must be > 200")

	// 1. Context with tiny budget (e.g. 50 bytes) -> declined
	ctxTiny := largebody.WithSemanticFactBudget(context.Background(), 50)
	dec, err := assessor.AssessLargeBody(ctxTiny, proof)
	require.NoError(t, err)
	assert.True(t, dec.Declined())
	assert.Equal(t, largebody.DeclineReasonAuthorityBlocker, dec.Reason)

	// 2. Context with ample budget -> accepted
	ctxAmple := largebody.WithSemanticFactBudget(context.Background(), aggBytes+1000)
	acc, err := assessor.AssessLargeBody(ctxAmple, proof)
	require.NoError(t, err)
	assert.True(t, acc.Accepted())
}

func TestCompaction_ValidateExecuteLargeBody_DetectsTamperedFacts(t *testing.T) {
	t.Parallel()

	genID := "gen-stamp-val"
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	proof := makeBlocker2ValidProof("openailegacy", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	proof.Source = src.digest
	proof.BodyBytes = src.size
	proof.CompactionComplete = true
	proof.CompactionFacts = compactionfacts.RequestFacts{
		Operation:  aipapi.OperationOpenAIChatCompletions,
		ItemCount:  1,
		ItemHashes: [][32]byte{{1, 2, 3}},
	}

	stamp, err := largebody.BindAssessmentStamp(genID, proof, genID)
	require.NoError(t, err)

	acc, err := largebody.NewAcceptedAssessment(stamp, largebody.WireRequestFacts{
		ProfileID:      "openailegacy",
		Operation:      aipapi.OperationOpenAIChatCompletions,
		Delivery:       aipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		Rewrite:        largebody.NewNoRewrite(),
		ClientModel:    "gpt-4o",
		CandidateModel: "gpt-4o",
	}, largebody.WireDomainFacts{
		ProfileID:      "openailegacy",
		Operation:      aipapi.OperationOpenAIChatCompletions,
		Delivery:       aipapi.DeliveryModeStreaming,
		UniversalModel: true,
	})
	require.NoError(t, err)
	acc = acc.WithCompactionFacts(proof.CompactionFacts, proof.CompactionComplete)

	live := largebody.LiveExecutionFacts{
		GenerationID:              genID,
		ProfileID:                 "openailegacy",
		Source:                    src.digest,
		BodyBytes:                 src.size,
		Mode:                      largebody.BodyModeIdentityJSON,
		Rewrite:                   largebody.NewNoRewrite(),
		CandidateDomainGeneration: genID,
	}

	// Valid facts pass
	err = largebody.ValidateExecuteLargeBody(acc, src, live)
	require.NoError(t, err)

	// Tampered facts return StampInvariantError with Field == "compaction facts"
	tampered := acc
	tampered.CompactionFacts = acc.CompactionFacts.Clone()
	tampered.CompactionFacts.ItemHashes[0] = [32]byte{99}

	err = largebody.ValidateExecuteLargeBody(tampered, src, live)
	require.Error(t, err)
	var invErr *largebody.StampInvariantError
	require.True(t, errors.As(err, &invErr))
	assert.Equal(t, "compaction facts", invErr.Field)
}

func TestCompaction_MultiTurn_MixedCanonicalAndWire_HeuristicDetection(t *testing.T) {
	t.Parallel()

	spy := newSpyCompactionDetector()

	aLegID := "aleg-multiturn-test"
	meta1 := compaction.PreservationMeta{
		TraceID: "trace-turn-1",
		ALegID:  aLegID,
	}

	// Turn 1: Canonical call with 10 messages, total ~38,000 characters => ~9,500 tokens (> 8000 threshold).
	msgTail1 := "penultimate message: the quick brown fox jumps over the lazy dog"
	msgTail2 := "final confirmation message: acknowledged and completed"
	var msgs []aipapi.Message
	for range 8 {
		msgs = append(msgs, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: strings.Repeat("a", 4800)}},
		})
	}
	msgs = append(msgs, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: msgTail1}},
	})
	msgs = append(msgs, aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: msgTail2}},
	})

	canonicalCall1 := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
		Messages: msgs,
	}

	// Observe Turn 1 canonical request opened on real detector
	evs1 := spy.RequestOpened(meta1, canonicalCall1)
	assert.Empty(t, evs1, "Initial turn must not trigger compaction heuristic")

	// Turn 2: Wire request on the SAME A-leg (aLegID).
	// Context is compacted: earlier 8 messages replaced with a single summary message.
	// The two tail messages msgTail1 and msgTail2 are preserved in order.
	// Token count is reduced to ~1,500 tokens (reduction > 8,000 tokens, > 25%).
	compactedItems := []aipapi.Item{
		{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleSystem,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Compacted summary of previous conversation"}},
		},
		{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: msgTail1}},
		},
		{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: msgTail2}},
		},
	}

	callCompacted := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
		Items: compactedItems,
	}
	factsTurn2 := compactionfacts.ExtractFactsFromCall(callCompacted)

	meta2 := compaction.PreservationMeta{
		TraceID: "trace-turn-2",
		ALegID:  aLegID,
	}

	// RequestOpenedFacts on real detector
	evs2 := spy.RequestOpenedFacts(meta2, factsTurn2)
	require.NotEmpty(t, evs2, "Compaction heuristic must detect transition from turn 1 canonical to turn 2 wire facts")

	foundHeuristic := false
	for _, ev := range evs2 {
		if ev.RuleID == "local.compaction_heuristic.v1" && ev.Phase == compaction.PhaseCompleted && ev.Evidence == compaction.EvidenceHistoryHeuristic {
			foundHeuristic = true
			break
		}
	}
	assert.True(t, foundHeuristic, "Must emit compaction completed event with EvidenceHistoryHeuristic")

	// Verify reverse transition: Turn A Wire -> Turn B Canonical on same A-leg
	aLegIDReverse := "aleg-wire-to-canonical"
	metaA := compaction.PreservationMeta{
		TraceID: "trace-wire-a",
		ALegID:  aLegIDReverse,
	}
	factsTurnA := compactionfacts.ExtractFactsFromCall(canonicalCall1)
	evsA := spy.RequestOpenedFacts(metaA, factsTurnA)
	assert.Empty(t, evsA)

	metaB := compaction.PreservationMeta{
		TraceID: "trace-canonical-b",
		ALegID:  aLegIDReverse,
	}
	evsB := spy.RequestOpened(metaB, callCompacted)
	require.NotEmpty(t, evsB, "Compaction heuristic must detect transition from wire turn to canonical turn")
	foundReverse := false
	for _, ev := range evsB {
		if ev.RuleID == "local.compaction_heuristic.v1" && ev.Phase == compaction.PhaseCompleted {
			foundReverse = true
			break
		}
	}
	assert.True(t, foundReverse, "Must emit compaction completed event on wire -> canonical transition")
}

func TestCompaction_ResponseCompletion_RealDetector_StreamsEvents(t *testing.T) {
	t.Parallel()

	spy := newSpyCompactionDetector()
	ex, openWireCalls := setupWireCompactionExecutor(t, spy)

	// Configure backend to stream chunk containing the Codex local checkpoint post marker
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				*openWireCalls++
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "Finished context checkpoint compaction successfully"},
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}

	call := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "context checkpoint compaction for active session"}}},
		},
	}
	facts := compactionfacts.ExtractFactsFromCall(call)
	require.True(t, facts.StartRuleMatched)
	require.Equal(t, "codex.local_checkpoint.v1", facts.StartRuleID)

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"system","content":"context checkpoint compaction for active session"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openailegacy", src, true)
	acc.CompactionFacts = facts
	acc.CompactionComplete = true

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-compaction"})

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Drain the stream to trigger response observation
	for {
		ev, err := res.Stream.Recv(ctx)
		if err != nil || ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}
	_ = res.Stream.Close()

	// Verify events emitted by real detector
	events := spy.Events()
	t.Logf("spy.Events(): %d events: %+v", len(events), events)
	require.NotEmpty(t, events)

	var hasStarted, hasCompleted bool
	for _, ev := range events {
		if ev.RuleID == "codex.local_checkpoint.v1" {
			if ev.Phase == compaction.PhaseStarted {
				hasStarted = true
			}
			if ev.Phase == compaction.PhaseCompleted {
				hasCompleted = true
			}
		}
	}
	assert.True(t, hasStarted, "Detector must record PhaseStarted on request open")
	assert.True(t, hasCompleted, "Detector must record PhaseCompleted on post marker response release")
}

// TestCompaction_PreservationMeta_SessionID_WireCanonicalParity pins that:
//  1. Both canonical (observeCompactionOpened) and wire (observeCompactionOpenedWire) executions
//     faithfully stamp the authoritative session ID into PreservationMeta.SessionID without loss.
//  2. Both lanes yield identical PreservationMeta.SessionID for turns within the same session.
//  3. The canonical AuthoritativeSessionID oracle is preserved and shared recorder real secureID
//     isolation is unaffected.
func TestCompaction_PreservationMeta_SessionID_WireCanonicalParity(t *testing.T) {
	t.Parallel()

	spyWire := newSpyCompactionDetector()
	exWire, _ := setupWireCompactionExecutor(t, spyWire)

	spyCanonical := newSpyCompactionDetector()
	exCanonical, _ := setupWireCompactionExecutor(t, spyCanonical)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-compaction"})

	// Turn 1: Fresh canonical request creates an authoritative session
	call := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions,
		},
		Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "canonical session turn 1"}}},
		},
	}
	resCanonical, err := exCanonical.Execute(ctx, &call)
	require.NoError(t, err)
	_ = resCanonical.Close()

	spyCanonical.mu.Lock()
	canonicalMetas := spyCanonical.metas
	spyCanonical.mu.Unlock()
	require.NotEmpty(t, canonicalMetas, "canonical execution must capture preservation metadata")
	canonicalSessionID := canonicalMetas[0].SessionID
	require.NotEmpty(t, canonicalSessionID, "canonical PreservationMeta.SessionID must not be empty")

	// Turn 2: Fresh wire request creates an authoritative session
	facts := compactionfacts.ExtractFactsFromCall(call)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"wire session turn 1"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openailegacy", src, true)
	acc.CompactionFacts = facts
	acc.CompactionComplete = true

	resWire, err := exWire.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	_ = resWire.Stream.Close()

	spyWire.mu.Lock()
	wireMetas := spyWire.metas
	spyWire.mu.Unlock()
	require.NotEmpty(t, wireMetas, "wire execution must capture preservation metadata")
	wireSessionID := wireMetas[0].SessionID
	require.NotEmpty(t, wireSessionID, "wire PreservationMeta.SessionID must not be empty")

	// 3. Resumed turn under established session: verify exact equality between canonical and wire
	resumedCall := call
	resumedCall.Session.AuthoritativeSessionID = canonicalSessionID
	resumedFacts := compactionfacts.ExtractFactsFromCall(resumedCall)

	// Canonical observation of resumed call
	prepCanonical := &preparedRequest{
		recvTurnFacts: recvTurnFacts{
			traceID: "trace-resumed",
			aLegID:  "aleg-resumed",
		},
		call: &resumedCall,
		identity: &identityBoundTurn{
			traceID: "trace-resumed",
			aLeg:    b2bua.ALegRecord{ALegID: "aleg-resumed"},
			call:    &resumedCall,
		},
	}
	readyStub := newReadyAttempt(&attemptSession{
		bleg: b2bua.BLegRecord{BLegID: "bleg-1", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "default", Model: "gpt-4o"}},
	}, pendingSelectionEffects{})

	canonicalResumedMeta := exCanonical.observeCompactionOpened(ctx, prepCanonical, openedAttempt{ready: readyStub})
	assert.Equal(t, canonicalSessionID, canonicalResumedMeta.SessionID, "canonical resumed turn must preserve AuthoritativeSessionID")

	// Wire observation of resumed turn under same session ID
	wp := &wireAttemptPayload{
		sessionID: canonicalSessionID,
	}
	prepWire := &preparedRequest{
		recvTurnFacts: recvTurnFacts{
			traceID:     "trace-resumed",
			aLegID:      "aleg-resumed",
			wirePayload: wp,
		},
	}
	wireResumedMeta := exWire.observeCompactionOpenedWire(ctx, prepWire, openedAttempt{ready: readyStub}, resumedFacts)
	assert.Equal(t, canonicalSessionID, wireResumedMeta.SessionID, "wire resumed turn must preserve authoritative sessionID from wirePayload")

	// Exact differential parity pinned:
	assert.Equal(t, canonicalResumedMeta.SessionID, wireResumedMeta.SessionID, "wire and canonical PreservationMeta.SessionID must be strictly equal")
	assert.Equal(t, canonicalSessionID, wireResumedMeta.SessionID)
}
