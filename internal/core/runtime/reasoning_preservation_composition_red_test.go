package runtime_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

const rpStoredThought = "stored-thought"

var rpLargeThought = strings.Repeat("R", 512)

var rpReplaySupport = aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}

func rpArtifact(text string) reasoningpreservation.TurnArtifact {
	anchor, err := reasoningpreservation.ComputeAnchor(aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart("visible answer")},
	})
	if err != nil {
		panic("rpArtifact: ComputeAnchor: " + err.Error())
	}
	return reasoningpreservation.TurnArtifact{
		ID: "art-1", Anchor: anchor, SourceBackend: "src-be", SourceModel: "src-m",
		Reasoning: []reasoningpreservation.PlacedReasoning{{BeforeNonReasoningPart: 0, Part: aipapi.Part{
			Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: text},
		}}},
		ReasoningBytes: len(text),
	}
}

var (
	rpStoredArtifact = rpArtifact(rpStoredThought)
	rpLargeArtifact  = rpArtifact(rpLargeThought)
)

type reasoningPreservationTransform struct {
	mode      string
	artifacts []reasoningpreservation.TurnArtifact
	replay    aipapi.ReasoningReplaySupport
	calls     *atomic.Int64
	onUnrep   string
	eligible  *bool
}

func (r *reasoningPreservationTransform) ID() string { return "reasoning-preservation-xform" }
func (r *reasoningPreservationTransform) Order() int { return 0 }
func (r *reasoningPreservationTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (r *reasoningPreservationTransform) HandleAttempt(_ context.Context, call *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	eligible := true
	if r.eligible != nil {
		eligible = *r.eligible
	}
	replay := r.replay
	onUnrep := r.onUnrep
	if onUnrep == "" {
		onUnrep = "reject"
	}

	switch r.mode {
	case "restore_missing", "restore_per_candidate", "restore_large", "preserve_or_conflict", "exclude_all_unrepresentable":
		if r.mode == "exclude_all_unrepresentable" {
			replay = aipapi.ReasoningReplaySupport{}
		}
		res, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
			Action:            reasoningpreservation.ActionRestore,
			OnUnrepresentable: onUnrep,
			Call:              call,
			Artifacts:         r.artifacts,
			Eligible:          eligible,
			ReplaySupport:     replay,
		})
		if err != nil {
			return request.AttemptDecision{}, err
		}
		if res.Exclude {
			return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "unrepresentable_replay"}, nil
		}
		if r.mode == "preserve_or_conflict" && res.Mutated {
			return request.AttemptDecision{}, errors.New("preserve/conflict path must not mutate")
		}
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	default:
		return request.AttemptDecision{}, errors.New("unknown reasoning preservation transform mode")
	}
}

func rpReasoningPart(text string) aipapi.Part {
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: text,
	}}
}

func rpMissingReasoningCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		ID: "rp-baseline", Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible answer")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("follow-up")}},
		},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
}

func rpClientReasoningCall(selector, clientThought string) *aipapi.Call {
	call := rpMissingReasoningCall(selector)
	call.Messages[0].Parts = append([]aipapi.Part{rpReasoningPart(clientThought)}, call.Messages[0].Parts...)
	return call
}

func rpReasoningTexts(call *aipapi.Call) []string {
	if call == nil {
		return nil
	}
	var out []string
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil {
				out = append(out, p.Reasoning.Text)
			}
		}
	}
	return out
}

func rpHasReasoningText(call aipapi.Call, want string) bool {
	return slices.Contains(rpReasoningTexts(&call), want)
}

func rpWire(t *testing.T, bundle aipfeature.FeatureBundle) (*hooks.Bus, *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	bus := hooks.New(hooks.Config{ResponsePartHooks: aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneResponsePartHooks)})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: bundle.PlaneSet,
	})
	if want, got := len(aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneAttemptTransforms)), len(snap.AttemptTransforms()); want != got {
		t.Fatalf("precondition: snapshot AttemptTransforms len=%d want %d", got, want)
	}
	if want, got := len(aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneStreamObserverFactories)), len(snap.StreamObserverFactories()); want != got {
		t.Fatalf("precondition: snapshot StreamObserverFactories len=%d want %d", got, want)
	}
	return bus, snap
}

