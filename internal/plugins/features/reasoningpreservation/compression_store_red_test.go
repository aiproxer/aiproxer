//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func digestFor(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func compressionLimitsForTest() reasoningpreservation.CompressionLimits {
	return reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        2,
		MaxPendingTotal:             3,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 150,
		MaxSurrogateBytesTotal:      200,
	}
}

func newCompressionStore(t *testing.T, now func() time.Time, limits reasoningpreservation.CompressionLimits) reasoningpreservation.CompressionStore {
	t.Helper()
	opts := defaultStoreOptions(now)
	opts.CompressionLimits = limits
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs, ok := st.(reasoningpreservation.CompressionStore)
	require.True(t, ok, "store must implement CompressionStore")
	return cs
}

func appendArtifactForCompression(t *testing.T, cs reasoningpreservation.CompressionStore, p reasoningpreservation.SessionPartition, id string, bytes int) [32]byte {
	t.Helper()
	art := sampleArtifact(id, "reasoning-"+id, bytes)
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	// digest is anchor-like: use sha of id
	return digestFor(id)
}

func semanticDigestFor(policy string) [32]byte { return digestFor("semantic-" + policy) }

func egressHashFor(policy string) [32]byte { return digestFor("egress-" + policy) }

func surrogateFor(digest [32]byte, policy string, segs ...reasoningpreservation.SurrogateSegment) reasoningpreservation.ReasoningSurrogate {
	total := 0
	for _, s := range segs {
		total += s.Bytes
	}
	sem := semanticDigestFor(policy)
	eg := egressHashFor(policy)
	return reasoningpreservation.ReasoningSurrogate{
		OriginalDigest:      digest,
		PolicyRevision:      policy,
		Sanitization:        "none",
		Segments:            segs,
		Bytes:               total,
		SemanticDigest:      sem,
		EgressPolicyHash:    eg,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
}

func surrogateForWithDigests(digest [32]byte, policy string, sem [32]byte, eg [32]byte, segs ...reasoningpreservation.SurrogateSegment) reasoningpreservation.ReasoningSurrogate {
	total := 0
	for _, s := range segs {
		total += s.Bytes
	}
	return reasoningpreservation.ReasoningSurrogate{
		OriginalDigest:      digest,
		PolicyRevision:      policy,
		Sanitization:        "none",
		Segments:            segs,
		Bytes:               total,
		SemanticDigest:      sem,
		EgressPolicyHash:    eg,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
}

// Table-driven pending/session bound
func TestCompression_Reserve_PendingPerSessionBound(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := compressionLimitsForTest()
	limits.MaxPendingPerSession = 1
	limits.MaxPendingTotal = 10
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-pending-session")
	d1 := appendArtifactForCompression(t, cs, p, "t1", 32)
	d2 := appendArtifactForCompression(t, cs, p, "t2", 32)
	policy := "v1"
	_, err := cs.ReserveCompression(context.Background(), p, "t1", d1, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, err)
	_, err = cs.ReserveCompression(context.Background(), p, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsBudgetError(err))
	var be *reasoningpreservation.BudgetError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, reasoningpreservation.BudgetPendingPerSession, be.Kind)
}

// Pending total bound across sessions table
func TestCompression_Reserve_PendingTotalBound(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := compressionLimitsForTest()
	limits.MaxPendingPerSession = 10
	limits.MaxPendingTotal = 2
	cs := newCompressionStore(t, now, limits)
	policy := "v1"
	parts := []reasoningpreservation.SessionPartition{
		reasoningpreservation.NewSessionPartition("s-a"),
		reasoningpreservation.NewSessionPartition("s-b"),
		reasoningpreservation.NewSessionPartition("s-c"),
	}
	for i, p := range parts {
		d := appendArtifactForCompression(t, cs, p, fmt.Sprintf("id-%d", i), 32)
		_, _ = cs.ReserveCompression(context.Background(), p, fmt.Sprintf("id-%d", i), d, policy, semanticDigestFor(policy), egressHashFor(policy))
		// first two succeed
	}
	// third should be budget
	p3 := reasoningpreservation.NewSessionPartition("s-c")
	// s-c already has 1 pending from loop, but total now =3?
	// Use fresh session to test total exhaustion
	pFresh := reasoningpreservation.NewSessionPartition("s-fresh")
	dFresh := appendArtifactForCompression(t, cs, pFresh, "fresh", 32)
	_ = p3 // already
	_, err := cs.ReserveCompression(context.Background(), pFresh, "fresh", dFresh, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.Error(t, err)
	var be *reasoningpreservation.BudgetError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, reasoningpreservation.BudgetPendingTotal, be.Kind)
}

func TestCompression_Attach_SurrogatePerTurnBound(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := compressionLimitsForTest()
	limits.MaxSurrogateBytesPerTurn = 10
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-turn")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	// surrogate exceeds per-turn
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "01234567890", Bytes: 11})
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
	require.Error(t, err)
	var be *reasoningpreservation.BudgetError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, reasoningpreservation.BudgetSurrogatePerTurn, be.Kind)
}

