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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 1.2 — Freeze surfaced-winner/original-first lifecycle (Requirement 4).
// Compression config/store/submission does not exist yet; these tests freeze
// the lifecycle contracts the future optional lane will rely on.

func lifecycleConfig(t *testing.T) reasoningpreservation.Config {
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

// TestLifecycle_onlySuccessReleasedCommits proves only success_released reaches
// the commit path. Every other StreamOutcome yields no stored artifact (AC3, AC4).
func TestLifecycle_onlySuccessReleasedCommits(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	cases := []struct {
		name       string
		outcome    response.StreamOutcome
		wantStored bool
	}{
		{"success_released", response.OutcomeSuccessReleased, true},
		{"failed", response.OutcomeFailed, false},
		{"cancelled", response.OutcomeCancelled, false},
		{"closed", response.OutcomeClosed, false},
		{"replaced", response.OutcomeReplaced, false},
		{"gate_replaced", response.OutcomeGateReplaced, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryStore(t, exactStoreOptions(time.Now))
			sess := "sess-lifecycle-" + tc.name
			obs := openExactObserver(t, cfg, store, sess, nil)
			require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
			require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"}))
			require.NoError(t, obs.Finish(context.Background(), tc.outcome))
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
			require.NoError(t, err)
			if tc.wantStored {
				assert.Len(t, snap, 1, "success_released must append exactly one original artifact")
				assert.Len(t, snap[0].Reasoning, 1)
			} else {
				assert.Empty(t, snap, "outcome %q must not store artifact and must not allow future compression lane", tc.outcome)
			}
		})
	}
}

// TestLifecycle_nonSuccessOutcomesNeverCommitEvenWithTelemetry proves swallowed
// retries, race losers and gate-discarded semantics: non-success outcomes produce
// no artifact regardless of attempt sequence (AC4). Uses per-attempt session partitions.
func TestLifecycle_nonSuccessOutcomesNeverCommitEvenWithTelemetry(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	nonSuccess := []response.StreamOutcome{
		response.OutcomeFailed,
		response.OutcomeCancelled,
		response.OutcomeClosed,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	}
	for _, outcome := range nonSuccess {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			store := newMemoryStore(t, exactStoreOptions(time.Now))
			tel := reasoningpreservation.NewTelemetry()
			sess := "sess-race-" + string(outcome)
			// Loser attempt.
			loser := openExactObserverWithTelemetry(t, cfg, store, sess, tel)
			require.NoError(t, loser.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "loser-thought"}))
			require.NoError(t, loser.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "loser-ans"}))
			require.NoError(t, loser.Finish(context.Background(), outcome))
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
			require.NoError(t, err)
			assert.Empty(t, snap, "parallel loser/swallowed retry with outcome %q must not produce original", outcome)
		})
	}
}

// TestLifecycle_failedThenSuccessOnSamePartition proves a swallowed failure
// does not poison a later surfaced winner on the same authoritative partition (AC4 boundary).
func TestLifecycle_failedThenSuccessOnSamePartition(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	sess := "sess-fail-then-success"
	failObs := openExactObserver(t, cfg, store, sess, nil)
	require.NoError(t, failObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_fail")}))
	require.NoError(t, failObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}))
	require.NoError(t, failObs.Finish(context.Background(), response.OutcomeFailed))
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Empty(t, snap, "failed attempt must leave no original")

	okObs := openExactObserver(t, cfg, store, sess, nil)
	require.NoError(t, okObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_ok")}))
	require.NoError(t, okObs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "y"}))
	require.NoError(t, okObs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1, "surfaced success after swallowed retry must append original")
}

