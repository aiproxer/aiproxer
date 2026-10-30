//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helpers for egress stage tests

func egressCfgWithLimits(t *testing.T, maxInputBytes, maxInputTokens int) reasoningpreservation.Config {
	t.Helper()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MaxInputBytes = maxInputBytes
	cfg.Compression.MaxInputTokens = maxInputTokens
	cfg.Compression.MinSourceBytes = 1
	return cfg
}

func egressStoreForTest(t *testing.T, cfg reasoningpreservation.Config) reasoningpreservation.CompressionStore {
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
	cs, ok := st.(reasoningpreservation.CompressionStore)
	require.True(t, ok)
	return cs
}

func sensitiveArtifact(id, sensitiveText string, now time.Time) reasoningpreservation.TurnArtifact {
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, sensitiveText, "", nil)
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         [32]byte{1, 2, 3},
		SourceBackend:  "backend",
		SourceModel:    "model",
		Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt:      now,
		ReasoningBytes: len(sensitiveText),
	}
}

// sensitive text containing secret token that sanitizer should redact
const sensitiveToken = "sk-secret-123"

type redactingSanitizer struct{}

func (redactingSanitizer) SanitizeText(_ context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, sensitiveToken, "[REDACTED]"), nil
}

type errorSanitizer struct{}

func (errorSanitizer) SanitizeText(_ context.Context, _ string) (string, error) {
	return "", assert.AnError
}

type maliciousSanitizer struct {
	calls int
}

func (m *maliciousSanitizer) SanitizeText(_ context.Context, text string) (string, error) {
	m.calls++
	return "MALICIOUS[" + text + "]", nil
}

type countingSanitizer struct {
	calls  int
	lastIn string
}

func (c *countingSanitizer) SanitizeText(_ context.Context, text string) (string, error) {
	c.calls++
	c.lastIn = text
	return strings.ReplaceAll(text, sensitiveToken, "[REDACTED]"), nil
}

type countingBackground struct {
	submitCalls atomic.Int32
}

func (c *countingBackground) SubmitCollect(_ context.Context, _ auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	c.submitCalls.Add(1)
	return auxiliary.JobID("job-1"), nil
}

func (c *countingBackground) Await(_ context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (c *countingBackground) Forget(_ auxiliary.JobID) {}
func (c *countingBackground) Poll(_ context.Context, _ auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollPending}, nil
}
func (c *countingBackground) SubmitCount() int { return int(c.submitCalls.Load()) }

// TestEgressStage_Allow_FakeNextSeesSanitizedOnly verifies allow path passes sanitized (original) segments
// to next, promotes hash, and does not clear reservation before next.
func TestEgressStage_Allow_FakeNextSeesSanitizedOnly(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-allow")
	sensitive := "ordinary reasoning with " + sensitiveToken + " embedded"
	art := sensitiveArtifact("art-allow", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	// allow policy without redaction
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeAllowPolicy{version: "vAllow1"},
		Sanitizer:    &recordingSanitizer{replacement: "[REDACTED]"},
	}
	var got *reasoningpreservation.PreparedReservation
	var nextCalls int
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		nextCalls++
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.Equal(t, 1, nextCalls, "allow must call next")
	require.NotNil(t, got)
	require.Len(t, got.Segments, 1)
	assert.Equal(t, sensitive, got.Segments[0].Text, "allow passes original text")
	assert.Equal(t, 0, got.Segments[0].Index)
	expHash := reasoningpreservation.ComputeEgressPolicyHash(got.Decision, cfg.Compression.Route)
	assert.Equal(t, expHash, got.EgressPolicyHash)
	assert.NotEqual(t, [32]byte{}, got.EgressPolicyHash)
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.True(t, state.Pending.PolicyHashAuthoritative)
	assert.Equal(t, expHash, state.Pending.EgressPolicyHash)
	stats := cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	combined := got.Segments[0].Text
	assert.NotContains(t, combined, got.Reservation.Correlation.TraceID)
	assert.NotContains(t, combined, "sess-allow")
}

