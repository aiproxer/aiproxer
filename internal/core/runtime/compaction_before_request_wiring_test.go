package runtime_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedSequence records one global callback order across the detector and
// preserver spies so the pre-open ordering assertion is meaningful.
type sharedSequence struct {
	mu    sync.Mutex
	order []string
}

func (s *sharedSequence) append(step string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, step)
}

func (s *sharedSequence) snapshot() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

// spyBeforeRequestDetector records PreviewRequest/RequestOpened ordering for the
// pre-open wiring test. PreviewRequest returns a completion candidate so the
// preserver receives correlation metadata.
type spyBeforeRequestDetector struct {
	mu       sync.Mutex
	order    []string
	previews []compaction.PreservationMeta
	opened   []compaction.PreservationMeta
	shared   *sharedSequence
}

func (s *spyBeforeRequestDetector) append(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, step)
}

func (s *spyBeforeRequestDetector) PreviewRequest(meta compaction.PreservationMeta, _ aipapi.Call) compaction.RequestPreview {
	s.mu.Lock()
	s.previews = append(s.previews, meta)
	s.mu.Unlock()
	s.append("detector-preview-request")
	s.shared.append("detector-preview-request")
	return compaction.RequestPreview{
		Kind:          compaction.PreviewCompletionCandidate,
		RuleID:        "test.before_request.v1",
		Evidence:      compaction.EvidenceHistoryHeuristic,
		TransactionID: "tx-before-1",
	}
}

func (s *spyBeforeRequestDetector) RequestOpened(meta compaction.PreservationMeta, _ aipapi.Call) []compaction.Event {
	s.mu.Lock()
	s.opened = append(s.opened, meta)
	s.mu.Unlock()
	s.append("detector-request-opened")
	s.shared.append("detector-request-opened")
	return nil
}

func (s *spyBeforeRequestDetector) PreviewResponse(compaction.PreservationMeta, aipapi.Event) compaction.ResponsePreview {
	return compaction.ResponsePreview{Kind: compaction.PreviewNone}
}

func (s *spyBeforeRequestDetector) ResponseReleased(compaction.PreservationMeta, aipapi.Event) []compaction.Event {
	return nil
}

func (s *spyBeforeRequestDetector) snapshot() (order []string, previews, opened []compaction.PreservationMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order),
		slices.Clone(s.previews),
		slices.Clone(s.opened)
}

// spyBeforeRequestPreserver records BeforeRequest invocation with its ctx, preview and meta.
type spyBeforeRequestPreserver struct {
	mu       sync.Mutex
	order    []string
	calls    int
	ctxs     []context.Context
	previews []compaction.RequestPreview
	metas    []compaction.PreservationMeta
	shared   *sharedSequence
}

func (p *spyBeforeRequestPreserver) ID() string { return "before-request-spy" }

func (p *spyBeforeRequestPreserver) BeforeRequest(ctx context.Context, _ *aipapi.Call, preview compaction.RequestPreview, meta compaction.PreservationMeta, _ compaction.Services) error {
	p.mu.Lock()
	p.calls++
	p.order = append(p.order, "preserver-before-request")
	p.ctxs = append(p.ctxs, ctx)
	p.previews = append(p.previews, preview)
	p.metas = append(p.metas, meta)
	shared := p.shared
	p.mu.Unlock()
	shared.append("preserver-before-request")
	return nil
}

func (p *spyBeforeRequestPreserver) RequestOpened(_ context.Context, _ aipapi.Call, _ []compaction.Event, _ compaction.PreservationMeta, _ compaction.Services) error {
	p.mu.Lock()
	p.order = append(p.order, "preserver-request-opened")
	shared := p.shared
	p.mu.Unlock()
	shared.append("preserver-request-opened")
	return nil
}

func (p *spyBeforeRequestPreserver) BeforeResponseRelease(_ context.Context, _ *aipapi.Event, _ compaction.ResponsePreview, _ compaction.PreservationMeta, _ compaction.Services) error {
	return nil
}

