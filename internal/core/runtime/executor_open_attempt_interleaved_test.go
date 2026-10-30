package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// capturingBackend records the canonical call passed to Open so attempt-open tests can
// assert that the shaped call is the one used for negotiation and backend open.
type capturingBackend struct {
	captured func(aipapi.Call)
	stream   aipapi.ManagedEventStream
}

func (b *capturingBackend) Open(ctx context.Context, call aipapi.Call, c routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	if b.captured != nil {
		b.captured(call)
	}
	if b.stream != nil {
		return b.stream, nil
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func interleavedBackend(caps aipapi.BackendCaps, capture func(aipapi.Call)) *execbackend.Backend {
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
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func recoverableInterleavedBackend(capture func(aipapi.Call)) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if capture != nil {
				capture(call)
			}
			return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
		},
	}
}

func interleavedBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: selector},
		Tools:      []aipapi.ToolDef{{Name: "search", Description: "search the web"}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("plan this")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
}

// TestExecutor_OpenAttempt_ShapesThinkerCallBeforeOpen proves task 5.1: a thinker candidate
// receives instructions and tool suppression after route selection and before capability
// negotiation, and the shaped call is the one passed to backend Open. The advanced cycle
// cursor is persisted at the route-selection-authoritative point.
func TestExecutor_OpenAttempt_ShapesThinkerCallBeforeOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
		"unused-exec": recoverableInterleavedBackend(nil),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step and emit a memo."}, nil)

	call := interleavedBaseCall("[thinker]thinker-be:m^unused-exec:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Tools) != 0 {
		t.Fatalf("thinker call must reach backend Open with no tools, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != "" {
		t.Fatalf("thinker call must reach backend Open with zero ToolChoice, got %q", shaped.ToolChoice.Mode)
	}
	if len(shaped.Instructions) == 0 {
		t.Fatal("thinker call must reach backend Open with thinker instructions prepended")
	}
	if !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker instructions not prepended: %+v", shaped.Instructions[0])
	}

	// Cycle cursor persisted after successful thinker backend open.
	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.Cycle.IsEmpty() {
		t.Fatal("cycle state must be persisted after thinker attempt opens")
	}
	if state.Cycle.SelectorKey == "" {
		t.Fatal("persisted cycle must carry selector key")
	}
	if len(state.Cycle.Sequence) != 2 || state.Cycle.Sequence[1].Role != interleavedstate.RoleThinker {
		t.Fatalf("persisted cycle sequence mismatch: %+v", state.Cycle.Sequence)
	}
}