func TestCompression_Attach_SurrogatePerSessionBound(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := compressionLimitsForTest()
	limits.MaxSurrogateBytesPerSession = 15
	limits.MaxSurrogateBytesPerTurn = 100
	limits.MaxSurrogateBytesTotal = 1000
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-sur-session")
	policy := "v1"
	d1 := appendArtifactForCompression(t, cs, p, "t1", 32)
	d2 := appendArtifactForCompression(t, cs, p, "t2", 32)
	claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d1, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d1, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hello", Bytes: 10})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	claim2, err := cs.ReserveCompression(context.Background(), p, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d2, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "world!", Bytes: 10})
	err = cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2)
	require.Error(t, err)
	var be *reasoningpreservation.BudgetError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, reasoningpreservation.BudgetSurrogatePerSession, be.Kind)
}

func TestCompression_Attach_SurrogateTotalBound(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := compressionLimitsForTest()
	limits.MaxSurrogateBytesPerSession = 1000
	limits.MaxSurrogateBytesTotal = 15
	limits.MaxSurrogateBytesPerTurn = 100
	cs := newCompressionStore(t, now, limits)
	policy := "v1"
	p1 := reasoningpreservation.NewSessionPartition("s1")
	p2 := reasoningpreservation.NewSessionPartition("s2")
	d1 := appendArtifactForCompression(t, cs, p1, "t1", 32)
	d2 := appendArtifactForCompression(t, cs, p2, "t2", 32)
	claim1, err := cs.ReserveCompression(context.Background(), p1, "t1", d1, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d1, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hello", Bytes: 10})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	claim2, err := cs.ReserveCompression(context.Background(), p2, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d2, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "world!", Bytes: 10})
	err = cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2)
	require.Error(t, err)
	var be *reasoningpreservation.BudgetError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, reasoningpreservation.BudgetSurrogateTotal, be.Kind)
}

