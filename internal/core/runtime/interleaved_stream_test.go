package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func interleavedBackendWithStream(caps aipapi.BackendCaps, capture func(aipapi.Call), newStream func() aipapi.ManagedEventStream) *execbackend.Backend {
	return &execbackend.Backend{
		Caps: caps,
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(ctx context.Context, call aipapi.Call, c routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if capture != nil {
				capture(call)
			}
			if newStream != nil {
				return newStream(), nil
			}
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func thinkerMemoStream(memoBody string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + memoBody + interleavedthinking.MemoCloseTag},
		{Kind: aipapi.EventResponseFinished},
	})
}

func executorTextStream(text string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	})
}

// TestExecutor_HiddenInterleavedContinuation_EmitsExecutorOnlyAndStoresMemo proves task 5.2:
// hidden mode drains thinker output, stores memo state, continues with thinker suppression,
// emits only executor output, and records both B-legs under one A-leg.
func TestExecutor_HiddenInterleavedContinuation_EmitsExecutorOnlyAndStoresMemo(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("executor answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan: ship it")
		}),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
		Enabled:               true,
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	if err != nil {
		t.Fatal(err)
	}
	ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg id")
	}

	second := interleavedBaseCall(selector)
	second.Session = aipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 aLegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if got := collected.Reasoning.String(); got != "" {
		t.Fatalf("hidden mode must not surface thinker reasoning, got %q", got)
	}
	if got := collected.Text.String(); got != "executor answer" {
		t.Fatalf("client text: got %q want %q", got, "executor answer")
	}
	if strings.Contains(collected.Text.String(), interleavedthinking.MemoOpenTag) {
		t.Fatal("memo wrapper tags must not reach the client")
	}

	_, err = st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.Memo != "plan: ship it" {
		t.Fatalf("stored memo: got %q want %q", stored.Memo, "plan: ship it")
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceFull {
		t.Fatalf("extraction source: got %q want %q", stored.ExtractionSource, interleavedthinking.ExtractionSourceFull)
	}

	attempts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	if len(attempts) < 3 {
		t.Fatalf("want at least 3 attempt records (exec + thinker + exec continuation), got %d: %+v", len(attempts), attempts)
	}
	var thinkerAttempts, execAttempts int
	for _, att := range attempts {
		switch att.BackendID {
		case "thinker-be":
			thinkerAttempts++
		case "exec-be":
			execAttempts++
		}
	}
	if thinkerAttempts != 1 {
		t.Fatalf("thinker attempt records: got %d want 1", thinkerAttempts)
	}
	if execAttempts < 2 {
		t.Fatalf("executor attempt records: got %d want at least 2", execAttempts)
	}
}

func interleavedVisibleExecutor(t *testing.T, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
		Enabled:               true,
		Instructions:          "Think step by step.",
		StreamToClient:        "visible",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	if err != nil {
		t.Fatal(err)
	}
	ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)
	return ex, st
}

// TestExecutor_VisibleInterleavedContinuation_EmitsReasoningThenExecutor proves task 6.1:
// visible mode surfaces sanitized thinker reasoning before executor output and stores memo state.
func TestExecutor_VisibleInterleavedContinuation_EmitsReasoningThenExecutor(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("executor answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan: ship it")
		}),
	}
	ex, st := interleavedVisibleExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	var sawReasoningBeforeExec bool
	var gotReasoning strings.Builder
	var gotText strings.Builder
	ctx := context.Background()
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		switch ev.Kind {
		case aipapi.EventReasoningDelta:
			if gotText.Len() > 0 {
				t.Fatal("thinker reasoning must stream before executor text")
			}
			if strings.Contains(ev.Delta, interleavedthinking.MemoOpenTag) || strings.Contains(ev.Delta, interleavedthinking.MemoCloseTag) {
				t.Fatalf("memo wrapper leaked into visible reasoning: %q", ev.Delta)
			}
			gotReasoning.WriteString(ev.Delta)
			sawReasoningBeforeExec = true
		case aipapi.EventTextDelta:
			gotText.WriteString(ev.Delta)
		case aipapi.EventResponseFinished:
			if !sawReasoningBeforeExec {
				t.Fatal("expected visible thinker reasoning before finish")
			}
		}
	}
	_ = stream.Close()

	if got := gotReasoning.String(); got != "plan: ship it" {
		t.Fatalf("reasoning: got %q want %q", got, "plan: ship it")
	}
	if got := gotText.String(); got != "executor answer" {
		t.Fatalf("text: got %q want %q", got, "executor answer")
	}

	_, err = st.FetchInterleavedState(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.Memo != "plan: ship it" {
		t.Fatalf("stored memo: got %q want %q", stored.Memo, "plan: ship it")
	}
	if !stored.VisibleToClient {
		t.Fatal("visible mode must mark memo VisibleToClient")
	}
}

