//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	"github.com/stretchr/testify/require"
)

// helpers for auxreq scheduler integration (self-contained, deterministic channels)
type backgroundRunner func(context.Context, *aipapi.Call) (aipapi.EventStream, error)

func (r backgroundRunner) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return r(ctx, call)
}

func certFinishedStream() aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}})
}

func certBackgroundRequest() auxiliary.Request {
	return auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "local:test"}}}
}

func certPollPendingRunner(start, release chan struct{}) backgroundRunner {
	return func(_ context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
		return &certGatedStream{started: start, release: release}, nil
	}
}

type certGatedStream struct {
	started chan<- struct{}
	release <-chan struct{}
	start   sync.Once
	emitted bool
}

func (s *certGatedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if !s.emitted {
		s.emitted = true
		s.start.Do(func() { close(s.started) })
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	select {
	case <-s.release:
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}
func (s *certGatedStream) Close() error { return nil }

type certBoundRunner struct {
	name  string
	start chan<- struct{}
	gate  <-chan struct{}
	calls atomic.Int32
}

func (r *certBoundRunner) Execute(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	r.calls.Add(1)
	if r.start != nil {
		close(r.start)
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: r.name},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func certNewScheduler(t *testing.T, runner func() auxreq.ExecutorRunner, cfg auxreq.SchedulerConfig) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), runner, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func certNewSchedulerNoCleanup(t *testing.T, runner func() auxreq.ExecutorRunner, cfg auxreq.SchedulerConfig) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), runner, cfg)
	require.NoError(t, err)
	return s
}

func newTestClockAux() *testClockAux {
	return &testClockAux{now: time.Unix(100, 0)}
}

type testClockAux struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClockAux) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClockAux) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type blockingEgressPolicy struct{ block chan struct{} }

func (b *blockingEgressPolicy) Decide(ctx context.Context, in reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	select {
	case <-b.block:
		return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
	case <-ctx.Done():
		return reasoningpreservation.CompressionEgressDecision{}, ctx.Err()
	}
}

type blockingSanitizer struct{ block chan struct{} }

func (b *blockingSanitizer) SanitizeText(ctx context.Context, txt string) (string, error) {
	select {
	case <-b.block:
		return txt, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type blockingBackgroundClient struct{ block chan struct{} }

func (b *blockingBackgroundClient) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	select {
	case <-b.block:
		return auxiliary.JobID("job-block"), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (b *blockingBackgroundClient) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (b *blockingBackgroundClient) Forget(id auxiliary.JobID) {}
func (b *blockingBackgroundClient) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollPending}, nil
}

type countingPinCert struct{ releases atomic.Int32 }

func (p *countingPinCert) Kind() genpin.Kind { return genpin.KindAsync }
func (p *countingPinCert) Release()          { p.releases.Add(1) }

type countingRetainerCert struct {
	pin     *countingPinCert
	retains atomic.Int32
	allow   atomic.Bool
}

func (r *countingRetainerCert) RuntimeInstanceID() string   { return "instance" }
func (r *countingRetainerCert) RuntimeGenerationID() string { return "generation" }
func (r *countingRetainerCert) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	if kind != genpin.KindAsync || !r.allow.Load() {
		return nil, false
	}
	r.retains.Add(1)
	return r.pin, true
}

func newCompressionStoreWithOptions(t *testing.T, opts reasoningpreservation.StoreOptions) reasoningpreservation.CompressionStore {
	t.Helper()
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs, ok := st.(reasoningpreservation.CompressionStore)
	require.True(t, ok)
	return cs
}

func longArtifactForReload(id, text string) reasoningpreservation.TurnArtifact {
	visible := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor, _ := reasoningpreservation.ComputeAnchor(aipapi.Message{Role: aipapi.RoleAssistant, Parts: visible})
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, text, "", nil)
	return reasoningpreservation.TurnArtifact{
		ID: id, Anchor: anchor, SourceBackend: "be", SourceModel: "m",
		Reasoning: []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt: time.Now().UTC(), ReasoningBytes: len(text),
	}
}

