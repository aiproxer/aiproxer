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
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var errParallelRaceMemoUpdate = errors.New("parallel race memo update failed")

type failUpdateMemoStore struct {
	inner       interleavedthinking.MemoStore
	updateCalls atomic.Int32
}

func (s *failUpdateMemoStore) Put(ctx context.Context, scope interleavedthinking.Scope, state interleavedthinking.MemoState) (interleavedthinking.MemoRef, error) {
	return s.inner.Put(ctx, scope, state)
}

func (s *failUpdateMemoStore) Get(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef) (interleavedthinking.MemoState, bool, error) {
	return s.inner.Get(ctx, scope, ref)
}

func (s *failUpdateMemoStore) Update(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef, state interleavedthinking.MemoState) (interleavedthinking.MemoRef, error) {
	s.updateCalls.Add(1)
	return interleavedthinking.MemoRef{}, errParallelRaceMemoUpdate
}

func (s *failUpdateMemoStore) Delete(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef) error {
	return s.inner.Delete(ctx, scope, ref)
}

func (s *failUpdateMemoStore) LatestEntry(ctx context.Context, scope interleavedthinking.Scope) (interleavedthinking.MemoState, interleavedthinking.MemoRef, bool, error) {
	return s.inner.LatestEntry(ctx, scope)
}

type parallelRaceCleanupStream struct {
	events       []aipapi.Event
	idx          int
	blockNotify  chan struct{}
	blockRelease chan struct{}
	waitReady    <-chan struct{}
	cancelCount  atomic.Int32
	closeCount   atomic.Int32
}

func (s *parallelRaceCleanupStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx == 0 && s.waitReady != nil {
		select {
		case <-s.waitReady:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if s.idx == 0 && s.blockRelease != nil {
		if s.blockNotify != nil {
			select {
			case s.blockNotify <- struct{}{}:
			default:
			}
		}
		select {
		case <-s.blockRelease:
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

func (s *parallelRaceCleanupStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	_ = s.cancelCount.CompareAndSwap(0, 1)
	return aipapi.CancelResult{}
}

func (s *parallelRaceCleanupStream) Close() error {
	_ = s.closeCount.CompareAndSwap(0, 1)
	return nil
}

func hybridParallelBackends(t *testing.T) (map[string]execbackend.Backend, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	slowRelease := make(chan struct{})
	t.Cleanup(func() { close(slowRelease) })
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var slowOpens, fastOpens atomic.Int32
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("parallel plan")
		}),
		"slow-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				slowOpens.Add(1)
				return &parallelRaceCleanupStream{
					blockRelease: slowRelease,
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "slow-wins"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
		"fast-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				fastOpens.Add(1)
				return executorTextStream("fast-wins"), nil
			},
		},
	}
	return backends, &slowOpens, &fastOpens
}

func hybridParallelExecutor(t *testing.T, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)
	return ex, st
}

func assertHybridParallelRaceOutcome(t *testing.T, slowOpens, fastOpens int32, attempts []aipapi.AttemptRecord) {
	t.Helper()
	if fastOpens != 1 && fastOpens != 2 {
		t.Fatalf("parallel winner opens: fast=%d want 1 or 2", fastOpens)
	}
	if slowOpens != 0 && slowOpens != 1 {
		t.Fatalf("parallel loser opens: slow=%d want 0 or 1", slowOpens)
	}
	var fastSuccess, slowCancelled bool
	for _, att := range attempts {
		switch att.BackendID {
		case "fast-exec":
			if att.Outcome == aipapi.AttemptSuccess {
				fastSuccess = true
			}
		case "slow-exec":
			if att.Outcome == aipapi.AttemptCancelled {
				slowCancelled = true
			}
		}
	}
	if !fastSuccess {
		t.Fatalf("fast-exec winner must succeed: %+v", attempts)
	}
	if slowOpens == 1 && !slowCancelled {
		t.Fatalf("slow-exec loser must be cancelled when opened: slow=%d attempts=%+v", slowOpens, attempts)
	}
}

// TestExecutor_HybridParallelExecutorRace_WinnerAndLosers proves task 6.2: when the hybrid
// selector picks the embedded parallel executor branch, the existing parallel race runs
// unchanged with winner selection and loser cancellation.
func TestExecutor_HybridParallelExecutorRace_WinnerAndLosers(t *testing.T) {
	t.Parallel()

	backends, slowOpens, fastOpens := hybridParallelBackends(t)
	ex, st := hybridParallelExecutor(t, backends)

	selector := "[thinker]thinker-be:m^fast-exec:m!slow-exec:m"
	call := interleavedBaseCall(selector)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := collected.Text.String(); got != "fast-wins" {
		t.Fatalf("client text: got %q want %q", got, "fast-wins")
	}

	attempts, err := st.LoadAttempts(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	assertHybridParallelRaceOutcome(t, slowOpens.Load(), fastOpens.Load(), attempts)
}

// TestExecutor_HybridThinkerThenParallelContinuation proves task 6.2: thinker capture followed
// by executor continuation runs the embedded parallel race and emits only the winning executor.
func TestExecutor_HybridThinkerThenParallelContinuation(t *testing.T) {
	t.Parallel()

	backends, slowOpens, fastOpens := hybridParallelBackends(t)
	ex, st := hybridParallelExecutor(t, backends)

	selector := "[thinker]thinker-be:m^fast-exec:m!slow-exec:m"

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	attemptCountBefore, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts before continuation: %v", err)
	}
	slowOpens.Store(0)
	fastOpens.Store(0)

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := collected.Text.String(); got != "fast-wins" {
		t.Fatalf("client text: got %q want %q", got, "fast-wins")
	}
	if strings.Contains(collected.Text.String(), interleavedthinking.MemoOpenTag) {
		t.Fatal("memo wrapper must not reach client")
	}

	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok || stored.Memo != "parallel plan" {
		t.Fatalf("stored memo: ok=%v err=%v memo=%q", ok, err, stored.Memo)
	}

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	assertHybridParallelRaceOutcome(t, slowOpens.Load(), fastOpens.Load(), attempts[len(attemptCountBefore):])

	var thinkerAttempts int
	for _, att := range attempts {
		if att.BackendID == "thinker-be" {
			thinkerAttempts++
		}
	}
	if thinkerAttempts != 1 {
		t.Fatalf("thinker attempts: got %d want 1 in %+v", thinkerAttempts, attempts)
	}
}