// TestEgressStage_Redact_FakeNextSeesSanitizedOnly verifies redact path sanitizes locally before accounting
// and next sees only redacted text.
func TestEgressStage_Redact_FakeNextSeesSanitizedOnly(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-redact")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-redact", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vRedact1", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got)
	require.Len(t, got.Segments, 1)
	assert.NotContains(t, got.Segments[0].Text, sensitiveToken, "next must never see unredacted sensitive")
	assert.Contains(t, got.Segments[0].Text, "[REDACTED]")
	assert.Equal(t, "prefix [REDACTED] suffix", got.Segments[0].Text)
	expHash := reasoningpreservation.ComputeEgressPolicyHash(got.Decision, cfg.Compression.Route)
	assert.Equal(t, expHash, got.EgressPolicyHash)
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.Equal(t, expHash, state.Pending.EgressPolicyHash)
}

func TestEgressStage_TrustedSanitizer_MaliciousPolicyIgnored(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-malicious")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-malicious", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	malicious := &maliciousSanitizer{}
	trusted := &countingSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vMalicious", sanitizer: malicious},
		Sanitizer:    trusted,
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got)
	assert.Equal(t, 0, malicious.calls, "malicious policy sanitizer must be ignored")
	assert.Equal(t, 1, trusted.calls, "trusted service sanitizer must be used")
	assert.NotContains(t, got.Segments[0].Text, "MALICIOUS")
	assert.Contains(t, got.Segments[0].Text, "[REDACTED]")
}

func TestEgressStage_TrustedSanitizer_NilPolicySanitizerUsesService(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-nil-policy-sanitizer")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-nil-policy", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	trusted := &countingSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactNoSanitizerPolicy{version: "vNilPolicy"},
		Sanitizer:    trusted,
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got, "nil policy sanitizer with trusted service must succeed")
	assert.Equal(t, 1, trusted.calls)
	assert.Contains(t, got.Segments[0].Text, "[REDACTED]")
	state, _, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.NotNil(t, state.Pending)
	assert.True(t, state.Pending.PolicyHashAuthoritative)
}

func TestEgressStage_TrustedSanitizer_NilServiceSanitizerClears(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-nil-service")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-nil-service", sensitive, time.Unix(1_700_000_000, 0).UTC())
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
		Sanitizer:    nil,
	}
	called := false
	next := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
		called = true
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	assert.False(t, called, "nil trusted sanitizer must cause clear, not next")
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	if ok {
		assert.Nil(t, state.Pending)
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1, "original must remain")
}

func TestEgressStage_TrustedSanitizer_ErrorServiceClears(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-error-service")
	sensitive := "prefix " + sensitiveToken + " suffix"
	art := sensitiveArtifact("art-error-service", sensitive, time.Unix(1_700_000_000, 0).UTC())
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
	called := false
	next := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
		called = true
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	assert.False(t, called)
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	if ok {
		assert.Nil(t, state.Pending)
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

// TestEgressStage_Deny_Missing_Mismatch_ClearsReservation verifies deny/missing/mismatch
// clears reservation, decrements counters, and never calls next/provider.
func TestEgressStage_Deny_Missing_Mismatch_ClearsReservation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		policy reasoningpreservation.EgressPolicy
	}{
		{"deny", fakeDenyPolicy{version: "v1"}},
		{"missing_nil", nil},
		{"mismatch_route", reasoningpreservation.NewRouteBoundEgressPolicy(map[string]struct{}{"allowed-route": {}}, fakeAllowPolicy{version: "v1"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := egressCfgWithLimits(t, 4096, 4096)
			cs := egressStoreForTest(t, cfg)
			p := reasoningpreservation.NewSessionPartition("sess-" + tc.name)
			art := sensitiveArtifact("art-"+tc.name, "sensitive "+sensitiveToken, time.Unix(1_700_000_000, 0).UTC())
			_, err := cs.Append(context.Background(), p, art)
			require.NoError(t, err)
			snap, _ := cs.Snapshot(context.Background(), p)
			corr := buildTestCorrelation(p, snap[0], cfg)
			res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
			require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
			statsBefore := cs.CompressionStats()
			require.Equal(t, 1, statsBefore.TotalPending)
			svc := reasoningpreservation.CompressionServices{
				Client:       &fakeBackground{},
				Poller:       &fakeBackground{},
				EgressPolicy: tc.policy,
				Sanitizer:    redactingSanitizer{},
			}
			called := false
			next := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
				called = true
				return nil
			}
			egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
			require.NoError(t, egressStage(context.Background(), res))
			assert.False(t, called, "denied must never call next/provider")
			state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
			if ok {
				assert.Nil(t, state.Pending, "pending must be cleared on deny")
			}
			statsAfter := cs.CompressionStats()
			assert.Equal(t, 0, statsAfter.TotalPending, "counters must be decremented")
			snap2, _ := cs.Snapshot(context.Background(), p)
			assert.Len(t, snap2, 1, "original must not be evicted on deny")
		})
	}
}

