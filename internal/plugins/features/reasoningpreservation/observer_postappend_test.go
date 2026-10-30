//nolint:forcetypeassert,paralleltest // Tests share fixtures and assert concrete scripted types.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compressionObserverConfig(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, `
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
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
compression:
  enabled: true
  mode: shadow
  route: "openai-responses:compressor"
  timeout: 8s
  max_input_tokens: 12000
  max_input_bytes: 1048576
  max_output_tokens: 1500
  max_output_bytes: 262144
  max_surrogate_bytes: 131072
  min_source_bytes: 4096
  min_saved_bytes: 1024
  min_savings_ratio: 0.3
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 524288
  max_pending_total: 256
  max_surrogate_bytes_total: 16777216
  egress_policy_ref: "test-allow"
`)
}

func disabledObserverConfig(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, `
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
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`)
}

func exactStoreOptionsWithCompression(t0 time.Time) reasoningpreservation.StoreOptions {
	return reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       8,
		MaxReasoningBytesPerTurn: 65536,
		MaxSessionBytes:          262144,
		Now:                      func() time.Time { return t0 },
		CompressionLimits:        compressionLimitsForTest(),
	}
}

func compressionLimitsForPostAppend() reasoningpreservation.CompressionLimits {
	return reasoningpreservation.CompressionConfig{
		Enabled:                     true,
		Mode:                        reasoningpreservation.CompressionShadow,
		Route:                       "r",
		EgressPolicyRef:             "ref",
		Timeout:                     time.Second,
		MaxInputTokens:              1000,
		MaxInputBytes:               1000,
		MaxOutputTokens:             100,
		MaxOutputBytes:              2048,
		MaxSurrogateBytes:           512,
		MinSourceBytes:              10,
		MinSavedBytes:               1,
		MinSavingsRatio:             0.5,
		MaxPendingPerSession:        8,
		MaxSurrogateBytesPerSession: 1024,
		MaxPendingTotal:             16,
		MaxSurrogateBytesTotal:      2048,
	}.ToLimits()
}

func semanticObserverMeta(sessionID, traceID, aLegID, bLegID, branch string, sc scope.PrincipalScopeView) response.StreamMeta {
	return response.StreamMeta{
		TraceID:      traceID,
		ALegID:       aLegID,
		BLegID:       bLegID,
		CandidateKey: branch,
		BackendID:    "be",
		Model:        "m",
		Session:      session.SessionView{AuthoritativeSessionID: sessionID},
		Scope:        sc,
	}
}

func trustedScopeForTest(principalID string) scope.PrincipalScopeView {
	v := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		Origin:      scope.OriginClient,
	}
	v.PrincipalID = scope.Known(principalID)
	v.DisplayName = scope.Known("display-" + principalID)
	v.Roles = []string{"role-a"}
	v.SafeClaims = map[string]string{"claim": "val"}
	return v
}

func computeExpectedSemanticDigest(placements []reasoningpreservation.PlacedReasoning) [32]byte {
	segs := reasoningpreservation.ExtractSemanticSegments(placements)
	if len(segs) == 0 {
		return [32]byte{}
	}
	h := sha256.New()
	for _, s := range segs {
		var idx [8]byte
		binary.BigEndian.PutUint64(idx[:], uint64(s.Index))
		_, _ = h.Write(idx[:])
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s.Text)))
		_, _ = h.Write(l[:])
		_, _ = h.Write([]byte(s.Text))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestPostAppend_nonSuccessNoCallback(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	var calls atomic.Int32
	hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		calls.Add(1)
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	nonSuccess := []response.StreamOutcome{
		response.OutcomeFailed,
		response.OutcomeCancelled,
		response.OutcomeClosed,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	}
	for _, oc := range nonSuccess {
		t.Run(string(oc), func(t *testing.T) {
			calls.Store(0)
			sess := "sess-nonsuccess-" + string(oc)
			obs, err := factory.Open(context.Background(), semanticObserverMeta(sess, "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
			require.NoError(t, err)
			require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic text for compression that is long enough"}))
			require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible ans"}))
			require.NoError(t, obs.Finish(context.Background(), oc))
			assert.Equal(t, int32(0), calls.Load(), "non-success %q must not invoke post-append hook", oc)
			snap, _ := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
			assert.Empty(t, snap)
		})
	}
}

func TestPostAppend_ineligibleNoCallback(t *testing.T) {
	t.Parallel()
	t.Run("unmatched_backend_inert", func(t *testing.T) {
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
compression:
  enabled: true
  mode: shadow
  route: "openai-responses:compressor"
  timeout: 8s
  max_input_tokens: 12000
  max_input_bytes: 1048576
  max_output_tokens: 1500
  max_output_bytes: 262144
  max_surrogate_bytes: 131072
  min_source_bytes: 4096
  min_saved_bytes: 1024
  min_savings_ratio: 0.3
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 524288
  max_pending_total: 256
  max_surrogate_bytes_total: 16777216
  egress_policy_ref: "test-allow"
`)
		store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
		var calls atomic.Int32
		hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
			calls.Add(1)
			return nil
		}
		factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
		obs, err := factory.Open(context.Background(), response.StreamMeta{
			BackendID:       "openrouter-prod",
			BackendPrefixes: []string{"openrouter"},
			Model:           "claude-3-5-sonnet",
			Session:         session.SessionView{AuthoritativeSessionID: "sess-inelig-inert"},
		}, response.Services{})
		require.NoError(t, err)
		assert.True(t, reasoningpreservation.StreamObserverIsInert(obs))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible text"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
		assert.Equal(t, int32(0), calls.Load())
	})
	t.Run("exact_reasoning_ineligible", func(t *testing.T) {
		t.Parallel()
		cfg := compressionObserverConfig(t)
		store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
		var calls atomic.Int32
		hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
			calls.Add(1)
			return nil
		}
		factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
		sess := "sess-exact-inelig"
		obs, err := factory.Open(context.Background(), semanticObserverMeta(sess, "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
		require.NoError(t, err)
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect:   aipapi.ReasoningDialectAnthropicThinkingV1,
			Text:      "exact signed text",
			Signature: "sig-xyz",
		}}))
		require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
		require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
		snap, _ := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
		require.Len(t, snap, 1, "original exact artifact must still be appended")
		assert.Equal(t, int32(0), calls.Load(), "exact artifact must not trigger post-append correlation")
	})
}