// TestExecutor_OpenAttempt_InjectorCallReceivesMemoBeforeOpen proves task 5.1: an executor
// candidate receives the latest memo as planning context before capability negotiation and
// backend Open, and the updated memo reference is persisted at injection time.
func TestExecutor_OpenAttempt_InjectorCallReceivesMemoBeforeOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"exec-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	cv := wireInterleavedTestSteering(ex)

	// Seed an A-leg via a valid selector whose executor branch is reachable first.
	first := interleavedBaseCall("[thinker]other-be:m^exec-be:m")
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

	// Seed a memo for the A-leg scope and record its reference on the A-leg interleaved state.
	// Since #391 the memo is presented to executors exclusively through the
	// conversation-view steering overlay, so that overlay is seeded below too.
	_, err = memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "plan: do the thing",
		SourceSelector:        "[thinker]exec-be:m^exec-be:m",
		Backend:               "exec-be",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("memo put: %v", err)
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle: interleavedstate.CycleState{SelectorKey: "exec-be:m", Sequence: []interleavedstate.CycleEntry{{Key: "exec-be:m", Role: interleavedstate.RoleThinker}}, NextIndex: 0},
	}); err != nil {
		t.Fatalf("seed interleaved state: %v", err)
	}

	// Second request: resume the same A-leg and select the executor branch of a thinker-aware
	// weighted selector. The stored cycle key mismatches the new selector, so the planner resets
	// and picks the executor branch (first entry), which receives the memo.
	second := interleavedBaseCall("[thinker]exec-be:m^exec-be:m")
	second.Session = aipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 aLegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
	// Seed the memo steering overlay anchored after the ingress terminal user
	// message, mirroring what a thinker capture publishes since #391.
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(*second, conversationview.Snapshot{})
	if err != nil {
		t.Fatalf("resolve memo anchor: %v", err)
	}
	_ = cv.CreateALeg(context.Background(), aLegID)
	if _, err := cv.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID: "interleaved-thinking-memo",
		Message: conversationview.StoredMessageV1{
			Role: aipapi.RoleUser,
			Text: interleavedthinking.SessionSteeringGuidanceHeader + "\n" + "plan: do the thing",
		},
		Placement: conversationview.StoredPlacement{
			Kind:   conversationview.PlacementAfterMessage,
			Anchor: &anchor,
		},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "interleaved_thinking_memo",
	}); err != nil {
		t.Fatalf("seed memo steering overlay: %v", err)
	}
	seededSnapshot, err := cv.Snapshot(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("snapshot seeded memo steering overlay: %v", err)
	}
	if len(seededSnapshot.Steering) != 1 || !seededSnapshot.Steering[0].Active {
		t.Fatalf("memo steering overlay not active before executor turn: %+v", seededSnapshot.Steering)
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Messages) == 0 {
		t.Fatal("executor call must reach backend Open with conversation messages")
	}
	// #391: the memo must arrive as a standalone synthetic user message
	// projected from the steering overlay; the original messages stay intact.
	var steeringMsgs []aipapi.Message
	for _, m := range shaped.Messages {
		if strings.Contains(textOf(m), interleavedthinking.SessionSteeringGuidanceHeader) {
			steeringMsgs = append(steeringMsgs, m)
		}
	}
	if len(steeringMsgs) != 1 {
		t.Fatalf("executor call must carry exactly one projected steering message, got %d (requested a-leg %q, resolved a-leg %q): %+v", len(steeringMsgs), aLegID, second.Session.ALegID, shaped.Messages)
	}
	if steeringMsgs[0].Role != aipapi.RoleUser {
		t.Fatalf("projected steering must be a user message, got %q", steeringMsgs[0].Role)
	}
	if !strings.Contains(textOf(steeringMsgs[0]), "plan: do the thing") {
		t.Fatalf("projected steering missing memo body: %+v", steeringMsgs[0])
	}
	if strings.Contains(textOf(shaped.Messages[len(shaped.Messages)-1]), "plan this\n[") ||
		len(shaped.Messages[len(shaped.Messages)-1].Parts) != 1 {
		t.Fatalf("original ingress tail must stay byte-identical: %+v", shaped.Messages[len(shaped.Messages)-1])
	}
	if len(shaped.Tools) != 1 {
		t.Fatalf("executor call must keep tools, got %d", len(shaped.Tools))
	}

	// Updated memo reference persisted at injection time: budget decremented and version bumped.
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup after injection: ok=%v err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 1 {
		t.Fatalf("memo budget must be decremented after injection: got %d want 1", stored.RegularTurnsRemaining)
	}
	if stored.InjectedCount != 1 {
		t.Fatalf("memo injection count must be incremented: got %d want 1", stored.InjectedCount)
	}
}

// TestExecutor_OpenAttempt_ThinkerCycleCursorAdvancesAfterSuccessfulOpen proves the durable
// thinker cycle cursor is persisted only after backend Open succeeds. A recoverable pre-output
// Open failure on the first planned candidate (bad:m) does not roll back or advance persistence;
// failover to ok:m succeeds and the final NextIndex reflects the successful open.
func TestExecutor_OpenAttempt_ThinkerCycleCursorAdvancesAfterSuccessfulOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotMu sync.Mutex
	var opened []string
	capture := func(backend string) func(aipapi.Call) {
		return func(aipapi.Call) {
			gotMu.Lock()
			opened = append(opened, backend)
			gotMu.Unlock()
		}
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"bad": recoverableInterleavedBackend(capture("bad")),
		"ok": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture("ok"),
		),
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			func(aipapi.Call) {},
		),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, interleavedthinking.NewMemoStore(4096))

	selector := "[thinker]thinker-be:m^bad:m^ok:m"
	wantSeq := []interleavedstate.CycleEntry{
		{Key: "bad:m", Role: interleavedstate.RoleExecutor},
		{Key: "ok:m", Role: interleavedstate.RoleExecutor},
		{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
	}
	wantKey := "thinker-be:m^bad:m^ok:m"

	call := interleavedBaseCall(selector)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute with failover: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	openedCopy := append([]string(nil), opened...)
	gotMu.Unlock()
	if len(openedCopy) != 2 || openedCopy[0] != "bad" || openedCopy[1] != "ok" {
		t.Fatalf("open order: got %+v want [bad ok]", openedCopy)
	}

	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.Cycle.SelectorKey != wantKey {
		t.Fatalf("selector key: got %q want %q", state.Cycle.SelectorKey, wantKey)
	}
	if len(state.Cycle.Sequence) != len(wantSeq) {
		t.Fatalf("sequence len: got %d want %d", len(state.Cycle.Sequence), len(wantSeq))
	}
	for i, want := range wantSeq {
		got := state.Cycle.Sequence[i]
		if got.Key != want.Key || got.Role != want.Role {
			t.Fatalf("sequence[%d]: got %+v want %+v", i, got, want)
		}
	}
	// Cycle cursor persisted only after successful backend open; bad excluded on recoverable
	// failure without advancing; ok consumed on failover retry before its Open succeeded.
	if state.Cycle.NextIndex != 2 {
		t.Fatalf("NextIndex: got %d want 2 (ok planning iteration consumed after successful open)", state.Cycle.NextIndex)
	}
}

