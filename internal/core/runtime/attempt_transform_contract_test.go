package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

const attemptTransformMarker = "attempt-transform-marker"

type recordingAttemptTransform struct {
	id       string
	mode     string
	calls    *atomic.Int64
	metaMu   sync.Mutex
	metas    []request.AttemptMeta
	callPtrs []*aipapi.Call
}

func (r *recordingAttemptTransform) ID() string {
	if r.id != "" {
		return r.id
	}
	return "recording-attempt-xform"
}
func (r *recordingAttemptTransform) Order() int                        { return 0 }
func (r *recordingAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (r *recordingAttemptTransform) HandleAttempt(_ context.Context, call *aipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	r.metaMu.Lock()
	r.metas = append(r.metas, meta)
	r.callPtrs = append(r.callPtrs, call)
	r.metaMu.Unlock()
	switch r.mode {
	case "exclude_first":
		if meta.BackendID == "a" || strings.HasPrefix(meta.CandidateKey, "a:") {
			return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "unrepresentable_replay"}, nil
		}
	case "mutate_marker":
		if call != nil {
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart(attemptTransformMarker)},
			})
		}
	case "mutate_tool_after_shape":
		if call != nil {
			call.Tools = append(call.Tools, aipapi.ToolDef{Name: "post-shape-tool", Description: "added after shape"})
		}
	case "mutate_tools_caps":
		if call != nil {
			call.Tools = append(call.Tools, aipapi.ToolDef{Name: "need-tools", Description: "forces CapabilityTools"})
		}
	case "tag_candidate":
		if call != nil {
			call.ID = "tagged:" + meta.CandidateKey
		}
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func attemptTransformBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "baseline-immutable",
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
}

func contributeAttemptTransformBundle(t *testing.T, xform request.AttemptTransform) aipfeature.FeatureBundle {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	if len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)) != 1 {
		t.Fatal("bundle must carry AttemptTransforms contribution")
	}
	return b
}

// wireMergedAttemptSurface mirrors bootstrap: MergeBundlesGenerated + SnapshotOptions contribution.
func wireMergedAttemptSurface(t *testing.T, bundle aipfeature.FeatureBundle) (*hooks.Bus, *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: gen.Frozen,
	})
	if want, got := len(aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)), len(snap.AttemptTransforms()); want != got {
		t.Fatalf("precondition: snapshot AttemptTransforms len=%d want %d", got, want)
	}
	return bus, snap
}

func TestCandidateAttemptTransform_invokedWithMetaMutationObservedByOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "mutate_marker", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"be": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call aipapi.Call, c routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened.Add(1)
				gotMu.Lock()
				gotCall = call
				gotMu.Unlock()
				_ = c
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	stream, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("be:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		if _, err := aipapi.Collect(t.Context(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}
	if opened.Load() == 0 {
		t.Fatal("expected backend Open after Execute")
	}

	gotMu.Lock()
	openedCall := gotCall
	gotMu.Unlock()
	hasMarker := false
	for _, msg := range openedCall.Instructions {
		if strings.Contains(textOf(msg), attemptTransformMarker) {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		t.Fatal("RED: AttemptTransform contributed via FeatureBundle must mutate the candidate clone observed by backend Open (runner absent)")
	}
	xform.metaMu.Lock()
	metas := append([]request.AttemptMeta(nil), xform.metas...)
	xform.metaMu.Unlock()
	if len(metas) == 0 {
		t.Fatal("RED: AttemptTransform must be invoked with AttemptMeta for the selected candidate")
	}
	meta := metas[0]
	if meta.BackendID == "" || meta.CandidateKey == "" || meta.Model == "" {
		t.Fatalf("RED: AttemptMeta incomplete: %+v", meta)
	}
}

func TestCandidateAttemptTransform_runsAfterInterleavedShapeBeforeCapabilities(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "mutate_tool_after_shape", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
		Enabled:      true,
		Instructions: "Think step by step and emit a memo.",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	ex.Backends = map[string]execbackend.Backend{
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			func(c aipapi.Call) {
				gotMu.Lock()
				gotCall = c
				gotMu.Unlock()
			},
		),
		"unused-exec": recoverableInterleavedBackend(nil),
	}

	call := interleavedBaseCall("[thinker]thinker-be:m^unused-exec:m")
	stream, execErr := ex.Execute(t.Context(), call)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		if _, err := aipapi.Collect(t.Context(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()
	if len(shaped.Instructions) == 0 || !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatal("interleaved shape must run (thinker instructions) before asserting transform placement")
	}
	hasPostShapeTool := false
	for _, tool := range shaped.Tools {
		if tool.Name == "post-shape-tool" {
			hasPostShapeTool = true
			break
		}
	}
	if !hasPostShapeTool {
		t.Fatal("RED: AttemptTransform must run after shapeAttemptCall so post-shape tool mutations reach Open (thinker shape otherwise leaves Tools empty)")
	}
}

func TestCandidateAttemptTransform_mutationAffectsRequiredCapabilitiesBeforeOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "mutate_tools_caps", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var openedA, openedB atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedA.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedB.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	stream, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("a:m|b:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if openedA.Load() != 0 {
		t.Fatal("RED: AttemptTransform tool mutation must rederive RequiredCapabilities before Open so backend a (no tools) never opens")
	}
	if openedB.Load() == 0 {
		t.Fatal("RED: after capability reject of a, failover must open backend b which advertises tools")
	}
}