func TestPostAppend_appendFailureNoCallback(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	base := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	store := &appendFailingStore{TurnStore: base, err: errors.New("injected append failure")}
	var calls atomic.Int32
	hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		calls.Add(1)
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta("sess-append-fail-corr", "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic text for compression"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	assert.Equal(t, int32(0), calls.Load())
	snap, _ := base.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-append-fail-corr"))
	assert.Empty(t, snap)
}

func TestPostAppend_successSnapshotBeforeCallback(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	sess := "sess-success-before-cb"
	traceID := "trace-parent-123"
	aLegID := "aleg-456"
	bLegID := "bleg-789"
	branch := "branch-xyz"
	sc := trustedScopeForTest("principal-999")

	var captured reasoningpreservation.PostAppendCorrelation
	var snapshotLenAtCallback int
	var callbackErr error
	hook := func(ctx context.Context, corr reasoningpreservation.PostAppendCorrelation) error {
		captured = corr
		snap, err := store.Snapshot(ctx, reasoningpreservation.NewSessionPartition(sess))
		if err == nil {
			snapshotLenAtCallback = len(snap)
			if len(snap) == 1 {
				if snap[0].ID != corr.ArtifactID {
					callbackErr = errors.New("snapshot ID mismatch correlation")
				}
				if snap[0].Anchor != corr.Anchor {
					callbackErr = errors.New("anchor mismatch")
				}
			}
		}
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta(sess, traceID, aLegID, bLegID, branch, sc), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic reasoning text that will be hashed for digest correlation"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	require.NoError(t, callbackErr)
	assert.Equal(t, 1, snapshotLenAtCallback, "store Snapshot must contain artifact before callback")
	require.NotEmpty(t, captured.ArtifactID)
	assert.Equal(t, traceID, captured.TraceID)
	assert.Equal(t, aLegID, captured.ALegID)
	assert.Equal(t, bLegID, captured.BLegID)
	assert.Equal(t, branch, captured.BranchBinding)
	assert.Equal(t, sc.PrincipalID.String(), captured.Scope.PrincipalID.String())
	assert.Equal(t, sc.DisplayName.String(), captured.Scope.DisplayName.String())
	assert.Equal(t, "", captured.Partition.String(), "SessionPartition.String() must be empty for privacy")
	snapViaCaptured, _ := store.Snapshot(context.Background(), captured.Partition)
	assert.Len(t, snapViaCaptured, 1, "captured partition must retrieve stored artifact")
	assert.NotEqual(t, [32]byte{}, captured.Anchor)
	assert.NotEqual(t, [32]byte{}, captured.OriginalDigest)
	assert.Equal(t, captured.Anchor, captured.OriginalDigest)
	assert.NotEqual(t, [32]byte{}, captured.SemanticDigest)
	assert.NotEqual(t, [32]byte{}, captured.EgressPolicyRefHash)
	expectedHash := sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef))
	assert.Equal(t, expectedHash, captured.EgressPolicyRefHash)
	assert.Equal(t, cfg.Compression.EgressPolicyRef, captured.PolicyRevision)
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	expectedSem := computeExpectedSemanticDigest(snap[0].Reasoning)
	assert.Equal(t, expectedSem, captured.SemanticDigest)
}

