package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestStreamObserver_capturesRedactedOpaqueAsSeparatePart(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, defaultStoreOptions(now))
	cfg := decodeValidConfig(t, `
action: restore
use_builtin_catalog: false
rules:
  - id: r1
    backend: anthropic
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
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, st)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "anthropic",
		Model:     "claude-3-5-haiku-20241022",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-opaque-1"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	opaque := []byte(`{"type":"redacted_thinking","data":"opaque-blob"}`)
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig-a"},
		{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: opaque},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
		{Kind: aipapi.EventResponseFinished},
	}
	for _, ev := range events {
		if err := obs.Observe(context.Background(), ev); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := st.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-opaque-1"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("artifact_count=%d want 1", len(snap))
	}
	got := snap[0].Reasoning
	if len(got) != 2 {
		t.Fatalf("reasoning_parts=%d want 2 (thinking+redacted)", len(got))
	}
	if got[0].Part.Reasoning == nil || got[0].Part.Reasoning.Dialect != aipapi.ReasoningDialectAnthropicThinkingV1 {
		t.Fatal("first part must be anthropic thinking dialect")
	}
	if got[0].Part.Reasoning.Text != "plan" || got[0].Part.Reasoning.Signature != "sig-a" {
		t.Fatal("thinking text/signature structural miss")
	}
	if got[1].Part.Reasoning == nil || got[1].Part.Reasoning.Dialect != aipapi.ReasoningDialectAnthropicRedactedThinkingV1 {
		t.Fatal("second part must be redacted thinking dialect")
	}
	var envelope struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(got[1].Part.Reasoning.Opaque, &envelope); err != nil {
		t.Fatalf("opaque unmarshal: %v", err)
	}
	if envelope.Type != "redacted_thinking" || envelope.Data != "opaque-blob" {
		t.Fatalf("opaque structural miss type_ok=%v data_ok=%v", envelope.Type == "redacted_thinking", envelope.Data == "opaque-blob")
	}
}
