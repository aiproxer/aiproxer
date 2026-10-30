package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/stretchr/testify/require"
)

// causeMarkingTransform stands for a proxy-owned request transformation that
// changed the request and recorded its structural cause.
type causeMarkingTransform struct{}

func (causeMarkingTransform) ID() string                        { return "test-cause-marking" }
func (causeMarkingTransform) Order() int                        { return 900 }
func (causeMarkingTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (causeMarkingTransform) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.StructuralCause = "compaction"
	return nil
}

// testCausePrincipal authenticates the request the secure-session path admits.
func testCausePrincipal() context.Context {
	return execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-structural-cause"})
}

// recordedUsage keeps every usage sample the executor publishes, in order.
type recordedUsage struct{ events []usage.Event }

func (r *recordedUsage) OnUsage(_ context.Context, ev usage.Event) error {
	r.events = append(r.events, ev)
	return nil
}

// installCauseMarking wires the cause-marking transform and a usage observer
// into the rig's executor, the way the production snapshot does.
func installCauseMarking(t *testing.T, rig *bindPanicRig) *recordedUsage {
	t.Helper()
	obs := &recordedUsage{}
	planes := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(planes, aipfeature.PlaneRequestTransforms, "test-cause-marking", []request.Transform{causeMarkingTransform{}}))
	rig.ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(rig.ex.Bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	return obs
}

// TestStructuralCause_CorrectiveReplacementDoesNotRepublish is the P1
// regression on the real receive-phase replacement path. The first attempt
// publishes the cause and reports healthy usage, then finishes with no visible
// output. The corrective retry of that empty visible turn retires the attempt
// and re-opens the same backend and model; the replacement must not publish
// the cause again.
//
// A recoverable pre-output Recv error does not reach this path: it excludes its
// candidate, so the replacement can only land on a different candidate.
func TestStructuralCause_CorrectiveReplacementDoesNotRepublish(t *testing.T) {
	rig := newBindPanicRig(t)
	obs := installCauseMarking(t, rig)
	rig.ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
	var opens atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"causebe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if opens.Add(1) == 1 {
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
						// Healthy under the detector's inclusive formula: 8000/(8000+8000) = 0.5, at the threshold.
						{Kind: aipapi.EventUsageDelta, InputTokens: 8000, CacheReadTokens: 8000, OutputTokens: 5, TotalTokens: 16005},
						{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
					}), nil
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "corrected"},
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
				}), nil
			},
		},
	}
	stream, err := rig.ex.Execute(testCausePrincipal(), canonicalPanicCall("call-cause-corrective", "causebe:m"))
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_, _ = aipapi.Collect(context.Background(), stream)

	require.Equal(t, int32(2), opens.Load(), "the empty visible turn is corrected by exactly one re-open of the same candidate")
	require.GreaterOrEqual(t, len(obs.events), 2)
	require.Equal(t, "compaction", obs.events[0].StructuralCause, "the first attempt publishes the cause")
	require.Empty(t, obs.events[len(obs.events)-1].StructuralCause, "the corrective replacement on the same key must not republish the cause")
}

// TestStructuralCause_NilStreamOpenDoesNotConsumeThePublication is the P2
// regression: a backend that opens no stream publishes nothing, so the
// failover candidate that serves the turn still publishes the cause.
//
// A stream-less Open is a failed open, so it is excluded rather than retried
// on the same key; the cause publication stays scoped to the attempt that
// actually produced usage.
func TestStructuralCause_NilStreamOpenDoesNotConsumeThePublication(t *testing.T) {
	rig := newBindPanicRig(t)
	obs := installCauseMarking(t, rig)
	var opensNil, opensGood atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensNil.Add(1)
				return nil, nil
			},
		},
		"causebe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensGood.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	stream, err := rig.ex.Execute(testCausePrincipal(), canonicalPanicCall("call-cause-nil-stream", "nilstream:m|causebe:m"))
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	_, _ = aipapi.Collect(context.Background(), stream)

	require.Equal(t, int32(1), opensNil.Load(), "the stream-less candidate is opened once")
	require.Equal(t, int32(1), opensGood.Load(), "the failover candidate serves the turn")
	require.Len(t, obs.events, 1, "only the attempt that produced usage publishes")
	require.Equal(t, "compaction", obs.events[0].StructuralCause, "a nil-stream open publishes nothing, so the failover publishes the cause")
}
