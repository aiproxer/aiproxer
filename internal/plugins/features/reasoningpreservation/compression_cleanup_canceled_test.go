//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingStore records the context passed to ClearCompression for inspection.
type capturingStore struct {
	reasoningpreservation.CompressionStore
	capturedCtx           context.Context
	capturedErrAtCall     error
	capturedPartition     reasoningpreservation.SessionPartition
	capturedArtifactID    string
	capturedReservationID string
	clearCalls            int
}

func (c *capturingStore) ClearCompression(ctx context.Context, p reasoningpreservation.SessionPartition, artifactID string, expectedReservationID string) error {
	c.capturedCtx = ctx
	c.capturedErrAtCall = ctx.Err()
	c.capturedPartition = p
	c.capturedArtifactID = artifactID
	c.capturedReservationID = expectedReservationID
	c.clearCalls++
	return c.CompressionStore.ClearCompression(ctx, p, artifactID, expectedReservationID)
}

// blockingStore simulates a slow ClearCompression that respects ctx.Done.
type blockingStore struct {
	reasoningpreservation.CompressionStore
	delay       time.Duration
	capturedCtx context.Context
}

func (b *blockingStore) ClearCompression(ctx context.Context, p reasoningpreservation.SessionPartition, artifactID string, expectedReservationID string) error {
	b.capturedCtx = ctx
	select {
	case <-time.After(b.delay):
		return b.CompressionStore.ClearCompression(context.Background(), p, artifactID, expectedReservationID)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func canceledCtxWithScope(t *testing.T, sc scope.PrincipalScopeView) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = scope.WithScope(ctx, sc)
	cancel()
	require.Error(t, ctx.Err())
	return ctx
}

// Test 1: canceled parent during egress deny still clears pending + original retained + no submit.
func TestCanceledCleanup_EgressDeny_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-canceled-deny")
	sensitive := "payload " + sensitiveToken
	art := sensitiveArtifact("art-cancel-deny", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	countingClient := &countingBackground{}
	svc := reasoningpreservation.CompressionServices{
		Client:       countingClient,
		Poller:       countingClient,
		EgressPolicy: fakeDenyPolicy{version: "vDeny"},
		Sanitizer:    redactingSanitizer{},
	}
	called := false
	next := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error { called = true; return nil }
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	sc := scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-cancel")}
	ctx := canceledCtxWithScope(t, sc)
	require.NoError(t, egress(ctx, res))
	assert.False(t, called)
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending, "canceled deny must still clear pending via cleanup context")
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1, "original must remain")
	assert.Equal(t, 0, countingClient.SubmitCount())
}

// Test1 sanitizer error path with canceled parent
func TestCanceledCleanup_EgressSanitizerError_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-sanitizer")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-cancel-san", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "v1", sanitizer: redactingSanitizer{}},
		Sanitizer:    errorSanitizer{},
	}
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, egress(ctx, res))
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1)
}

// Prepare oversize after redaction with canceled parent
func TestCanceledCleanup_EgressPrepareOversize_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	sensitive := "prefix " + sensitiveToken + " suffix"
	sanitizedLen := len(strings.ReplaceAll(sensitive, sensitiveToken, "[REDACTED]"))
	cfg := egressCfgWithLimits(t, sanitizedLen-1, 0)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-oversize")
	art := sensitiveArtifact("art-cancel-oversize", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "v1", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, egress(ctx, res))
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

// Test 3: submit build/error/empty/bind failure with canceled parent
func TestCanceledCleanup_SubmitBuildError_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-build")
	badSegs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "   "}}
	pr, snapArt := reservationForSubmit(t, cs, p, "art-cancel-build", cfg, badSegs)
	pr.Segments = badSegs
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, stage(ctx, pr))
	assert.Equal(t, 0, fake.SubmitCount())
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
}

func TestCanceledCleanup_SubmitError_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-submit-err")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-cancel-submit-err", cfg, nil)
	fake := &submitStageFake{err: assert.AnError}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, stage(ctx, pr))
	assert.Equal(t, 1, fake.SubmitCount())
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
}

