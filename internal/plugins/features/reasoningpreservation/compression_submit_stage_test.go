//nolint:paralleltest // Tests share package-level fixtures and run serially.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// counting fake for submit stage tests
type submitStageFake struct {
	submitCalls atomic.Int32
	awaitCalls  atomic.Int32
	pollCalls   atomic.Int32
	forgetCalls atomic.Int32
	lastJob     auxiliary.JobID
	lastReq     auxiliary.Request
	lastOpts    auxiliary.SubmitOptions
	lastCtx     context.Context
	lastScope   scope.PrincipalScopeView
	hasScope    bool
	err         error
	emptyJob    bool
}

func (f *submitStageFake) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	f.submitCalls.Add(1)
	f.lastCtx = ctx
	f.lastReq = req
	f.lastOpts = opts
	if sc, ok := scope.ScopeFromContext(ctx); ok {
		f.lastScope = sc
		f.hasScope = true
	}
	if f.err != nil {
		return "", f.err
	}
	if f.emptyJob {
		return "", nil
	}
	f.lastJob = auxiliary.JobID("job-test-1")
	return f.lastJob, nil
}

func (f *submitStageFake) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	f.awaitCalls.Add(1)
	panic("Await must not be called in submit stage")
}

func (f *submitStageFake) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	f.pollCalls.Add(1)
	panic("Poll must not be called in submit stage")
}
func (f *submitStageFake) Forget(id auxiliary.JobID) { f.forgetCalls.Add(1) }
func (f *submitStageFake) SubmitCount() int          { return int(f.submitCalls.Load()) }
func (f *submitStageFake) ForgetCount() int          { return int(f.forgetCalls.Load()) }
func (f *submitStageFake) AwaitCount() int           { return int(f.awaitCalls.Load()) }
func (f *submitStageFake) PollCount() int            { return int(f.pollCalls.Load()) }

func storeForSubmit(t *testing.T, cfg reasoningpreservation.Config) reasoningpreservation.CompressionStore {
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

func reservationForSubmit(t *testing.T, cs reasoningpreservation.CompressionStore, p reasoningpreservation.SessionPartition, id string, cfg reasoningpreservation.Config, segs []reasoningpreservation.CompressorInputSegment) (reasoningpreservation.PreparedReservation, reasoningpreservation.TurnArtifact) {
	t.Helper()
	cfg.Compression.MinSourceBytes = 1
	// create artifact with reasoning that matches segments if provided else simple; use long text to satisfy MinSourceBytes
	longText := strings.Repeat("a", 5000)
	var art reasoningpreservation.TurnArtifact
	if len(segs) > 0 {
		// for build-error case caller will override pr.Segments after; use longText artifact for reservation success
		hasBad := false
		for _, s := range segs {
			if strings.TrimSpace(s.Text) == "" {
				hasBad = true
				break
			}
		}
		if hasBad {
			art = reasoningpreservation.TurnArtifact{
				ID:             id,
				Anchor:         sha256.Sum256([]byte(id)),
				SourceBackend:  "be",
				SourceModel:    "m",
				Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, longText, "", nil))},
				CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
				ReasoningBytes: 5000,
			}
		} else {
			placements := make([]reasoningpreservation.PlacedReasoning, 0, len(segs))
			for _, s := range segs {
				part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, s.Text, "", nil)
				placements = append(placements, placedReasoning(s.Index, part))
			}
			art = reasoningpreservation.TurnArtifact{
				ID:             id,
				Anchor:         sha256.Sum256([]byte(id)),
				SourceBackend:  "be",
				SourceModel:    "m",
				Reasoning:      placements,
				CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
				ReasoningBytes: 5000,
			}
		}
	} else {
		art = reasoningpreservation.TurnArtifact{
			ID:             id,
			Anchor:         sha256.Sum256([]byte(id)),
			SourceBackend:  "be",
			SourceModel:    "m",
			Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, longText, "", nil))},
			CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
			ReasoningBytes: 5000,
		}
	}
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	var snapArt reasoningpreservation.TurnArtifact
	for _, a := range snap {
		if a.ID == id {
			snapArt = a
			break
		}
	}
	require.Equal(t, id, snapArt.ID)
	corr := buildTestCorrelation(p, snapArt, cfg)
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	// egress promote
	authoritative := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, cfg.Compression.Route)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), res.Claim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	// decide segments: use segs if provided else extract
	useSegs := segs
	if len(useSegs) == 0 {
		useSegs = reasoningpreservation.ExtractSemanticSegments(snapArt.Reasoning)
		if len(useSegs) == 0 {
			useSegs = []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: strings.Repeat("a", 100)}}
		}
	}
	pr := reasoningpreservation.PreparedReservation{
		Reservation:      res,
		Segments:         useSegs,
		Decision:         reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"},
		EgressPolicyHash: authoritative,
		Route:            cfg.Compression.Route,
	}
	return pr, snapArt
}

