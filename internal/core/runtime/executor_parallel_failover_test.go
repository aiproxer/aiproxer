package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_HybridParallelAllLegsFailFailoverToPrimaryInOneExecute(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	opened := map[string]int{}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	recordOpen := func(backend string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			mu.Lock()
			opened[backend]++
			mu.Unlock()
			switch backend {
			case "fail1", "fail2":
				return runtime.ParallelPreWinFailStream{}, nil
			case "good":
				return executorTextStream("good-wins"), nil
			case "thinker-be":
				t.Fatal("thinker branch must not open when parallel soft-fails into primary failover")
				return nil, nil
			default:
				t.Fatalf("unexpected backend %q", backend)
				return nil, nil
			}
		}
	}
	ex, _ := hybridParallelExecutor(t, map[string]execbackend.Backend{
		"thinker-be": {Caps: caps, TransportCaps: transport, Open: recordOpen("thinker-be")},
		"fail1":      {Caps: caps, TransportCaps: transport, Open: recordOpen("fail1")},
		"fail2":      {Caps: caps, TransportCaps: transport, Open: recordOpen("fail2")},
		"good":       {Caps: caps, TransportCaps: transport, Open: recordOpen("good")},
	})

	selector := "[thinker]thinker-be:m^fail1:m!fail2:m|good:m"
	stream, err := ex.Execute(context.Background(), interleavedBaseCall(selector))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := collected.Text.String(); got != "good-wins" {
		t.Fatalf("client text: got %q want %q", got, "good-wins")
	}

	mu.Lock()
	got := map[string]int{"fail1": opened["fail1"], "fail2": opened["fail2"], "good": opened["good"], "thinker-be": opened["thinker-be"]}
	mu.Unlock()
	if got["good"] != 1 {
		t.Fatalf("good backend opens: got %d want 1 (%+v)", got["good"], got)
	}
	if got["thinker-be"] != 0 {
		t.Fatalf("thinker backend must not open on parallel soft-fail retry, got %d", got["thinker-be"])
	}
	if got["fail1"] != 1 || got["fail2"] != 1 {
		t.Fatalf("parallel legs must open once each, got %+v", got)
	}
}

func TestExecutor_ParallelCommitMemoFailureEndsALegScope(t *testing.T) {
	t.Parallel()

	var winnerStream, loserStream *parallelRaceCleanupStream
	slowReady := make(chan struct{}, 1)
	slowRelease := make(chan struct{})
	defer close(slowRelease)
	var fastOpens atomic.Int32
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("scope cleanup plan")
		}),
		"slow-exec": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				loserStream = &parallelRaceCleanupStream{
					blockNotify:  slowReady,
					blockRelease: slowRelease,
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "slow-wins"},
						{Kind: aipapi.EventResponseFinished},
					},
				}
				return loserStream, nil
			},
		},
		"fast-exec": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				fastOpens.Add(1)
				winnerStream = &parallelRaceCleanupStream{
					waitReady: slowReady,
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "fast-wins"},
						{Kind: aipapi.EventResponseFinished},
					},
				}
				return winnerStream, nil
			},
		},
		"recovery": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return executorTextStream("recovery-wins"), nil
			},
		},
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	innerMemo := interleavedthinking.NewMemoStore(4096)
	memoStore := &failUpdateMemoStore{inner: innerMemo}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.ALegLifecycle = lc
	ex.Backends = backends
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)
	selector := "[thinker]thinker-be:m^fast-exec:m!slow-exec:m|recovery:m"

	first := seedThinkerFirstCall(t, st, selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("seed execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("seed collect: %v", err)
	}
	aLegID := first.Session.ALegID

	_, err = innerMemo.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "scope cleanup plan",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("seed memo: %v", err)
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "thinker-be:m^parallel:fast-exec:m!slow-exec:m|recovery:m",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "parallel:fast-exec:m!slow-exec:m", Role: interleavedstate.RoleExecutor},
				{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
			},
			NextIndex: 0,
		},
	}); err != nil {
		t.Fatalf("seed interleaved state: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	_, err = ex.Execute(context.Background(), second)
	if err == nil {
		t.Fatal("expected commit memo injection failure")
	}
	if !errors.Is(err, errParallelRaceMemoUpdate) {
		t.Fatalf("expected memo update error, got: %v", err)
	}
	if winnerStream == nil || loserStream == nil {
		t.Fatal("parallel streams must open before commit failure")
	}
	assertParallelRaceCleanupOnce(t, winnerStream, "winner")
	assertParallelRaceCleanupOnce(t, loserStream, "loser")
	winnerCancels := winnerStream.cancelCount.Load()
	loserCancels := loserStream.cancelCount.Load()

	third := interleavedBaseCall("recovery:m")
	resumeInterleavedCall(first, third)
	stream, err := ex.Execute(context.Background(), third)
	if errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatal("A-leg scope must be clean after parallel hard error")
	}
	if err != nil {
		t.Fatalf("resume execute after hard error: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("resume collect: %v", err)
	}
	if got := collected.Text.String(); got != "recovery-wins" {
		t.Fatalf("resume text: got %q want recovery-wins", got)
	}
	if winnerStream.cancelCount.Load() != winnerCancels {
		t.Fatalf("stale winner must not be cancelled again on resume")
	}
	if loserStream.cancelCount.Load() != loserCancels {
		t.Fatalf("stale loser must not be cancelled again on resume")
	}
}