func TestClearCompression_StaleExpectedID_Conflict(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-clear-stale")
	art := sensitiveArtifact("art-clear-stale", "payload", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res1 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res1.Outcome)
	stats := cs.CompressionStats()
	require.Equal(t, 1, stats.TotalPending)
	// stale clear with wrong ID should conflict and not clear
	err = cs.ClearCompression(context.Background(), p, snap[0].ID, "wrong-id")
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending, "new pending must remain after stale clear")
	stats = cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	// correct clear should succeed
	require.NoError(t, cs.ClearCompression(context.Background(), p, snap[0].ID, res1.Claim.ReservationID))
	stats = cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	_, ok, _ = cs.GetCompressionState(context.Background(), p, snap[0].ID)
	assert.False(t, ok, "pending should be cleared")
	// reserve again new reservation
	art2 := sensitiveArtifact("art-clear-stale", "payload2", time.Unix(1_700_000_001, 0).UTC())
	// need to re-append same ID? ID same but we already cleared pending, artifact still exists, can reserve again with new ID
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res2.Outcome)
	require.NotEqual(t, res1.Claim.ReservationID, res2.Claim.ReservationID)
	// stale clear with old ID should not clear new pending
	err = cs.ClearCompression(context.Background(), p, snap[0].ID, res1.Claim.ReservationID)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	state, ok, _ = cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, res2.Claim.ReservationID, state.Pending.ReservationID, "new pending must remain")
	stats = cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	// original artifact still present
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1)
	_ = art2
}

// TestEgressStage_StaleCAS_FailOpen verifies that a stale provisional hash CAS failure
// clears reservation, fails open (no next), and decrements counters.
func TestEgressStage_StaleCAS_FailOpen(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-stale-cas")
	art := sensitiveArtifact("art-stale", "eligible stale test payload long enough", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	staleCorr := res.Correlation
	staleCorr.EgressPolicyRefHash = sha256.Sum256([]byte("stale-provisional"))
	staleRes := reasoningpreservation.ReservationResult{
		Outcome:     res.Outcome,
		Claim:       res.Claim,
		Correlation: staleCorr,
	}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeAllowPolicy{version: "v1"},
		Sanitizer:    redactingSanitizer{},
	}
	called := false
	next := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
		called = true
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), staleRes))
	assert.False(t, called, "stale CAS must fail-open without next")
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	if ok {
		assert.Nil(t, state.Pending, "stale CAS must clear pending")
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending, "stale CAS must decrement counters")
	snap2, _ := cs.Snapshot(context.Background(), p)
	assert.Len(t, snap2, 1, "original must remain on stale CAS")
}

func TestEgressStage_StaleClear_NewPendingRemains(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-stale-clear-new")
	art := sensitiveArtifact("art-stale-clear", "eligible payload", time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res1 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res1.Outcome)
	// stale CAS path clears res1; verify via egress stage with stale hash
	staleCorr := res1.Correlation
	staleCorr.EgressPolicyRefHash = sha256.Sum256([]byte("stale"))
	staleRes := reasoningpreservation.ReservationResult{Outcome: res1.Outcome, Claim: res1.Claim, Correlation: staleCorr}
	svc := reasoningpreservation.CompressionServices{Client: &fakeBackground{}, Poller: &fakeBackground{}, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: redactingSanitizer{}}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, nil)
	require.NoError(t, egressStage(context.Background(), staleRes))
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	// reserve again new pending
	corr2 := buildTestCorrelation(p, snap[0], cfg)
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr2)
	require.Equal(t, reasoningpreservation.ReservationReserved, res2.Outcome)
	stats = cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending)
	// try stale clear of old res1 ID via direct CAS clear should not clear new pending
	err = cs.ClearCompression(context.Background(), p, snap[0].ID, res1.Claim.ReservationID)
	require.Error(t, err)
	assert.True(t, reasoningpreservation.IsConflictError(err))
	state, ok, _ := cs.GetCompressionState(context.Background(), p, snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, res2.Claim.ReservationID, state.Pending.ReservationID)
	stats = cs.CompressionStats()
	assert.Equal(t, 1, stats.TotalPending, "new pending must remain")
}