func computeSemanticDigestCert(placements []reasoningpreservation.PlacedReasoning) [32]byte {
	segs := reasoningpreservation.ExtractSemanticSegments(placements)
	if len(segs) == 0 {
		return [32]byte{}
	}
	h := sha256.New()
	for _, s := range segs {
		var idx [8]byte
		idx[0] = byte(s.Index >> 56)
		idx[1] = byte(s.Index >> 48)
		idx[2] = byte(s.Index >> 40)
		idx[3] = byte(s.Index >> 32)
		idx[4] = byte(s.Index >> 24)
		idx[5] = byte(s.Index >> 16)
		idx[6] = byte(s.Index >> 8)
		idx[7] = byte(s.Index)
		h.Write(idx[:])
		var l [4]byte
		l[0] = byte(len(s.Text) >> 24)
		l[1] = byte(len(s.Text) >> 16)
		l[2] = byte(len(s.Text) >> 8)
		l[3] = byte(len(s.Text))
		h.Write(l[:])
		h.Write([]byte(s.Text))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func certStoreForSubmit(t *testing.T, cfg reasoningpreservation.Config) reasoningpreservation.CompressionStore {
	t.Helper()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	opts := defaultStoreOptions(now)
	opts.CompressionLimits = cfg.Compression.ToLimits()
	st, err := reasoningpreservation.NewMemoryTurnStore(opts)
	require.NoError(t, err)
	cs, ok := st.(reasoningpreservation.CompressionStore)
	require.True(t, ok)
	return cs
}

func certArtifact(id, text string, bytes int) reasoningpreservation.TurnArtifact {
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, text, "", nil)
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         [32]byte{1, 2, 3},
		SourceBackend:  "backend",
		SourceModel:    "model",
		Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt:      time.Unix(1_700_000_000, 0).UTC(),
		ReasoningBytes: bytes,
	}
}

type redactEgressCert struct{}

func (redactEgressCert) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressRedactThenAllow, PolicyVersion: "v1"}, nil
}

