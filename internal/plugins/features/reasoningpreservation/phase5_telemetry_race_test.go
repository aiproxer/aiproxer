package reasoningpreservation_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestPhase5_telemetryRecordsClassifyOutcomes(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase5RestoreYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	tel := reasoningpreservation.NewTelemetry()
	xform := reasoningpreservation.NewAttemptTransform(cfg, store, tel)

	missingCall, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-tel"), arts[0]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	meta := request.AttemptMeta{
		BackendID: "be", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: "sess-tel"},
	}
	if _, err := xform.HandleAttempt(context.Background(), &missingCall, meta, request.Services{}); err != nil {
		t.Fatalf("restore missing: %v", err)
	}

	preservedCall, _ := missingRestoreFixture(t)
	preservedCall.Messages[0].Parts = append([]aipapi.Part{
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil),
	}, preservedCall.Messages[0].Parts...)
	if _, err := xform.HandleAttempt(context.Background(), &preservedCall, meta, request.Services{}); err != nil {
		t.Fatalf("preserved: %v", err)
	}

	conflictCall, _ := missingRestoreFixture(t)
	conflictCall.Messages[0].Parts = append([]aipapi.Part{
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "other-thought", "", nil),
	}, conflictCall.Messages[0].Parts...)
	if _, err := xform.HandleAttempt(context.Background(), &conflictCall, meta, request.Services{}); err != nil {
		t.Fatalf("conflict: %v", err)
	}

	unmatchedCall := aipapi.Call{Messages: []aipapi.Message{assistantMsg(aipapi.TextPart("totally different"))}}
	if _, err := xform.HandleAttempt(context.Background(), &unmatchedCall, meta, request.Services{}); err != nil {
		t.Fatalf("unmatched: %v", err)
	}

	dupArt := arts[0]
	dupArt.ID = "art-2"
	dupArt.CreatedAt = time.Time{}
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-amb"), dupArt); err != nil {
		t.Fatalf("Append amb1: %v", err)
	}
	dupArt.ID = "art-3"
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("sess-amb"), dupArt); err != nil {
		t.Fatalf("Append amb2: %v", err)
	}
	ambCall, _ := missingRestoreFixture(t)
	ambMeta := meta
	ambMeta.Session.AuthoritativeSessionID = "sess-amb"
	if _, err := xform.HandleAttempt(context.Background(), &ambCall, ambMeta, request.Services{}); err != nil {
		t.Fatalf("ambiguous: %v", err)
	}

	unrepCall, _ := missingRestoreFixture(t)
	unrepMeta := meta
	unrepMeta.ReplaySupport = aipapi.ReasoningReplaySupport{}
	if _, err := xform.HandleAttempt(context.Background(), &unrepCall, unrepMeta, request.Services{}); err != nil {
		t.Fatalf("unrepresentable: %v", err)
	}

	// Observe classify outcomes require eligibility; unmatched candidates stay inert.
	observeCfg := decodeValidConfig(t, `
action: observe
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
`)
	obsXform := reasoningpreservation.NewAttemptTransform(observeCfg, store, tel)
	obsCall, _ := missingRestoreFixture(t)
	if _, err := obsXform.HandleAttempt(context.Background(), &obsCall, meta, request.Services{}); err != nil {
		t.Fatalf("observe classify missing: %v", err)
	}

	stateXform := reasoningpreservation.NewAttemptTransform(cfg, failingSnapshotStore{}, tel)
	stateCall, _ := missingRestoreFixture(t)
	if _, err := stateXform.HandleAttempt(context.Background(), &stateCall, meta, request.Services{}); err != nil {
		t.Fatalf("state_error: %v", err)
	}

	snap := tel.Snapshot()
	for _, want := range []reasoningpreservation.SafeOutcome{
		reasoningpreservation.OutcomeRestored,
		reasoningpreservation.OutcomePreserved,
		reasoningpreservation.OutcomeConflicting,
		reasoningpreservation.OutcomeUnmatched,
		reasoningpreservation.OutcomeAmbiguous,
		reasoningpreservation.OutcomeUnrepresentable,
		reasoningpreservation.OutcomeMissing,
		reasoningpreservation.OutcomeStateError,
	} {
		if snap[want] == 0 {
			t.Fatalf("telemetry missing outcome %s in %v", want, snap)
		}
	}
}