func TestCanceledCleanup_SubmitEmptyJob_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-empty")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-cancel-empty", cfg, nil)
	fake := &submitStageFake{emptyJob: true}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, stage(ctx, pr))
	assert.Equal(t, 1, fake.SubmitCount())
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok)
}

func TestCanceledCleanup_SubmitBindFailure_ClearsAndForgetOnceDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-bind")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-cancel-bind", cfg, nil)
	// tamper claim to cause bind failure
	pr.Reservation.Claim.OriginalDigest = sha256.Sum256([]byte("tampered"))
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, stage(ctx, pr))
	assert.Equal(t, 1, fake.SubmitCount())
	assert.Equal(t, 1, fake.ForgetCount(), "bind failure must Forget exactly once even with canceled parent")
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
}

// Test 4: raw guard oversize with canceled parent - direct raw guard hook (bypass poll cancellation)
func TestCanceledCleanup_RawGuardOversize_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-raw")
	// Use non-poll setup: reserve directly and create candidate with oversize
	_, arts := missingRestoreFixture(t)
	_, _ = setupPollPendingForFixture(t, cs, p, arts[0])
	oversizeText := strings.Repeat("a", cfg.Compression.MaxOutputBytes+10)
	var c aipapi.Collected
	c.Text.WriteString(oversizeText)
	c.FinishReceived = true
	st, ok, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.True(t, ok)
	require.NotNil(t, st.Pending)
	cand := &reasoningpreservation.CompletedPollCandidate{
		Partition: p, ArtifactID: arts[0].ID, ReservationID: st.Pending.ReservationID, JobID: st.Pending.JobID, Collected: c,
	}
	poller := &pollTestPoller{}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	tel := reasoningpreservation.NewTelemetry()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := arts[0] // dummy
	aipCall := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible")}}}}
	res := reasoningpreservation.CompressionPollAttemptResult{Kind: reasoningpreservation.PollKindCompleted, Candidate: cand}
	ar := reasoningpreservation.HandleCompletedPollCandidateForTest(ctx, cfg, cs, svc, tel, res, aipCall)
	require.Equal(t, reasoningpreservation.AdoptionOutcomeRawOversize, ar.Outcome)
	require.Equal(t, 1, poller.ForgetCount(), "oversize must Forget once despite canceled parent")
	st2, found, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	if found && st2.Pending != nil {
		t.Fatalf("pending must be cleared despite canceled parent, got %v", st2.Pending)
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 0, stats.TotalSurrogateBytes)
	_ = call
}