// Multi-session aggregate exhaustion preserves originals
func TestCompression_AggregateExhaustion_PreservesOriginals(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             2,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	cs := newCompressionStore(t, now, limits)
	policy := "v1"
	// Fill aggregate with 2 sessions
	for i := range 2 {
		p := reasoningpreservation.NewSessionPartition(fmt.Sprintf("sess-%d", i))
		d := appendArtifactForCompression(t, cs, p, fmt.Sprintf("t-%d", i), 32)
		_, err := cs.ReserveCompression(context.Background(), p, fmt.Sprintf("t-%d", i), d, policy, semanticDigestFor(policy), egressHashFor(policy))
		require.NoError(t, err)
	}
	// Exhausted - new session cannot bypass via new partition
	pFresh := reasoningpreservation.NewSessionPartition("sess-fresh")
	dFresh := appendArtifactForCompression(t, cs, pFresh, "fresh", 32)
	_, err := cs.ReserveCompression(context.Background(), pFresh, "fresh", dFresh, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.Error(t, err)
	require.True(t, reasoningpreservation.IsBudgetError(err))
	// Original must still be present
	snap, err := cs.Snapshot(context.Background(), pFresh)
	require.NoError(t, err)
	require.Len(t, snap, 1)
	assert.Equal(t, "fresh", snap[0].ID)
	// Also surrogates aggregate exhaustion preserves originals
	limits2 := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      10,
	}
	cs2 := newCompressionStore(t, now, limits2)
	p1 := reasoningpreservation.NewSessionPartition("s1")
	d1 := appendArtifactForCompression(t, cs2, p1, "t1", 32)
	claim1, err := cs2.ReserveCompression(context.Background(), p1, "t1", d1, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs2.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs2.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d1, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hello10", Bytes: 10})
	require.NoError(t, cs2.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	// next surrogate would exceed total, must reject without evicting original
	p2 := reasoningpreservation.NewSessionPartition("s2")
	d2 := appendArtifactForCompression(t, cs2, p2, "t2", 32)
	claim2, err := cs2.ReserveCompression(context.Background(), p2, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs2.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs2.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d2, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "world10", Bytes: 10})
	err = cs2.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2)
	require.Error(t, err)
	snap, err = cs2.Snapshot(context.Background(), p1)
	require.NoError(t, err)
	require.Len(t, snap, 1)
	snap, err = cs2.Snapshot(context.Background(), p2)
	require.NoError(t, err)
	require.Len(t, snap, 1)
}

// Stale CAS conflicts
func TestCompression_StaleCAS(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-cas")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	// wrong digest
	wrongDigest := digestFor("wrong")
	wrongDigestClaim := reasoningpreservation.CompressionClaim{
		Partition:      claim.Partition,
		ArtifactID:     claim.ArtifactID,
		ReservationID:  claim.ReservationID,
		OriginalDigest: wrongDigest,
		PolicyRevision: claim.PolicyRevision,
	}
	err = cs.BindCompressionJob(context.Background(), wrongDigestClaim, auxiliary.JobID("job1"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// wrong policy
	wrongPolicyClaim := reasoningpreservation.CompressionClaim{
		Partition:      claim.Partition,
		ArtifactID:     claim.ArtifactID,
		ReservationID:  claim.ReservationID,
		OriginalDigest: claim.OriginalDigest,
		PolicyRevision: "v2",
	}
	err = cs.BindCompressionJob(context.Background(), wrongPolicyClaim, auxiliary.JobID("job1"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// wrong reservation
	wrongResClaim := reasoningpreservation.CompressionClaim{
		Partition:      claim.Partition,
		ArtifactID:     claim.ArtifactID,
		ReservationID:  "bad-res",
		OriginalDigest: claim.OriginalDigest,
		PolicyRevision: claim.PolicyRevision,
	}
	err = cs.BindCompressionJob(context.Background(), wrongResClaim, auxiliary.JobID("job1"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// correct bind
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	// attach with wrong job
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("wrong-job"), sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// attach with wrong policy
	sur2 := surrogateFor(d, "v2", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur2)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// attach with wrong digest
	wrongSur := surrogateFor(wrongDigest, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), wrongSur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
}

func TestCompression_DoubleAttach_Rejected(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-double")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	// second attach same reservation should be stale
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
}

func TestCompression_RepeatedStale_NoDrift(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-drift")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 2, stats.TotalSurrogateBytes)
	// repeated stale attaches must not drift
	for range 10 {
		err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
		require.Error(t, err)
	}
	stats = cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 2, stats.TotalSurrogateBytes)
	// stale bind attempts also no drift
	for range 10 {
		_ = cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1"))
	}
	stats = cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 2, stats.TotalSurrogateBytes)
}

func TestCompression_DeleteReappend_FreshState(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-reappend")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	// delete original should clear optional and decrement
	require.NoError(t, cs.Delete(context.Background(), p, "t1"))
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalSurrogateBytes)
	// re-append same ID should allow fresh reservation
	d2 := appendArtifactForCompression(t, cs, p, "t1", 32)
	claim2, err := cs.ReserveCompression(context.Background(), p, "t1", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	assert.NotEqual(t, claim.ReservationID, claim2.ReservationID)
	state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotNil(t, state.Pending)
	assert.Nil(t, state.Surrogate)
}

func TestCompression_TTLExpiry_ClearsOptional(t *testing.T) {
	t.Parallel()
	now, advance := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	// Use short TTL
	opts := defaultStoreOptions(now)
	opts.TTL = time.Hour
	opts.CompressionLimits = limits
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs := st.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-ttl")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	advance(2 * time.Hour)
	// Snapshot triggers expiry
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, snap)
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalSurrogateBytes)
	assert.Equal(t, 0, stats.TotalPending)
	// optional state should be gone
	_, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCompression_DefensiveCopies(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-copy")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "original", Bytes: 8})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Surrogate)
	state.Surrogate.Segments[0].Text = "mutated"
	state.Surrogate.Bytes = 9999
	state2, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "original", state2.Surrogate.Segments[0].Text)
	assert.Equal(t, 8, state2.Surrogate.Bytes)
}

func TestCompression_ConcurrentExactlyOnce(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-conc")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	// Concurrent attach + clear: exactly once decrement
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
	}()
	go func() {
		defer wg.Done()
		_ = cs.ClearCompression(context.Background(), p, "t1", "")
	}()
	wg.Wait()
	stats := cs.CompressionStats()
	// counters must not be negative and must be 0 or 2
	assert.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
	assert.LessOrEqual(t, stats.TotalSurrogateBytes, 2)
	assert.GreaterOrEqual(t, stats.TotalPending, 0)
	// Now concurrent clears
	wg.Add(5)
	for range 5 {
		go func() {
			defer wg.Done()
			_ = cs.ClearCompression(context.Background(), p, "t1", "")
		}()
	}
	wg.Wait()
	stats = cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
	// Delete also idempotent under concurrency
	// re-setup
	d2 := appendArtifactForCompression(t, cs, p, "t2", 32)
	claim2, err := cs.ReserveCompression(context.Background(), p, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d2, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2))
	wg.Add(3)
	for range 3 {
		go func() {
			defer wg.Done()
			_ = cs.Delete(context.Background(), p, "t2")
		}()
	}
	wg.Wait()
	stats = cs.CompressionStats()
	assert.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
}

