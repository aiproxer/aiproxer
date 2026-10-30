package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

const observerHookMarker = "post-hook-mutated"

type recordingStreamObserver struct {
	events   []aipapi.Event
	outcomes []response.StreamOutcome
	finishN  atomic.Int64
	observeN atomic.Int64
	failObs  bool
	mu       sync.Mutex
}

func (r *recordingStreamObserver) Observe(_ context.Context, ev aipapi.Event) error {
	r.observeN.Add(1)
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	if r.failObs {
		return errors.New("observer observe failure")
	}
	return nil
}

func (r *recordingStreamObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	r.finishN.Add(1)
	r.mu.Lock()
	r.outcomes = append(r.outcomes, outcome)
	r.mu.Unlock()
	return nil
}

type recordingStreamObserverFactory struct {
	id        string
	opens     *atomic.Int64
	observers []*recordingStreamObserver
	failObs   bool
	metaMu    sync.Mutex
	metas     []response.StreamMeta
	obsMu     sync.Mutex
}

func (f *recordingStreamObserverFactory) ID() string {
	if f.id != "" {
		return f.id
	}
	return "recording-stream-obs"
}
func (f *recordingStreamObserverFactory) Order() int                        { return 0 }
func (f *recordingStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f *recordingStreamObserverFactory) Open(_ context.Context, meta response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	if f.opens != nil {
		f.opens.Add(1)
	}
	f.metaMu.Lock()
	f.metas = append(f.metas, meta)
	f.metaMu.Unlock()
	obs := &recordingStreamObserver{failObs: f.failObs}
	f.obsMu.Lock()
	f.observers = append(f.observers, obs)
	f.obsMu.Unlock()
	return obs, nil
}

func (f *recordingStreamObserverFactory) snapshotObservers() []*recordingStreamObserver {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	out := make([]*recordingStreamObserver, len(f.observers))
	copy(out, f.observers)
	return out
}

type mutateTextResponseHook struct{}

func (mutateTextResponseHook) ID() string                        { return "mutate-text" }
func (mutateTextResponseHook) Order() int                        { return 0 }
func (mutateTextResponseHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (mutateTextResponseHook) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdkhooks.PartMeta) error {
	if ev != nil && ev.Kind == aipapi.EventTextDelta {
		ev.Delta = observerHookMarker
	}
	return nil
}

type replaceAllGate struct{}

func (replaceAllGate) ID() string                        { return "replace-all-obs" }
func (replaceAllGate) Order() int                        { return 0 }
func (replaceAllGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (replaceAllGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "gate-replaced-text"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

type observerFailAfterStream struct {
	events []aipapi.Event
	i      int
	fail   error
}

func (s *observerFailAfterStream) Recv(context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}
func (*observerFailAfterStream) Close() error { return nil }
func (*observerFailAfterStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func contributeStreamObserverBundle(t *testing.T, factory response.StreamObserverFactory, extras ...func(*aipfeature.ContributionSet)) aipfeature.FeatureBundle {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "obs-factory", []response.StreamObserverFactory{factory}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	for _, extra := range extras {
		extra(cs)
	}
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	if len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneStreamObserverFactories)) != 1 {
		t.Fatal("bundle must carry StreamObserverFactories contribution")
	}
	return b
}

func wireMergedObserverSurface(t *testing.T, bundle aipfeature.FeatureBundle) (*hooks.Bus, *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	bus := hooks.New(hooks.Config{
		ResponsePartHooks: aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneResponsePartHooks),
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: bundle.PlaneSet,
	})
	if want, got := len(aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneStreamObserverFactories)), len(snap.StreamObserverFactories()); want != got {
		t.Fatalf("precondition: snapshot StreamObserverFactories len=%d want %d", got, want)
	}
	return bus, snap
}

func observerBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "obs-call",
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