// TestExecutor_VisibleInterleavedNoRetryAfterThinkerOutput proves task 6.1: recoverable
// executor failures after visible thinker output must not silently restart.
func TestExecutor_VisibleInterleavedNoRetryAfterThinkerOutput(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var fallbackOpens atomic.Int32
	var execPhase atomic.Int32
	badExecRecv := &recoverableAfterLifecycleStream{}
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("visible plan")
		}),
		"bad-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execPhase.Add(1) == 1 {
					return executorTextStream("setup exec"), nil
				}
				return badExecRecv, nil
			},
		},
		"good-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				fallbackOpens.Add(1)
				return executorTextStream("fallback exec"), nil
			},
		},
	}
	ex, st := interleavedVisibleExecutor(t, backends)

	selector := "[thinker]thinker-be:m^bad-exec:m|good-exec:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	badExecRecv.sentLifecycle = false
	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	ctx := context.Background()
	var sawReasoning bool
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			var uf *aipapi.UpstreamFailureError
			if sawReasoning && errors.As(err, &uf) && uf.Phase == aipapi.PhasePostOutput && !uf.Recoverable {
				break
			}
			if errors.Is(err, io.EOF) && sawReasoning {
				break
			}
			t.Fatalf("recv after visible reasoning: sawReasoning=%v err=%v", sawReasoning, err)
		}
		if ev.Kind == aipapi.EventReasoningDelta {
			sawReasoning = true
		}
		if ev.Kind == aipapi.EventTextDelta {
			t.Fatal("must not emit executor text after post-output executor failure")
		}
		if ev.Kind == aipapi.EventResponseFinished && sawReasoning {
			t.Fatal("must not finish successfully after post-output executor failure")
		}
	}
	_ = stream.Close()

	if !sawReasoning {
		t.Fatal("expected visible thinker reasoning before executor failure")
	}
	if fallbackOpens.Load() != 0 {
		t.Fatalf("fallback executor must not open after visible thinker output, opens=%d", fallbackOpens.Load())
	}

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	for _, att := range attempts {
		if att.BackendID == "good-exec" {
			t.Fatalf("good-exec must not run after visible thinker output: %+v", attempts)
		}
	}
}

type recoverableAfterLifecycleStream struct {
	sentLifecycle bool
}

