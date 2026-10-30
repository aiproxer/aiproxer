package reasoningpreservation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

const phase5RestoreYAML = `
action: restore
use_builtin_catalog: false
rules:
  - id: test-be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 32768
`

func TestPhase5_clientHintSpoofCannotAccessPartition(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	call, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-real"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	before := cloneCall(t, call)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session: session.SessionView{
			AuthoritativeSessionID: "",
			ClientSessionHint:      "auth-real",
		},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue {
		t.Fatalf("dec=%+v", dec)
	}
	if !reflectDeepEqualMessages(before.Messages, call.Messages) {
		t.Fatal("client-hint spoof must not restore another partition")
	}
}

func TestPhase5_authoritativePartitionRestoreAndCrossSessionIsolation(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	call, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-a"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: "sess-a"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptContinue {
		t.Fatalf("dec=%+v", dec)
	}
	if !callHasReasoning(call) {
		t.Fatal("authoritative partition must restore")
	}

	call2, _ := missingRestoreFixture(t)
	before2 := cloneCall(t, call2)
	dec2, err := xform.HandleAttempt(context.Background(), &call2, request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: "sess-b"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt2: %v", err)
	}
	if dec2.Kind != request.AttemptContinue {
		t.Fatalf("dec2=%+v", dec2)
	}
	if !reflectDeepEqualMessages(before2.Messages, call2.Messages) {
		t.Fatal("cross-session restore must miss")
	}
}

func TestPhase5_restartNewStoreIsStateMiss(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store1 := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	call, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store1.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-restart"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	store2 := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	xform := reasoningpreservation.NewAttemptTransform(cfg, store2)
	before := cloneCall(t, call)
	_, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: "sess-restart"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if !reflectDeepEqualMessages(before.Messages, call.Messages) {
		t.Fatal("restart/new process store must treat same session id as state miss")
	}
}

func TestPhase5_TTLEvictionClearsPayloads(t *testing.T) {
	t.Parallel()
	now, advance := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL:                time.Minute,
		MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: now,
	})
	_, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-ttl"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	advance(2 * time.Minute)
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-ttl"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("TTL must clear reachable payloads, got %d", len(snap))
	}
}

func TestPhase5_crossPluginInstanceIsolation(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	partsA, _, err := reasoningpreservation.FeatureBundleWithParts(cfg)
	if err != nil {
		t.Fatalf("partsA: %v", err)
	}
	partsB, _, err := reasoningpreservation.FeatureBundleWithParts(cfg)
	if err != nil {
		t.Fatalf("partsB: %v", err)
	}
	_, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := partsA.Store.Append(context.Background(), reasoningpreservation.NewSessionPartition("shared-id"), arts[0]); err != nil {
		t.Fatalf("Append A: %v", err)
	}
	snapB, err := partsB.Store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("shared-id"))
	if err != nil {
		t.Fatalf("Snapshot B: %v", err)
	}
	if len(snapB) != 0 {
		t.Fatal("feature-plugin instances must not share store state")
	}
}

func TestPhase5_safeInventoryNoSensitiveFields(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	parts, _, err := reasoningpreservation.FeatureBundleWithParts(cfg)
	if err != nil {
		t.Fatalf("FeatureBundleWithParts: %v", err)
	}
	parts.Telemetry.Record(reasoningpreservation.OutcomeObserved, map[string]int{"count": 1, "bytes": 12})
	parts.Telemetry.Record(reasoningpreservation.OutcomeRestored, map[string]int{"restored": 1})
	inv := parts.Inventory()
	if !inv.Enabled || !inv.ProcessLocal || inv.Action != reasoningpreservation.ActionRestore {
		t.Fatalf("inventory=%+v", inv)
	}
	if inv.RuleCount != 1 || len(inv.RuleIDs) != 1 || inv.RuleIDs[0] != "test-be" {
		t.Fatalf("rules=%+v", inv)
	}
	if inv.AggregateCounters["observed"] != 1 || inv.AggregateCounters["restored"] != 1 {
		t.Fatalf("aggregates=%v", inv.AggregateCounters)
	}
	var b strings.Builder
	b.WriteString(inv.Action)
	b.WriteString(inv.CatalogVersion)
	b.WriteString(strings.Join(inv.RuleIDs, ","))
	b.WriteString(inv.TTL)
	for k, v := range inv.AggregateCounters {
		b.WriteString(k)
		b.WriteRune(rune(v))
	}
	blob := b.String()
	for _, needle := range []string{"auth-real", "chain-of-thought", "signature", "anchor", "opaque"} {
		if strings.Contains(blob, needle) {
			t.Fatalf("inventory leaked %q", needle)
		}
	}
}

func TestPhase5_observerStoreFailurePreservesFinish(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, failingAppendStore{})
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-fail-store"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish must fail-open: %v", err)
	}
	if !strings.Contains(factory.LastSafeDiagnostic(), "state_error") {
		t.Fatalf("expected state_error diagnostic, got %q", factory.LastSafeDiagnostic())
	}
}

func TestPhase5_cancelCloseGateReplaceNeverCommit(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	for _, outcome := range []response.StreamOutcome{
		response.OutcomeCancelled,
		response.OutcomeClosed,
		response.OutcomeFailed,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			obs, err := factory.Open(context.Background(), response.StreamMeta{
				BackendID: "be", Model: "m",
				Session: session.SessionView{AuthoritativeSessionID: "sess-" + string(outcome)},
			}, response.Services{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"})
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"})
			if err := obs.Finish(context.Background(), outcome); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-"+string(outcome)))
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(snap) != 0 {
				t.Fatalf("%s must discard pending artifact", outcome)
			}
		})
	}
}

func TestPhase5_dialectIneligibleExcludes(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	call, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-dialect"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	xform := reasoningpreservation.NewAttemptTransform(cfg, store)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{},
		Session:       session.SessionView{AuthoritativeSessionID: "sess-dialect"},
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if dec.Kind != request.AttemptExcludeCandidate || dec.ReasonCode != "unrepresentable_replay" {
		t.Fatalf("want exclude unrepresentable_replay, got %+v", dec)
	}
}

func TestPhase5_disabledMeansNoParticipants(t *testing.T) {
	t.Parallel()
	inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
	if inv.Enabled || inv.ProcessLocal || len(inv.AggregateCounters) != 0 {
		t.Fatalf("absent/disabled inventory must be zero posture, got %+v", inv)
	}
	tel := (*reasoningpreservation.Telemetry)(nil)
	tel.Record(reasoningpreservation.OutcomeObserved, nil)
	if len(tel.Snapshot()) != 0 {
		t.Fatal("nil telemetry must no-op")
	}
}

func TestPhase5_concurrentStoreRace(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 1 << 20, Now: now,
	})
	_, arts := missingRestoreFixture(t)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			art := arts[0]
			art.CreatedAt = time.Time{}
			art.ID = "race-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
			_, _ = store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-race"), art)
			_, _ = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-race"))
		}(i)
	}
	wg.Wait()
}

type failingAppendStore struct{}

func (failingAppendStore) Append(context.Context, reasoningpreservation.SessionPartition, reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, errors.New("append boom")
}

func (failingAppendStore) Snapshot(context.Context, reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return nil, nil
}

func (failingAppendStore) Delete(context.Context, reasoningpreservation.SessionPartition, ...string) error {
	return nil
}

func callHasReasoning(call aipapi.Call) bool {
	for _, m := range call.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil && p.Reasoning.Text != "" {
				return true
			}
		}
	}
	return false
}