func TestCandidateAttemptTransform_excludeSkipsOpenAndFailsover(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "exclude_first", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var openedA, openedB atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedA.Add(1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("should-not-open-a"))
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedB.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	stream, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("a:m|b:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if openedA.Load() != 0 {
		t.Fatal("RED: exclude_candidate must prevent backend Open for the excluded candidate")
	}
	if openedB.Load() == 0 {
		t.Fatal("RED: normal failover must open the next candidate after exclude_candidate")
	}
}

func TestCandidateAttemptTransform_sequentialAttemptsStartFromImmutableBaseline(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "mutate_marker", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var mu sync.Mutex
	var openCalls []aipapi.Call
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				openCalls = append(openCalls, call)
				mu.Unlock()
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				openCalls = append(openCalls, call)
				mu.Unlock()
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}

	baseline := attemptTransformBaseCall("a:m|b:m")
	stream, execErr := ex.Execute(t.Context(), baseline)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if baseline.ID != "baseline-immutable" {
		t.Fatal("immutable baseline must not observe attempt-transform mutations")
	}
	mu.Lock()
	got := append([]aipapi.Call(nil), openCalls...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("want sequential failover opens>=2 got %d", len(got))
	}
	countMarker := func(c aipapi.Call) int {
		n := 0
		for _, msg := range c.Instructions {
			if strings.Contains(textOf(msg), attemptTransformMarker) {
				n++
			}
		}
		return n
	}
	if countMarker(got[0]) != 1 || countMarker(got[1]) != 1 {
		t.Fatal("RED: each sequential attempt must CloneCall(baseline) and run AttemptTransform independently (no shared mutation accumulation)")
	}
}

func TestCandidateAttemptTransform_parallelArmsIndependentClones(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &recordingAttemptTransform{mode: "tag_candidate", calls: &atomic.Int64{}}
	bundle := contributeAttemptTransformBundle(t, xform)
	bus, snap := wireMergedAttemptSurface(t, bundle)

	var mu sync.Mutex
	var openIDs []string
	var openArrived atomic.Int32
	gate := make(chan struct{})
	const openBarrierTimeout = 3 * time.Second
	tracking := func(name string) execbackend.Backend {
		return execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				openIDs = append(openIDs, call.ID)
				mu.Unlock()
				if openArrived.Add(1) == 2 {
					close(gate)
				}
				select {
				case <-gate:
				case <-time.After(openBarrierTimeout):
					return nil, errors.New("parallel open barrier timed out waiting for peer arm")
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: name + "-response"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"slow": tracking("slow"),
		"fast": tracking("fast"),
	}

	stream, execErr := ex.Execute(t.Context(), attemptTransformBaseCall("slow:model!fast:model"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	mu.Lock()
	ids := append([]string(nil), openIDs...)
	mu.Unlock()
	if len(ids) < 2 {
		t.Fatalf("parallel arms must each Open, got %d", len(ids))
	}
	tagged := 0
	seen := map[string]struct{}{}
	for _, id := range ids {
		if strings.HasPrefix(id, "tagged:") {
			tagged++
			seen[id] = struct{}{}
		}
	}
	if tagged < 2 || len(seen) < 2 {
		t.Fatal("RED: each parallel arm must invoke AttemptTransform on an independent CloneCall(baseline)")
	}
}
