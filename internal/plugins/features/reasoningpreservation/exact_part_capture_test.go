package reasoningpreservation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func exactResponsesOpaque(t *testing.T, id string) json.RawMessage {
	t.Helper()
	return mustOpaqueJSON(t, `{"id":"`+id+`","type":"reasoning","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"encrypted_content":null,"status":"completed"}`)
}

func exactResponsesPart(t *testing.T, id string) *aipapi.ReasoningPart {
	t.Helper()
	return &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  exactResponsesOpaque(t, id),
	}
}

func observeExactConfig(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, `
action: restore
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
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
}

func exactStoreOptions(now func() time.Time) reasoningpreservation.StoreOptions {
	return reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       8,
		MaxReasoningBytesPerTurn: 65536,
		MaxSessionBytes:          262144,
		Now:                      now,
	}
}

func openExactObserver(t *testing.T, cfg reasoningpreservation.Config, store reasoningpreservation.TurnStore, sessionID string, tel *reasoningpreservation.Telemetry) response.StreamObserver {
	t.Helper()
	var factory *reasoningpreservation.StreamObserverFactory
	if tel != nil {
		factory = reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
	} else {
		factory = reasoningpreservation.NewStreamObserverFactory(cfg, store)
	}
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: sessionID},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return obs
}

func assertNoSecretLeak(t *testing.T, s string) {
	t.Helper()
	for _, f := range []string{"SECRET_SUM", "SECRET_BODY", "SECRET_ENC", `"encrypted_content"`, "rs_secret"} {
		if strings.Contains(s, f) {
			t.Fatalf("content leak %q in %q", f, s)
		}
	}
}

func TestStreamObserver_exactReasoningPart_capturesOpaqueDialectPosition(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-1", nil)
	opaque := exactResponsesOpaque(t, "rs_1")
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
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
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-1"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("artifact_count=%d want 1", len(snap))
	}
	got := snap[0].Reasoning
	if len(got) != 1 {
		t.Fatalf("reasoning_parts=%d want 1", len(got))
	}
	if got[0].BeforeNonReasoningPart != 0 {
		t.Fatalf("placement=%d want 0 (before text)", got[0].BeforeNonReasoningPart)
	}
	rp := got[0].Part.Reasoning
	if rp == nil || rp.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("dialect miss: %+v", rp)
	}
	if rp.Text != "" || rp.Signature != "" {
		t.Fatal("exact Responses part must not synthesize Chat text/signature")
	}
	if !bytes.Equal(rp.Opaque, opaque) {
		t.Fatalf("opaque bytes must be preserved exactly")
	}
}

func TestStreamObserver_exactReasoningPart_priorChatDeltaFlushedNotDropped(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-mixed", nil)
	opaque := exactResponsesOpaque(t, "rs_1")
	events := []aipapi.Event{
		{Kind: aipapi.EventReasoningDelta, Delta: "prior-chat"},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
		{Kind: aipapi.EventTextDelta, Delta: "ans"},
	}
	for _, ev := range events {
		if err := obs.Observe(context.Background(), ev); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-mixed"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 || len(snap[0].Reasoning) != 2 {
		t.Fatalf("uncorrelated prior Chat delta + exact part must store both segments, got %+v", snap)
	}
	chat := snap[0].Reasoning[0].Part.Reasoning
	exact := snap[0].Reasoning[1].Part.Reasoning
	if chat == nil || chat.Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 || chat.Text != "prior-chat" {
		t.Fatalf("prior Chat segment must be flushed, got %+v", chat)
	}
	if exact == nil || exact.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 || exact.Text != "" {
		t.Fatalf("exact Responses segment must remain exact-only, got %+v", exact)
	}
	if !bytes.Equal(exact.Opaque, opaque) {
		t.Fatal("exact opaque must be preserved")
	}
}

func TestStreamObserver_exactReasoningPart_orderAmongTextAndTools(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-order", nil)
	events := []aipapi.Event{
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_a")},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "n"},
		{Kind: aipapi.EventToolCallArgsDelta, Delta: `{}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "n"},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_b")},
		{Kind: aipapi.EventTextDelta, Delta: "bye"},
	}
	for _, ev := range events {
		if err := obs.Observe(context.Background(), ev); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-order"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 || len(snap[0].Reasoning) != 2 {
		t.Fatalf("want 2 exact parts, got %+v", snap)
	}
	if snap[0].Reasoning[0].BeforeNonReasoningPart != 0 {
		t.Fatalf("rs_a placement=%d want 0", snap[0].Reasoning[0].BeforeNonReasoningPart)
	}
	// text + tool = 2 non-reasoning parts before second reasoning
	if snap[0].Reasoning[1].BeforeNonReasoningPart != 2 {
		t.Fatalf("rs_b placement=%d want 2", snap[0].Reasoning[1].BeforeNonReasoningPart)
	}
	if !bytes.Contains(snap[0].Reasoning[0].Part.Reasoning.Opaque, []byte(`"rs_a"`)) ||
		!bytes.Contains(snap[0].Reasoning[1].Part.Reasoning.Opaque, []byte(`"rs_b"`)) {
		t.Fatal("emission order of multiple exact parts must be preserved")
	}
}

