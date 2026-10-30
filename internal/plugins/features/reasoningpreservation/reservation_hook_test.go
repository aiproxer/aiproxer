//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
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

func reservationConfigWithLimits(t *testing.T, minSource int, pendingPerSession, pendingTotal int) reasoningpreservation.Config {
	t.Helper()
	// reuse helper but adjust
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = minSource
	cfg.Compression.MaxPendingPerSession = pendingPerSession
	cfg.Compression.MaxPendingTotal = pendingTotal
	// ensure store limits will be wired via bundle
	return cfg
}

func newReservationStore(t *testing.T, cfg reasoningpreservation.Config) reasoningpreservation.TurnStore {
	t.Helper()
	opts := reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       8,
		MaxReasoningBytesPerTurn: 65536,
		MaxSessionBytes:          262144,
		Now:                      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		CompressionLimits:        cfg.Compression.ToLimits(),
	}
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	return st
}

func TestReservation_TryReserve_SuccessVisible(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 10
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-reserve-success")
	art := sampleArtifactWithTime("art-1", "eligible semantic reasoning text that is long enough for reserve", 64, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, snap, 1)
	placements := snap[0].Reasoning
	segs := reasoningpreservation.ExtractSemanticSegments(placements)
	require.NotEmpty(t, segs)
	// Build correlation as observer does
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	assert.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	assert.NotEmpty(t, res.Claim.ReservationID)
	assert.True(t, res.IsReserved())
	// pending visible via store
	state, ok, err := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, res.Claim.ReservationID, state.Pending.ReservationID)
	assert.Equal(t, corr.SemanticDigest, state.Pending.SemanticDigest)
	assert.Equal(t, corr.EgressPolicyRefHash, state.Pending.EgressPolicyHash)
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	// original never evicted
	snap2, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, snap2, 1)
}

func TestReservation_BelowThresholdNoReserve(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1000
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-below-threshold")
	art := sampleArtifactWithTime("art-low", "short", 16, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
	corr := buildTestCorrelation(p, snap[0], cfg)
	// Ensure source bytes below threshold
	assert.True(t, corr.SourceBytes < cfg.Compression.MinSourceBytes, "source must be below threshold for test")
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	assert.Equal(t, reasoningpreservation.ReservationSkippedBelowThreshold, res.Outcome)
	assert.Empty(t, res.Claim.ReservationID)
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	// no pending state
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	assert.False(t, ok, "below threshold must not create reservation entry")
}

func TestReservation_IneligibleNoReserve(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	store := newMemoryStore(t, exactStoreOptionsWithCompression(time.Unix(1_700_000_000, 0).UTC()))
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-ineligible")
	// Exact reasoning part should be ineligible (semantic digest zero after observer filter, but TryReserve should also skip)
	part := reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "signed text", "sig", nil)
	art := reasoningpreservation.TurnArtifact{
		ID:             "art-exact",
		Anchor:         [32]byte{9, 9},
		SourceBackend:  "be",
		SourceModel:    "m",
		Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		ReasoningBytes: 32,
	}
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	// Build correlation with zero digest (ineligible)
	corr := reasoningpreservation.PostAppendCorrelation{
		Partition:           p,
		ArtifactID:          "art-exact",
		OriginalDigest:      [32]byte{9, 9},
		SemanticDigest:      [32]byte{},
		EgressPolicyRefHash: sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef)),
		SourceBytes:         0,
		PolicyRevision:      cfg.Compression.EgressPolicyRef,
		Scope:               scope.PrincipalScopeView{},
	}
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	assert.Equal(t, reasoningpreservation.ReservationSkippedIneligible, res.Outcome)
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