func fixedSuccessBackend(text string) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: text},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func TestFinalStreamObserver_postHookMutatedEventObserved(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	bundle := contributeStreamObserverBundle(t, factory, func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "hook", []sdkhooks.ResponsePartHook{mutateTextResponseHook{}})
	})
	bus, snap := wireMergedObserverSurface(t, bundle)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("orig")}

	stream, execErr := ex.Execute(t.Context(), observerBaseCall("be:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := col.Text.String(); got != observerHookMarker {
		t.Fatalf("response hook must mutate client text; got %q", got)
	}

	if factory.opens.Load() == 0 {
		t.Fatal("RED: StreamObserverFactory contributed via FeatureBundle must Open for the active B-leg (runner absent)")
	}
	obs := factory.snapshotObservers()
	if len(obs) == 0 {
		t.Fatal("RED: Open must return an observer")
	}
	obs[0].mu.Lock()
	events := append([]aipapi.Event(nil), obs[0].events...)
	obs[0].mu.Unlock()
	saw := false
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == observerHookMarker {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatal("RED: observer must Observe post-response-hook mutated final events")
	}
}

func TestFinalStreamObserver_gateReplacementObservesReplacementOnly(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	bundle := contributeStreamObserverBundle(t, factory, func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "gate", []completion.Gate{replaceAllGate{}})
	})
	bus, snap := wireMergedObserverSurface(t, bundle)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("orig")}

	stream, execErr := ex.Execute(t.Context(), observerBaseCall("be:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := col.Text.String(); got != "gate-replaced-text" {
		t.Fatalf("gate must replace client text; got %q", got)
	}

	observers := factory.snapshotObservers()
	if len(observers) == 0 {
		t.Fatal("RED: gate replacement must open observer lifecycle for original and/or replacement stream")
	}
	var sawGateReplaced, sawReplacementText, sawOrigText bool
	for _, obs := range observers {
		obs.mu.Lock()
		for _, o := range obs.outcomes {
			if o == response.OutcomeGateReplaced {
				sawGateReplaced = true
			}
		}
		for _, ev := range obs.events {
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "gate-replaced-text" {
				sawReplacementText = true
			}
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "orig" {
				sawOrigText = true
			}
		}
		obs.mu.Unlock()
	}
	if !sawGateReplaced {
		t.Fatal("RED: original observer must Finish with gate_replaced when completion gate replaces buffered output")
	}
	if !sawReplacementText {
		t.Fatal("RED: only the final replacement stream may be observed/captured")
	}
	if sawOrigText {
		t.Fatal("RED: original buffered gate stream must not be captured after gate_replaced")
	}
}

func TestFinalStreamObserver_successReleasedAfterResponseFinished(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	bundle := contributeStreamObserverBundle(t, factory)
	bus, snap := wireMergedObserverSurface(t, bundle)

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("ok")}

	stream, execErr := ex.Execute(t.Context(), observerBaseCall("be:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !col.FinishReceived {
		t.Fatal("expected response_finished released to collector")
	}

	observers := factory.snapshotObservers()
	if len(observers) == 0 {
		t.Fatal("RED: successful stream must Open a final-stream observer")
	}
	obs := observers[0]
	if obs.finishN.Load() != 1 {
		t.Fatalf("RED: Finish must run exactly once; got %d", obs.finishN.Load())
	}
	obs.mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
	events := append([]aipapi.Event(nil), obs.events...)
	obs.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeSuccessReleased {
		t.Fatalf("RED: want Finish(success_released) after response_finished release, got %#v", outcomes)
	}
	sawFinished := false
	for _, ev := range events {
		if ev.Kind == aipapi.EventResponseFinished {
			sawFinished = true
		}
	}
	if !sawFinished {
		t.Fatal("RED: observer must Observe response_finished before success_released Finish")
	}
}