func TestExecutor_OpenAttempt_MemoCommitWaitsForSuccessfulOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	var gotMu sync.Mutex
	var opened []string
	var gotCall aipapi.Call
	capture := func(backend string) func(aipapi.Call) {
		return func(call aipapi.Call) {
			gotMu.Lock()
			opened = append(opened, backend)
			gotCall = call
			gotMu.Unlock()
		}
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"bad": recoverableInterleavedBackend(nil),
		"ok": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture("ok"),
		),
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			func(aipapi.Call) {},
		),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	cv := wireInterleavedTestSteering(ex)

	call := interleavedBaseCall("ok:m")
	firstStream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("create A-leg: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("collect first: %v", err)
	}
	aLegID := call.Session.ALegID
	_, err = memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "memo survives failed open",
		RegularTurnsRemaining: 1,
	})
	if err != nil {
		t.Fatalf("memo put: %v", err)
	}
	gotMu.Lock()
	opened = nil
	gotCall = aipapi.Call{}
	gotMu.Unlock()

	second := interleavedBaseCall("[thinker]thinker-be:m^bad:m^ok:m")
	resumeInterleavedCall(call, second)
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(*second, conversationview.Snapshot{})
	if err != nil {
		t.Fatalf("resolve memo anchor: %v", err)
	}
	_ = cv.CreateALeg(context.Background(), aLegID)
	if _, err := cv.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID: "interleaved-thinking-memo",
		Message: conversationview.StoredMessageV1{
			Role: aipapi.RoleUser,
			Text: interleavedthinking.SessionSteeringGuidanceHeader + "\n" + "memo survives failed open",
		},
		Placement: conversationview.StoredPlacement{
			Kind:   conversationview.PlacementAfterMessage,
			Anchor: &anchor,
		},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "interleaved_thinking_memo",
	}); err != nil {
		t.Fatalf("seed memo steering overlay: %v", err)
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("execute with failover: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	openedCopy := append([]string(nil), opened...)
	shaped := gotCall
	gotMu.Unlock()
	if len(openedCopy) != 1 || openedCopy[0] != "ok" {
		t.Fatalf("expected only successful backend to open, got %+v", openedCopy)
	}
	if msg := findMemoSteeringMessage(t, shaped); msg == nil || !strings.Contains(textOf(*msg), "memo survives failed open") {
		t.Fatalf("successful backend must receive memo after failed candidate, got %+v", shaped.Messages)
	}
	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 0 || stored.InjectedCount != 1 {
		t.Fatalf("memo must be committed exactly once by successful open, got budget=%d injected=%d", stored.RegularTurnsRemaining, stored.InjectedCount)
	}
}

// TestExecutor_OpenAttempt_NonThinkerSelectorInert proves task 5.1: a non-thinker selector
// with interleaved thinking configured does not mutate the call (Req 3.4) and does not
// persist any interleaved state (Req 10.2).
func TestExecutor_OpenAttempt_NonThinkerSelectorInert(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"stub": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)

	call := interleavedBaseCall("stub:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Instructions) != 0 {
		t.Fatalf("non-thinker call must not receive thinker instructions, got %d", len(shaped.Instructions))
	}
	if len(shaped.Tools) != 1 {
		t.Fatalf("non-thinker call must keep tools, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("non-thinker call must keep tool choice, got %q", shaped.ToolChoice.Mode)
	}

	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if !state.IsEmpty() {
		t.Fatalf("non-thinker selector must not persist interleaved state, got %+v", state)
	}
}

// TestExecutor_OpenAttempt_DisabledConfigInert proves Req 3.5/10.2: with no interleaved
// config and no memo store, the attempt-open path is identical to a deployment without the
// feature even when the selector contains a [thinker] branch (cycle still advances but no
// shaping or memo state is applied).
func TestExecutor_OpenAttempt_DisabledConfigInertForTools(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
		"unused-exec": recoverableInterleavedBackend(nil),
	}

	call := interleavedBaseCall("[thinker]thinker-be:m^unused-exec:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	// Disabled config: interleavedEnabled is false, so no shaping, no persistence.
	if len(shaped.Tools) != 1 {
		t.Fatalf("disabled config must not suppress tools, got %d", len(shaped.Tools))
	}
	if len(shaped.Instructions) != 0 {
		t.Fatalf("disabled config must not prepend instructions, got %d", len(shaped.Instructions))
	}
	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if !state.IsEmpty() {
		t.Fatalf("disabled config must not persist interleaved state, got %+v", state)
	}
}

func textOf(m aipapi.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == aipapi.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

type oaRtx struct {
	runs    *atomic.Int32
	order   *[]string
	orderMu *sync.Mutex
	marker  string
}

func (r oaRtx) ID() string                        { return "oa-rtx" }
func (r oaRtx) Order() int                        { return 10 }
func (r oaRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (r oaRtx) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if r.runs != nil {
		r.runs.Add(1)
	}
	if r.order != nil && r.orderMu != nil {
		r.orderMu.Lock()
		*r.order = append(*r.order, "rtx")
		r.orderMu.Unlock()
	}
	if r.marker != "" && len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		call.Messages[0].Parts[0].Text = r.marker + call.Messages[0].Parts[0].Text
	}
	return nil
}

type oaGate struct {
	runs    *atomic.Int32
	order   *[]string
	orderMu *sync.Mutex
}

func (g oaGate) ID() string                        { return "oa-gate" }
func (g oaGate) Order() int                        { return 0 }
func (g oaGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (g oaGate) Handle(_ context.Context, _ completion.Meta, _ completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	if g.runs != nil {
		g.runs.Add(1)
	}
	if g.order != nil && g.orderMu != nil {
		g.orderMu.Lock()
		*g.order = append(*g.order, "gate")
		g.orderMu.Unlock()
	}
	return completion.PassOriginalOutcome(), nil
}

func TestExecutor_OpenAttempt_InterleavedShapingRunsAfterTransformsBeforeCompletionGates(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var rtxRuns atomic.Int32
	var gateRuns atomic.Int32
	var orderMu sync.Mutex
	var stageOrder []string

	var gotMu sync.Mutex
	var gotCall aipapi.Call
	capture := func(c aipapi.Call) {
		orderMu.Lock()
		stageOrder = append(stageOrder, "open")
		orderMu.Unlock()
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: voidWorkspaceResolver{},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{oaRtx{
				runs:    &rtxRuns,
				order:   &stageOrder,
				orderMu: &orderMu,
				marker:  "rtx:",
			}},
			CompletionGates: []completion.Gate{oaGate{
				runs:    &gateRuns,
				order:   &stageOrder,
				orderMu: &orderMu,
			}},
		}),
	})

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"thinker-be": *interleavedBackend(
			aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			capture,
		),
		"unused-exec": recoverableInterleavedBackend(nil),
	}
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step and emit a memo."}, nil)

	call := interleavedBaseCall("[thinker]thinker-be:m^unused-exec:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if rtxRuns.Load() != 1 {
		t.Fatalf("request transform runs: got %d want 1", rtxRuns.Load())
	}
	if gateRuns.Load() != 1 {
		t.Fatalf("completion gate runs: got %d want 1", gateRuns.Load())
	}

	orderMu.Lock()
	gotOrder := append([]string(nil), stageOrder...)
	orderMu.Unlock()
	if len(gotOrder) != 3 || gotOrder[0] != "rtx" || gotOrder[1] != "open" || gotOrder[2] != "gate" {
		t.Fatalf("stage order: got %v want [rtx open gate]", gotOrder)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if got := textOf(shaped.Messages[0]); got != "rtx:plan this" {
		t.Fatalf("backend open must see request-transform mutation, got user text %q", got)
	}
	if len(shaped.Tools) != 0 {
		t.Fatalf("thinker shaping must suppress tools after transforms, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != "" {
		t.Fatalf("thinker shaping must clear tool choice after transforms, got %q", shaped.ToolChoice.Mode)
	}
	if len(shaped.Instructions) == 0 || !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker shaping must prepend instructions after transforms, got %+v", shaped.Instructions)
	}
}