func rpExecutor(t *testing.T, xform *reasoningPreservationTransform, mutate func(*runtime.Executor)) (*runtime.Executor, *reasoningPreservationTransform) {
	t.Helper()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	if mutate != nil {
		mutate(ex)
	}
	return ex, xform
}

func rpCollectCall(t *testing.T, ex *runtime.Executor, call *aipapi.Call) {
	t.Helper()
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		if strings.Contains(err.Error(), `unknown part kind "reasoning"`) {
			t.Fatal("RED: Call.Validate must accept PartReasoning so client-preserved/conflicting history can reach AttemptTransform (Phase 2.1)")
		}
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
}

func rpCollect(t *testing.T, ex *runtime.Executor, selector string) {
	t.Helper()
	rpCollectCall(t, ex, rpMissingReasoningCall(selector))
}

func rpStreamingBackend(openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoningReplay),
		ReplaySupport: rpReplaySupport,
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: openFn,
	}
}

type reasoningAwareTokenEstimator struct{ base int64 }

func (r reasoningAwareTokenEstimator) EstimateRequestTokens(_ context.Context, call aipapi.Call) modelcatalog.SizeEstimate {
	tokens := r.base
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil {
				tokens += int64(len(p.Reasoning.Text))
			}
		}
	}
	return modelcatalog.SizeEstimate{Available: true, Units: "tokens", Input: tokens, Basis: "test-reasoning-restore"}
}

func TestReasoningPreservationComposition_disabledFeatureNonInterference_characterization(t *testing.T) {
	t.Parallel()
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store, ex.Bus, ex.Rand = st, hooks.New(hooks.Config{}), routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opened.Add(1)
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
	})}
	call := rpMissingReasoningCall("be:m")
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(t.Context(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opened.Load() != 1 || call.ID != "rp-baseline" {
		t.Fatalf("baseline unchanged without feature: opens=%d id=%q", opened.Load(), call.ID)
	}
}

func TestReasoningPreservationComposition_exactRestoreBeforeOpen(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_missing", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var gotCall aipapi.Call
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(2)
		ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			gotCall = call
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		})}
	})
	rpCollect(t, ex, "be:m")
	if xform.calls.Load() == 0 {
		t.Fatal("RED: reasoning preservation AttemptTransform must run before backend Open (runner absent)")
	}
	if !rpHasReasoningText(gotCall, rpStoredThought) {
		t.Fatal("RED: restored PartReasoning must reach backend Open after exact restoration path")
	}
}

func TestReasoningPreservationComposition_clientReasoningPreservedNoOverwrite(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"preserved", "conflicting"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			xform := &reasoningPreservationTransform{
				mode: "preserve_or_conflict", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
				replay: rpReplaySupport, calls: &atomic.Int64{},
			}
			var gotCall aipapi.Call
			ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
				ex.Rand = routing.NewSeededRng(2)
				ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					gotCall = call
					return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
				})}
			})
			rpCollectCall(t, ex, rpClientReasoningCall("be:m", "client-thought"))
			if xform.calls.Load() == 0 {
				t.Fatal("RED: reasoning preservation AttemptTransform must run to enforce preserved/conflicting no-overwrite policy")
			}
			if !rpHasReasoningText(gotCall, "client-thought") || rpHasReasoningText(gotCall, rpStoredThought) {
				t.Fatal("RED: preserved/conflicting client PartReasoning must not be overwritten by restoration")
			}
		})
	}
}