// TestCertification_MultiSession_ReservationsAttachmentsNeverExceedTotals
func TestCertification_MultiSession_ReservationsAttachmentsNeverExceedTotals(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{
		MaxPendingPerSession:        2,
		MaxPendingTotal:             3,
		MaxSurrogateBytesPerTurn:    50,
		MaxSurrogateBytesPerSession: 60,
		MaxSurrogateBytesTotal:      80,
	}
	opts := defaultStoreOptions(now)
	opts.CompressionLimits = limits
	cs := newCompressionStoreWithOptions(t, opts)
	policy := "v1"
	sessions := []reasoningpreservation.SessionPartition{
		reasoningpreservation.NewSessionPartition("sess-a"),
		reasoningpreservation.NewSessionPartition("sess-b"),
		reasoningpreservation.NewSessionPartition("sess-c"),
		reasoningpreservation.NewSessionPartition("sess-d"),
		reasoningpreservation.NewSessionPartition("sess-e"),
	}
	for si, p := range sessions {
		for ai := range 2 {
			id := string(rune('a'+si)) + string(rune('0'+ai))
			_ = id
			id2 := "t-" + p.String() + "-" + string(rune('0'+ai)) // keep deterministic but unique
			// Use non-empty opaque string for partition key via original helper: partition already carries opaque; id is artifact id
			_ = id2
			artID := "t-" + string(rune('A'+si)) + "-" + string(rune('0'+ai))
			art := sampleArtifact(artID, "reasoning-"+artID, 32)
			_, err := cs.Append(context.Background(), p, art)
			require.NoError(t, err)
		}
	}
	// Use explicit artifact ids for reservation attempts
	var reserved []reasoningpreservation.CompressionClaim
	for si, p := range sessions {
		for ai := range 2 {
			artID := "t-" + string(rune('A'+si)) + "-" + string(rune('0'+ai))
			d := digestFor(artID)
			sem := semanticDigestFor(policy)
			eg := egressHashFor(policy)
			claim, err := cs.ReserveCompression(context.Background(), p, artID, d, policy, sem, eg)
			if err == nil {
				reserved = append(reserved, claim)
			} else {
				require.True(t, reasoningpreservation.IsBudgetError(err))
			}
			stats := cs.CompressionStats()
			require.LessOrEqual(t, stats.TotalPending, limits.MaxPendingTotal)
		}
	}
	require.Equal(t, limits.MaxPendingTotal, len(reserved))
	stats := cs.CompressionStats()
	require.Equal(t, limits.MaxPendingTotal, stats.TotalPending)
	for _, claim := range reserved {
		sem := semanticDigestFor(policy)
		eg := egressHashFor(policy)
		require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
		require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job-"+claim.ArtifactID)))
		sur := surrogateFor(claim.OriginalDigest, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "x123456789", Bytes: 10})
		require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job-"+claim.ArtifactID), sur))
		stats2 := cs.CompressionStats()
		require.LessOrEqual(t, stats2.TotalSurrogateBytes, limits.MaxSurrogateBytesTotal)
	}
	statsFinal := cs.CompressionStats()
	require.Equal(t, 0, statsFinal.TotalPending)
	require.Equal(t, 30, statsFinal.TotalSurrogateBytes)
	for _, claim := range reserved {
		require.NoError(t, cs.Delete(context.Background(), claim.Partition, claim.ArtifactID))
	}
	statsClean := cs.CompressionStats()
	require.Equal(t, 0, statsClean.TotalPending)
	require.Equal(t, 0, statsClean.TotalSurrogateBytes)

	t.Run("concurrent_reservations", func(t *testing.T) {
		t.Parallel()
		now2, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
		opts2 := defaultStoreOptions(now2)
		opts2.CompressionLimits = reasoningpreservation.CompressionLimits{MaxPendingPerSession: 2, MaxPendingTotal: 3, MaxSurrogateBytesPerTurn: 50, MaxSurrogateBytesPerSession: 100, MaxSurrogateBytesTotal: 100}
		cs2 := newCompressionStoreWithOptions(t, opts2)
		for i := range 5 {
			p := reasoningpreservation.NewSessionPartition("conc-" + string(rune('0'+i)))
			art := sampleArtifact("conc-t-"+string(rune('0'+i)), "r", 32)
			_, err := cs2.Append(context.Background(), p, art)
			require.NoError(t, err)
		}
		var wg sync.WaitGroup
		results := make([]error, 5)
		wg.Add(5)
		for i := range 5 {
			go func() {
				defer wg.Done()
				p := reasoningpreservation.NewSessionPartition("conc-" + string(rune('0'+i)))
				id := "conc-t-" + string(rune('0'+i))
				d := digestFor(id)
				_, err := cs2.ReserveCompression(context.Background(), p, id, d, policy, semanticDigestFor(policy), egressHashFor(policy))
				results[i] = err
			}()
		}
		wg.Wait()
		successes := 0
		for _, e := range results {
			if e == nil {
				successes++
			} else {
				require.True(t, reasoningpreservation.IsBudgetError(e))
			}
		}
		require.Equal(t, 3, successes)
		require.Equal(t, 3, cs2.CompressionStats().TotalPending)
	})
}

