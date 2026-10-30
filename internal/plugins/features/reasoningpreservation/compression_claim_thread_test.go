package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompression_ClaimThread(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, compressionLimitsForTest())
	p := reasoningpreservation.NewSessionPartition("sess-claim-thread")
	d := appendArtifactForCompression(t, cs, p, "t1", 32)
	policy := "v1"
	semDigest := semanticDigestFor(policy)
	egHash := egressHashFor(policy)

	// 1. Reserve returns populated CompressionClaim value matching inputs.
	claim, err := cs.ReserveCompression(context.Background(), p, "t1", d, policy, semDigest, egHash)
	require.NoError(t, err)
	assert.Equal(t, p, claim.Partition)
	assert.Equal(t, "t1", claim.ArtifactID)
	assert.NotEmpty(t, claim.ReservationID)
	assert.Equal(t, d, claim.OriginalDigest)
	assert.Equal(t, policy, claim.PolicyRevision)

	// 2. UpdateReservationPolicyHash with that claim succeeds (promotes to authoritative).
	authHash := sha256.Sum256([]byte("auth-hash"))
	routeHash := sha256.Sum256([]byte("test-route"))
	err = cs.UpdateReservationPolicyHash(context.Background(), claim, egHash, semDigest, authHash, reasoningpreservation.SanitizationNone, routeHash)
	require.NoError(t, err)

	// 3. BindCompressionJob with that claim succeeds.
	jobID := auxiliary.JobID("job-thread-1")
	err = cs.BindCompressionJob(context.Background(), claim, jobID)
	require.NoError(t, err)

	// 4. BindCompressionJob with a stale ReservationID in the claim fails with exact legacy "reservation mismatch".
	staleClaim := reasoningpreservation.CompressionClaim{
		Partition:      claim.Partition,
		ArtifactID:     claim.ArtifactID,
		ReservationID:  "stale-res-id",
		OriginalDigest: claim.OriginalDigest,
		PolicyRevision: claim.PolicyRevision,
	}
	err = cs.BindCompressionJob(context.Background(), staleClaim, jobID)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "reservation mismatch")

	// 5. Zero-valued claim returns missing fields conflict error for all mutation operations.
	var zeroClaim reasoningpreservation.CompressionClaim
	err = cs.UpdateReservationPolicyHash(context.Background(), zeroClaim, egHash, semDigest, authHash, reasoningpreservation.SanitizationNone, routeHash)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "missing update fields")

	err = cs.BindCompressionJob(context.Background(), zeroClaim, jobID)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "missing binding fields")

	sur := surrogateFor(d, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
	sur.EgressPolicyHash = authHash
	err = cs.AttachSurrogate(context.Background(), zeroClaim, jobID, sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "missing attach fields")

	// 6. Corrupted-claim rejections in AttachSurrogate:
	// Altered OriginalDigest -> "digest mismatch"
	corruptDigestClaim := claim
	corruptDigestClaim.OriginalDigest = sha256.Sum256([]byte("corrupted-original-digest"))
	err = cs.AttachSurrogate(context.Background(), corruptDigestClaim, jobID, sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "digest mismatch")

	// Altered PolicyRevision -> "policy mismatch"
	corruptPolicyClaim := claim
	corruptPolicyClaim.PolicyRevision = "corrupted-policy-revision"
	err = cs.AttachSurrogate(context.Background(), corruptPolicyClaim, jobID, sur)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	assert.Contains(t, err.Error(), "policy mismatch")

	// Fully correct claim attaches successfully (happy attach)
	err = cs.AttachSurrogate(context.Background(), claim, jobID, sur)
	require.NoError(t, err)
}

func TestCompression_ClaimLifecycle_ProductionStages(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	tel := reasoningpreservation.NewTelemetry()
	p := reasoningpreservation.NewSessionPartition("sess-claim-lifecycle-prod")
	longText := strings.Repeat("a", 100)
	art := longArtifact("art-lifecycle-prod", longText)
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)

	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, snap, 1)

	corr := buildTestCorrelation(p, snap[0], cfg)

	// Raw surrogate response from compressor
	rawObj := map[string]any{"schema_version": 1, "segments": []map[string]any{{"index": 0, "text": strings.Repeat("b", 10)}}}
	raw, err := json.Marshal(rawObj)
	require.NoError(t, err)
	var c aipapi.Collected
	c.Text.WriteString(string(raw))
	c.FinishReceived = true

	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{
		Client:       poller,
		Poller:       poller,
		EgressPolicy: pollTestEgress{},
		Sanitizer:    pollTestSan{},
	}

	// 1. TryReserveCompression -> returns ReservationResult containing Claim
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.True(t, res.IsReserved())
	require.NotEmpty(t, res.Claim.ReservationID)
	require.Equal(t, p, res.Claim.Partition)
	require.Equal(t, art.ID, res.Claim.ArtifactID)

	// 2. Post-reservation egress promote stage chained to post-egress submit stage
	submitStage := reasoningpreservation.NewPostEgressSubmitStageWithTelemetry(cfg, cs, svc, tel)
	egressStage := reasoningpreservation.NewPostReservationEgressStageWithTelemetry(cfg, cs, svc, submitStage, tel)

	// Execute egress stage with res (which carries the threaded Claim into submit stage)
	err = egressStage(context.Background(), res)
	require.NoError(t, err)

	// Verify store state after submit: pending exists, authoritative is true, JobID is bound
	stBeforePoll, ok, err := cs.GetCompressionState(context.Background(), p, art.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, stBeforePoll.Pending)
	require.True(t, stBeforePoll.Pending.PolicyHashAuthoritative)
	require.NotEmpty(t, stBeforePoll.Pending.JobID)

	// 3. Adoption stage via attempt transform (HandleAttempt with decoder adoption stage)
	decoderStage := reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, tel)
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svc, reasoningpreservation.CompanionPolicy{}, decoderStage, tel)

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible answer")}}}}
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID:     "be",
		Model:         "m",
		ReplaySupport: pollTestSupport,
		Session:       session.SessionView{AuthoritativeSessionID: "sess-claim-lifecycle-prod"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)

	// 4. Assert surrogate attached and pending cleared
	stAfterPoll, ok, err := cs.GetCompressionState(context.Background(), p, art.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, stAfterPoll.Pending, "pending must be cleared after adoption attach")
	require.NotNil(t, stAfterPoll.Surrogate, "surrogate must be attached")
	assert.Equal(t, 10, stAfterPoll.Surrogate.Bytes)
}