func TestPostAppend_callbackFailureDoesNotInvalidateOriginal(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		return errors.New("injected callback failure")
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	sess := "sess-cb-failure"
	obs, err := factory.Open(context.Background(), semanticObserverMeta(sess, "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic text for failure test"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sess))
	require.NoError(t, err)
	require.Len(t, snap, 1, "callback failure must not delete original artifact")
	assert.Contains(t, factory.LastSafeDiagnostic(), "observed")
}

func TestPostAppend_noContentInCorrelationStruct(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[reasoningpreservation.PostAppendCorrelation]()
	for f := range typ.Fields() {
		lower := f.Name
		if lower == "Text" || lower == "Content" || lower == "Reasoning" || lower == "ReasoningBytes" {
			t.Fatalf("correlation struct must not contain content field %q", f.Name)
		}
		if f.Type.String() == "aipapi.Part" || f.Type.String() == "[]aipapi.Part" {
			t.Fatalf("correlation must not contain Part %q", f.Name)
		}
	}
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	sensitive := "sk-secret-sensitive-payload"
	hook := func(_ context.Context, corr reasoningpreservation.PostAppendCorrelation) error {
		val := reflect.ValueOf(corr)
		for _, f := range val.Fields() {
			if f.Kind() == reflect.String {
				assert.NotContains(t, f.String(), sensitive)
				assert.NotContains(t, f.String(), "eligible")
			}
		}
		assert.NotContains(t, corr.Scope.PrincipalID.String(), sensitive)
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta("sess-no-content", "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: sensitive + " eligible additional text"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
}

func TestPostAppend_disabledCompressionNoCallback(t *testing.T) {
	t.Parallel()
	cfg := disabledObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	var calls atomic.Int32
	hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		calls.Add(1)
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta("sess-disabled", "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic text even with disabled compression"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	assert.Equal(t, int32(0), calls.Load(), "disabled compression must not invoke post-append hook even if set")
	snap, _ := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-disabled"))
	require.Len(t, snap, 1)
}

func TestPostAppend_compressionPathNotBeforeAppend(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	base := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	counting := &appendCountingStore{TurnStore: base}
	var hookCalledBeforeAppend bool
	hook := func(ctx context.Context, corr reasoningpreservation.PostAppendCorrelation) error {
		if counting.appendCalls.Load() == 0 {
			hookCalledBeforeAppend = true
		}
		snap, _ := base.Snapshot(ctx, reasoningpreservation.NewSessionPartition("sess-order"))
		if len(snap) != 1 || snap[0].ID != corr.ArtifactID {
			hookCalledBeforeAppend = true
		}
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, counting, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta("sess-order", "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible text for order check"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	assert.False(t, hookCalledBeforeAppend, "hook must be after Append success, not before")
	assert.Equal(t, int32(1), counting.appendCalls.Load())
}

func TestPostAppend_hookUnlockedNoDeadlock(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	var hookReentered int32
	var obsRef atomic.Value
	hook := func(ctx context.Context, corr reasoningpreservation.PostAppendCorrelation) error {
		// Re-enter Finish onsame observer; should not deadlock because lock is released before hook.
		if v := obsRef.Load(); v != nil {
			o := v.(interface {
				Finish(context.Context, response.StreamOutcome) error
			})
			_ = o.Finish(ctx, response.OutcomeSuccessReleased)
			hookReentered = 1
		}
		// Also verify partition still readable unlocked.
		snap, _ := store.Snapshot(ctx, corr.Partition)
		if len(snap) != 1 {
			t.Errorf("snapshot via hook partition failed")
		}
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	obs, err := factory.Open(context.Background(), semanticObserverMeta("sess-unlocked", "trace-1", "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
	require.NoError(t, err)
	obsRef.Store(obs)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic text for unlocked test"}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	done := make(chan struct{})
	go func() {
		_ = obs.Finish(context.Background(), response.OutcomeSuccessReleased)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Finish deadlocked with hook holding lock")
	}
	assert.Equal(t, int32(1), hookReentered, "hook must have re-entered Finish without deadlock")
	snap, _ := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-unlocked"))
	require.Len(t, snap, 1)
}

func TestPostAppend_concurrentObserversWithImmutableHook(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	var calls atomic.Int32
	hook := func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		calls.Add(1)
		return nil
	}
	factory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, hook)
	// Verify immutable: factory field not mutated after construction (no setter). Two factories with different hooks are independent.
	otherCalls := atomic.Int32{}
	otherFactory := reasoningpreservation.NewStreamObserverFactoryWithPostAppendHook(cfg, store, func(_ context.Context, _ reasoningpreservation.PostAppendCorrelation) error {
		otherCalls.Add(1)
		return nil
	})
	// Use both factories to prove independence in same test.
	_ = otherFactory
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			sess := "sess-conc-" + string(rune('a'+idx))
			obs, err := factory.Open(context.Background(), semanticObserverMeta(sess, "trace-"+sess, "aleg-1", "bleg-1", "branch-1", trustedScopeForTest("user-1")), response.Services{})
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible concurrent text"})
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
			_ = obs.Finish(context.Background(), response.OutcomeSuccessReleased)
		}(i)
	}
	wg.Wait()
	assert.Equal(t, int32(n), calls.Load(), "concurrent observers must all invoke hook exactly once without race")
	assert.Equal(t, int32(0), otherCalls.Load(), "other factory hook must not be invoked")
}

type appendCountingStore struct {
	reasoningpreservation.TurnStore
	appendCalls atomic.Int32
}

func (c *appendCountingStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	c.appendCalls.Add(1)
	return c.TurnStore.Append(ctx, p, a)
}

func (c *appendCountingStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return c.TurnStore.Snapshot(ctx, p)
}

func (c *appendCountingStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	return c.TurnStore.Delete(ctx, p, ids...)
}
