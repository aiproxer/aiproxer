package reasoningpreservation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

type countingStore struct {
	inner         reasoningpreservation.TurnStore
	snapshotCalls atomic.Int64
	appendCalls   atomic.Int64
	deleteCalls   atomic.Int64
}

func (s *countingStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	s.appendCalls.Add(1)
	return s.inner.Append(ctx, p, a)
}

func (s *countingStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	s.snapshotCalls.Add(1)
	return s.inner.Snapshot(ctx, p)
}

func (s *countingStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	s.deleteCalls.Add(1)
	return s.inner.Delete(ctx, p, ids...)
}

func builtinRestoreCfg(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, `
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
}

func TestAttemptTransform_unmatchedSkipsStoreSnapshot(t *testing.T) {
	t.Parallel()
	cfg := builtinRestoreCfg(t)
	inner := newMemoryStore(t, defaultStoreOptions(time.Now))
	store := &countingStore{inner: inner}
	tel := reasoningpreservation.NewTelemetry()
	call, artifacts := missingRestoreFixture(t)
	if _, err := inner.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-1"), artifacts[0]); err != nil {
		t.Fatalf("seed Append: %v", err)
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
		t.Fatal("unmatched must not mutate")
	}
	if store.snapshotCalls.Load() != 0 || store.appendCalls.Load() != 0 || store.deleteCalls.Load() != 0 {
		t.Fatalf("unmatched must not touch store: snapshot=%d append=%d delete=%d",
			store.snapshotCalls.Load(), store.appendCalls.Load(), store.deleteCalls.Load())
	}
	if len(tel.Snapshot()) != 0 {
		t.Fatalf("unmatched must not emit telemetry, got %v", tel.Snapshot())
	}
}

func TestAttemptTransform_eligibleStillSnapshots(t *testing.T) {
	t.Parallel()
	cfg := builtinRestoreCfg(t)
	inner := newMemoryStore(t, defaultStoreOptions(time.Now))
	store := &countingStore{inner: inner}
	tel := reasoningpreservation.NewTelemetry()
	call, artifacts := missingRestoreFixture(t)
	if _, err := inner.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-1"), artifacts[0]); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store, tel)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "moonshotai/kimi-k2",
		ReplaySupport:   aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:         session.SessionView{AuthoritativeSessionID: "sess-1"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue {
		t.Fatalf("dec=%+v", dec)
	}
	if store.snapshotCalls.Load() != 1 {
		t.Fatalf("eligible path must Snapshot once, got %d", store.snapshotCalls.Load())
	}
	if len(tel.Snapshot()) == 0 {
		t.Fatal("eligible path must record feature outcomes after Snapshot/classify/restore")
	}
}

func TestStreamObserverFactory_unmatchedOpenIsInertNoStore(t *testing.T) {
	t.Parallel()
	cfg := builtinRestoreCfg(t)
	inner := newMemoryStore(t, defaultStoreOptions(time.Now))
	store := &countingStore{inner: inner}
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "claude-3-5-sonnet",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-inert"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !reasoningpreservation.StreamObserverIsInert(obs) {
		t.Fatal("unmatched Open must return inert no-op observer (no event parse/buffer path)")
	}
	if factory.FailureMode() != sdkhooks.FailOpen {
		t.Fatalf("FailureMode=%v want FailOpen", factory.FailureMode())
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventReasoningDelta, Delta: "secret-plan"},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"x":1}`)},
	}
	for _, ev := range events {
		if err := obs.Observe(context.Background(), ev); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if store.snapshotCalls.Load() != 0 || store.appendCalls.Load() != 0 || store.deleteCalls.Load() != 0 {
		t.Fatalf("inert observer must not touch store: snapshot=%d append=%d delete=%d",
			store.snapshotCalls.Load(), store.appendCalls.Load(), store.deleteCalls.Load())
	}
	if len(tel.Snapshot()) != 0 {
		t.Fatalf("inert observer must not emit telemetry, got %v", tel.Snapshot())
	}
}

func TestStreamObserverFactory_eligibleOpenIsActive(t *testing.T) {
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
	inner := newMemoryStore(t, defaultStoreOptions(time.Now))
	store := &countingStore{inner: inner}
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "kimi-k2",
		Session:         session.SessionView{AuthoritativeSessionID: "sess-active"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reasoningpreservation.StreamObserverIsInert(obs) {
		t.Fatal("eligible Open must use active observer")
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "plan"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if store.appendCalls.Load() != 1 {
		t.Fatalf("eligible active observer must Append once, got %d", store.appendCalls.Load())
	}
	if tel.Snapshot()[reasoningpreservation.OutcomeObserved] != 1 {
		t.Fatalf("want observed=1, got %v", tel.Snapshot())
	}
}