func TestCompression_FIFOEviction_ClearsOptionalAndPreservesCounters(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	opts := defaultStoreOptions(now)
	opts.MaxTurnsPerSession = 2
	opts.MaxSessionBytes = 1024
	opts.CompressionLimits = limits
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs := st.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-fifo")
	policy := "v1"
	// append two with surrogates
	for i := range 2 {
		id := fmt.Sprintf("t%d", i)
		d := appendArtifactForCompression(t, cs, p, id, 32)
		claim, err := cs.ReserveCompression(context.Background(), p, id, d, policy, semanticDigestFor(policy), egressHashFor(policy))
		require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
		require.NoError(t, err)
		require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID(fmt.Sprintf("job%d", i))))
		sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
		require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID(fmt.Sprintf("job%d", i)), sur))
	}
	stats := cs.CompressionStats()
	require.Equal(t, 4, stats.TotalSurrogateBytes)
	// third append evicts oldest original; optional must be cleared and decremented exactly once
	d3 := digestFor("t2")
	// Actually append will evict t0
	art := sampleArtifact("t2", "payload", 32)
	_, err = cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	stats = cs.CompressionStats()
	assert.Equal(t, 2, stats.TotalSurrogateBytes, "FIFO eviction of original must decrement surrogate exactly once")
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	// t0 evicted, remaining are t1 and t2 (t2 has no surrogate)
	found := map[string]bool{}
	for _, a := range snap {
		found[a.ID] = true
	}
	assert.False(t, found["t0"])
	_ = d3
}

