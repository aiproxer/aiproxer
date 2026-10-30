package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestFeatureBundle_contributesPorts(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validRestoreYAML)
	b, err := reasoningpreservation.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if transforms := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms); len(transforms) != 1 {
		t.Fatalf("AttemptTransforms=%d", len(transforms))
	}
	if observers := aipfeature.Get(b.PlaneSet, aipfeature.PlaneStreamObserverFactories); len(observers) != 1 {
		t.Fatalf("StreamObserverFactories=%d", len(observers))
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAttemptTransform_observeDoesNotMutate(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validObserveYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1024,
		MaxSessionBytes:          4096,
		Now:                      time.Now,
	})
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	call, artifacts := missingRestoreFixture(t)
	partition := reasoningpreservation.NewSessionPartition("sess-1")
	if _, err := store.Append(context.Background(), partition, artifacts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before := cloneCall(t, call)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID:       "openrouter-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "kimi-k2",
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
		t.Fatal("observe must not mutate")
	}
}

func TestStreamObserver_appendsOnlyOnSuccessReleased(t *testing.T) {
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
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1024,
		MaxSessionBytes:          4096,
		Now:                      time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
	if err := obs.Finish(context.Background(), response.OutcomeFailed); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-obs"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("failed outcome must not persist, got %d", len(snap))
	}

	obs2, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open2: %v", err)
	}
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
	if err := obs2.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish success: %v", err)
	}
	snap, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-obs"))
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("success_released must persist one artifact, got %d", len(snap))
	}
}

func reflectDeepEqualMessages(a, b []aipapi.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || len(a[i].Parts) != len(b[i].Parts) {
			return false
		}
		for j := range a[i].Parts {
			if a[i].Parts[j].Kind != b[i].Parts[j].Kind || a[i].Parts[j].Text != b[i].Parts[j].Text {
				return false
			}
		}
	}
	return true
}