func TestSubmitStage_RequestBuildErrorClears(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-build-error")
	// Build error via empty text segment (BuildCompressorAuxRequest requires non-empty trimmed text)
	badSegs := []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "   "}}
	pr, snapArt := reservationForSubmit(t, cs, p, "art-build-error", cfg, badSegs)
	// override segments to bad (reservation already used good text for reserve, now inject bad for submit)
	pr.Segments = badSegs
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 0, fake.SubmitCount(), "Build error must not submit")
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "reservation must be cleared on build error")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1, "original must remain")
	assert.Equal(t, 0, fake.AwaitCount())
	assert.Equal(t, 0, fake.PollCount())
}

func TestSubmitStage_NilClientClears(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-nil-client")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-nil-client", cfg, nil)
	svc := reasoningpreservation.CompressionServices{Client: nil, Poller: &submitStageFake{}, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NotPanics(t, func() { _ = stage(context.Background(), pr) })
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "nil client must clear reservation")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
}

func TestSubmitStage_SubmitErrorClears(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-submit-error")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-submit-error", cfg, nil)
	fake := &submitStageFake{err: errors.New("queue full")}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 1, fake.SubmitCount())
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "submit error must clear reservation")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
	assert.Equal(t, 0, fake.ForgetCount(), "submit error has no job to forget")
}

func TestSubmitStage_EmptyJobClears(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-empty-job")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-empty-job", cfg, nil)
	fake := &submitStageFake{emptyJob: true}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 1, fake.SubmitCount())
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "empty JobID must clear reservation")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
	assert.Equal(t, 0, fake.ForgetCount())
}

func TestSubmitStage_SuccessfulBindState(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-success-bind")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-success", cfg, nil)
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 1, fake.SubmitCount())
	assert.Equal(t, 0, fake.AwaitCount())
	assert.Equal(t, 0, fake.PollCount())
	assert.Equal(t, 0, fake.ForgetCount())
	state, ok, err := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, state.Pending)
	assert.Equal(t, pr.Reservation.Claim.ReservationID, state.Pending.ReservationID)
	assert.Equal(t, fake.lastJob, state.Pending.JobID)
	assert.True(t, state.Pending.PolicyHashAuthoritative)
	assert.Equal(t, pr.EgressPolicyHash, state.Pending.EgressPolicyHash)
	// envelope
	assert.Equal(t, "reasoning_preservation_compressor", fake.lastReq.Role)
	assert.Equal(t, "private", fake.lastReq.Visibility)
	assert.Equal(t, auxiliary.SessionModeDetached, fake.lastReq.SessionMode)
	assert.Empty(t, fake.lastReq.Call.Tools)
	assert.Equal(t, "none", string(fake.lastReq.Call.ToolChoice.Mode))
	assert.Contains(t, fake.lastReq.DisablePlugins, "reasoning-output-preservation")
	// prompt sanitized: must contain segment text, not lineage
	var blob strings.Builder
	for _, m := range fake.lastReq.Call.Messages {
		for _, pt := range m.Parts {
			blob.WriteString(pt.Text)
		}
	}
	assert.Contains(t, blob.String(), pr.Segments[0].Text)
	assert.NotContains(t, blob.String(), pr.Reservation.Correlation.TraceID)
	assert.NotContains(t, blob.String(), pr.Reservation.Correlation.ALegID)
	assert.NotContains(t, blob.String(), "sess-success-bind")
	// timeout exact
	assert.Equal(t, cfg.Compression.Timeout, fake.lastOpts.Timeout)
	assert.NotEmpty(t, fake.lastOpts.CoalesceKey)
	assert.True(t, strings.HasPrefix(fake.lastOpts.CoalesceKey, "sha256:"))
}