func TestPhase5_telemetryRecordsObservedOversizeEvicted(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, `
action: observe
use_builtin_catalog: false
rules:
  - id: test-be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 32
  max_session_bytes: 4096
`)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 1, MaxReasoningBytesPerTurn: 32, MaxSessionBytes: 4096, Now: time.Now,
	})
	tel := reasoningpreservation.NewTelemetry()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)

	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "tiny"})
	_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a1"})
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish1: %v", err)
	}

	obs2, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-obs"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open2: %v", err)
	}
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "tiny2"})
	_ = obs2.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a2"})
	if err := obs2.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish2: %v", err)
	}

	obs3, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be", Model: "m",
		Session: session.SessionView{AuthoritativeSessionID: "sess-over"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open3: %v", err)
	}
	_ = obs3.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: strings.Repeat("R", 64)})
	_ = obs3.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"})
	if err := obs3.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish3: %v", err)
	}

	snap := tel.Snapshot()
	for _, want := range []reasoningpreservation.SafeOutcome{
		reasoningpreservation.OutcomeObserved,
		reasoningpreservation.OutcomeEvicted,
		reasoningpreservation.OutcomeOversize,
	} {
		if snap[want] == 0 {
			t.Fatalf("telemetry missing outcome %s in %v", want, snap)
		}
	}
}

func TestPhase5_opaqueAliasingRace(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 32, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 1 << 20, Now: now,
	})
	partition := reasoningpreservation.NewSessionPartition("sess-opaque-race")
	// Caller-owned artifacts must be goroutine-local. A shared shallow copy of TurnArtifact
	// aliases nested Reasoning/Opaque pointers across workers and creates a false store race.
	const workers = 64
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			art := sampleArtifact(fmt.Sprintf("opaque-%d", i), "thought", 64)
			art.CreatedAt = time.Time{}
			opaque := mustOpaqueJSON(t, `{"k":"v"}`)
			art.Reasoning[0].Part.Reasoning.Opaque = opaque
			if _, err := store.Append(context.Background(), partition, art); err != nil {
				errCh <- fmt.Errorf("Append %d: %w", i, err)
				return
			}
			// Mutate caller-owned bytes/fields after Append; store must retain defensive copies.
			opaque[2] = 'X'
			art.Reasoning[0].Part.Reasoning.Text = "mutated-caller"
			snap, err := store.Snapshot(context.Background(), partition)
			if err != nil {
				errCh <- fmt.Errorf("Snapshot %d: %w", i, err)
				return
			}
			if len(snap) == 0 {
				return
			}
			if len(snap[0].Reasoning) > 0 && snap[0].Reasoning[0].Part.Reasoning != nil {
				snap[0].Reasoning[0].Part.Reasoning.Opaque = mustOpaqueJSON(t, `{"k":"mutated-snap"}`)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	snap, err := store.Snapshot(context.Background(), partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, art := range snap {
		if len(art.Reasoning) == 0 || art.Reasoning[0].Part.Reasoning == nil {
			continue
		}
		got := string(art.Reasoning[0].Part.Reasoning.Opaque)
		if got == `{"k":"mutated-snap"}` {
			t.Fatal("opaque aliasing: snapshot mutation leaked into store")
		}
		if got != `{"k":"v"}` {
			t.Fatalf("opaque aliasing: caller opaque mutation leaked into store, got %q", got)
		}
		if art.Reasoning[0].Part.Reasoning.Text == "mutated-caller" {
			t.Fatal("text aliasing: caller mutation leaked into store")
		}
	}
}

type failingSnapshotStore struct{}

func (failingSnapshotStore) Append(context.Context, reasoningpreservation.SessionPartition, reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, nil
}

func (failingSnapshotStore) Snapshot(context.Context, reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return nil, errors.New("snapshot boom")
}

func (failingSnapshotStore) Delete(context.Context, reasoningpreservation.SessionPartition, ...string) error {
	return nil
}

func TestPhase5_disabledConfigHasNoParticipantsUntilConstructed(t *testing.T) {
	t.Parallel()
	inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
	if inv.Enabled || len(inv.AggregateCounters) != 0 {
		t.Fatalf("disabled inventory=%+v", inv)
	}
	parts, bundle, err := reasoningpreservation.FeatureBundleWithParts(decodeValidConfig(t, phase5RestoreYAML))
	if err != nil {
		t.Fatal(err)
	}
	if parts.Store == nil || parts.Telemetry == nil || len(feature.Get(bundle.PlaneSet, feature.PlaneAttemptTransforms)) == 0 || len(feature.Get(bundle.PlaneSet, feature.PlaneStreamObserverFactories)) == 0 {
		t.Fatal("enabled construction must expose store/telemetry/participants")
	}
}