func (r *recoverableAfterLifecycleStream) Recv(context.Context) (aipapi.Event, error) {
	if !r.sentLifecycle {
		r.sentLifecycle = true
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	return aipapi.Event{}, aipapi.RecoverablePreOutputError(errors.New("exec down after visible thinker"))
}

func (r *recoverableAfterLifecycleStream) Close() error { return nil }

func (r *recoverableAfterLifecycleStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func interleavedExecutor(t *testing.T, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
		Enabled:               true,
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	if err != nil {
		t.Fatal(err)
	}
	ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)
	return ex, st
}

func weightedBranchKey(b routing.WeightedBranch) string {
	if b.Parallel != nil {
		legs := make([]string, 0, len(b.Parallel.Branches))
		for _, leg := range b.Parallel.Branches {
			legs = append(legs, leg.Target.String())
		}
		return "parallel:" + strings.Join(legs, "!")
	}
	return b.Target.String()
}

func thinkerCycleState(t *testing.T, selector string, nextIndex int) interleavedstate.CycleState {
	t.Helper()
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatalf("parse %q: %v", selector, err)
	}
	w := sel.Alternatives[0].Weighted
	if w == nil {
		t.Fatal("expected weighted selector")
	}
	keys := make([]string, 0, len(w.Branches))
	for _, b := range w.Branches {
		keys = append(keys, weightedBranchKey(b))
	}
	selKey := strings.Join(keys, "^")
	entries := make([]interleavedstate.CycleEntry, 0, len(w.Branches)+2)
	for _, b := range w.Branches {
		if b.IsThinker {
			continue
		}
		wt := int64(1)
		if b.Weight > 0 {
			wt = int64(b.Weight)
		}
		key := weightedBranchKey(b)
		for range wt {
			entries = append(entries, interleavedstate.CycleEntry{Key: key, Role: interleavedstate.RoleExecutor})
		}
	}
	for _, b := range w.Branches {
		if !b.IsThinker {
			continue
		}
		entries = append(entries, interleavedstate.CycleEntry{Key: weightedBranchKey(b), Role: interleavedstate.RoleThinker})
		break
	}
	if nextIndex < 0 {
		for i, e := range entries {
			if e.Role == interleavedstate.RoleThinker {
				nextIndex = i
				break
			}
		}
	}
	return interleavedstate.CycleState{SelectorKey: selKey, Sequence: entries, NextIndex: nextIndex}
}

func seedThinkerFirstCall(t *testing.T, st *b2bua.MemoryStore, selector string) *aipapi.Call {
	t.Helper()
	aLeg, err := st.CreateALeg(context.Background(), "thinker-first")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetInterleavedState(context.Background(), aLeg.ALegID, interleavedstate.State{
		Cycle: thinkerCycleState(t, selector, -1),
	}); err != nil {
		t.Fatal(err)
	}
	call := interleavedBaseCall(selector)
	call.Session = aipapi.SessionRef{ALegID: aLeg.ALegID}
	return call
}

func resumeInterleavedCall(first, second *aipapi.Call) {
	second.Session = aipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 first.Session.ALegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
}

// TestExecutor_HiddenInterleavedThinkerPreOutputRecoveryThenExecutor proves task 5.3:
// recoverable thinker failures before visible output retry through the existing recv path,
// then hidden continuation completes with executor output.
func TestExecutor_HiddenInterleavedThinkerPreOutputRecoveryThenExecutor(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"bad-thinker": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("thinker down"))
			},
		},
		"good-thinker": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan: retry ok")
		}),
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("executor after recovery")
		}),
	}
	ex, st := interleavedExecutor(t, backends)

	selector := "[thinker]bad-thinker:m^exec-be:m|[thinker]good-thinker:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

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
	if got := collected.Text.String(); got != "executor after recovery" {
		t.Fatalf("client text: got %q want %q", got, "executor after recovery")
	}

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var swallowed int
	for _, att := range attempts {
		if att.BackendID == "bad-thinker" && att.Outcome == aipapi.AttemptSwallowedFailure {
			swallowed++
		}
	}
	if swallowed < 1 {
		t.Fatalf("bad-thinker swallowed attempts: got %d want at least 1 in %+v", swallowed, attempts)
	}
}