func TestStreamObserver_exactReasoningPart_deepCopyOpaqueBeforeAfterCommit(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-copy", nil)
	opaque := exactResponsesOpaque(t, "rs_1")
	part := &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  opaque,
	}
	if err := obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: part}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}); err != nil {
		t.Fatalf("Observe text: %v", err)
	}
	opaque[2] = 'X' // mutate caller-owned buffer before Finish/commit
	part.Opaque[4] = 'Y'
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-copy"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 || len(snap[0].Reasoning) != 1 {
		t.Fatalf("want 1 artifact, got %+v", snap)
	}
	stored := snap[0].Reasoning[0].Part.Reasoning.Opaque
	if bytes.Equal(stored, opaque) || bytes.Contains(stored, []byte("X")) {
		t.Fatal("store must keep defensive Opaque copy independent of caller mutation")
	}
	want := exactResponsesOpaque(t, "rs_1")
	if !bytes.Equal(stored, want) {
		t.Fatalf("stored opaque corrupted")
	}
	snap[0].Reasoning[0].Part.Reasoning.Opaque[0] = 'Z'
	snap2, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-copy"))
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	if snap2[0].Reasoning[0].Part.Reasoning.Opaque[0] == 'Z' {
		t.Fatal("Snapshot must return defensive Opaque copy")
	}
}

func TestStreamObserver_exactReasoningPart_successOnlyCommit(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	for i, outcome := range []response.StreamOutcome{
		response.OutcomeFailed,
		response.OutcomeCancelled,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	} {
		sess := fmt.Sprintf("sess-exact-fail-%d", i)
		obs := openExactObserver(t, cfg, store, sess, nil)
		_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")})
		_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
		if err := obs.Finish(context.Background(), outcome); err != nil {
			t.Fatalf("Finish %s: %v", outcome, err)
		}
		snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snap) != 0 {
			t.Fatalf("outcome %s must store nothing, got %d", outcome, len(snap))
		}
	}
}

func TestStreamObserver_exactReasoningPart_invalidIncompleteDiscarded(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	cases := []struct {
		name string
		part *aipapi.ReasoningPart
	}{
		{name: "nil", part: nil},
		{name: "empty", part: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1}},
		{name: "unnormalized", part: &aipapi.ReasoningPart{
			Dialect: " " + aipapi.ReasoningDialectOpenAIResponsesItemV1 + " ",
			Opaque:  mustOpaqueJSON(t, `{"id":"rs_1"}`),
		}},
		{name: "malformed_json", part: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{`),
		}},
		{name: "oversize", part: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`"` + strings.Repeat("S", aipapi.MaxReasoningOpaqueBytes) + `"`),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := "sess-exact-bad-" + tc.name
			obs := openExactObserver(t, cfg, store, sess, nil)
			if err := obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: tc.part}); err != nil {
				t.Fatalf("Observe must not panic/error: %v", err)
			}
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"})
			if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(snap) != 0 {
				t.Fatalf("invalid exact part must not become replayable, got %d", len(snap))
			}
		})
	}
}

func TestStreamObserver_exactReasoningPart_aggregateTurnBoundDiscards(t *testing.T) {
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
  max_reasoning_bytes_per_turn: 64
  max_session_bytes: 4096
`)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 64, MaxSessionBytes: 4096, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-exact-bound"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	big := mustOpaqueJSON(t, `{"id":"rs_1","pad":"`+strings.Repeat("x", 128)+`"}`)
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  big,
	}})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-bound"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatal("over-bound exact opaque must discard pending artifact")
	}
	assertNoSecretLeak(t, factory.LastSafeDiagnostic())
}