func TestCertification_PollVsFinishForgetExpiryShutdown(t *testing.T) {
	t.Parallel()
	t.Run("poll_vs_finish_and_forget", func(t *testing.T) {
		t.Parallel()
		started := make(chan struct{})
		release := make(chan struct{})
		s := certNewScheduler(t, func() auxreq.ExecutorRunner {
			return certPollPendingRunner(started, release)
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 4})
		id, err := s.SubmitCollect(context.Background(), certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-finish"})
		require.NoError(t, err)
		res, err := s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollPending, res.State)
		<-started
		res, err = s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollPending, res.State)
		close(release)
		_, err = s.Await(context.Background(), id)
		require.NoError(t, err)
		res, err = s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollCompleted, res.State)
		s.Forget(id)
		res, err = s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollNotFound, res.State)
	})
	t.Run("poll_vs_forget_race", func(t *testing.T) {
		t.Parallel()
		s := certNewScheduler(t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				return certFinishedStream(), nil
			})
		}, auxreq.SchedulerConfig{})
		id, err := s.SubmitCollect(context.Background(), certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-forget-race"})
		require.NoError(t, err)
		_, err = s.Await(context.Background(), id)
		require.NoError(t, err)
		var wg sync.WaitGroup
		wg.Add(2)
		var pollRes auxiliary.PollResult
		var pollErr error
		go func() { defer wg.Done(); pollRes, pollErr = s.Poll(context.Background(), id) }()
		go func() { defer wg.Done(); s.Forget(id) }()
		wg.Wait()
		if pollErr == nil {
			require.True(t, pollRes.State == auxiliary.PollCompleted || pollRes.State == auxiliary.PollNotFound)
		}
	})
	t.Run("poll_vs_expiry", func(t *testing.T) {
		t.Parallel()
		clock := newTestClockAux()
		s := certNewScheduler(t, func() auxreq.ExecutorRunner {
			return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				return certFinishedStream(), nil
			})
		}, auxreq.SchedulerConfig{ResultTTL: time.Minute, Now: clock.Now})
		id, err := s.SubmitCollect(context.Background(), certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "ttl"})
		require.NoError(t, err)
		_, err = s.Await(context.Background(), id)
		require.NoError(t, err)
		res, err := s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollCompleted, res.State)
		clock.Advance(2 * time.Minute)
		res, err = s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollNotFound, res.State)
	})
	t.Run("poll_vs_shutdown", func(t *testing.T) {
		t.Parallel()
		started := make(chan struct{})
		release := make(chan struct{})
		s := certNewSchedulerNoCleanup(t, func() auxreq.ExecutorRunner {
			return certPollPendingRunner(started, release)
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 4})
		id, err := s.SubmitCollect(context.Background(), certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "shutdown"})
		require.NoError(t, err)
		<-started
		res, err := s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollPending, res.State)
		require.NoError(t, s.Close())
		close(release)
		_, err = s.Await(context.Background(), id)
		require.Error(t, err)
		res, err = s.Poll(context.Background(), id)
		if err == nil {
			require.True(t, res.State == auxiliary.PollFailed || res.State == auxiliary.PollNotFound || res.State == auxiliary.PollCompleted)
		}
	})
	t.Run("store_stale_completion", func(t *testing.T) {
		t.Parallel()
		now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
		cs := newCompressionStore(t, now, reasoningpreservation.CompressionLimits{MaxPendingPerSession: 10, MaxPendingTotal: 10, MaxSurrogateBytesPerTurn: 100, MaxSurrogateBytesPerSession: 1000, MaxSurrogateBytesTotal: 1000})
		p := reasoningpreservation.NewSessionPartition("sess-stale")
		art := sampleArtifact("t-stale", "reasoning", 32)
		_, err := cs.Append(context.Background(), p, art)
		require.NoError(t, err)
		snap, _ := cs.Snapshot(context.Background(), p)
		d := snap[0].Anchor
		sem := semanticDigestFor("v1")
		eg := egressHashFor("v1")
		claim, err := cs.ReserveCompression(context.Background(), p, snap[0].ID, d, "v1", sem, eg)
		require.NoError(t, err)
		require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
		require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job-stale")))
		sur := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
		require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job-stale"), sur))
		for range 5 {
			err = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job-stale"), sur)
			require.Error(t, err)
		}
		require.Equal(t, 0, cs.CompressionStats().TotalPending)
		require.Equal(t, 2, cs.CompressionStats().TotalSurrogateBytes)
	})
}

func TestCertification_CounterUpdatesExactlyOnce(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	cs := newCompressionStore(t, now, reasoningpreservation.CompressionLimits{MaxPendingPerSession: 10, MaxPendingTotal: 10, MaxSurrogateBytesPerTurn: 100, MaxSurrogateBytesPerSession: 1000, MaxSurrogateBytesTotal: 1000})
	p := reasoningpreservation.NewSessionPartition("sess-counter")
	art := sampleArtifact("t-counter", "reasoning", 32)
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	d := snap[0].Anchor
	sem := semanticDigestFor("v1")
	eg := egressHashFor("v1")
	claim, err := cs.ReserveCompression(context.Background(), p, snap[0].ID, d, "v1", sem, eg)
	require.NoError(t, err)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1")))
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); _ = cs.ClearCompression(context.Background(), p, snap[0].ID, "") }()
	go func() { defer wg.Done(); _ = cs.Delete(context.Background(), p, snap[0].ID) }()
	go func() {
		defer wg.Done()
		sur := surrogateFor(d, "v1", reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 2})
		_ = cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job1"), sur)
	}()
	wg.Wait()
	stats := cs.CompressionStats()
	require.GreaterOrEqual(t, stats.TotalPending, 0)
	require.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
	wg.Add(5)
	for range 5 {
		go func() { defer wg.Done(); _ = cs.ClearCompression(context.Background(), p, snap[0].ID, "") }()
	}
	wg.Wait()
	stats2 := cs.CompressionStats()
	require.GreaterOrEqual(t, stats2.TotalPending, 0)
	for range 5 {
		_ = cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job1"))
	}
	stats3 := cs.CompressionStats()
	require.Equal(t, stats2.TotalPending, stats3.TotalPending)
}

