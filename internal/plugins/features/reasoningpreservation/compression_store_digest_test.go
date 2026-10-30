package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Table-driven digest CAS: correct digests pass; mismatches and zero rejected.
func TestCompression_DigestCAS(t *testing.T) {
	t.Parallel()
	baseSem := semanticDigestFor("v1")
	baseEg := egressHashFor("v1")
	wrongSem := digestFor("wrong-semantic")
	wrongEg := digestFor("wrong-egress")
	var zero [32]byte

	tests := []struct {
		name       string
		surSem     [32]byte
		surEg      [32]byte
		wantErr    bool
		wantIsConf bool
	}{
		{"correct_digests_pass", baseSem, baseEg, false, false},
		{"wrong_semantic_digest_rejected", wrongSem, baseEg, true, true},
		{"wrong_egress_hash_rejected", baseSem, wrongEg, true, true},
		{"zero_semantic_digest_rejected", zero, baseEg, true, true},
		{"zero_egress_still_rejected_via_sem", zero, zero, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
			cs := newCompressionStore(t, now, compressionLimitsForTest())
			p := reasoningpreservation.NewSessionPartition("sess-digest-" + tc.name)
			d := appendArtifactForCompression(t, cs, p, "t1", 32)
			policy := "v1"
			// For zero-sem cases, reserve still stores correct digests; attach provides tc digests.
			// For the success case, attach digests match reservation.
			// For the zero-reservation path, we test separate case below.
			claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, baseSem, baseEg)
			require.NoError(t, err)
			require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, baseEg, baseSem, baseEg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
			require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
			sur := surrogateForWithDigests(d, policy, tc.surSem, tc.surEg, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
			err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantIsConf {
					assert.True(t, reasoningpreservation.IsConflictError(err))
				}
				// counters must not drift on failed attach
				stats := cs.CompressionStats()
				assert.Equal(t, 1, stats.TotalPending, "pending must remain on conflict")
				assert.Equal(t, 0, stats.TotalSurrogateBytes, "no surrogate bytes on conflict")
			} else {
				require.NoError(t, err)
				stats := cs.CompressionStats()
				assert.Equal(t, 0, stats.TotalPending)
				assert.Equal(t, 2, stats.TotalSurrogateBytes)
				state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
				require.NoError(t, err)
				require.True(t, ok)
				require.NotNil(t, state.Surrogate)
				assert.Equal(t, baseSem, state.Surrogate.SemanticDigest)
				assert.Equal(t, baseEg, state.Surrogate.EgressPolicyHash)
			}
		})
	}
}

// Zero semantic digest stored at reserve must also be rejected at attach.
func TestCompression_ZeroReservedDigestRejectedAtAttach(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-zero-reserved")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	var zero [32]byte
	eg := egressHashFor(policy)
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, zero, eg)
	require.NoError(t, err, "reserve may accept zero but attach must reject")
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, zero, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateForWithDigests(d, policy, zero, eg, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
}

// Digests survive GetCompressionState read with defensive copies.
func TestCompression_DigestDefensiveCopy(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-digest-copy")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	sem := semanticDigestFor(policy)
	eg := egressHashFor(policy)
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, sem, eg)
	require.NoError(t, err)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	// Pending defensive copy
	state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, sem, state.Pending.SemanticDigest)
	assert.Equal(t, eg, state.Pending.EgressPolicyHash)
	// mutate returned copy
	state.Pending.SemanticDigest[0] ^= 0xFF
	state.Pending.EgressPolicyHash[0] ^= 0xFF
	state2, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sem, state2.Pending.SemanticDigest, "pending semantic digest defensive copy")
	assert.Equal(t, eg, state2.Pending.EgressPolicyHash, "pending egress hash defensive copy")

	// also verify surrogate defensive copy after attach
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	sur := surrogateForWithDigests(d, policy, sem, eg, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hello", Bytes: 5})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur))
	state3, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state3.Surrogate)
	assert.Equal(t, sem, state3.Surrogate.SemanticDigest)
	assert.Equal(t, eg, state3.Surrogate.EgressPolicyHash)
	state3.Surrogate.SemanticDigest[0] ^= 0xFF
	state3.Surrogate.Segments[0].Text = "mutated"
	state4, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sem, state4.Surrogate.SemanticDigest, "surrogate digest defensive copy")
	assert.Equal(t, "hello", state4.Surrogate.Segments[0].Text, "surrogate segment defensive copy")
}

// Correct digests with replacement still delta-accounts.
func TestCompression_DigestWithReplacement(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-digest-replace")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	// v1
	sem1 := semanticDigestFor("v1")
	eg1 := egressHashFor("v1")
	claim1, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v1", sem1, eg1)
	require.NoError(t, err)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim1, eg1, sem1, eg1, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim1, auxiliary.JobID("job1")))
	sur1 := surrogateForWithDigests(d, "v1", sem1, eg1, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "0123456789", Bytes: 10})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim1, auxiliary.JobID("job1"), sur1))
	// v2 replacement
	sem2 := semanticDigestFor("v2")
	eg2 := egressHashFor("v2")
	claim2, err := cs.ReserveCompression(context.Background(), p, "t1", d, "v2", sem2, eg2)
	require.NoError(t, err)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, eg2, sem2, eg2, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job2")))
	sur2 := surrogateForWithDigests(d, "v2", sem2, eg2, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	require.NoError(t, cs.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job2"), sur2))
	state, ok, err := cs.GetCompressionState(context.Background(), p, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Surrogate)
	assert.Equal(t, sem2, state.Surrogate.SemanticDigest)
	assert.Equal(t, eg2, state.Surrogate.EgressPolicyHash)
}