func TestCompression_OptionalPressureNeverEvictsOriginal(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        1,
		MaxPendingTotal:             1,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 5,
		MaxSurrogateBytesTotal:      5,
	}
	opts := defaultStoreOptions(now)
	opts.MaxTurnsPerSession = 4
	opts.MaxSessionBytes = 1024
	opts.CompressionLimits = limits
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs := st.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-never-evict")
	policy := "v1"
	// fill surrogate aggregate
	d1 := appendArtifactForCompression(t, cs, p, "t1", 32)
	claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d1, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d1, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hello", Bytes: 5})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	// second artifact original appended should remain even though surrogate budget exhausted
	d2 := appendArtifactForCompression(t, cs, p, "t2", 32)
	claim2, err := cs.ReserveCompression(context.Background(), p, "t2", d2, policy, semanticDigestFor(policy), egressHashFor(policy))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor(policy), semanticDigestFor(policy), egressHashFor(policy), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	// reserve may succeed for pending? But pendingPerSession limit is 1, so after surrogate attached pending=0, so reserve may succeed. Let's check.
	// Actually pendingPerSession is 0 now, so reserve should succeed.
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d2, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "world", Bytes: 5})
	err = cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsBudgetError(err))
	// originals must both remain
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, snap, 2)
	ids := map[string]bool{}
	for _, a := range snap {
		ids[a.ID] = true
	}
	assert.True(t, ids["t1"])
	assert.True(t, ids["t2"])
	// ensure FIFO not triggered by optional pressure
	assert.Equal(t, 5, cs.CompressionStats().TotalSurrogateBytes)
}

func TestCompression_Replacement_NoDrift(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-replace-nodrift")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	// v1
	claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor("v1"), semanticDigestFor("v1"), egressHashFor("v1"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "0123456789", Bytes: 10})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	stats := cs.CompressionStats()
	require.Equal(t, 0, stats.TotalPending)
	require.Equal(t, 10, stats.TotalSurrogateBytes)
	// re-compression path: reserve v2 while surrogate v1 exists (different revision) => allowed
	claim2, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v2", semanticDigestFor("v2"), egressHashFor("v2"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor("v2"), semanticDigestFor("v2"), egressHashFor("v2"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err, "re-compression with different policy revision should be allowed")
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d, "v2", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2))
	stats = cs.CompressionStats()
	assert.Equal(t, 2, stats.TotalSurrogateBytes, "replacement must delta-account: 10 replaced by 2 => total 2 not 12")
	foundBytes := 0
	for _, v := range stats.SurrogateBytesPerSession {
		foundBytes = v
	}
	assert.Equal(t, 2, foundBytes, "per-session bytes must be 2")
	assert.Equal(t, 0, stats.TotalPending, "pending must be back to 0 after replacement attach")
	state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Surrogate)
	assert.Equal(t, "v2", state.Surrogate.PolicyRevision)
	assert.Equal(t, 2, state.Surrogate.Bytes)
	assert.Nil(t, state.Pending, "pending must be nil after replacement")
}

func TestCompression_Replacement_BudgetCredit(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	tests := []struct {
		name          string
		newBytes      int
		shouldSucceed bool
	}{
		{"credit_succeeds_with_smaller_replacement", 6, true},
		{"credit_rejects_larger_replacement", 14, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			limits := reasoningpreservation.CompressionLimits{
				MaxPendingPerSession:        10,
				MaxPendingTotal:             10,
				MaxSurrogateBytesPerTurn:    100,
				MaxSurrogateBytesPerSession: 10,
				MaxSurrogateBytesTotal:      1000,
			}
			cs := newCompressionStore(t, now, limits)
			p := reasoningpreservation.NewSessionPartition("sess-budget-credit-" + tc.name)
			d := appendArtifactForCompression(t, cs, p, "t1", 32)
			claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
			require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor("v1"), semanticDigestFor("v1"), egressHashFor("v1"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
			require.NoError(t, err)
			require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
			sur1 := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "0123456789", Bytes: 10})
			require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
			// per-session limit L=10 fully consumed; re-compress with v2
			claim2, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v2", semanticDigestFor("v2"), egressHashFor("v2"))
			require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor("v2"), semanticDigestFor("v2"), egressHashFor("v2"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
			require.NoError(t, err)
			require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
			sur2 := surrogateFor(d, "v2", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "x", Bytes: tc.newBytes})
			// Fill text to match bytes if needed
			if tc.newBytes > len(sur2.Segments[0].Text) {
				// adjust segment text length for realism, not required for byte count
				sur2.Segments[0].Text = string(make([]byte, tc.newBytes))
			}
			err = cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2)
			if tc.shouldSucceed {
				require.NoError(t, err, "cur-old+new = 6 <=10 should succeed")
				stats := cs.CompressionStats()
				assert.Equal(t, tc.newBytes, stats.TotalSurrogateBytes)
			} else {
				require.Error(t, err, "cur-old+new = 14 >10 should be rejected")
				var be *reasoningpreservation.BudgetError
				require.ErrorAs(t, err, &be)
				assert.Equal(t, reasoningpreservation.BudgetSurrogatePerSession, be.Kind)
				// original surrogate must remain intact (10 bytes)
				stats := cs.CompressionStats()
				assert.Equal(t, 10, stats.TotalSurrogateBytes, "failed replacement must not mutate counters")
				state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
				require.NoError(t, err)
				require.True(t, ok)
				require.NotNil(t, state.Surrogate)
				assert.Equal(t, "v1", state.Surrogate.PolicyRevision)
			}
		})
	}
}