func TestCertification_GenerationReloadUsesCapturedOld(t *testing.T) {
	t.Parallel()
	firstGate := make(chan struct{})
	first := &certBoundRunner{name: "old-gen", start: make(chan struct{}), gate: firstGate}
	second := &certBoundRunner{name: "new-gen", start: make(chan struct{}), gate: nil}
	var active atomic.Value
	active.Store(auxreq.ExecutorRunner(first))
	sched := certNewScheduler(t, func() auxreq.ExecutorRunner {
		if v := active.Load(); v != nil {
			return v.(auxreq.ExecutorRunner)
		}
		return nil
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	oldClient := sched.BindRunner(first)
	oldCfg := pollTestConfig(t)
	oldCfg.Compression.Route = "old-route"
	oldCfg.Compression.EgressPolicyRef = "v1-old"
	oldCs := certStoreForSubmit(t, oldCfg)
	oldP := reasoningpreservation.NewSessionPartition("sess-reload-old")
	oldArt := longArtifactForReload("art-old", "aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa aaaaaaaaaa")
	_, err := oldCs.Append(context.Background(), oldP, oldArt)
	require.NoError(t, err)
	snap, _ := oldCs.Snapshot(context.Background(), oldP)
	oldSnapArt := snap[0]
	semOld := computeSemanticDigestCert(oldSnapArt.Reasoning)
	egOld := sha256.Sum256([]byte(oldCfg.Compression.EgressPolicyRef))
	claimOld, err := oldCs.ReserveCompression(context.Background(), oldP, oldSnapArt.ID, oldSnapArt.Anchor, oldCfg.Compression.EgressPolicyRef, semOld, egOld)
	require.NoError(t, err)
	authOld := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: oldCfg.Compression.EgressPolicyRef}, oldCfg.Compression.Route)
	routeHashOld := sha256.Sum256([]byte(oldCfg.Compression.Route))
	require.NoError(t, oldCs.UpdateReservationPolicyHash(context.Background(), claimOld, egOld, semOld, authOld, reasoningpreservation.SanitizationNone, routeHashOld))
	coalesceOld := "reload-old-key"
	oldID, err := oldClient.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "old:selector"}}}, auxiliary.SubmitOptions{CoalesceKey: coalesceOld, Timeout: time.Second})
	require.NoError(t, err)
	active.Store(auxreq.ExecutorRunner(second))
	newClient := sched.BindRunner(second)
	newCfg := pollTestConfig(t)
	newCfg.Compression.Route = "new-route"
	newCfg.Compression.EgressPolicyRef = "v1-new"
	newCs := certStoreForSubmit(t, newCfg)
	newP := reasoningpreservation.NewSessionPartition("sess-reload-new")
	newArt := longArtifactForReload("art-new", "bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb bbbbbbbbbb")
	_, err = newCs.Append(context.Background(), newP, newArt)
	require.NoError(t, err)
	newSnap, _ := newCs.Snapshot(context.Background(), newP)
	newSnapArt := newSnap[0]
	semNew := computeSemanticDigestCert(newSnapArt.Reasoning)
	egNew := sha256.Sum256([]byte(newCfg.Compression.EgressPolicyRef))
	claimNew, err := newCs.ReserveCompression(context.Background(), newP, newSnapArt.ID, newSnapArt.Anchor, newCfg.Compression.EgressPolicyRef, semNew, egNew)
	require.NoError(t, err)
	authNew := reasoningpreservation.ComputeEgressPolicyHash(reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: newCfg.Compression.EgressPolicyRef}, newCfg.Compression.Route)
	routeHashNew := sha256.Sum256([]byte(newCfg.Compression.Route))
	require.NoError(t, newCs.UpdateReservationPolicyHash(context.Background(), claimNew, egNew, semNew, authNew, reasoningpreservation.SanitizationNone, routeHashNew))
	newID, err := newClient.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "new:selector"}}}, auxiliary.SubmitOptions{CoalesceKey: "reload-new-key", Timeout: time.Second})
	require.NoError(t, err)
	close(firstGate)
	oldRes, err := oldClient.Await(context.Background(), oldID)
	require.NoError(t, err)
	require.Equal(t, "old-gen", oldRes.Text.String())
	newRes, err := newClient.Await(context.Background(), newID)
	require.NoError(t, err)
	require.Equal(t, "new-gen", newRes.Text.String())
	stOld, ok, _ := oldCs.GetCompressionState(context.Background(), oldP, oldSnapArt.ID)
	require.True(t, ok)
	require.Equal(t, authOld, stOld.Pending.EgressPolicyHash)
	require.Equal(t, routeHashOld, stOld.Pending.AuthorizedRouteHash)
	stNew, ok, _ := newCs.GetCompressionState(context.Background(), newP, newSnapArt.ID)
	require.True(t, ok)
	require.Equal(t, authNew, stNew.Pending.EgressPolicyHash)
}