// Completed decode invalid with canceled parent - direct decoder stage
func TestCanceledCleanup_AdoptionDecodeInvalid_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-decode")
	longText := strings.Repeat("a", 100)
	art := longArtifact("art-cancel-decode", longText)
	jobID, resID := setupAdoptionPendingCorrect(t, cs, p, art, cfg)
	rawObj := map[string]any{"schema_version": 1, "segments": "not-array"}
	raw, _ := json.Marshal(rawObj)
	poller := &pollTestPoller{}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	stage := reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, reasoningpreservation.NewTelemetry())
	cand := &reasoningpreservation.CompletedPollCandidate{Partition: p, ArtifactID: art.ID, ReservationID: resID, JobID: jobID, Collected: aipapi.Collected{}}
	arIn := reasoningpreservation.AdoptionResult{Outcome: reasoningpreservation.AdoptionOutcomeBoundedRaw, Candidate: cand, BoundedRaw: raw, RawByteCount: len(raw)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	arOut := stage(ctx, arIn)
	require.NotNil(t, arOut)
	require.Equal(t, 1, poller.ForgetCount(), "decode invalid must Forget once despite canceled parent")
	st, ok, _ := cs.GetCompressionState(context.Background(), p, art.ID)
	if ok && st.Pending != nil {
		t.Fatalf("pending must be cleared on decode invalid despite canceled parent")
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	_ = resID
}

func TestCanceledCleanup_AdoptionStale_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-stale")
	longText := strings.Repeat("a", 100)
	art := longArtifact("art-cancel-stale", longText)
	jobID, resID := setupAdoptionPendingCorrect(t, cs, p, art, cfg)
	// Use wrong route hash via config change to trigger stale
	cfgBad := cfg
	cfgBad.Compression.Route = "different-route"
	rawObj := map[string]any{"schema_version": 1, "segments": []map[string]any{{"index": 0, "text": strings.Repeat("b", 10)}}}
	raw, _ := json.Marshal(rawObj)
	poller := &pollTestPoller{}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	stage := reasoningpreservation.NewDecoderAdoptionStage(cfgBad, cs, svc, reasoningpreservation.NewTelemetry())
	cand := &reasoningpreservation.CompletedPollCandidate{Partition: p, ArtifactID: art.ID, ReservationID: resID, JobID: jobID, Collected: aipapi.Collected{}}
	arIn := reasoningpreservation.AdoptionResult{Outcome: reasoningpreservation.AdoptionOutcomeBoundedRaw, Candidate: cand, BoundedRaw: raw, RawByteCount: len(raw)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	arOut := stage(ctx, arIn)
	_ = arOut
	require.Equal(t, 1, poller.ForgetCount(), "stale must Forget once despite canceled parent")
	st, ok, _ := cs.GetCompressionState(context.Background(), p, art.ID)
	if ok && st.Pending != nil {
		t.Fatalf("stale must clear pending despite canceled parent")
	}
}

func TestCanceledCleanup_AdoptionBudget_ClearsDespiteCanceledParent(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cfg.Compression.MaxSurrogateBytesPerSession = 5
	cfg.Compression.MaxSurrogateBytes = 20
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-cancel-budget")
	longText := strings.Repeat("a", 100)
	art := longArtifact("art-cancel-budget", longText)
	jobID, resID := setupAdoptionPendingCorrect(t, cs, p, art, cfg)
	rawObj := map[string]any{"schema_version": 1, "segments": []map[string]any{{"index": 0, "text": strings.Repeat("b", 10)}}}
	raw, _ := json.Marshal(rawObj)
	poller := &pollTestPoller{}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	stage := reasoningpreservation.NewDecoderAdoptionStage(cfg, cs, svc, reasoningpreservation.NewTelemetry())
	cand := &reasoningpreservation.CompletedPollCandidate{Partition: p, ArtifactID: art.ID, ReservationID: resID, JobID: jobID, Collected: aipapi.Collected{}}
	arIn := reasoningpreservation.AdoptionResult{Outcome: reasoningpreservation.AdoptionOutcomeBoundedRaw, Candidate: cand, BoundedRaw: raw, RawByteCount: len(raw)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	arOut := stage(ctx, arIn)
	_ = arOut
	require.Equal(t, 1, poller.ForgetCount(), "budget must Forget once despite canceled parent")
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 0, stats.TotalSurrogateBytes)
}

// Test 5: cleanup context preserves trusted scope and has deadline <= 2s
func TestCanceledCleanup_ContextPreservesValuesAndDeadline(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	storeOpts := reasoningpreservation.StoreOptions{
		TTL: 1 * 3600 * 1e9, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144,
		Now:               func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		CompressionLimits: cfg.Compression.ToLimits(),
	}
	st, _ := reasoningpreservation.NewMemoryTurnStore(storeOpts)
	cs := st.(reasoningpreservation.CompressionStore)
	p := reasoningpreservation.NewSessionPartition("sess-cleanup-ctx")
	art := sensitiveArtifact("art-ctx", "payload", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	capt := &capturingStore{CompressionStore: cs}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeDenyPolicy{version: "v1"},
		Sanitizer:    redactingSanitizer{},
	}
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, capt, svc, nil)
	sc := scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-preserve")}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = scope.WithScope(ctx, sc)
	deadlineCtx, cancel2 := context.WithDeadline(ctx, time.Now().Add(24*time.Hour))
	defer cancel2()
	cancel() // cancel parent but WithoutCancel should preserve values and not be canceled
	require.Error(t, ctx.Err())
	require.Error(t, deadlineCtx.Err())
	// Use deadlineCtx as parent which is canceled but has values
	require.NoError(t, egress(deadlineCtx, res))
	require.NotNil(t, capt.capturedCtx)
	assert.NoError(t, capt.capturedErrAtCall, "cleanup ctx must not be canceled at call time")
	if sc2, ok := scope.ScopeFromContext(capt.capturedCtx); ok {
		assert.Equal(t, "user-preserve", sc2.PrincipalID.String())
	} else {
		t.Fatalf("cleanup ctx must preserve scope")
	}
	deadline, ok := capt.capturedCtx.Deadline()
	require.True(t, ok, "cleanup ctx must have deadline")
	// Deadline is 2s from cleanup start; after egress the deadline may be slightly in past due to defer cancel,
	// so check absolute duration from creation, not remaining.
	assert.LessOrEqual(t, time.Until(deadline)+500*time.Millisecond, 2*time.Second+500*time.Millisecond, "deadline must be <= hard timeout 2s+drift")
	// Also ensure deadline is not zero and was within 2s window
	assert.Greater(t, deadline.Sub(time.Now().Add(-3*time.Second)), 0*time.Second)
	assert.Equal(t, 1, capt.clearCalls)
	assert.Equal(t, res.Claim.ReservationID, capt.capturedReservationID, "CAS reservation ID must be preserved")
}