func TestReasoningPreservationComposition_failoverRestorationIsolation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, redCalls string
		backendA       func(*sync.Mutex, *[]aipapi.Call) execbackend.Backend
	}{
		{"sequential", "RED: AttemptTransform must run independently per sequential candidate", func(mu *sync.Mutex, calls *[]aipapi.Call) execbackend.Backend {
			return rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				*calls = append(*calls, call)
				mu.Unlock()
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
			})
		}},
		{"recv", "RED: AttemptTransform must run for each recv-replaced candidate", func(mu *sync.Mutex, calls *[]aipapi.Call) execbackend.Backend {
			return rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				*calls = append(*calls, call)
				mu.Unlock()
				return &observerFailAfterStream{
					events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}},
					fail:   aipapi.RecoverablePreOutputError(errors.New("recv-pre-output")),
				}, nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			xform := &reasoningPreservationTransform{
				mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
				replay: rpReplaySupport, calls: &atomic.Int64{},
			}
			var mu sync.Mutex
			var openCalls []aipapi.Call
			ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
				ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(11)
				ex.Backends = map[string]execbackend.Backend{
					"a": tc.backendA(&mu, &openCalls),
					"b": rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						mu.Lock()
						openCalls = append(openCalls, call)
						mu.Unlock()
						return aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventTextDelta, Delta: "replacement"},
							{Kind: aipapi.EventResponseFinished},
						}), nil
					}),
				}
			})
			rpCollect(t, ex, "a:m|b:m")
			mu.Lock()
			got := append([]aipapi.Call(nil), openCalls...)
			mu.Unlock()
			if len(got) < 2 {
				t.Fatalf("want failover opens>=2 got %d", len(got))
			}
			if xform.calls.Load() < 2 {
				t.Fatal(tc.redCalls)
			}
			if !rpHasReasoningText(got[0], rpStoredThought) || !rpHasReasoningText(got[1], rpStoredThought) {
				t.Fatal("RED: restored PartReasoning must reach each isolated failover attempt clone")
			}
		})
	}
}

func TestReasoningPreservationComposition_restoredContextLimitExclusionRecompute(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_large", artifacts: []reasoningpreservation.TurnArtifact{rpLargeArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var openedSmall, openedBig atomic.Int64
	var gotBig aipapi.Call
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(2)
		ex.CatalogResolver = contextLimitCatalogResolver{}
		ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
		ex.RequestTokenEstimator = reasoningAwareTokenEstimator{base: 5}
		ex.Backends = map[string]execbackend.Backend{
			"smallctx": rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedSmall.Add(1)
				_ = call
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			}),
			"bigctx": rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedBig.Add(1)
				gotBig = call
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			}),
		}
	})
	rpCollect(t, ex, "smallctx:m|bigctx:m")
	if xform.calls.Load() == 0 {
		t.Fatal("RED: AttemptTransform must run before context-limit eligibility recompute")
	}
	if openedSmall.Load() != 0 {
		t.Fatal("RED: restored reasoning size must exclude smallctx before Open")
	}
	if openedBig.Load() == 0 {
		t.Fatal("RED: failover must open bigctx after restored reasoning pushes request over smallctx limit")
	}
	if !rpHasReasoningText(gotBig, rpLargeThought) {
		t.Fatal("RED: bigctx Open must observe restored PartReasoning after context-limit failover")
	}
}

func TestReasoningPreservationComposition_parallelRestorationIndependentClones(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var mu sync.Mutex
	var openCalls []aipapi.Call
	var openArrived atomic.Int32
	gate := make(chan struct{})
	track := func(name string) execbackend.Backend {
		return rpStreamingBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			mu.Lock()
			openCalls = append(openCalls, call)
			mu.Unlock()
			if openArrived.Add(1) == 2 {
				close(gate)
			}
			select {
			case <-gate:
			case <-time.After(3 * time.Second):
				return nil, errors.New("parallel open barrier timed out waiting for peer arm")
			}
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: name + "-response"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		})
	}
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(1)
		ex.Backends = map[string]execbackend.Backend{"slow": track("slow"), "fast": track("fast")}
	})
	rpCollect(t, ex, "slow:model!fast:model")
	mu.Lock()
	got := append([]aipapi.Call(nil), openCalls...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("parallel arms must each Open, got %d", len(got))
	}
	if xform.calls.Load() < 2 {
		t.Fatal("RED: AttemptTransform must run independently on each parallel arm clone")
	}
	restored := 0
	for _, call := range got {
		if rpHasReasoningText(call, rpStoredThought) {
			restored++
		}
	}
	if restored < 2 {
		t.Fatal("RED: each parallel arm Open must observe restored PartReasoning on its independent clone")
	}
}