func TestExecutor_HiddenInterleavedContinuationRetrySuppressesThinker(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var badOpens atomic.Int32
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan: retry executor only")
		}),
		"bad-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if badOpens.Add(1) == 1 {
					return executorTextStream("setup executor"), nil
				}
				return &recoverableAfterLifecycleStream{}, nil
			},
		},
	}
	ex, st := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^bad-exec:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	firstCollected, err := aipapi.Collect(context.Background(), firstStream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := firstCollected.Text.String(); got != "setup executor" {
		t.Fatalf("first client text: got %q want %q", got, "setup executor")
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err == nil {
		t.Fatal("expected no eligible executor after continuation recv failure")
	}

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var thinkerAttempts int
	for _, att := range attempts {
		if att.BackendID == "thinker-be" {
			thinkerAttempts++
		}
	}
	if thinkerAttempts != 1 {
		t.Fatalf("continuation retry must not select thinker again, got %d thinker attempts in %+v", thinkerAttempts, attempts)
	}
}

// TestExecutor_HiddenInterleavedNoEligibleContinuation proves task 5.3: when thinker
// suppression leaves no executor candidate, the hidden continuation surfaces no-eligible.
func TestExecutor_HiddenInterleavedNoEligibleContinuation(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var missingExecOpens atomic.Int32
	backends := map[string]execbackend.Backend{
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan only")
		}),
		"missing-exec": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if missingExecOpens.Add(1) == 1 {
					return nil, aipapi.RecoverablePreOutputError(errors.New("bootstrap executor"))
				}
				return nil, aipapi.RecoverablePreOutputError(errors.New("executor unavailable"))
			},
		},
	}
	ex, _ := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^missing-exec:m"
	call := interleavedBaseCall(selector)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if !errors.Is(err, routing.ErrNoEligibleCandidate) {
		t.Fatalf("collect: got %v want ErrNoEligibleCandidate", err)
	}
}

type interruptedPartialThinkerStream struct {
	phase int
}

func (s *interruptedPartialThinkerStream) Recv(context.Context) (aipapi.Event, error) {
	switch s.phase {
	case 0:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	case 1:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventMessageStarted}, nil
	case 2:
		s.phase++
		return aipapi.Event{
			Kind:  aipapi.EventTextDelta,
			Delta: interleavedthinking.MemoOpenTag + "partial plan",
		}, nil
	default:
		return aipapi.Event{}, errors.New("thinker stream interrupted")
	}
}

func (s *interruptedPartialThinkerStream) Close() error { return nil }

func (s *interruptedPartialThinkerStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_HiddenInterleavedInterruptedThinkerPersistsPartialMemo(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("setup exec")
		}),
		"thinker-be": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &interruptedPartialThinkerStream{}, nil
			},
		},
	}
	ex, st := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected thinker interruption error")
	}
	_ = stream.Close()

	_, err = st.FetchInterleavedState(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	// The whole captured output is the memo with residual wrapper tags stripped:
	// the complete open tag is removed defensively, leaving the partial body.
	if stored.Memo != "partial plan" {
		t.Fatalf("stored memo: got %q want %q", stored.Memo, "partial plan")
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceFull {
		t.Fatalf("interrupted partial memo must use full extraction, got %q", stored.ExtractionSource)
	}
	if !stored.StreamInterrupted {
		t.Fatal("interrupted thinker memo must set StreamInterrupted=true")
	}
}

type interruptedBeforeVisibleOutputStream struct {
	phase int
}

func (s *interruptedBeforeVisibleOutputStream) Recv(context.Context) (aipapi.Event, error) {
	switch s.phase {
	case 0:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	case 1:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventMessageStarted}, nil
	case 2:
		s.phase++
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "<proxy_thinker_m"}, nil
	default:
		return aipapi.Event{}, errors.New("thinker stream interrupted before visible output")
	}
}

func (s *interruptedBeforeVisibleOutputStream) Close() error { return nil }

func (s *interruptedBeforeVisibleOutputStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_VisibleInterleavedInterruptedThinkerMemoNotMarkedVisible(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("setup exec")
		}),
		"thinker-be": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &interruptedBeforeVisibleOutputStream{}, nil
			},
		},
	}
	ex, _ := interleavedVisibleExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected thinker interruption error")
	}
	_ = stream.Close()

	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if strings.TrimSpace(stored.Memo) == "" {
		t.Fatal("interrupted thinker must persist fallback memo content")
	}
	if stored.VisibleToClient {
		t.Fatal("visible mode must not mark memo VisibleToClient without committed client-visible output")
	}
}