func TestReservation_SessionSaturationBudgetExhausted(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	cfg.Compression.MaxPendingPerSession = 1
	cfg.Compression.MaxPendingTotal = 10
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-saturated")
	// First artifact reserve succeeds
	art1 := sampleArtifactWithTime("art-1", "eligible semantic reasoning text A long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art1)
	require.NoError(t, err)
	snap1, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap1, 1)
	corr1 := buildTestCorrelation(p, snap1[0], cfg)
	res1 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr1)
	require.Equal(t, reasoningpreservation.ReservationReserved, res1.Outcome)
	// Second artifact same session should be budget exhausted
	art2 := sampleArtifactWithTime("art-2", "eligible semantic reasoning text B long enough", 64, time.Unix(1_700_000_001, 0).UTC())
	_, err = cs.Append(context.Background(), p, art2)
	require.NoError(t, err)
	snap2, _ := cs.Snapshot(context.Background(), p)
	// find art-2 ID; snapshot contains both
	var art2Snap reasoningpreservation.TurnArtifact
	for _, a := range snap2 {
		if a.ID == "art-2" {
			art2Snap = a
		}
	}
	require.Equal(t, "art-2", art2Snap.ID)
	corr2 := buildTestCorrelation(p, art2Snap, cfg)
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr2)
	assert.Equal(t, reasoningpreservation.ReservationBudgetExceeded, res2.Outcome)
	assert.True(t, reasoningpreservation.IsBudgetError(res2.Err))
	// Originals never evicted
	snapAll, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snapAll, 2, "budget exhaustion must not evict original")
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending, "only first reservation should count")
}

func TestReservation_AggregateSaturation(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	cfg.Compression.MaxPendingPerSession = 10
	cfg.Compression.MaxPendingTotal = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p1 := reasoningpreservation.NewSessionPartition("sess-agg-a")
	p2 := reasoningpreservation.NewSessionPartition("sess-agg-b")
	artA := sampleArtifactWithTime("art-a", "eligible semantic text aggregate A", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p1, artA)
	snapA, _ := cs.Snapshot(context.Background(), p1)
	corrA := buildTestCorrelation(p1, snapA[0], cfg)
	resA := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corrA)
	require.Equal(t, reasoningpreservation.ReservationReserved, resA.Outcome)
	artB := sampleArtifactWithTime("art-b", "eligible semantic text aggregate B", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p2, artB)
	snapB, _ := cs.Snapshot(context.Background(), p2)
	corrB := buildTestCorrelation(p2, snapB[0], cfg)
	resB := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corrB)
	assert.Equal(t, reasoningpreservation.ReservationBudgetExceeded, resB.Outcome)
	// originals untouched
	snapA2, _ := cs.Snapshot(context.Background(), p1)
	snapB2, _ := cs.Snapshot(context.Background(), p2)
	assert.Len(t, snapA2, 1)
	assert.Len(t, snapB2, 1)
}

func TestReservation_StaleCorrelationFailOpen(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-stale")
	art := sampleArtifactWithTime("art-1", "eligible stale test text long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corrGood := buildTestCorrelation(p, snap[0], cfg)
	resGood := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corrGood)
	require.Equal(t, reasoningpreservation.ReservationReserved, resGood.Outcome)
	// Stale: wrong artifactID
	corrStale := corrGood
	corrStale.ArtifactID = "non-existent"
	resStale := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corrStale)
	assert.Equal(t, reasoningpreservation.ReservationNotFound, resStale.Outcome)
	// stale digest mismatch also fail-open as conflict (reserve will hit pending exists)
	corrConflict := corrGood
	corrConflict.SemanticDigest = sha256.Sum256([]byte("different"))
	resConflict := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corrConflict)
	assert.Equal(t, reasoningpreservation.ReservationConflict, resConflict.Outcome)
	// original untouched, only one pending
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1)
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
}

func TestReservation_CorrectSemanticDigest(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-digest")
	art := sampleArtifactWithTime("art-digest", "correct digest payload for verification", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	expectedDigest := computeExpectedSemanticDigest(snap[0].Reasoning)
	corr := buildTestCorrelation(p, snap[0], cfg)
	assert.Equal(t, expectedDigest, corr.SemanticDigest, "correlation must carry correct semantic digest")
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, expectedDigest, state.Pending.SemanticDigest)
}