// TestLifecycle_ineligibleOversizeAndStateErrorProduceNoArtifact covers AC3
// remaining guards: ineligible model, oversized reasoning, empty authoritative session.
func TestLifecycle_ineligibleOversizeAndStateErrorProduceNoArtifact(t *testing.T) {
	t.Parallel()
	t.Run("ineligible_unmatched_model", func(t *testing.T) {
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
			Session:         session.SessionView{AuthoritativeSessionID: "sess-inelig"},
		}, response.Services{})
		require.NoError(t, err)
		// Ineligible -> inert observer (no store I/O path to future compression).
		assert.True(t, reasoningpreservation.StreamObserverIsInert(obs))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thought"}))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
		snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-inelig"))
		require.NoError(t, err)
		assert.Empty(t, snap)
		assert.Empty(t, tel.Snapshot(), "ineligible capture must not record outcome or enable compression")
	})

	t.Run("oversized", func(t *testing.T) {
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
		obs := openExactObserver(t, cfg, store, "sess-oversize", nil)
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "1234567890"}))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
		snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-oversize"))
		require.NoError(t, err)
		assert.Empty(t, snap, "oversized reasoning must discard pending and not reach store/compression")
	})

	t.Run("empty_authoritative_session_state_miss", func(t *testing.T) {
		t.Parallel()
		cfg := lifecycleConfig(t)
		store := newMemoryStore(t, exactStoreOptions(time.Now))
		obs := openExactObserver(t, cfg, store, "", nil)
		// open uses "" authoritative session; Finish should treat as state miss
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
		emptySnap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(""))
		require.NoError(t, err)
		assert.Empty(t, emptySnap, "empty authoritative session must not capture under shared empty key")
	})
}

// TestLifecycle_originalAppendOccursInsideFinishBeforeReturn freezes ordering:
// on success_released the original append happens inside/before Finish returns (AC1, AC2).
func TestLifecycle_originalAppendOccursInsideFinishBeforeReturn(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	sess := "sess-order-check"
	obs := openExactObserver(t, cfg, store, sess, nil)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"}))
	// Snapshot before Finish must be empty; after Finish must be present synchronously.
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Empty(t, snap)
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1, "original must be appended inside Finish before it returns")
	// Defensive: second Finish is idempotent and does not append another revision.
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap2, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	assert.Len(t, snap2, 1, "idempotent Finish must not create duplicate originals or second reservation")
}

// appendFailingStore fails Append to prove append failure leaves zero artifact and no compression lane (AC3).
type appendFailingStore struct {
	reasoningpreservation.TurnStore
	err   error
	calls int
}

func (f *appendFailingStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	f.calls++
	return reasoningpreservation.EvictionSummary{}, f.err
}

func (f *appendFailingStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return f.TurnStore.Snapshot(ctx, p)
}

func (f *appendFailingStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	return f.TurnStore.Delete(ctx, p, ids...)
}

// TestLifecycle_appendFailureLeavesZeroStateAndNoCompression proves AC3 ordering guard.
func TestLifecycle_appendFailureLeavesZeroStateAndNoCompression(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	base := newMemoryStore(t, exactStoreOptions(time.Now))
	store := &appendFailingStore{TurnStore: base, err: errors.New("injected append failure")}
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	obs, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-append-fail"},
	}, response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}))
	// Finish is fail-open per observer contract; it must not return error.
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	assert.Equal(t, 1, store.calls, "Finish must have attempted original append")
	snap, err := base.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-append-fail"))
	require.NoError(t, err)
	assert.Empty(t, snap, "append failure must leave zero artifact state and no reservation/compression work")
	assert.Contains(t, strings.ToLower(factory.LastSafeDiagnostic()), "state_error")
}

// precedenceStore records append order vs Finish return.
type precedenceStore struct {
	reasoningpreservation.TurnStore
	mu         sync.Mutex
	appendSeq  int
	finishDone bool
	orderOk    bool
}

func newPrecedenceStore(t *testing.T) *precedenceStore {
	t.Helper()
	base := newMemoryStore(t, exactStoreOptions(time.Now))
	return &precedenceStore{TurnStore: base}
}

func (c *precedenceStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finishDone {
		c.orderOk = false
	}
	c.appendSeq++
	c.orderOk = true
	return c.TurnStore.Append(ctx, p, a)
}

func (c *precedenceStore) markFinishDone() {
	c.mu.Lock()
	c.finishDone = true
	c.mu.Unlock()
}

// TestLifecycle_appendPrecedesAnyFutureCompression proves original append precedes
// any future optional lane; captured by asserting Append occurs inside Finish (AC1, AC2).
func TestLifecycle_appendPrecedesAnyFutureCompression(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newPrecedenceStore(t)
	obs, err := reasoningpreservation.NewStreamObserverFactory(cfg, store).Open(context.Background(), response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-precedence"},
	}, response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "y"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	store.markFinishDone()
	// Append must have occurred before markFinishDone.
	store.mu.Lock()
	ok := store.orderOk && store.appendSeq == 1
	store.mu.Unlock()
	assert.True(t, ok, "original append must precede any post-Finish reservation/compression step")
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-precedence"))
	require.NoError(t, err)
	require.Len(t, snap, 1)
}