func TestFinalStreamObserver_lifecycleOutcomesTable(t *testing.T) {
	t.Parallel()

	type outcomeCase struct {
		name    string
		want    response.StreamOutcome
		setup   func(t *testing.T, ex *runtime.Executor, factory *recordingStreamObserverFactory) aipapi.EventStream
		require func(t *testing.T, factory *recordingStreamObserverFactory)
	}

	cases := []outcomeCase{
		{
			name: "failed",
			want: response.OutcomeFailed,
			setup: func(t *testing.T, ex *runtime.Executor, _ *recordingStreamObserverFactory) aipapi.EventStream {
				t.Helper()
				ex.Backends = map[string]execbackend.Backend{
					"be": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							return &observerFailAfterStream{
								events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
								fail:   errors.New("upstream failed"),
							}, nil
						},
					},
				}
				stream, err := ex.Execute(t.Context(), observerBaseCall("be:m"))
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				_, _ = aipapi.Collect(t.Context(), stream)
				return stream
			},
		},
		{
			name: "cancelled",
			want: response.OutcomeCancelled,
			setup: func(t *testing.T, ex *runtime.Executor, _ *recordingStreamObserverFactory) aipapi.EventStream {
				t.Helper()
				release := make(chan struct{})
				ex.Backends = map[string]execbackend.Backend{
					"be": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							return &blockingTailStreamForObserver{
								events: []aipapi.Event{
									{Kind: aipapi.EventResponseStarted},
									{Kind: aipapi.EventMessageStarted},
									{Kind: aipapi.EventTextDelta, Delta: "partial"},
									{Kind: aipapi.EventResponseFinished},
								},
								releaseTail: release,
							}, nil
						},
					},
				}
				ctx, cancel := context.WithCancel(t.Context())
				stream, err := ex.Execute(ctx, observerBaseCall("be:m"))
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				if _, err := stream.Recv(ctx); err != nil {
					t.Fatalf("recv started: %v", err)
				}
				cancel()
				_, _ = stream.Recv(ctx)
				close(release)
				return stream
			},
		},
		{
			name: "closed",
			want: response.OutcomeClosed,
			setup: func(t *testing.T, ex *runtime.Executor, _ *recordingStreamObserverFactory) aipapi.EventStream {
				t.Helper()
				release := make(chan struct{})
				ex.Backends = map[string]execbackend.Backend{
					"be": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							return &blockingTailStreamForObserver{
								events: []aipapi.Event{
									{Kind: aipapi.EventResponseStarted},
									{Kind: aipapi.EventMessageStarted},
									{Kind: aipapi.EventTextDelta, Delta: "partial"},
									{Kind: aipapi.EventResponseFinished},
								},
								releaseTail: release,
							}, nil
						},
					},
				}
				stream, err := ex.Execute(t.Context(), observerBaseCall("be:m"))
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				if _, err := stream.Recv(t.Context()); err != nil {
					t.Fatalf("recv: %v", err)
				}
				if err := stream.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
				close(release)
				return stream
			},
		},
		{
			name: "replaced",
			want: response.OutcomeReplaced,
			setup: func(t *testing.T, ex *runtime.Executor, _ *recordingStreamObserverFactory) aipapi.EventStream {
				t.Helper()
				var opens atomic.Int64
				ex.MaxAttempts = 3
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							opens.Add(1)
							return &observerFailAfterStream{
								events: []aipapi.Event{
									{Kind: aipapi.EventResponseStarted},
									{Kind: aipapi.EventMessageStarted},
								},
								fail: aipapi.RecoverablePreOutputError(errors.New("pre-output replace")),
							}, nil
						},
					},
					"b": {
						Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
						Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							opens.Add(1)
							return aipapi.NewFixedEventStream([]aipapi.Event{
								{Kind: aipapi.EventResponseStarted},
								{Kind: aipapi.EventMessageStarted},
								{Kind: aipapi.EventTextDelta, Delta: "replacement"},
								{Kind: aipapi.EventResponseFinished},
							}), nil
						},
					},
				}
				stream, err := ex.Execute(t.Context(), observerBaseCall("a:m|b:m"))
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				var sawReplacement bool
				for {
					ev, rerr := stream.Recv(t.Context())
					if rerr != nil {
						if errors.Is(rerr, io.EOF) {
							break
						}
						// Recv replacement may surface intermediate stream errors; keep draining.
						if !aipapi.IsRecoverablePreOutput(rerr) {
							break
						}
						continue
					}
					if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, "replacement") {
						sawReplacement = true
					}
				}
				if !sawReplacement {
					t.Fatal("recv replacement must surface second candidate text")
				}
				if opens.Load() < 2 {
					t.Fatalf("want recv replacement opens>=2 got %d", opens.Load())
				}
				return stream
			},
			require: func(t *testing.T, factory *recordingStreamObserverFactory) {
				t.Helper()
				observers := factory.snapshotObservers()
				var sawReplaced, sawSuccess bool
				for _, obs := range observers {
					obs.mu.Lock()
					for _, o := range obs.outcomes {
						if o == response.OutcomeReplaced {
							sawReplaced = true
						}
						if o == response.OutcomeSuccessReleased {
							sawSuccess = true
						}
					}
					if obs.finishN.Load() != 1 {
						t.Fatalf("each observer Finish once; got %d", obs.finishN.Load())
					}
					obs.mu.Unlock()
				}
				if !sawReplaced {
					t.Fatal("RED: pre-output recv replacement must Finish original observer as replaced")
				}
				if !sawSuccess {
					t.Fatal("RED: replacement B-leg observer must Finish success_released")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
			bundle := contributeStreamObserverBundle(t, factory)
			bus, snap := wireMergedObserverSurface(t, bundle)
			ex := runtime.TestExecutor()
			ex.Store = st
			ex.Bus = bus
			ex.RuntimeSnapshot = snap
			ex.Rand = routing.NewSeededRng(11)

			stream := tc.setup(t, ex, factory)
			if stream != nil {
				_ = stream.Close()
			}
			if factory.opens.Load() == 0 {
				t.Fatalf("RED: StreamObserverFactory must Open so Finish(%s) can run (runner absent)", tc.want)
			}
			if tc.require != nil {
				tc.require(t, factory)
				return
			}
			observers := factory.snapshotObservers()
			if len(observers) == 0 {
				t.Fatal("RED: Open must produce an observer")
			}
			obs := observers[0]
			if obs.finishN.Load() != 1 {
				t.Fatalf("RED: Finish exactly once; got %d", obs.finishN.Load())
			}
			obs.mu.Lock()
			outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
			obs.mu.Unlock()
			if len(outcomes) != 1 || outcomes[0] != tc.want {
				t.Fatalf("RED: want Finish(%s), got %#v", tc.want, outcomes)
			}
		})
	}
}