// TestEgressStage_RedactionBeforeSizing verifies that input byte/token accounting
// occurs after sanitization: original too big but sanitized fits should succeed.
func TestEgressStage_RedactionBeforeSizing(t *testing.T) {
	t.Parallel()
	sensitive := "prefix " + sensitiveToken + " suffix"
	sanitizedLen := len(strings.ReplaceAll(sensitive, sensitiveToken, "[REDACTED]"))
	origLen := len(sensitive)
	require.Greater(t, origLen, sanitizedLen)
	cfg := egressCfgWithLimits(t, sanitizedLen, 0)
	require.Equal(t, sanitizedLen, cfg.Compression.MaxInputBytes)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-sizing")
	art := sensitiveArtifact("art-sizing", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vSizing", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got, "sanitized fits should succeed even though original too big")
	assert.Equal(t, "prefix [REDACTED] suffix", got.Segments[0].Text)
	assert.Equal(t, sanitizedLen, len(got.Segments[0].Text))
	cfg2 := egressCfgWithLimits(t, sanitizedLen-1, 0)
	cs2 := egressStoreForTest(t, cfg2)
	_, err = cs2.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap2, _ := cs2.Snapshot(context.Background(), p)
	require.Len(t, snap2, 1)
	corr2 := buildTestCorrelation(p, snap2[0], cfg2)
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg2, cs2, corr2)
	require.Equal(t, reasoningpreservation.ReservationReserved, res2.Outcome)
	called := false
	next2 := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
		called = true
		return nil
	}
	egressStage2 := reasoningpreservation.NewPostReservationEgressStage(cfg2, cs2, reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vSizing", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}, next2)
	require.NoError(t, egressStage2(context.Background(), res2))
	assert.False(t, called, "sanitized still too big must not call next")
	stats := cs2.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending, "oversize after redaction must clear counters")
}

// TestEgressStage_ControlPlaneMetadataAbsent ensures segments never contain control-plane fields.
func TestEgressStage_ControlPlaneMetadataAbsent(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-metadata")
	sensitive := "clean reasoning text without secret"
	art := sensitiveArtifact("art-metadata", sensitive, time.Unix(1_700_000_000, 0).UTC())
	art.SourceBackend = "backend-secret"
	art.SourceModel = "model-secret"
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	corr.Scope = scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-999"),
		DisplayName: scope.Known("display-secret"),
	}
	corr.TraceID = "trace-secret-123"
	corr.ALegID = "aleg-secret"
	corr.BLegID = "bleg-secret"
	corr.BranchBinding = "branch-secret"
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeAllowPolicy{version: "v1"},
		Sanitizer:    redactingSanitizer{},
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got)
	require.Len(t, got.Segments, 1)
	combined := got.Segments[0].Text
	assert.NotContains(t, combined, "trace-secret-123")
	assert.NotContains(t, combined, "aleg-secret")
	assert.NotContains(t, combined, "bleg-secret")
	assert.NotContains(t, combined, "branch-secret")
	assert.NotContains(t, combined, "user-999")
	assert.NotContains(t, combined, "display-secret")
	assert.NotContains(t, combined, "backend-secret")
	assert.NotContains(t, combined, "model-secret")
	assert.NotContains(t, combined, "sess-metadata")
}