// TestLifecycle_committedOriginalImmutable proves compressor failure cannot delete
// or invalidate a committed original via public store API (AC5). Covers unknown-ID
// delete, Snapshot mutate, and stale clear patterns.
func TestLifecycle_committedOriginalImmutable(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	sess := "sess-immutable"
	obs := openExactObserver(t, cfg, store, sess, nil)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	originalID := snap[0].ID
	originalAnchor := snap[0].Anchor

	// Unknown-ID delete must not remove committed original (mimics failed adoption cleanup).
	require.NoError(t, store.Delete(context.Background(), reasoningpreservation.NewSessionPartition(sess), "unknown-does-not-exist"))
	snap, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	assert.Equal(t, originalID, snap[0].ID)
	assert.Equal(t, originalAnchor, snap[0].Anchor)

	// Snapshot+mutate defensive copy: mutating returned slice must not rewrite store.
	snap[0].ID = "mutated"
	snap[0].Reasoning[0].Part.Reasoning.Opaque[0] = 'X'
	snap[0].ReasoningBytes = 99999
	snap2, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap2, 1)
	assert.Equal(t, originalID, snap2[0].ID, "Snapshot must return defensive copies")
	assert.Equal(t, originalAnchor, snap2[0].Anchor)
	assert.NotEqual(t, byte('X'), snap2[0].Reasoning[0].Part.Reasoning.Opaque[0])

	// Stale clear: delete with empty ids is no-op.
	require.NoError(t, store.Delete(context.Background(), reasoningpreservation.NewSessionPartition(sess)))
	snap3, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap3, 1)

	// Mix of known+unknown: deleting only unknown must keep original.
	require.NoError(t, store.Delete(context.Background(), reasoningpreservation.NewSessionPartition(sess), "also-unknown"))
	snap4, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap4, 1)
}

// TestLifecycle_knownDeleteRemovesOnlyTargetedArtifact proves Delete authority is
// scoped: deleting the committed artifact removes it but does not corrupt sibling session.
func TestLifecycle_knownDeleteRemovesOnlyTargetedArtifact(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	sessA := "sess-del-a"
	sessB := "sess-del-b"
	for _, sess := range []string{sessA, sessB} {
		obs := openExactObserver(t, cfg, store, sess, nil)
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	}
	snapA, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sessA))
	require.NoError(t, err)
	require.Len(t, snapA, 1)
	require.NoError(t, store.Delete(context.Background(), reasoningpreservation.NewSessionPartition(sessA), snapA[0].ID))
	snapA, err = store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sessA))
	require.NoError(t, err)
	assert.Empty(t, snapA, "explicit targeted delete must remove only that partition's artifact")
	snapB, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sessB))
	require.NoError(t, err)
	assert.Len(t, snapB, 1, "sibling partition must remain intact")
}

// TestLifecycle_finishIsNonBlocking proves observer does not synchronously wait
// for remote semantic compression before completing final-stream lifecycle (AC6).
// Today Finish performs only local store work; verified by time-bound completion.
func TestLifecycle_finishIsNonBlocking(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-nonblock", nil)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	start := time.Now()
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 500*time.Millisecond, "Finish must complete synchronously without waiting for remote compression")
	// Second rapid Finish must also be instant (idempotent).
	start = time.Now()
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

// TestLifecycle_contextCancellationPreservesNoPartialState proves Append respects
// context cancellation and leaves zero artifact when ctx is cancelled before Finish store I/O.
func TestLifecycle_contextCancellationPreservesNoPartialState(t *testing.T) {
	t.Parallel()
	cfg := lifecycleConfig(t)
	store := newMemoryStore(t, exactStoreOptions(time.Now))
	obs := openExactObserver(t, cfg, store, "sess-cancel-ctx", nil)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: exactResponsesPart(t, "rs_1")}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Observer records state_error internally but remains fail-open.
	require.NoError(t, obs.Finish(ctx, response.OutcomeSuccessReleased))
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-cancel-ctx"))
	require.NoError(t, err)
	assert.Empty(t, snap, "cancelled context must not leave partial committed artifact")
}

func openExactObserverWithTelemetry(t *testing.T, cfg reasoningpreservation.Config, store reasoningpreservation.TurnStore, sessionID string, tel *reasoningpreservation.Telemetry) response.StreamObserver {
	t.Helper()
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store, tel)
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