func TestReasoningPreservationComposition_weightedRestorationIndependentClones(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "restore_per_candidate", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{},
	}
	var mu sync.Mutex
	var openCalls []aipapi.Call
	var backends []string
	track := func(name string) execbackend.Backend {
		return rpStreamingBackend(func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			mu.Lock()
			openCalls = append(openCalls, call)
			backends = append(backends, cand.Primary.Backend)
			mu.Unlock()
			_ = name
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "ok"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		})
	}
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(7)
		ex.Backends = map[string]execbackend.Backend{"a": track("a"), "b": track("b")}
	})
	ex.Backends["a"] = rpStreamingBackend(func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		mu.Lock()
		openCalls = append(openCalls, call)
		backends = append(backends, cand.Primary.Backend)
		mu.Unlock()
		return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
	})
	rpCollect(t, ex, "[weight=1]a:m^[weight=1]b:m")
	mu.Lock()
	got := append([]aipapi.Call(nil), openCalls...)
	gotBE := append([]string(nil), backends...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("weighted failover must open >=2 candidates, got %d backends=%v", len(got), gotBE)
	}
	if xform.calls.Load() < 2 {
		t.Fatal("RED: AttemptTransform must run independently for each weighted candidate attempt")
	}
	if !rpHasReasoningText(got[0], rpStoredThought) || !rpHasReasoningText(got[1], rpStoredThought) {
		t.Fatalf("RED: weighted candidates must each observe isolated restored reasoning; opens=%v texts0=%v texts1=%v", gotBE, rpReasoningTexts(&got[0]), rpReasoningTexts(&got[1]))
	}
}

func TestReasoningPreservationComposition_responseHookAndGateCapture_RED(t *testing.T) {
	t.Parallel()
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "hook", []sdkhooks.ResponsePartHook{mutateTextResponseHook{}})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventReasoningDelta, Delta: "thought"},
			{Kind: aipapi.EventTextDelta, Delta: "orig"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})}
	stream, err := ex.Execute(t.Context(), rpMissingReasoningCall("be:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Text.String() != observerHookMarker {
		t.Fatalf("response hook must mutate client text; got %q", col.Text.String())
	}
	if factory.opens.Load() == 0 {
		t.Fatal("RED: stream observer must Open so response-hook mutation can be captured for reasoning preservation")
	}
	obs := factory.snapshotObservers()
	if len(obs) == 0 {
		t.Fatal("RED: observer must exist for hook-mutated capture")
	}
	obs[0].mu.Lock()
	events := append([]aipapi.Event(nil), obs[0].events...)
	obs[0].mu.Unlock()
	sawHook, sawReasoning := false, false
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == observerHookMarker {
			sawHook = true
		}
		if ev.Kind == aipapi.EventReasoningDelta && ev.Delta == "thought" {
			sawReasoning = true
		}
	}
	if !sawHook || !sawReasoning {
		t.Fatal("RED: observer must capture post-hook mutated text and reasoning deltas")
	}

	factory2 := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs2 := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs2, aipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory2})
	_ = aipfeature.Contribute(cs2, aipfeature.PlaneCompletionGates, "gate", []completion.Gate{replaceAllGate{}})
	b2 := aipfeature.BundleFromPlanes(cs2.Freeze(), nil)
	if err := b2.Validate(); err != nil {
		t.Fatal(err)
	}
	st2, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus2, snap2 := rpWire(t, b2)
	ex2 := runtime.TestExecutor()
	ex2.Store, ex2.Bus, ex2.RuntimeSnapshot, ex2.Rand = st2, bus2, snap2, routing.NewSeededRng(2)
	ex2.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("orig")}
	stream2, err := ex2.Execute(t.Context(), rpMissingReasoningCall("be:m"))
	if err != nil {
		t.Fatalf("execute gate: %v", err)
	}
	col2, err := aipapi.Collect(t.Context(), stream2)
	if err != nil {
		t.Fatalf("collect gate: %v", err)
	}
	if col2.Text.String() != "gate-replaced-text" {
		t.Fatalf("gate must replace client text; got %q", col2.Text.String())
	}
	observers := factory2.snapshotObservers()
	if len(observers) == 0 {
		t.Fatal("RED: gate replacement must open observer lifecycle for capture")
	}
	var sawGateReplaced, sawReplacement, sawOrig bool
	for _, o := range observers {
		o.mu.Lock()
		for _, outcome := range o.outcomes {
			if outcome == response.OutcomeGateReplaced {
				sawGateReplaced = true
			}
		}
		for _, ev := range o.events {
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "gate-replaced-text" {
				sawReplacement = true
			}
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "orig" {
				sawOrig = true
			}
		}
		o.mu.Unlock()
	}
	if !sawGateReplaced || !sawReplacement || sawOrig {
		t.Fatal("RED: gate_replaced must discard original capture and observe replacement only")
	}
}