// TestEgressStage_TokenBudgetAfterRedaction verifies token budget also after redaction.
func TestEgressStage_TokenBudgetAfterRedaction(t *testing.T) {
	t.Parallel()
	sensitive := "prefix " + sensitiveToken + " suffix"
	sanitized := strings.ReplaceAll(sensitive, sensitiveToken, "[REDACTED]")
	cfg := egressCfgWithLimits(t, 4096, len(sanitized))
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-token-budget")
	art := sensitiveArtifact("art-token", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vTok", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svc, next)
	require.NoError(t, egressStage(context.Background(), res))
	require.NotNil(t, got)
	assert.Equal(t, sanitized, got.Segments[0].Text)
	cfg2 := egressCfgWithLimits(t, 4096, len(sanitized)-1)
	cs2 := egressStoreForTest(t, cfg2)
	_, err = cs2.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap2, _ := cs2.Snapshot(context.Background(), p)
	corr2 := buildTestCorrelation(p, snap2[0], cfg2)
	res2 := reasoningpreservation.TryReserveCompression(context.Background(), cfg2, cs2, corr2)
	require.Equal(t, reasoningpreservation.ReservationReserved, res2.Outcome)
	called := false
	next2 := func(_ context.Context, _ reasoningpreservation.PreparedReservation) error {
		called = true
		return nil
	}
	egressStage2 := reasoningpreservation.NewPostReservationEgressStage(cfg2, cs2, svc, next2)
	require.NoError(t, egressStage2(context.Background(), res2))
	assert.False(t, called)
	stats := cs2.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
}

func TestEgressStage_IntegrationViaBuildPostAppendHook(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cs := egressStoreForTest(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-bundle-integration")
	sensitive := "integration " + sensitiveToken
	art := sensitiveArtifact("art-bundle", sensitive, time.Unix(1_700_000_000, 0).UTC())
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	var got *reasoningpreservation.PreparedReservation
	next := func(_ context.Context, pr reasoningpreservation.PreparedReservation) error {
		cp := pr
		got = &cp
		return nil
	}
	svc := reasoningpreservation.CompressionServices{
		Client:       &fakeBackground{},
		Poller:       &fakeBackground{},
		EgressPolicy: fakeRedactPolicy{version: "vBundle", sanitizer: redactingSanitizer{}},
		Sanitizer:    redactingSanitizer{},
	}
	hook := reasoningpreservation.BuildPostAppendHookWithEgressNext(cfg, cs, svc, next)
	require.NotNil(t, hook)
	require.NoError(t, hook(context.Background(), corr))
	require.NotNil(t, got)
	assert.NotContains(t, got.Segments[0].Text, sensitiveToken)
}

func TestObserverBundle_Egress_Deny_ClearsPending_OriginalRetained_NoProviderCall(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cfg.Compression.Route = "openai-responses:compressor"
	countingClient := &countingBackground{}
	trustedSan := &countingSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       countingClient,
		Poller:       countingClient,
		EgressPolicy: fakeDenyPolicy{version: "vDenyBundle"},
		Sanitizer:    trustedSan,
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	pKey := "sess-bundle-deny"
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: pKey},
		TraceID:   "trace-deny",
		ALegID:    "aleg-deny",
		BLegID:    "bleg-deny",
		Scope:     scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-deny")},
	}
	obs, err := parts.Observer.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "ordinary reasoning with " + sensitiveToken}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := parts.Store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(pKey))
	require.NoError(t, err)
	require.Len(t, snap, 1, "original authoritative must be retained on deny")
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	state, ok, _ := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition(pKey), snap[0].ID)
	if ok {
		assert.Nil(t, state.Pending, "pending must be cleared on deny")
	}
	stats := cs.CompressionStats()
	assert.Equal(t, 0, stats.TotalPending)
	assert.Equal(t, 0, trustedSan.calls, "deny must not invoke sanitizer")
	assert.Equal(t, 0, countingClient.SubmitCount(), "no provider call on deny")
}

func TestObserverBundle_Egress_Mismatch_ClearsPending_NoProviderCall(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cfg.Compression.Route = "openai-responses:compressor"
	countingClient := &countingBackground{}
	trustedSan := &countingSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       countingClient,
		Poller:       countingClient,
		EgressPolicy: reasoningpreservation.NewRouteBoundEgressPolicy(map[string]struct{}{"allowed-route": {}}, fakeAllowPolicy{version: "v1"}),
		Sanitizer:    trustedSan,
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	pKey := "sess-bundle-mismatch"
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: pKey},
		TraceID:   "trace-mismatch",
		ALegID:    "aleg-mismatch",
		BLegID:    "bleg-mismatch",
		Scope:     scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-mismatch")},
	}
	obs, err := parts.Observer.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "ordinary reasoning with " + sensitiveToken}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := parts.Store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(pKey))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	state, ok, _ := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition(pKey), snap[0].ID)
	if ok {
		assert.Nil(t, state.Pending)
	}
	assert.Equal(t, 0, cs.CompressionStats().TotalPending)
	assert.Equal(t, 0, trustedSan.calls)
	assert.Equal(t, 0, countingClient.SubmitCount())
}