func TestSubmitStage_BindCASFailureForgetExactlyOnce(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-bind-cas")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-bind-cas", cfg, nil)
	// tamper original digest in claim to cause Bind failure
	pr.Reservation.Claim.OriginalDigest = sha256.Sum256([]byte("tampered"))
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 1, fake.SubmitCount())
	assert.Equal(t, 1, fake.ForgetCount(), "bind CAS failure must Forget exactly once")
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "reservation CAS clear must remove pending after bind failure")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1, "original must remain after bind failure")
	// ensure Forget job was the submitted one
	assert.Equal(t, fake.lastJob, auxiliary.JobID("job-test-1"))
}

func TestSubmitStage_TimeoutExact(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	// use distinct timeout
	cfg.Compression.Timeout = 9 * time.Second
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-timeout")
	pr, _ := reservationForSubmit(t, cs, p, "art-timeout", cfg, nil)
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 9*time.Second, fake.lastOpts.Timeout)
}

func TestSubmitStage_CoalesceKeyDeterministic(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-coalesce-det")
	pr, _ := reservationForSubmit(t, cs, p, "art-coalesce", cfg, nil)
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	key1 := fake.lastOpts.CoalesceKey
	// same pr again via new reservation (new artifact but same IDs) should produce same key when reusing same pr struct
	fake2 := &submitStageFake{}
	// need fresh reservation for second call with same correlation
	cs2 := storeForSubmit(t, cfg)
	p2 := reasoningpreservation.NewSessionPartition("sess-coalesce-det2")
	pr2, _ := reservationForSubmit(t, cs2, p2, "art-coalesce", cfg, nil)
	// ensure same IDs/digests/route
	pr2.Reservation.Correlation.ArtifactID = pr.Reservation.Correlation.ArtifactID
	pr2.Reservation.Correlation.OriginalDigest = pr.Reservation.Correlation.OriginalDigest
	pr2.Reservation.Correlation.SemanticDigest = pr.Reservation.Correlation.SemanticDigest
	pr2.EgressPolicyHash = pr.EgressPolicyHash
	pr2.Reservation.Correlation.PolicyRevision = pr.Reservation.Correlation.PolicyRevision
	pr2.Route = pr.Route
	// segment text differs but key must stay same
	pr2.Segments = []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "different raw text that should not affect key"}}
	// need reservationID for pr2 to be valid (use its own)
	require.NoError(t, reasoningpreservation.NewPostEgressSubmitStage(cfg, cs2, reasoningpreservation.CompressionServices{Client: fake2, Poller: fake2, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}})(context.Background(), pr2))
	key2 := fake2.lastOpts.CoalesceKey
	assert.Equal(t, key1, key2, "coalesce must be deterministic and ignore raw segment text")

	// changes must affect key
	base := pr
	tests := []struct {
		name string
		mut  func(*reasoningpreservation.PreparedReservation)
	}{
		{"artifact", func(p *reasoningpreservation.PreparedReservation) {
			p.Reservation.Correlation.ArtifactID = "different-art"
		}},
		{"original", func(p *reasoningpreservation.PreparedReservation) {
			p.Reservation.Correlation.OriginalDigest = sha256.Sum256([]byte("orig2"))
		}},
		{"semantic", func(p *reasoningpreservation.PreparedReservation) {
			p.Reservation.Correlation.SemanticDigest = sha256.Sum256([]byte("sem2"))
		}},
		{"egress", func(p *reasoningpreservation.PreparedReservation) {
			p.EgressPolicyHash = sha256.Sum256([]byte("egress2"))
		}},
		{"policy", func(p *reasoningpreservation.PreparedReservation) { p.Reservation.Correlation.PolicyRevision = "v2" }},
		{"route", func(p *reasoningpreservation.PreparedReservation) { p.Route = "different-route" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			csTmp := storeForSubmit(t, cfg)
			pTmp := reasoningpreservation.NewSessionPartition("sess-" + tc.name)
			prTmp, _ := reservationForSubmit(t, csTmp, pTmp, "art-"+tc.name, cfg, nil)
			// start from base copy
			prTmp.Reservation.Correlation.ArtifactID = base.Reservation.Correlation.ArtifactID
			prTmp.Reservation.Correlation.OriginalDigest = base.Reservation.Correlation.OriginalDigest
			prTmp.Reservation.Correlation.SemanticDigest = base.Reservation.Correlation.SemanticDigest
			prTmp.EgressPolicyHash = base.EgressPolicyHash
			prTmp.Reservation.Correlation.PolicyRevision = base.Reservation.Correlation.PolicyRevision
			prTmp.Route = base.Route
			// apply mutation
			tc.mut(&prTmp)
			f := &submitStageFake{}
			require.NoError(t, reasoningpreservation.NewPostEgressSubmitStage(cfg, csTmp, reasoningpreservation.CompressionServices{Client: f, Poller: f, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}})(context.Background(), prTmp))
			assert.NotEqual(t, key1, f.lastOpts.CoalesceKey, "key must change with %s", tc.name)
		})
	}
	// ensure key is content-free: not contains segment text
	assert.NotContains(t, key1, pr.Segments[0].Text)
}