// Test 6: blocking store respects timeout and does not hang
func TestCanceledCleanup_BlockingStoreRespectsTimeout(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-blocking")
	art := sensitiveArtifact("art-blocking", "eligible blocking payload long enough", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	// Blocking store with long delay
	blocking := &blockingStore{CompressionStore: cs, delay: 5 * time.Second}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeDenyPolicy{version: "v1"},
		Sanitizer:    redactingSanitizer{},
	}
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, blocking, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.NoError(t, egress(ctx, res))
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 3*time.Second, "blocking store must be cut by 2s cleanup timeout, not hang 5s")
	assert.Less(t, elapsed, 4*time.Second)
	// Even though store blocked, the egress stage should return quickly (fail-open)
}

// Test 7: Stale cleanup does not overwrite newer pending
func TestCanceledCleanup_StaleClearPreservesNewPending(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-stale-cleanup-preserves")
	art := sensitiveArtifact("art-stale", "eligible stale cleanup payload long enough", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res1 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res1.Outcome)
	// Simulate stale replacement: clear res1 and reserve res2
	require.NoError(t, cs.ClearCompression(context.Background(), p, snap[0].ID, res1.Claim.ReservationID))
	corr2 := buildTestCorrelation(p, snap[0], cfg)
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr2)
	require.Equal(t, reasoningpreservation.ReservationReserved, res2.Outcome)
	// Now attempt egress with stale res1 and canceled ctx - should NOT delete res2 due to CAS
	capt := &capturingStore{CompressionStore: cs}
	// Use non-deny to trigger CAS promotion failure path; easiest is stale provisional hash
	staleCorr := res1.Correlation
	staleCorr.EgressPolicyRefHash = sha256.Sum256([]byte("stale"))
	staleRes := reasoningpreservation.ReservationResult{Outcome: res1.Outcome, Claim: res1.Claim, Correlation: staleCorr}
	svc := reasoningpreservation.CompressionServices{Client: &fakeBackground{}, Poller: &fakeBackground{}, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: redactingSanitizer{}}
	egress := reasoningpreservation.NewPostReservationEgressStage(cfg, capt, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, egress(ctx, staleRes))
	// Verify replacement still present
	st, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, st.Pending)
	assert.Equal(t, res2.Claim.ReservationID, st.Pending.ReservationID, "stale cleanup must not delete replacement")
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
}

// ensure imports used
var (
	_ = atomic.Int32{}
	_ = response.StreamMeta{}
	_ = session.SessionView{}
	_ = json.RawMessage{}
	_ = request.AttemptMeta{}
)