func TestExecutor_HybridParallelMemoBudgetCommittedOnlyForWinner(t *testing.T) {
	t.Parallel()

	slowRelease := make(chan struct{})
	t.Cleanup(func() { close(slowRelease) })
	var mu sync.Mutex
	openedCalls := map[string]aipapi.Call{}
	var fastOpens atomic.Int32
	capture := func(backend string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			mu.Lock()
			openedCalls[backend] = call
			mu.Unlock()
			if backend == "fast-exec" {
				fastOpens.Add(1)
			}
			if backend == "slow-exec" {
				return &parallelRaceCleanupStream{
					blockRelease: slowRelease,
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "slow-wins"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			}
			return executorTextStream("fast-wins"), nil
		}
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("parallel budget plan")
		}),
		"slow-exec": {Caps: caps, TransportCaps: transport, Open: capture("slow-exec")},
		"fast-exec": {Caps: caps, TransportCaps: transport, Open: capture("fast-exec")},
	}
	ex, _ := hybridParallelExecutor(t, backends)
	selector := "[thinker]thinker-be:m^fast-exec:m!slow-exec:m"

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	mu.Lock()
	openedCalls = map[string]aipapi.Call{}
	mu.Unlock()
	fastOpens.Store(0)

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 1 || stored.InjectedCount != 1 {
		t.Fatalf("parallel race must commit memo once for winner only, got budget=%d injected=%d", stored.RegularTurnsRemaining, stored.InjectedCount)
	}
	mu.Lock()
	fastCall, fastOK := openedCalls["fast-exec"]
	slowCall, slowOK := openedCalls["slow-exec"]
	mu.Unlock()
	if fastOpens.Load() != 1 {
		t.Fatalf("parallel winner must not reopen for memo commit, fast opens=%d", fastOpens.Load())
	}
	if !fastOK || !callContainsText(fastCall, "parallel budget plan") {
		t.Fatalf("winner must receive injected memo, got %+v", fastCall)
	}
	if slowOK && !callContainsText(slowCall, "parallel budget plan") {
		t.Fatalf("opened loser should receive the same shaped memo call, got %+v", slowCall.Instructions)
	}
}

func TestParallelRace_CommitMemoInjectionFailureCleansUpStreams(t *testing.T) {
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
			return thinkerMemoStream("cleanup plan")
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
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	innerMemo := interleavedthinking.NewMemoStore(4096)
	memoStore := &failUpdateMemoStore{inner: innerMemo}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)
	selector := "[thinker]thinker-be:m^fast-exec:m!slow-exec:m"

	first := seedThinkerFirstCall(t, st, selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("seed execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("seed collect: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("seed execute must set A-leg id")
	}
	_, err = innerMemo.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "cleanup plan",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("seed memo: %v", err)
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "thinker-be:m^parallel:fast-exec:m!slow-exec:m",
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
	if fastOpens.Load() == 0 {
		t.Fatal("second execute did not open parallel winner backend")
	}
	if memoStore.updateCalls.Load() == 0 {
		t.Fatal("memo Update was never called; parallel commit path not exercised")
	}
	if err == nil {
		t.Fatal("expected commit memo injection failure on second execute")
	}
	if !errors.Is(err, errParallelRaceMemoUpdate) {
		t.Fatalf("expected memo update error, got: %v", err)
	}
	if winnerStream == nil {
		t.Fatal("winner stream was never opened")
	}
	if loserStream == nil {
		t.Fatal("loser stream was never opened")
	}
	assertParallelRaceCleanupOnce(t, winnerStream, "winner")
	assertParallelRaceCleanupOnce(t, loserStream, "loser")
}

func assertParallelRaceCleanupOnce(t *testing.T, stream *parallelRaceCleanupStream, label string) {
	t.Helper()
	if stream.cancelCount.Load() != 1 {
		t.Fatalf("%s cancel count: got %d want 1", label, stream.cancelCount.Load())
	}
	if stream.closeCount.Load() != 1 {
		t.Fatalf("%s close count: got %d want 1", label, stream.closeCount.Load())
	}
}

func callContainsText(call aipapi.Call, want string) bool {
	for _, m := range call.Instructions {
		if strings.Contains(textOf(m), want) {
			return true
		}
	}
	for _, m := range call.Messages {
		if strings.Contains(textOf(m), want) {
			return true
		}
	}
	return false
}
