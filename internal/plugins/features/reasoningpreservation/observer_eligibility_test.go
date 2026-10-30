package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestStreamObserver_unmatchedBuiltinModelNoStoreNoTelemetry(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "claude-3-5-sonnet",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-unmatched"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventReasoningDelta, Delta: "secret-plan"},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
	}
	for _, ev := range events {
		if err := obs.Observe(context.Background(), ev); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-unmatched"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("unmatched model must not append store artifacts, got %d", len(snap))
	}
	if len(tel.Snapshot()) != 0 {
		t.Fatalf("unmatched model must not record feature outcomes, got %v", tel.Snapshot())
	}
}

func TestStreamObserver_matchedBuiltinModelCaptures(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "moonshotai/kimi-k2",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-matched"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-matched"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("matched builtin model must capture, got %d", len(snap))
	}
	if tel.Snapshot()[reasoningpreservation.OutcomeObserved] != 1 {
		t.Fatalf("want observed=1, got %v", tel.Snapshot())
	}
}

func TestStreamObserver_explicitEnabledOutsideBuiltinCaptures(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: true
rules:
  - id: enable-gpt56
    backend: openrouter-prod
    model_keywords: ["gpt-5.6"]
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "openai/gpt-5.6",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-explicit"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-explicit"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("explicit-enabled gpt-5.6 must capture outside builtin auto policy, got %d", len(snap))
	}
}

func TestStreamObserver_builtinGPT56ExcludedNoCapture(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openai-legacy",
		BackendPrefixes: []string{"openai-legacy"},
		Model:           "gpt-5.6",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-gpt56"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-gpt56"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("automatic policy must exclude gpt-5.6 capture, got %d", len(snap))
	}
	if len(tel.Snapshot()) != 0 {
		t.Fatalf("excluded automatic model must not record outcomes, got %v", tel.Snapshot())
	}
}

func TestAttemptTransform_unmatchedBuiltinModelNoRestoreTelemetry(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	call, artifacts := missingRestoreFixture(t)
	partition := reasoningpreservation.NewSessionPartition("sess-1")
	if _, err := store.Append(context.Background(), partition, artifacts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store, tel)
	before := cloneCall(t, call)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "claude-3-5-sonnet",
		ReplaySupport:   aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:         session.SessionView{AuthoritativeSessionID: "sess-1"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue {
		t.Fatalf("dec=%+v", dec)
	}
	if !reflectDeepEqualMessages(before.Messages, call.Messages) {
		t.Fatal("unmatched model must not mutate")
	}
	if len(tel.Snapshot()) != 0 {
		t.Fatalf("unmatched model must not record restore outcomes, got %v", tel.Snapshot())
	}
}

func TestStreamObserver_openClonesBackendPrefixes(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	prefixes := []string{"openrouter"}
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: prefixes,
		Model:           "kimi-k2",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-prefix-clone"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	prefixes[0] = "not-a-catalog-family"
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-prefix-clone"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("Open must clone BackendPrefixes so caller mutation cannot drop eligibility, got %d artifacts", len(snap))
	}
}

func TestStreamObserver_eligibleOversizeStillRecords(t *testing.T) {
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
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 8
  max_session_bytes: 262144
`)
	store := newMemoryStore(t, defaultStoreOptions(time.Now))
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "any-model",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-over"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "1234567890"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-over"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("oversize must not persist, got %d", len(snap))
	}
	if tel.Snapshot()[reasoningpreservation.OutcomeOversize] != 1 {
		t.Fatalf("eligible oversize must record outcome, got %v", tel.Snapshot())
	}
}