func TestCompression_Reserve_SameRevisionDuplicateRejected(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-same-rev")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor("v1"), semanticDigestFor("v1"), egressHashFor("v1"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	_, err = cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err), "same-revision duplicate reserve after attach must be conflict")
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 2, stats.TotalSurrogateBytes)
}

func TestCompression_Reserve_WhilePendingRejected(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-pending-conflict")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egressHashFor("v1"), semanticDigestFor("v1"), egressHashFor("v1"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	// while pending exists, any revision must be conflict
	_, err = cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	_, err = cs.ReserveCompression(context.Background(), p, "t1", d, "v2", semanticDigestFor("v2"), egressHashFor("v2"))
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	// pending count must remain 1
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	// clean up via attach to avoid leak
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
}

func TestCompression_Replacement_ConcurrentClearDeleteExactlyOnce(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        10,
		MaxPendingTotal:             10,
		MaxSurrogateBytesPerTurn:    100,
		MaxSurrogateBytesPerSession: 1000,
		MaxSurrogateBytesTotal:      1000,
	}
	cs := newCompressionStore(t, now, limits)
	p := reasoningpreservation.NewSessionPartition("sess-conc-replace")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	// v1 attach 10
	claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, egressHashFor("v1"), semanticDigestFor("v1"), egressHashFor("v1"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "0123456789", Bytes: 10})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	// v2 replacement 2
	claim2, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v2", semanticDigestFor("v2"), egressHashFor("v2"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, egressHashFor("v2"), semanticDigestFor("v2"), egressHashFor("v2"), reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, err)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateFor(d, "v2", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2))
	stats := cs.CompressionStats()
	require.Equal(t, 2, stats.TotalSurrogateBytes)
	require.Equal(t, 0, stats.TotalPending)
	// concurrent Clear/Delete interleavings must decrement exactly once and zero stats
	var wg sync.WaitGroup
	wg.Add(8)
	for range 4 {
		go func() {
			defer wg.Done()
			_ = cs.ClearCompression(context.Background(), p, "t1", "")
		}()
	}
	for range 4 {
		go func() {
			defer wg.Done()
			_ = cs.Delete(context.Background(), p, "t1")
		}()
	}
	wg.Wait()
	stats = cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending, "pending must be 0 after concurrent clear/delete")
	assert.Equal(t, 0, stats.TotalSurrogateBytes, "surrogate bytes must be 0 after delete following replacement (exactly once decrement)")
	assert.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
	// also verify GetCompressionState gone or empty
	_, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	// state may be absent after delete/clear; if present, pending/surrogate nil
	if ok {
		// if entry still exists (clear leaves no entry), ensure not leaking counts
		assert.Equal(t, 0, stats.TotalSurrogateBytes)
	}
}

// Ensure auxiliary import used
var (
	_ = auxiliary.JobID("")
	_ = aipapi.Part{}
)