func TestSubmitStage_AwaitPollNeverCalled(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-no-await-poll")
	pr, _ := reservationForSubmit(t, cs, p, "art-no-await", cfg, nil)
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	assert.Equal(t, 0, fake.AwaitCount())
	assert.Equal(t, 0, fake.PollCount())
}

func TestSubmitStage_CanceledCtxStillSubmits(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-canceled")
	pr, _ := reservationForSubmit(t, cs, p, "art-canceled", cfg, nil)
	// set originating scope and genpin in ctx; also ensure correlation scope matches originating
	origScope := scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-canceled")}
	pr.Reservation.Correlation.Scope = origScope
	ctx, cancel := context.WithCancel(context.Background())
	// install genpin retainer
	ret := &testRetainer{pin: &testPin{}}
	ctx = genpin.WithRetainer(ctx, ret)
	ctx = scope.WithScope(ctx, scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("other")})
	cancel() // cancel before submit
	require.Error(t, ctx.Err())
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(ctx, pr))
	assert.Equal(t, 1, fake.SubmitCount(), "canceled ctx must still submit via WithoutCancel")
	assert.True(t, fake.hasScope)
	assert.Equal(t, origScope.PrincipalID.String(), fake.lastScope.PrincipalID.String())
	// genpin must be preserved in submitCtx
	_, hasPin := genpin.FromContext(fake.lastCtx)
	assert.True(t, hasPin, "genpin must be preserved in submitCtx via WithoutCancel")
	assert.NoError(t, fake.lastCtx.Err(), "submitCtx must ignore parent cancel")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
}

func TestSubmitStage_ScopeEqualsOriginatingClone(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-scope-clone")
	origScope := scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("user-scope"), DisplayName: scope.Known("disp"), Roles: []string{"r1"}, SafeClaims: map[string]string{"k": "v"}}
	// create artifact and correlation with this scope directly via custom corr
	longScopeText := strings.Repeat("a", 5000)
	art := reasoningpreservation.TurnArtifact{
		ID: "art-scope", Anchor: sha256.Sum256([]byte("art-scope")), SourceBackend: "be", SourceModel: "m",
		Reasoning: []reasoningpreservation.PlacedReasoning{placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, longScopeText, "", nil))},
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), ReasoningBytes: 5000,
	}
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	corr := buildTestCorrelation(p, snap[0], cfg)
	corr.Scope = origScope
	res := reasoningpreservation.TryReserveCompression(context.Background(), cfg, cs, corr)
	require.Equal(t, reasoningpreservation.ReservationReserved, res.Outcome)
	authoritative := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, cfg.Compression.Route)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), res.Claim, corr.EgressPolicyRefHash, corr.SemanticDigest, authoritative, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	pr := reasoningpreservation.PreparedReservation{
		Reservation:      res,
		Segments:         []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: longScopeText}},
		Decision:         reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"},
		EgressPolicyHash: authoritative,
		Route:            cfg.Compression.Route,
	}
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NoError(t, stage(context.Background(), pr))
	require.True(t, fake.hasScope)
	assert.Equal(t, origScope.PrincipalID.String(), fake.lastScope.PrincipalID.String())
	assert.Equal(t, origScope.DisplayName.String(), fake.lastScope.DisplayName.String())
	// mutation of original must not affect submitted scope clone
	corr.Scope.PrincipalID = scope.Known("mutated")
	assert.NotEqual(t, "mutated", fake.lastScope.PrincipalID.String())
}