func TestObserverBundle_Egress_Redact_Promoted_Sanitized_NoProviderCall(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cfg.Compression.Route = "openai-responses:compressor"
	countingClient := &countingBackground{}
	trustedSan := &countingSanitizer{}
	malicious := &maliciousSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       countingClient,
		Poller:       countingClient,
		EgressPolicy: fakeRedactPolicy{version: "vRedactBundle", sanitizer: malicious},
		Sanitizer:    trustedSan,
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	pKey := "sess-bundle-redact"
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: pKey},
		TraceID:   "trace-redact",
		ALegID:    "aleg-redact",
		BLegID:    "bleg-redact",
		Scope:     scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-redact")},
	}
	obs, err := parts.Observer.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	sensitive := "prefix " + sensitiveToken + " suffix"
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: sensitive}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := parts.Store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(pKey))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	state, ok, _ := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition(pKey), snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending, "redact should promote to authoritative pending")
	assert.True(t, state.Pending.PolicyHashAuthoritative)
	assert.Equal(t, 1, trustedSan.calls, "trusted sanitizer must be invoked")
	assert.Equal(t, sensitive, trustedSan.lastIn, "sanitizer must see original source")
	assert.Equal(t, 0, malicious.calls, "malicious policy sanitizer must be ignored")
	expHash := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: "vRedactBundle"}, cfg.Compression.Route)
	assert.Equal(t, expHash, state.Pending.EgressPolicyHash)
	assert.Equal(t, 1, cs.CompressionStats().TotalPending)
	_ = snap[0].Reasoning[0].Part.Reasoning.Text
	assert.Equal(t, sensitive, snap[0].Reasoning[0].Part.Reasoning.Text, "original authoritative must remain unredacted")
	assert.Equal(t, 1, countingClient.SubmitCount(), "submit must occur after egress (4.4)")
	assert.NotEmpty(t, state.Pending.JobID, "JobID must be bound after successful submit")
}

func TestObserverBundle_Egress_Allow_Promoted_NoProviderCall(t *testing.T) {
	t.Parallel()
	cfg := egressCfgWithLimits(t, 4096, 4096)
	cfg.Compression.Route = "openai-responses:compressor"
	countingClient := &countingBackground{}
	trustedSan := &countingSanitizer{}
	svc := reasoningpreservation.CompressionServices{
		Client:       countingClient,
		Poller:       countingClient,
		EgressPolicy: fakeAllowPolicy{version: "vAllowBundle"},
		Sanitizer:    trustedSan,
	}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	pKey := "sess-bundle-allow"
	meta := response.StreamMeta{
		BackendID: "be",
		Model:     "m",
		Session:   session.SessionView{AuthoritativeSessionID: pKey},
		TraceID:   "trace-allow",
		ALegID:    "aleg-allow",
		BLegID:    "bleg-allow",
		Scope:     scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-allow")},
	}
	obs, err := parts.Observer.Open(context.Background(), meta, response.Services{})
	require.NoError(t, err)
	sensitive := "ordinary reasoning with " + sensitiveToken
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: sensitive}))
	require.NoError(t, obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"}))
	require.NoError(t, obs.Finish(context.Background(), response.OutcomeSuccessReleased))
	snap, err := parts.Store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(pKey))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	cs := parts.Store.(reasoningpreservation.CompressionStore)
	state, ok, _ := cs.GetCompressionState(context.Background(), reasoningpreservation.NewSessionPartition(pKey), snap[0].ID)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.True(t, state.Pending.PolicyHashAuthoritative)
	assert.Equal(t, 0, trustedSan.calls, "allow must not invoke sanitizer")
	assert.Equal(t, 1, cs.CompressionStats().TotalPending)
	assert.Equal(t, 1, countingClient.SubmitCount(), "submit must occur after egress (4.4)")
	assert.NotEmpty(t, state.Pending.JobID, "JobID must be bound after successful submit")
}