func TestRestoreMissingReasoning_exactResponsesPositionAndOpaque(t *testing.T) {
	t.Parallel()
	text := aipapi.TextPart("visible")
	tool := aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "n", Content: json.RawMessage(`{}`)}
	visible := []aipapi.Part{text, tool}
	anchor := anchorFor(t, visible...)
	opaque := exactResponsesOpaque(t, "rs_1")
	stored := aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  append(json.RawMessage(nil), opaque...),
		},
	}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-exact", anchor, placedReasoning(1, stored)),
	}
	call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(visible...)}}
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
	})
	if !got.Mutated || got.RestoredCount != 1 {
		t.Fatalf("expected exact restore, got=%+v", got)
	}
	if got.RestoredBytes != aipapi.ReasoningPayloadBytes(stored.Reasoning) {
		t.Fatalf("restored bytes = %d, want %d", got.RestoredBytes, aipapi.ReasoningPayloadBytes(stored.Reasoning))
	}
	parts := call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts=%d want 3", len(parts))
	}
	if parts[0].Kind != aipapi.PartText || parts[1].Kind != aipapi.PartReasoning || parts[2].Kind != aipapi.PartJSON {
		t.Fatalf("exact part must inject at recorded position among neighbors: %+v", parts)
	}
	if !bytes.Equal(parts[1].Reasoning.Opaque, opaque) {
		t.Fatal("restored opaque must match stored bytes")
	}
}

func TestRestoreMissingReasoning_exactDialectMismatchPolicies(t *testing.T) {
	t.Parallel()
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor := anchorFor(t, visible...)
	stored := aipapi.Part{
		Kind:      aipapi.PartReasoning,
		Reasoning: exactResponsesPart(t, "rs_1"),
	}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-exact", anchor, placedReasoning(0, stored)),
	}
	t.Run("reject", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(visible...)}}
		before := cloneCall(t, call)
		got := restoreMissing(t, reasoningpreservation.RestoreInput{
			Action:            reasoningpreservation.ActionRestore,
			OnUnrepresentable: reasoningpreservation.PolicyReject,
			Call:              &call,
			Artifacts:         artifacts,
			Eligible:          true,
			ReplaySupport:     aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		})
		if got.Mutated || !got.Exclude {
			t.Fatalf("reject mismatch want exclude no mutate, got=%+v", got)
		}
		if !bytes.Equal([]byte(before.Messages[0].Parts[0].Text), []byte(call.Messages[0].Parts[0].Text)) {
			t.Fatal("no partial submit")
		}
		assertNoSecretLeak(t, got.ReasonCode)
	})
	t.Run("log_skip", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(visible...)}}
		got := restoreMissing(t, reasoningpreservation.RestoreInput{
			Action:            reasoningpreservation.ActionRestore,
			OnUnrepresentable: reasoningpreservation.PolicyLogSkip,
			Call:              &call,
			Artifacts:         artifacts,
			Eligible:          true,
			ReplaySupport:     aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		})
		if got.Mutated || got.Exclude || got.RestoredCount != 0 {
			t.Fatalf("log_skip mismatch must continue without restore, got=%+v", got)
		}
	})
}

func TestRestoreMissingReasoning_invalidStoredExactEnvelope_stateError(t *testing.T) {
	t.Parallel()
	visible := []aipapi.Part{aipapi.TextPart("visible")}
	anchor := anchorFor(t, visible...)
	bad := aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{not-json`),
		},
	}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-bad-exact", anchor, placedReasoning(0, bad)),
	}
	for _, policy := range []string{reasoningpreservation.PolicyReject, reasoningpreservation.PolicyLogSkip} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(visible...)}}
			before := cloneCall(t, call)
			got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
				Action:        reasoningpreservation.ActionRestore,
				OnStateError:  policy,
				Call:          &call,
				Artifacts:     artifacts,
				Eligible:      true,
				ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
			})
			if err != nil {
				t.Fatalf("RestoreMissingReasoning: %v", err)
			}
			if got.Mutated || got.RestoredCount != 0 {
				t.Fatalf("invalid stored exact must not restore, got=%+v", got)
			}
			if got.ReasonCode != "state_error" {
				t.Fatalf("want state_error, got=%+v", got)
			}
			if policy == reasoningpreservation.PolicyReject && !got.Exclude {
				t.Fatal("reject must exclude")
			}
			if policy == reasoningpreservation.PolicyLogSkip && got.Exclude {
				t.Fatal("log_skip must not exclude")
			}
			if len(call.Messages[0].Parts) != len(before.Messages[0].Parts) {
				t.Fatal("no partial submit on state_error")
			}
			assertNoSecretLeak(t, got.ReasonCode)
			for _, o := range got.Outcomes {
				assertNoSecretLeak(t, string(o))
			}
		})
	}
}

func TestStreamObserver_exactReasoningPart_idempotentSuccessReleased(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-idem", nil)
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish1: %v", err)
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish2: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-idem"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("idempotent Finish must append once, got %d", len(snap))
	}
}

func TestStreamObserver_exactReasoningPart_successOnly_nonVacuous(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	failObs := openExactObserver(t, cfg, store, "sess-exact-fail-then-ok", nil)
	_ = failObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")})
	_ = failObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err := failObs.Finish(context.Background(), response.OutcomeFailed); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	okObs := openExactObserver(t, cfg, store, "sess-exact-fail-then-ok", nil)
	_ = okObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")})
	_ = okObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err := okObs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish success: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-fail-then-ok"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("failed attempt stores nothing; success stores one, got %d", len(snap))
	}
}

func TestStreamObserver_exactReasoningPart_encryptedPresenceRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	cases := []struct {
		name string
		raw  string
	}{
		{name: "absent", raw: `{"id":"rs_1","type":"reasoning","summary":[]}`},
		{name: "null", raw: `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null}`},
		{name: "value", raw: `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc-bytes"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryStore(t, exactStoreOptions(time.Now))
			sess := "sess-exact-enc-" + tc.name
			obs := openExactObserver(t, cfg, store, sess, nil)
			opaque := mustOpaqueJSON(t, tc.raw)
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
				Opaque:  opaque,
			}})
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"})
			if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(snap) != 1 || len(snap[0].Reasoning) != 1 {
				t.Fatalf("want 1 exact artifact, got %+v", snap)
			}
			if !bytes.Equal(snap[0].Reasoning[0].Part.Reasoning.Opaque, opaque) {
				t.Fatalf("capture/store must keep bytes-equivalent opaque for %s", tc.name)
			}
			call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(aipapi.TextPart("visible"))}}
			got := restoreMissing(t, reasoningpreservation.RestoreInput{
				Action:        reasoningpreservation.ActionRestore,
				Call:          &call,
				Artifacts:     snap,
				Eligible:      true,
				ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
			})
			if !got.Mutated || got.RestoredCount != 1 {
				t.Fatalf("restore: %+v", got)
			}
			if !bytes.Equal(call.Messages[0].Parts[0].Reasoning.Opaque, opaque) {
				t.Fatalf("restore must keep bytes-equivalent opaque for %s", tc.name)
			}
		})
	}
}