func TestExecutor_VisibleInterleavedCloseAfterStartBeforeReasoningMemoNotVisible(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("setup exec")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan before close")
		}),
	}
	ex, _ := interleavedVisibleExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	for i := range 2 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatalf("recv start event %d: %v", i, err)
		}
		if ev.Kind != aipapi.EventResponseStarted && ev.Kind != aipapi.EventMessageStarted {
			t.Fatalf("expected injected start event %d, got %v", i, ev.Kind)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	memoStore, ok := runtime.GetTestMemoStore(ex).(*interleavedthinking.InMemoryMemoStore)
	if !ok {
		t.Fatal("test memo store must be *interleavedthinking.InMemoryMemoStore")
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(first.Session.ALegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.VisibleToClient {
		t.Fatal("memo must not be marked visible when client closed before reasoning delta delivery")
	}
}

type interleavedCancelWaitStream struct {
	ctx       context.Context
	block     chan struct{}
	blockOnce sync.Once
}

func (c *interleavedCancelWaitStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	c.blockOnce.Do(func() {
		if c.block == nil {
			return
		}
		select {
		case c.block <- struct{}{}:
		default:
		}
	})
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-c.ctx.Done():
		return aipapi.Event{}, c.ctx.Err()
	}
}

func (c *interleavedCancelWaitStream) Close() error { return nil }

func (c *interleavedCancelWaitStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestExecutor_HiddenInterleavedCancellationDuringThinker proves task 5.3: client
// cancellation during the hidden thinker phase records cancellation and unblocks Recv.
func TestExecutor_HiddenInterleavedCancellationDuringThinker(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("setup exec")
		}),
		"thinker-be": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &interleavedCancelWaitStream{ctx: ctx}, nil
			},
		},
	}
	ex, st := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(ctx, second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv: got %v want context.Canceled", err)
	}
	_ = stream.Close()

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var cancelled int
	for _, att := range attempts {
		if att.BackendID == "thinker-be" && att.Outcome == aipapi.AttemptCancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("thinker cancelled attempts: got %d want 1 in %+v", cancelled, attempts)
	}
}

// TestExecutor_HiddenInterleavedCancellationDuringExecutor proves task 5.3: client
// cancellation during the hidden executor continuation records cancellation.
func TestExecutor_HiddenInterleavedCancellationDuringExecutor(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var execOpens atomic.Int32
	execBlocked := make(chan struct{}, 1)
	backends := map[string]execbackend.Backend{
		"exec-be": {
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return executorTextStream("setup exec"), nil
				}
				return &interleavedCancelWaitStream{ctx: ctx, block: execBlocked}, nil
			},
		},
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan for cancel")
		}),
	}
	ex, st := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	recvCtx, recvCancel := context.WithCancel(context.Background())
	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(recvCtx, second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(recvCtx)
		done <- err
	}()
	select {
	case <-execBlocked:
	case err := <-done:
		t.Fatalf("Recv finished before executor continuation blocked: %v", err)
	}
	recvCancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv: got %v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after executor cancellation")
	}
	_ = stream.Close()

	attempts, err := st.LoadAttempts(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var latestExec *aipapi.AttemptRecord
	for i := range attempts {
		att := &attempts[i]
		if att.BackendID != "exec-be" {
			continue
		}
		if latestExec == nil || att.Seq > latestExec.Seq {
			latestExec = att
		}
	}
	if latestExec == nil {
		t.Fatalf("missing exec-be attempt in %+v", attempts)
	}
	if latestExec.Outcome != aipapi.AttemptCancelled {
		t.Fatalf("latest executor attempt: got %s want cancelled in %+v", latestExec.Outcome, attempts)
	}
}

type handoffBlockingExecStream struct {
	closeCount atomic.Int32
}

func (s *handoffBlockingExecStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "must-not-emit"}, nil
}