func TestFinalStreamObserver_parallelOpensWinnerOnly(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}}
	bundle := contributeStreamObserverBundle(t, factory)
	bus, snap := wireMergedObserverSurface(t, bundle)

	var openBackends atomic.Int64
	var openStarted sync.WaitGroup
	openStarted.Add(2)
	tracking := func(name string) execbackend.Backend {
		return execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openBackends.Add(1)
				openStarted.Done()
				openStarted.Wait() // both parallel arms must reach Open before either returns
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

	stream, execErr := ex.Execute(t.Context(), observerBaseCall("slow:model!fast:model"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	_, _ = aipapi.Collect(t.Context(), stream)
	if openBackends.Load() < 2 {
		t.Fatalf("parallel arms must each Open backend, got %d", openBackends.Load())
	}
	if factory.opens.Load() == 0 {
		t.Fatal("RED: observer factory must Open for the winning surfaced B-leg")
	}
	if factory.opens.Load() != 1 {
		t.Fatalf("RED: losing parallel arms must never Open observers; opens=%d", factory.opens.Load())
	}
}

func TestFinalStreamObserver_failurePreservesOutputNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingStreamObserverFactory{opens: &atomic.Int64{}, failObs: true}
	bundle := contributeStreamObserverBundle(t, factory)
	bus, snap := wireMergedObserverSurface(t, bundle)

	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"be": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "committed"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	stream, execErr := ex.Execute(t.Context(), observerBaseCall("be:m"))
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect must preserve output despite observer failure: %v", err)
	}
	if got := col.Text.String(); got != "committed" {
		t.Fatalf("observer failure must not mutate output; got %q", got)
	}
	if opens.Load() != 1 {
		t.Fatalf("observer failure must not initiate retry after commitment; opens=%d", opens.Load())
	}
	if factory.opens.Load() == 0 {
		t.Fatal("RED: StreamObserverFactory must Open so Observe failure policy can apply (runner absent)")
	}
}

// blockingTailStreamForObserver blocks on the last event until releaseTail closes.
type blockingTailStreamForObserver struct {
	events      []aipapi.Event
	idx         int
	releaseTail <-chan struct{}
}

func (s *blockingTailStreamForObserver) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx == len(s.events)-1 {
		select {
		case <-s.releaseTail:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}
func (*blockingTailStreamForObserver) Close() error { return nil }
func (*blockingTailStreamForObserver) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}