func TestCertification_ShutdownClearsFinishesJobsAndNoOrphanGenpin(t *testing.T) {
	t.Parallel()
	ret := &countingRetainerCert{pin: &countingPinCert{}}
	ret.allow.Store(true)
	ctx := genpin.WithRetainer(context.Background(), ret)
	started := make(chan struct{})
	release := make(chan struct{})
	s := certNewSchedulerNoCleanup(t, func() auxreq.ExecutorRunner {
		return &certBoundRunner{name: "shutdown-gen", start: started, gate: release}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 4})
	firstRunner := &certBoundRunner{name: "shutdown-gen", start: started, gate: release}
	firstID, err := s.BindRunner(firstRunner).SubmitCollect(ctx, certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "shutdown-first", Timeout: time.Second})
	require.NoError(t, err)
	<-started
	secondID, err := s.SubmitCollect(ctx, certBackgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "shutdown-second", Timeout: time.Second})
	require.NoError(t, err)
	_ = secondID
	require.NoError(t, s.Close())
	close(release)
	require.Eventually(t, func() bool { return ret.pin.releases.Load() == ret.retains.Load() }, time.Second, 10*time.Millisecond)
	_, err = s.Await(context.Background(), firstID)
	require.Error(t, err)
	res, err := s.Poll(context.Background(), firstID)
	if err == nil {
		require.True(t, res.State == auxiliary.PollFailed || res.State == auxiliary.PollNotFound)
	}
	require.NoError(t, s.Close())
}