func TestStreamObserver_exactReasoningPart_providerSchemaNotFeatureValidated(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-exact-nonschema", nil)
	opaque := mustOpaqueJSON(t, `{"note":"missing-id-and-summary-is-adapter-owned"}`)
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  opaque,
	}})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-exact-nonschema"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("canonical layer accepts opaque JSON without provider schema fields, got %d", len(snap))
	}
	if !bytes.Equal(snap[0].Reasoning[0].Part.Reasoning.Opaque, opaque) {
		t.Fatal("opaque must round-trip unchanged")
	}
}

func TestStreamObserver_exactReasoningPart_sessionIsolation(t *testing.T) {
	t.Parallel()
	cfg := observeExactConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	for _, sess := range []string{"sess-a", "sess-b"} {
		obs := openExactObserver(t, cfg, store, sess, nil)
		_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, sess)})
		_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
		if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
			t.Fatalf("Finish %s: %v", sess, err)
		}
	}
	a, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-a"))
	if err != nil {
		t.Fatalf("Snapshot a: %v", err)
	}
	b, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-b"))
	if err != nil {
		t.Fatalf("Snapshot b: %v", err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("each session must isolate artifacts a=%d b=%d", len(a), len(b))
	}
	if bytes.Contains(a[0].Reasoning[0].Part.Reasoning.Opaque, []byte("sess-b")) ||
		bytes.Contains(b[0].Reasoning[0].Part.Reasoning.Opaque, []byte("sess-a")) {
		t.Fatal("session opaque contents must not cross partitions")
	}
}

func TestRestoreMissingReasoning_exactMultipleLayout(t *testing.T) {
	t.Parallel()
	visible := []aipapi.Part{aipapi.TextPart("mid"), {Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "n", Content: json.RawMessage(`{}`)}}
	anchor := anchorFor(t, visible...)
	ra := aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: exactResponsesPart(t, "rs_a")}
	rb := aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: exactResponsesPart(t, "rs_b")}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-multi", anchor, placedReasoning(0, ra), placedReasoning(2, rb)),
	}
	call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(visible...)}}
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
	})
	if !got.Mutated {
		t.Fatalf("expected restore, got=%+v", got)
	}
	parts := call.Messages[0].Parts
	if len(parts) != 4 {
		t.Fatalf("parts=%d want 4 layout [rs_a,text,tool,rs_b]", len(parts))
	}
	if parts[0].Kind != aipapi.PartReasoning || parts[1].Kind != aipapi.PartText ||
		parts[2].Kind != aipapi.PartJSON || parts[3].Kind != aipapi.PartReasoning {
		t.Fatalf("unexpected layout: %+v", parts)
	}
	if !bytes.Contains(parts[0].Reasoning.Opaque, []byte(`"rs_a"`)) || !bytes.Contains(parts[3].Reasoning.Opaque, []byte(`"rs_b"`)) {
		t.Fatal("exact opaque order/layout mismatch")
	}
}