func (p *spyBeforeRequestPreserver) snapshot() (order []string, calls int, previews []compaction.RequestPreview, metas []compaction.PreservationMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.order), p.calls,
		slices.Clone(p.previews),
		slices.Clone(p.metas)
}

func (p *spyBeforeRequestPreserver) contexts() []context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ctxs)
}

func stubBeforeRequestBackend() map[string]execbackend.Backend {
	return map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			})
		}),
	}
}

// TestCompactionBeforeRequest_PreOpenOrderingAndMeta proves the TP-2 wiring:
// PreviewRequest runs pre-open, BeforeRequest runs exactly once per logical
// request before RequestOpened, with pre-open meta (BLegID empty) carrying the
// preview correlation.
func TestCompactionBeforeRequest_PreOpenOrderingAndMeta(t *testing.T) {
	t.Parallel()

	detector := &spyBeforeRequestDetector{}
	preserver := &spyBeforeRequestPreserver{}
	seq := &sharedSequence{}
	detector.shared = seq
	preserver.shared = seq
	ex := configureRuntimeCompactionPreserver(t, detector, nil, preserver, nil, nil)
	ex.Backends = stubBeforeRequestBackend()

	call := compactCall("ck-before-request", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	_, previews, opened := detector.snapshot()
	_, calls, presPreviews, metas := preserver.snapshot()

	require.Len(t, previews, 1, "PreviewRequest must run exactly once pre-open")
	require.Equal(t, 1, calls, "preserver BeforeRequest must run exactly once per logical request")
	require.Len(t, opened, 1, "RequestOpened must still run once after open")

	require.Len(t, metas, 1)
	beforeMeta := metas[0]
	assert.Empty(t, beforeMeta.BLegID, "pre-open meta must leave BLegID empty (no B-leg yet)")
	assert.Zero(t, beforeMeta.AttemptSeq, "pre-open meta must leave AttemptSeq unset")
	assert.NotEmpty(t, beforeMeta.TraceID, "pre-open meta must carry TraceID")
	assert.NotEmpty(t, beforeMeta.ALegID, "pre-open meta must carry ALegID")
	assert.NotEmpty(t, beforeMeta.SessionID, "pre-open meta must carry SessionID")
	assert.Equal(t, "tx-before-1", beforeMeta.TransactionID, "pre-open meta must carry preview TransactionID")
	assert.Equal(t, "test.before_request.v1", beforeMeta.RuleID)
	assert.Equal(t, compaction.EvidenceHistoryHeuristic, beforeMeta.Evidence)

	require.Len(t, presPreviews, 1)
	assert.Equal(t, compaction.PreviewCompletionCandidate, presPreviews[0].Kind)
	assert.Equal(t, "tx-before-1", presPreviews[0].TransactionID)

	// Global ordering on one shared recorder: preview -> before-request ->
	// detector open -> preserver opened-callback. A shared sequence proves the
	// runtime interleaving; per-spy recorders cannot.
	assert.Equal(t, []string{"detector-preview-request", "preserver-before-request", "detector-request-opened", "preserver-request-opened"}, seq.snapshot(),
		"pre-open ordering must be PreviewRequest -> BeforeRequest -> RequestOpened, got %v", seq.snapshot())
}

// TestCompactionBeforeRequest_FailoverRunsOnce proves the pre-open hook does not
// repeat on retry/failover replacement B-legs.
func TestCompactionBeforeRequest_FailoverRunsOnce(t *testing.T) {
	t.Parallel()

	detector := &spyBeforeRequestDetector{}
	preserver := &spyBeforeRequestPreserver{}
	ex := configureRuntimeCompactionPreserver(t, detector, nil, preserver, nil, nil)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("bad upstream"))
			},
		},
		"good": stubBeforeRequestBackend()["openai"],
	}

	call := compactCall("ck-before-failover", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	call.Route = aipapi.RouteIntent{Selector: "bad:m|good:m"}
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	_, _, _ = detector.snapshot()
	_, calls, _, _ := preserver.snapshot()
	assert.Equal(t, 1, calls, "BeforeRequest must run once per logical request across failover, got %d", calls)
}