func TestCertification_OriginalEvictionClearsOptionalExactlyOnce(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	limits := reasoningpreservation.CompressionLimits{MaxPendingPerSession: 10, MaxPendingTotal: 10, MaxSurrogateBytesPerTurn: 100, MaxSurrogateBytesPerSession: 1000, MaxSurrogateBytesTotal: 1000}
	opts := defaultStoreOptions(now)
	opts.MaxTurnsPerSession = 2
	opts.MaxSessionBytes = 1024
	opts.CompressionLimits = limits
	cs := newCompressionStoreWithOptions(t, opts)
	p := reasoningpreservation.NewSessionPartition("sess-evict-once")
	policy := "v1"
	for i := range 2 {
		id := "evict-" + string(rune('0'+i))
		art := sampleArtifact(id, "payload", 32)
		_, err := cs.Append(context.Background(), p, art)
		require.NoError(t, err)
		snap, _ := cs.Snapshot(context.Background(), p)
		var snapArt reasoningpreservation.TurnArtifact
		for _, a := range snap {
			if a.ID == id {
				snapArt = a
				break
			}
		}
		sem := semanticDigestFor(policy)
		eg := egressHashFor(policy)
		claim, err := cs.ReserveCompression(context.Background(), p, id, snapArt.Anchor, policy, sem, eg)
		require.NoError(t, err)
		require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
		require.NoError(t, cs.BindCompressionJob(context.Background(), claim, auxiliary.JobID("job-"+id)))
		sur := surrogateFor(snapArt.Anchor, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 10})
		require.NoError(t, cs.AttachSurrogate(context.Background(), claim, auxiliary.JobID("job-"+id), sur))
	}
	require.Equal(t, 20, cs.CompressionStats().TotalSurrogateBytes)
	_, err := cs.Append(context.Background(), p, sampleArtifact("evict-2", "payload2", 32))
	require.NoError(t, err)
	require.Equal(t, 10, cs.CompressionStats().TotalSurrogateBytes)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); _ = cs.ClearCompression(context.Background(), p, "evict-1", "") }()
	go func() { defer wg.Done(); _ = cs.Delete(context.Background(), p, "evict-1") }()
	go func() { defer wg.Done(); _, _ = cs.Append(context.Background(), p, sampleArtifact("evict-3", "x", 32)) }()
	wg.Wait()
	stats := cs.CompressionStats()
	require.GreaterOrEqual(t, stats.TotalSurrogateBytes, 0)
	require.LessOrEqual(t, stats.TotalSurrogateBytes, 10)
	now2, advance := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	opts2 := defaultStoreOptions(now2)
	opts2.TTL = time.Hour
	opts2.CompressionLimits = limits
	cs2 := newCompressionStoreWithOptions(t, opts2)
	p2 := reasoningpreservation.NewSessionPartition("sess-ttl-once")
	art := sampleArtifact("ttl-1", "payload", 32)
	_, err = cs2.Append(context.Background(), p2, art)
	require.NoError(t, err)
	snap, _ := cs2.Snapshot(context.Background(), p2)
	snapArt := snap[0]
	sem := semanticDigestFor(policy)
	eg := egressHashFor(policy)
	claim2, err := cs2.ReserveCompression(context.Background(), p2, snapArt.ID, snapArt.Anchor, policy, sem, eg)
	require.NoError(t, err)
	require.NoError(t, cs2.UpdateReservationPolicyHash(context.Background(), claim2, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs2.BindCompressionJob(context.Background(), claim2, auxiliary.JobID("job-ttl")))
	sur := surrogateFor(snapArt.Anchor, policy, reasoningpreservation.SurrogateSegment{PlacementIndex: 0, Text: "hi", Bytes: 5})
	require.NoError(t, cs2.AttachSurrogate(context.Background(), claim2, auxiliary.JobID("job-ttl"), sur))
	require.Equal(t, 5, cs2.CompressionStats().TotalSurrogateBytes)
	advance(2 * time.Hour)
	_, _ = cs2.Snapshot(context.Background(), p2)
	require.Equal(t, 0, cs2.CompressionStats().TotalSurrogateBytes)
}