func (s *handoffBlockingExecStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func (s *handoffBlockingExecStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.closeCount.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type handoffTrackingThinkerStream struct {
	events     []aipapi.Event
	idx        int
	closeCount atomic.Int32
}

func (s *handoffTrackingThinkerStream) Recv(context.Context) (aipapi.Event, error) {
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *handoffTrackingThinkerStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func (s *handoffTrackingThinkerStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_HiddenInterleavedContinuationHandoffAbortsWhenCancelledDuringOpen(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	var execOpens atomic.Int32
	continuationOpenStarted := make(chan struct{}, 1)
	releaseOpen := make(chan struct{})
	var blockedExec *handoffBlockingExecStream
	backends := map[string]execbackend.Backend{
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return executorTextStream("setup exec"), nil
				}
				select {
				case continuationOpenStarted <- struct{}{}:
				default:
				}
				<-releaseOpen
				blockedExec = &handoffBlockingExecStream{}
				return blockedExec, nil
			},
		},
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return thinkerMemoStream("handoff cancel plan")
		}),
	}
	ex, _ := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(context.Background())
		done <- err
	}()
	select {
	case <-continuationOpenStarted:
	case err := <-done:
		t.Fatalf("Recv finished before continuation open blocked: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for continuation open")
	}
	if managed, ok := stream.(aipapi.ManagedEventStream); ok {
		managed.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	} else {
		t.Fatal("interleaved stream must implement ManagedEventStream")
	}
	close(releaseOpen)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Recv must fail after handoff abort")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after aborted handoff")
	}
	if blockedExec == nil {
		t.Fatal("continuation executor stream was never opened")
	}
	if blockedExec.closeCount.Load() != 2 {
		t.Fatalf("aborted executor stream cleanup count: got %d want 2 (cancel+close)", blockedExec.closeCount.Load())
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("outer close: %v", err)
	}
	if blockedExec.closeCount.Load() != 2 {
		t.Fatalf("outer close must not double-cleanup aborted executor: got %d want 2", blockedExec.closeCount.Load())
	}
}

func TestExecutor_HiddenInterleavedContinuationHandoffClosesThinkerStream(t *testing.T) {
	t.Parallel()

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	var execOpens atomic.Int32
	var thinkerStream *handoffTrackingThinkerStream
	backends := map[string]execbackend.Backend{
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return executorTextStream("setup exec"), nil
				}
				return executorTextStream("handoff exec"), nil
			},
		},
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				thinkerStream = &handoffTrackingThinkerStream{events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + "handoff close plan" + interleavedthinking.MemoCloseTag},
					{Kind: aipapi.EventResponseFinished},
				}}
				return thinkerStream, nil
			},
		},
	}
	ex, _ := interleavedExecutor(t, backends)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

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
	if got := collected.Text.String(); got != "handoff exec" {
		t.Fatalf("client text: got %q want %q", got, "handoff exec")
	}
	if thinkerStream == nil {
		t.Fatal("thinker stream was never opened")
	}
	if thinkerStream.closeCount.Load() != 1 {
		t.Fatalf("successful handoff thinker close count: got %d want 1", thinkerStream.closeCount.Load())
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("outer close: %v", err)
	}
	if thinkerStream.closeCount.Load() != 1 {
		t.Fatalf("outer close must not double-close thinker: got %d want 1", thinkerStream.closeCount.Load())
	}
}

func TestExecutor_VisibleInterleavedContinuation_FlushesPartialTagAtEOF(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return executorTextStream("executor answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "visible plan <proxy_thinker_me"},
				{Kind: aipapi.EventResponseFinished},
			})
		}),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	proc, err := interleavedthinking.NewProcessor(interleavedthinking.Config{
		Enabled:               true,
		Instructions:          "Think step by step.",
		StreamToClient:        "visible",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	if err != nil {
		t.Fatal(err)
	}
	ex.InterleavedProcessor = featurehost.NewInterleavedProcessorAdapter(proc)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg id")
	}

	second := interleavedBaseCall(selector)
	second.Session = aipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 aLegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	reasoning := collected.Reasoning.String()
	if !strings.Contains(reasoning, "<proxy_thinker_me") {
		t.Fatalf("EOF flush failed: reasoning %q does not contain flushed partial tag %q", reasoning, "<proxy_thinker_me")
	}
}