func TestReasoningPreservationComposition_streamObserverContributionDropped(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{mode: "restore_missing", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact}, replay: rpReplaySupport, calls: &atomic.Int64{}}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{factory})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, b)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "ok"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})}
	rpCollect(t, ex, "be:m")
	if factory.opens.Load() == 0 {
		t.Fatal("RED: reasoning FeatureBundle StreamObserverFactories must Open on winning B-leg (runner absent; see Phase 1.3)")
	}
}

func TestReasoningPreservationTransform_unrepresentableRejectExcludesCandidate(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "exclude_all_unrepresentable", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{}, onUnrep: "reject",
	}
	dec, err := xform.HandleAttempt(t.Context(), rpMissingReasoningCall("a:m"), request.AttemptMeta{BackendID: "a", CandidateKey: "a:m"}, request.Services{})
	if err != nil {
		t.Fatalf("RestoreMissingReasoning: %v", err)
	}
	if dec.Kind != request.AttemptExcludeCandidate || dec.ReasonCode != "unrepresentable_replay" {
		t.Fatalf("expected exclude_candidate/unrepresentable_replay, got dec=%+v", dec)
	}
	if xform.calls.Load() != 1 {
		t.Fatalf("transform must be invoked once, calls=%d", xform.calls.Load())
	}
}

func TestReasoningPreservationComposition_unrepresentableReplayAllExcluded_RED(t *testing.T) {
	t.Parallel()
	xform := &reasoningPreservationTransform{
		mode: "exclude_all_unrepresentable", artifacts: []reasoningpreservation.TurnArtifact{rpStoredArtifact},
		replay: rpReplaySupport, calls: &atomic.Int64{}, onUnrep: "reject",
	}
	var opens atomic.Int64
	ex, xform := rpExecutor(t, xform, func(ex *runtime.Executor) {
		ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(11)
		ex.Backends = map[string]execbackend.Backend{
			"a": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			}),
			"b": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			}),
		}
	})
	_, err := ex.Execute(t.Context(), rpMissingReasoningCall("a:m|b:m"))
	if err == nil {
		t.Fatal("RED: all candidates excluded for unrepresentable_replay must surface stable error (contribution/transform wiring absent or exclude path incomplete)")
	}
	if errors.Is(err, reasoningpreservation.ErrNotImplemented) {
		t.Fatal("RED: ErrNotImplemented fail-closed must not satisfy unrepresentable_replay acceptance; RestoreMissingReasoning must emit stable exclude classification")
	}
	safe := strings.ToLower(err.Error())
	if !strings.Contains(safe, "unrepresentable_replay") {
		t.Fatalf("RED: expected stable unrepresentable_replay classification in error, got %v", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("RED: exclude_candidate for all backends must prevent Open, opens=%d", opens.Load())
	}
	if xform.calls.Load() == 0 {
		t.Fatal("RED: reasoning preservation AttemptTransform must run per considered candidate to emit exclude_candidate decisions")
	}
}

func TestReasoningPreservationComposition_noRetryAfterFirstOutput_characterization(t *testing.T) {
	t.Parallel()
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store, ex.Bus, ex.MaxAttempts, ex.Rand = st, hooks.New(hooks.Config{}), 3, routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"one": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return &deltaThenErrStream{n: 0}, nil
		}),
		"two": rpStreamingBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		}),
	}
	stream, err := ex.Execute(t.Context(), rpMissingReasoningCall("one:m|two:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for range 3 {
		if _, err := stream.Recv(t.Context()); err != nil {
			t.Fatalf("unexpected recv error: %v", err)
		}
	}
	_, recvErr := stream.Recv(t.Context())
	if recvErr == nil {
		t.Fatal("expected error after committed output")
	}
	if aipapi.IsRecoverablePreOutput(recvErr) {
		t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
	}
	if opens.Load() != 1 {
		t.Fatalf("expected no failover backend open after text delta committed, opens=%d", opens.Load())
	}
	_ = stream.Close()
}