// TestCompactionBeforeRequest_AuxiliarySkipped proves auxiliary execution does
// not invoke the pre-open preserver callback.
func TestCompactionBeforeRequest_AuxiliarySkipped(t *testing.T) {
	t.Parallel()

	detector := &spyBeforeRequestDetector{}
	preserver := &spyBeforeRequestPreserver{}
	ex := configureRuntimeCompactionPreserver(t, detector, nil, preserver, nil, nil)
	ex.Backends = stubBeforeRequestBackend()

	call := compactCall("ck-before-aux", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	auxCtx := execctx.WithAuxiliaryDepth(context.Background(), 1)
	stream, err := ex.Execute(auxCtx, call)
	require.NoError(t, err)
	drain(t, stream)

	_, calls, _, _ := preserver.snapshot()
	assert.Equal(t, 0, calls, "auxiliary depth must skip BeforeRequest, got %d", calls)
}

// configureBeforeRequestExecutor mirrors configureRuntimeCompactionPreserver
// but allows session openers and an explicit preserver list (including empty)
// so pre-open propagation tests control the full fixture.
func configureBeforeRequestExecutor(t *testing.T, d runtime.CompactionDetector, preservers []compaction.Preserver, openers []session.Opener) *runtime.Executor {
	t.Helper()
	ex := compactionTestExecutor(t, d, nil)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionPreservers: preservers,
			SessionOpeners:       openers,
		}),
	})
	ex.CompactionDetector = d
	return ex
}

// stubSessionLabelOpener upserts fixed session labels so propagation tests can
// prove session-open labels reach the pre-open preserver observer ctx.
type stubSessionLabelOpener struct {
	labels map[string]string
}

func (o stubSessionLabelOpener) ID() string { return "test-session-labels" }

func (o stubSessionLabelOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: o.labels}, nil
}

// TestCompactionBeforeRequest_NoPreserversSkipsPreview proves the pre-open
// observer does not pay for a detector preview when no preservation callbacks
// are registered, while post-open observation still runs.
func TestCompactionBeforeRequest_NoPreserversSkipsPreview(t *testing.T) {
	t.Parallel()

	detector := &spyBeforeRequestDetector{}
	ex := configureBeforeRequestExecutor(t, detector, nil, nil)
	ex.Backends = stubBeforeRequestBackend()

	call := compactCall("ck-before-no-preservers", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	_, previews, opened := detector.snapshot()
	assert.Empty(t, previews, "PreviewRequest must be skipped with no preservers registered")
	assert.Len(t, opened, 1, "RequestOpened must still run post-open")
}

// TestCompactionBeforeRequest_PropagatesSessionView proves the pre-open
// observer ctx carries the minimal trusted session view (authoritative IDs
// plus session-open label upserts), so pre-open policy resolution sees the
// same session overrides as post-open callbacks.
func TestCompactionBeforeRequest_PropagatesSessionView(t *testing.T) {
	t.Parallel()

	detector := &spyBeforeRequestDetector{}
	preserver := &spyBeforeRequestPreserver{}
	ex := configureBeforeRequestExecutor(t, detector, []compaction.Preserver{preserver}, []session.Opener{
		stubSessionLabelOpener{labels: map[string]string{"test.preopen.marker": "1"}},
	})
	ex.Backends = stubBeforeRequestBackend()

	call := compactCall("ck-before-session-view", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	_, calls, _, metas := preserver.snapshot()
	require.Equal(t, 1, calls, "preserver BeforeRequest must run once")
	ctxs := preserver.contexts()
	require.Len(t, ctxs, 1)
	view, ok := session.SessionViewFromContext(ctxs[0])
	require.True(t, ok, "pre-open observer ctx must carry a session view")
	assert.NotEmpty(t, view.AuthoritativeSessionID, "session view must carry the authoritative session ID")
	assert.NotEmpty(t, view.ALegID, "session view must carry the A-leg ID")
	assert.Equal(t, "1", view.Labels["test.preopen.marker"], "session-open labels must reach the pre-open observer ctx")
	require.Len(t, metas, 1)
	assert.Equal(t, view.ALegID, metas[0].ALegID, "observer meta and session view must agree on A-leg")
}