func TestSubmitStage_NonSuccessObserverNeverSubmits(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	fake := &submitStageFake{}
	svc := reasoningpreservation.CompressionServices{Client: fake, Poller: fake, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	for _, oc := range []response.StreamOutcome{response.OutcomeFailed, response.OutcomeCancelled, response.OutcomeClosed, response.OutcomeReplaced, response.OutcomeGateReplaced} {
		t.Run(string(oc), func(t *testing.T) {
			fake.submitCalls.Store(0)
			meta := response.StreamMeta{BackendID: "be", Model: "m", Session: session.SessionView{AuthoritativeSessionID: "sess-non-success-" + string(oc)}, TraceID: "t", ALegID: "a", BLegID: "b", Scope: scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("u")}}
			obs, err := parts.Observer.Open(context.Background(), meta, response.Services{})
			require.NoError(t, err)
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: strings.Repeat("a", 5000)})
			_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ans"})
			require.NoError(t, obs.Finish(context.Background(), oc))
			assert.Equal(t, 0, fake.SubmitCount(), "non-success %s must never submit", oc)
		})
	}
}

func TestValidateFor_TypedNilCapabilities_Table(t *testing.T) {
	t.Parallel()
	baseCfg := compressionObserverConfig(t)
	baseCfg.Compression.MinSourceBytes = 1
	var nilClient *submitStageFake
	var nilPoller *submitStageFake
	var nilEgress *fakeAllowPolicy
	var nilSan *fakeSanitizer
	tests := []struct {
		name    string
		svc     reasoningpreservation.CompressionServices
		wantSub string
	}{
		{
			name: "typed-nil Client",
			svc: reasoningpreservation.CompressionServices{
				Client:       nilClient,
				Poller:       &submitStageFake{},
				EgressPolicy: fakeAllowPolicy{version: "v1"},
				Sanitizer:    fakeSanitizer{},
			},
			wantSub: "BackgroundClient",
		},
		{
			name: "typed-nil Poller",
			svc: reasoningpreservation.CompressionServices{
				Client:       &submitStageFake{},
				Poller:       nilPoller,
				EgressPolicy: fakeAllowPolicy{version: "v1"},
				Sanitizer:    fakeSanitizer{},
			},
			wantSub: "BackgroundPoller",
		},
		{
			name: "typed-nil EgressPolicy",
			svc: reasoningpreservation.CompressionServices{
				Client:       &submitStageFake{},
				Poller:       &submitStageFake{},
				EgressPolicy: nilEgress,
				Sanitizer:    fakeSanitizer{},
			},
			wantSub: "EgressPolicy",
		},
		{
			name: "typed-nil Sanitizer",
			svc: reasoningpreservation.CompressionServices{
				Client:       &submitStageFake{},
				Poller:       &submitStageFake{},
				EgressPolicy: fakeAllowPolicy{version: "v1"},
				Sanitizer:    nilSan,
			},
			wantSub: "TrustedTextSanitizer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseCfg, tc.svc, reasoningpreservation.CompanionPolicy{})
			require.Error(t, err, "typed-nil %s must fail", tc.name)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

func TestSubmitStage_TypedNilClientClearsNoPanic(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	cfg.Compression.MinSourceBytes = 1
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-typed-nil-direct")
	pr, snapArt := reservationForSubmit(t, cs, p, "art-typed-nil", cfg, nil)
	var nilClient *submitStageFake
	var typedNil auxiliary.BackgroundClient = nilClient
	svc := reasoningpreservation.CompressionServices{Client: typedNil, Poller: &submitStageFake{}, EgressPolicy: fakeAllowPolicy{version: "v1"}, Sanitizer: fakeSanitizer{}}
	stage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svc)
	require.NotPanics(t, func() { _ = stage(context.Background(), pr) })
	_, ok, _ := cs.GetCompressionState(context.Background(), p, snapArt.ID)
	assert.False(t, ok, "typed-nil client must clear reservation")
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1, "original intact")
}

// test retainer/pin helpers
type testPin struct{ released atomic.Int32 }

func (p *testPin) Kind() genpin.Kind { return genpin.KindAsync }
func (p *testPin) Release()          { p.released.Add(1) }

type testRetainer struct{ pin genpin.Pin }

func (r *testRetainer) RuntimeInstanceID() string   { return "test-instance" }
func (r *testRetainer) RuntimeGenerationID() string { return "test-gen" }
func (r *testRetainer) Retain(k genpin.Kind) (genpin.Pin, bool) {
	if k != genpin.KindAsync {
		return nil, false
	}
	return r.pin, true
}
