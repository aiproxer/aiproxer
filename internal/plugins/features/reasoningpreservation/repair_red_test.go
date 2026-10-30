package reasoningpreservation_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestRestoreMissingReasoning_observeCorruptNeverExcludes(t *testing.T) {
	t.Parallel()
	corrupt := []reasoningpreservation.TurnArtifact{{
		ID: "corrupt", ReasoningBytes: -1, SourceBackend: "b", SourceModel: "m",
	}}
	for _, policy := range []string{
		reasoningpreservation.PolicyReject,
		reasoningpreservation.PolicyLogSkip,
	} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			call, _ := missingRestoreFixture(t)
			before := cloneCall(t, call)
			got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
				Action:       reasoningpreservation.ActionObserve,
				OnStateError: policy,
				Call:         &call,
				Artifacts:    corrupt,
				Eligible:     true,
				ReplaySupport: aipapi.ReasoningReplaySupport{
					Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1},
				},
			})
			if err != nil {
				t.Fatalf("observe must not error: %v", err)
			}
			if got.Exclude || got.Mutated {
				t.Fatalf("observe must never exclude/mutate, got=%+v", got)
			}
			if !reflect.DeepEqual(before.Messages, call.Messages) {
				t.Fatal("observe must leave call unchanged")
			}
		})
	}
}

func TestStreamObserver_oversizeCountsReasoningBytesOnly(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 32
  max_session_bytes: 4096
`)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 32, MaxSessionBytes: 4096, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-oversize"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	largeText := strings.Repeat("T", 256)
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "short-thought"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: largeText})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-oversize"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("large visible text must not oversize small reasoning; artifacts=%d diag=%q", len(snap), factory.LastSafeDiagnostic())
	}

	obs2, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-oversize-2"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open2: %v", err)
	}
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: strings.Repeat("R", 64)})
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"})
	if err := obs2.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish2: %v", err)
	}
	snap2, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-oversize-2"))
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	if len(snap2) != 0 {
		t.Fatal("reasoning payload over limit must discard")
	}
	if !strings.Contains(factory.LastSafeDiagnostic(), "oversize") {
		t.Fatalf("expected oversize safe diagnostic, got %q", factory.LastSafeDiagnostic())
	}
}

func TestEmptyAuthoritativeSession_isStateMiss(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validRestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024, MaxSessionBytes: 4096, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: ""},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	emptySnap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(""))
	if err != nil {
		t.Fatalf("Snapshot empty key: %v", err)
	}
	if len(emptySnap) != 0 {
		t.Fatal("empty authoritative session must not capture under shared empty key")
	}

	art := turnArtifact("a1", anchorFor(t, aipapi.TextPart("visible answer")),
		placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "x", "", nil)))
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("real-sess"), art); err != nil {
		t.Fatalf("Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	call, _ := missingRestoreFixture(t)
	before := cloneCall(t, call)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "anthropic-prod", Model: "any",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: ""},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue || !reflect.DeepEqual(before.Messages, call.Messages) {
		t.Fatalf("empty session restore must no-op continue, dec=%+v mutated=%v", dec, !reflect.DeepEqual(before.Messages, call.Messages))
	}
}

func TestStreamObserver_toolCallArgsDeltaPreservedInAnchor(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 16384
`)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 16384, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-tool"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "lookup"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, Delta: `{"q":`})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, Delta: `"weather"}`})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "lookup"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "done"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-tool"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("expected artifact, got %d", len(snap))
	}
	wantAnchor := computeAnchor(t, assistantMsg(
		aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "lookup", Content: []byte(`{"q":"weather"}`)},
		aipapi.TextPart("done"),
	))
	if snap[0].Anchor != wantAnchor {
		t.Fatalf("tool args must participate in anchor; diag=%q", factory.LastSafeDiagnostic())
	}
	if snap[0].Reasoning[0].BeforeNonReasoningPart != 0 {
		t.Fatalf("reasoning placement want 0 got %d", snap[0].Reasoning[0].BeforeNonReasoningPart)
	}
}

type failingStore struct {
	reasoningpreservation.TurnStore
	err error
}

func (f failingStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, f.err
}

func TestStreamObserver_appendFailureRecordsSafeDiagnostic(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	base := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024, MaxSessionBytes: 4096, Now: time.Now,
	})
	store := failingStore{TurnStore: base, err: context.Canceled}
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-fail"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish must FailOpen: %v", err)
	}
	diag := factory.LastSafeDiagnostic()
	if !strings.Contains(diag, "state_error") {
		t.Fatalf("expected state_error diagnostic, got %q", diag)
	}
	for _, needle := range []string{"thought", "answer", "sess-fail"} {
		if strings.Contains(diag, needle) {
			t.Fatalf("diagnostic leaked %q in %q", needle, diag)
		}
	}
}

func TestDerivePlacements_allAtZeroPreserveOrder(t *testing.T) {
	t.Parallel()
	reasoning := []aipapi.Part{
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "a", "", nil),
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "b", "", nil),
	}
	got := derivePlacements(t, 3, reasoning)
	if len(got) != 2 || got[0].BeforeNonReasoningPart != 0 || got[1].BeforeNonReasoningPart != 0 {
		t.Fatalf("flat DerivePlacements must place all at 0 in order, got=%+v", got)
	}
	if got[0].Part.Reasoning.Text != "a" || got[1].Part.Reasoning.Text != "b" {
		t.Fatalf("order not preserved: %+v", got)
	}
}