func TestReservation_ProvisionalHashThenUpdate(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-provisional")
	art := sampleArtifactWithTime("art-prov", "provisional hash update test payload long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	// Provisional hash is ref hash, not authoritative
	provisional := corr.EgressPolicyRefHash
	assert.NotEqual(t, [32]byte{}, provisional)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.Equal(t, provisional, state.Pending.EgressPolicyHash, "provisional hash must be stored at reserve")
	// Simulate authoritative decision in 4.3 with full CAS
	authoritative := sha256.Sum256([]byte("authoritative-policy-v2"))
	claim := res.Claim
	err := cs.UpdateReservationPolicyHash(context.Background(), claim, provisional, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.NoError(t, err)
	state2, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state2.Pending)
	assert.Equal(t, authoritative, state2.Pending.EgressPolicyHash)
	assert.NotEqual(t, provisional, state2.Pending.EgressPolicyHash)
	// Attach with authoritative should succeed; with provisional should fail as conflict after update
	sem := corr.SemanticDigest
	surProvisional := reasoningpreservation.ReasoningSurrogate{
		OriginalDigest:      corr.OriginalDigest,
		PolicyRevision:      corr.PolicyRevision,
		Sanitization:        "none",
		SemanticDigest:      sem,
		EgressPolicyHash:    provisional,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
		Segments:            []reasoningpreservation.SurrogateSegment{{PlacementIndex: 0, Text: "sur", Bytes: 3}},
		Bytes:               3,
	}
	err = cs.AttachSurrogate(context.Background(), claim, "job-1", surProvisional)
	assert.Error(t, err, "provisional hash after authoritative update must be rejected")
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// Need to Bind first? Actually Attach requires pending with correct hash; let's bind then attach authoritative
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, "job-1"))
	surAuth := reasoningpreservation.ReasoningSurrogate{
		OriginalDigest:      corr.OriginalDigest,
		PolicyRevision:      corr.PolicyRevision,
		Sanitization:        "none",
		SemanticDigest:      sem,
		EgressPolicyHash:    authoritative,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
		Segments:            []reasoningpreservation.SurrogateSegment{{PlacementIndex: 0, Text: "sur", Bytes: 3}},
		Bytes:               3,
	}
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, "job-1", surAuth))
}

func TestReservation_ObserverIntegration_ReservationVisible(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 10
	// Use bundle wiring: BuildPostAppendHook via FeatureBundleWithPartsAndCompression
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeEgress{},
		Sanitizer:    fakeSanitizer{},
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	store := parts.Store
	cs := store.(reasoningpreservation.CompressionStore)
	obs := parts.Observer
	require.NotNil(t, obs)
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-observer-integration"},
		TraceID:   "trace-1",
		ALegID:    "aleg-1",
		BLegID:    "bleg-1",
		Scope:     scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-1")},
	}
	o, err := obs.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	require.NoError(t, o.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "eligible semantic reasoning text for observer integration that is long enough"}))
	require.NoError(t, o.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, o.Finish(context.Background(), response.OutcomeSuccessReleased))
	// original must be appended, pending reserved
	snap, err := cs.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-observer-integration"))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	state, ok, err := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition("sess-observer-integration"), snap[0].ID)
	require.NoError(t, err)
	require.True(t, ok, "reservation must be visible after observer Finish")
	require.NotNil(t, state.Pending)
	assert.NotEmpty(t, state.Pending.ReservationID)
	assert.NotEqual(t, [32]byte{}, state.Pending.SemanticDigest)
}

func TestReservation_ObserverIntegration_BelowThresholdNoReserve(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 5000
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeEgress{},
		Sanitizer:    fakeSanitizer{},
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	store := parts.Store
	cs := store.(reasoningpreservation.CompressionStore)
	obs := parts.Observer
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: "sess-below-threshold-integration"},
	}
	o, err := obs.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	require.NoError(t, o.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "short"}))
	require.NoError(t, o.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, o.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, _ := cs.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("sess-below-threshold-integration"))
	require.Len(t, snap, 1)
	_, ok, _ := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition("sess-below-threshold-integration"), snap[0].ID)
	assert.False(t, ok, "below threshold must not reserve")
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

func TestUpdate_StaleExpectedOldMismatchRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-update-stale-old")
	art := sampleArtifactWithTime("art-1", "eligible update stale old test long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	wrongOld := sha256.Sum256([]byte("wrong-old"))
	authoritative := sha256.Sum256([]byte("authoritative"))
	err := cs.UpdateReservationPolicyHash(context.Background(), claim, wrongOld, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.False(t, state.Pending.PolicyHashAuthoritative)
}

func TestUpdate_StaleDigestMismatchRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-update-stale-digest")
	art := sampleArtifactWithTime("art-1", "eligible stale digest mismatch long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	authoritative := sha256.Sum256([]byte("authoritative"))
	wrongDigest := sha256.Sum256([]byte("wrong-digest"))
	wrongDigestClaim := reasoningpreservation.CompressionClaim{
		Partition:      claim.Partition,
		ArtifactID:     claim.ArtifactID,
		ReservationID:  claim.ReservationID,
		OriginalDigest: wrongDigest,
		PolicyRevision: claim.PolicyRevision,
	}
	err := cs.UpdateReservationPolicyHash(context.Background(), wrongDigestClaim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	wrongSem := sha256.Sum256([]byte("wrong-sem"))
	err = cs.UpdateReservationPolicyHash(context.Background(), claim, corr.EgressPolicyRefHash, wrongSem, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
}

func TestUpdate_StalePolicyMismatchRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-update-stale-policy")
	art := sampleArtifactWithTime("art-1", "eligible stale policy mismatch long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	claim.PolicyRevision = "wrong-policy"
	authoritative := sha256.Sum256([]byte("authoritative"))
	err := cs.UpdateReservationPolicyHash(context.Background(), claim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
}

func TestUpdate_ZeroNewHashRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-update-zero")
	art := sampleArtifactWithTime("art-1", "eligible zero new hash rejected long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	var zero [32]byte
	err := cs.UpdateReservationPolicyHash(context.Background(), claim, corr.EgressPolicyRefHash, corr.SemanticDigest, zero, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route")))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.False(t, state.Pending.PolicyHashAuthoritative)
}

func TestBind_PrePromotionRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-bind-pre")
	art := sampleArtifactWithTime("art-1", "eligible bind pre-promotion long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	err := cs.BindCompressionJob(context.Background(), claim, "job-1")
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// after promotion should succeed
	authoritative := sha256.Sum256([]byte("authoritative"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, "job-1"))
}

func TestAttach_PrePromotionRejected(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-attach-pre")
	art := sampleArtifactWithTime("art-1", "eligible attach pre-promotion long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	claim := res.Claim
	// Attach without promotion should fail (still provisional)
	sur := reasoningpreservation.ReasoningSurrogate{
		OriginalDigest:      corr.OriginalDigest,
		PolicyRevision:      corr.PolicyRevision,
		Sanitization:        "none",
		SemanticDigest:      corr.SemanticDigest,
		EgressPolicyHash:    corr.EgressPolicyRefHash,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
		Segments:            []reasoningpreservation.SurrogateSegment{{PlacementIndex: 0, Text: "x", Bytes: 1}},
		Bytes:               1,
	}
	err := cs.AttachSurrogate(context.Background(), claim, "job-1", sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// now promote and bind, then attach authoritative should succeed
	authoritative := sha256.Sum256([]byte("authoritative"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, "job-1"))
	sur.EgressPolicyHash = authoritative
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, "job-1", sur))
}

func TestReservation_Chaining_NextCalled(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-chain-next")
	art := sampleArtifactWithTime("art-1", "eligible chaining next called long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	called := false
	var gotID string
	next := func(ctx context.Context, res reasoningpreservation.ReservationResult) error {
		called = true
		gotID = res.Claim.ReservationID
		assert.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
		return nil
	}
	hook := reasoningpreservation.NewCompressionReservationHook(cfg, cs, next)
	require.NoError(t, hook(context.Background(), corr))
	assert.True(t, called, "next must be called on reserved")
	assert.NotEmpty(t, gotID)
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.Equal(t, gotID, state.Pending.ReservationID)
}

func TestReservation_Chaining_NonReservedNextNotCalled(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 5000 // will be below threshold
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-chain-nonreserved")
	art := sampleArtifactWithTime("art-1", "short", 16, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	called := false
	next := func(ctx context.Context, res reasoningpreservation.ReservationResult) error {
		called = true
		return nil
	}
	hook := reasoningpreservation.NewCompressionReservationHook(cfg, cs, next)
	require.NoError(t, hook(context.Background(), corr))
	assert.False(t, called, "next must NOT be called when not reserved (below threshold)")
	// also ineligible
	cfg2 := compressionObserverConfig(t)
	store2 := newReservationStore(t, cfg2)
	cs2 := store2.(reasoningpreservation.CompressionStore)
	p2 := reasoningpreservation.NewSessionPartition("sess-chain-ineligible")
	art2 := reasoningpreservation.TurnArtifact{ID: "art-exact", Anchor: [32]byte{1}, SourceBackend: "be", SourceModel: "m", Reasoning: []reasoningpreservation.PlacedReasoning{placedReasoning(0, reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "x", "sig", nil))}, CreatedAt: time.Unix(1_700_000_000, 0).UTC(), ReasoningBytes: 10}
	_, _ = cs2.Append(context.Background(), p2, art2)
	corr2 := reasoningpreservation.PostAppendCorrelation{Partition: p2, ArtifactID: "art-exact", OriginalDigest: [32]byte{1}, SemanticDigest: [32]byte{}, EgressPolicyRefHash: sha256.Sum256([]byte(cfg2.Compression.EgressPolicyRef)), SourceBytes: 0, PolicyRevision: cfg2.Compression.EgressPolicyRef}
	called = false
	hook2 := reasoningpreservation.NewCompressionReservationHook(cfg2, cs2, next)
	require.NoError(t, hook2(context.Background(), corr2))
	assert.False(t, called, "next must NOT be called when ineligible")
}

func TestReservation_Chaining_NextErrorFailOpen(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-chain-error")
	art := sampleArtifactWithTime("art-1", "eligible chaining error fail open long enough", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	next := func(ctx context.Context, res reasoningpreservation.ReservationResult) error {
		return assert.AnError
	}
	hook := reasoningpreservation.NewCompressionReservationHook(cfg, cs, next)
	// hook must fail-open (return nil) even though next errors, original untouched
	err := hook(context.Background(), corr)
	require.NoError(t, err)
	snap2, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap2, 1, "original must remain even when next errors")
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
}

func TestReservation_SourceBytesZeroBelowThreshold(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 10
	store := newReservationStore(t, cfg)
	cs := store.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-zero-bytes")
	art := sampleArtifactWithTime("art-1", "eligible zero bytes test long enough but we force zero", 64, time.Unix(1_700_000_000, 0).UTC())
	_, _ = cs.Append(context.Background(), p, art)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	corr.SourceBytes = 0 // force zero
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	assert.Equal(t, reasoningpreservation.ReservationSkippedBelowThreshold, res.Outcome)
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

func buildTestCorrelation(p reasoningpreservation.SessionPartition, art reasoningpreservation.TurnArtifact, cfg reasoningpreservation.Config) reasoningpreservation.PostAppendCorrelation {
	segs := reasoningpreservation.ExtractSemanticSegments(art.Reasoning)
	var semDigest [32]byte
	srcBytes := 0
	if len(segs) > 0 {
		semDigest = computeExpectedSemanticDigest(art.Reasoning)
		for _, s := range segs {
			srcBytes += len(s.Text)
		}
	}
	egHash := sha256.Sum256([]byte(cfg.Compression.EgressPolicyRef))
	return reasoningpreservation.PostAppendCorrelation{
		Partition:           p,
		ArtifactID:          art.ID,
		Anchor:              art.Anchor,
		OriginalDigest:      art.Anchor,
		SemanticDigest:      semDigest,
		EgressPolicyRefHash: egHash,
		SourceBytes:         srcBytes,
		PolicyRevision:      cfg.Compression.EgressPolicyRef,
		Scope:               scope.PrincipalScopeView{},
		TraceID:             "trace-test",
		ALegID:              "aleg-test",
		BLegID:              "bleg-test",
		BranchBinding:       "branch-test",
	}
}