func TestCertification_NoLockAcrossPolicyProvider(t *testing.T) {
	t.Parallel()
	blockPolicy := &blockingEgressPolicy{block: make(chan struct{})}
	blockSan := &blockingSanitizer{block: make(chan struct{})}
	blockClient := &blockingBackgroundClient{block: make(chan struct{})}
	cfg := pollTestConfig(t)
	cfg.Compression.Route = "blocking-route"
	cs := certStoreForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-block")
	art := certArtifact("t-block", "reasoning-block", 32)
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	snap, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap, 1)
	snapArt := snap[0]
	sem := semanticDigestFor("v1")
	eg := egressHashFor("v1")
	claim, err := cs.ReserveCompression(context.Background(), p, snapArt.ID, snapArt.Anchor, "v1", sem, eg)
	require.NoError(t, err)
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, eg, sem, eg, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	svcPolicy := reasoningpreservation.CompressionServices{Client: blockClient, Poller: blockClient, EgressPolicy: blockPolicy, Sanitizer: blockSan}
	corr := reasoningpreservation.PostAppendCorrelation{
		Partition: p, ArtifactID: snapArt.ID, Anchor: snapArt.Anchor, OriginalDigest: snapArt.Anchor,
		SemanticDigest: sem, EgressPolicyRefHash: eg, SourceBytes: 10, PolicyRevision: "v1",
	}
	res := reasoningpreservation.ReservationResult{Outcome: reasoningpreservation.ReservationReserved, Claim: claim, Correlation: corr}
	egressStage := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svcPolicy, func(ctx context.Context, pr reasoningpreservation.PreparedReservation) error { return nil })
	done := make(chan struct{})
	go func() { _ = egressStage(context.Background(), res); close(done) }()
	time.Sleep(20 * time.Millisecond)
	quickCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err = cs.GetCompressionState(quickCtx, p, snapArt.ID)
	require.NoError(t, err)
	close(blockPolicy.block)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("egress blocked")
	}
	blockSan2 := &blockingSanitizer{block: make(chan struct{})}
	allowPolicy := pollTestEgress{}
	svcSan := reasoningpreservation.CompressionServices{Client: blockClient, Poller: blockClient, EgressPolicy: allowPolicy, Sanitizer: blockSan2}
	art2 := certArtifact("t-block2", "secret reasoning", 32)
	_, err = cs.Append(context.Background(), p, art2)
	require.NoError(t, err)
	snap2, _ := cs.Snapshot(context.Background(), p)
	require.Len(t, snap2, 2)
	var snapArt2 reasoningpreservation.TurnArtifact
	for _, a := range snap2 {
		if a.ID == "t-block2" {
			snapArt2 = a
			break
		}
	}
	require.Equal(t, "t-block2", snapArt2.ID)
	claim2, err := cs.ReserveCompression(context.Background(), p, snapArt2.ID, snapArt2.Anchor, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
	require.NoError(t, err)
	svcSan.EgressPolicy = redactEgressCert{}
	res2 := reasoningpreservation.ReservationResult{Outcome: reasoningpreservation.ReservationReserved, Claim: claim2, Correlation: reasoningpreservation.PostAppendCorrelation{
		Partition: p, ArtifactID: snapArt2.ID, Anchor: snapArt2.Anchor, OriginalDigest: snapArt2.Anchor,
		SemanticDigest: semanticDigestFor("v1"), EgressPolicyRefHash: egressHashFor("v1"), SourceBytes: 10, PolicyRevision: "v1",
	}}
	egressStage2 := reasoningpreservation.NewPostReservationEgressStage(cfg, cs, svcSan, func(ctx context.Context, pr reasoningpreservation.PreparedReservation) error { return nil })
	done2 := make(chan struct{})
	go func() { _ = egressStage2(context.Background(), res2); close(done2) }()
	time.Sleep(20 * time.Millisecond)
	quickCtx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	_, _, err = cs.GetCompressionState(quickCtx2, p, snapArt2.ID)
	require.NoError(t, err)
	close(blockSan2.block)
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("sanitizer blocked")
	}
	blockClient2 := &blockingBackgroundClient{block: make(chan struct{})}
	svcClient := reasoningpreservation.CompressionServices{Client: blockClient2, Poller: blockClient2, EgressPolicy: allowPolicy, Sanitizer: pollTestSan{}}
	pr := reasoningpreservation.PreparedReservation{
		Reservation: reasoningpreservation.ReservationResult{Outcome: reasoningpreservation.ReservationReserved, Claim: claim2, Correlation: reasoningpreservation.PostAppendCorrelation{
			Partition: p, ArtifactID: snapArt2.ID, Anchor: snapArt2.Anchor, OriginalDigest: snapArt2.Anchor, SemanticDigest: semanticDigestFor("v1"), EgressPolicyRefHash: egressHashFor("v1"), PolicyRevision: "v1",
		}},
		Segments: []reasoningpreservation.CompressorInputSegment{{Index: 0, Text: "hi"}},
		Decision: reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"},
		Route:    cfg.Compression.Route,
	}
	submitStage := reasoningpreservation.NewPostEgressSubmitStage(cfg, cs, svcClient)
	done3 := make(chan struct{})
	go func() { _ = submitStage(context.Background(), pr); close(done3) }()
	time.Sleep(20 * time.Millisecond)
	quickCtx3, cancel3 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel3()
	_, _, err = cs.GetCompressionState(quickCtx3, p, snapArt2.ID)
	require.NoError(t, err)
	close(blockClient2.block)
	select {
	case <-done3:
	case <-time.After(time.Second):
		t.Fatal("submit blocked")
	}
}
