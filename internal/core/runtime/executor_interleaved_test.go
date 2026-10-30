package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type interleavedOpenRecord struct {
	backend string
	role    interleavedstate.Role
	call    aipapi.Call
}

// TestExecutor_HiddenInterleavedEndToEnd proves task 8.1: one composed hidden-interleaved
// continuation exercises selector parse, thinker selection, memo capture, persisted state,
// executor continuation selection, and final client stream output; skipping any phase fails.
func TestExecutor_HiddenInterleavedEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		selector    = "[thinker]thinker-be:m^exec-be:m"
		selectorKey = "thinker-be:m^exec-be:m"
		memoBody    = "e2e hidden plan"
		execAnswer  = "e2e executor answer"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call aipapi.Call) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, call: call})
		opensMu.Unlock()
	}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, func(c aipapi.Call) {
			recordOpen("exec-be", c)
		}, func() aipapi.ManagedEventStream {
			return executorTextStream(execAnswer)
		}),
		"thinker-be": *interleavedBackendWithStream(caps, func(c aipapi.Call) {
			recordOpen("thinker-be", c)
		}, func() aipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
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

	preState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch pre-state: %v", err)
	}
	if preState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("selector parse: SelectorKey got %q want %q", preState.Cycle.SelectorKey, selectorKey)
	}
	if preState.Cycle.IsEmpty() {
		t.Fatal("selector parse: cycle state must be persisted after first turn")
	}
	if len(preState.Cycle.Sequence) != 2 {
		t.Fatalf("selector parse: cycle sequence len got %d want 2: %+v", len(preState.Cycle.Sequence), preState.Cycle.Sequence)
	}
	if preState.Cycle.Sequence[0].Role != interleavedstate.RoleExecutor || preState.Cycle.Sequence[0].Key != "exec-be:m" {
		t.Fatalf("selector parse: first cycle entry got %+v want exec-be:m executor", preState.Cycle.Sequence[0])
	}
	if preState.Cycle.Sequence[1].Role != interleavedstate.RoleThinker || preState.Cycle.Sequence[1].Key != "thinker-be:m" {
		t.Fatalf("selector parse: second cycle entry got %+v want thinker-be:m thinker", preState.Cycle.Sequence[1])
	}

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("second collect: %v", err)
	}

	opensMu.Lock()
	secondTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	if len(secondTurnOpens) < 2 {
		t.Fatalf("continuation: want thinker then executor opens, got %d: %+v", len(secondTurnOpens), secondTurnOpens)
	}
	if secondTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("thinker selection: first open got %q want thinker-be", secondTurnOpens[0].backend)
	}
	thinkerCall := secondTurnOpens[0].call
	if len(thinkerCall.Tools) != 0 {
		t.Fatalf("thinker selection: shaped call must suppress tools, got %d", len(thinkerCall.Tools))
	}
	if !strings.Contains(textOf(thinkerCall.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker selection: instructions not shaped: %+v", thinkerCall.Instructions)
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range secondTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("executor continuation: exec-be must open after thinker capture")
	}
	if len(continuationExec.call.Tools) != 1 {
		t.Fatalf("executor continuation: must keep tools, got %d", len(continuationExec.call.Tools))
	}
	// #391: the memo reaches the executor as a standalone synthetic user message
	// projected from the conversation-view steering overlay; existing messages
	// (including any tool output) stay byte-identical.
	steeringMsg := findMemoSteeringMessage(t, continuationExec.call)
	if steeringMsg == nil {
		t.Fatal("executor continuation: memo steering overlay must be projected into the call")
	}
	injected := textOf(*steeringMsg)
	if !strings.Contains(injected, memoBody) {
		t.Fatalf("executor continuation: memo not injected: %q", injected)
	}
	if !strings.Contains(injected, interleavedthinking.SessionSteeringGuidanceHeader) {
		t.Fatalf("executor continuation: steering guidance header missing: %q", injected)
	}
	if countMemoSteeringMessages(t, continuationExec.call) != 1 {
		t.Fatal("executor continuation: steering overlay must appear exactly once")
	}
	assertStandaloneMemoSteering(t, continuationExec.call, memoBody)

	if got := collected.Reasoning.String(); got != "" {
		t.Fatalf("stream output: hidden mode must not surface reasoning, got %q", got)
	}
	if got := collected.Text.String(); got != execAnswer {
		t.Fatalf("stream output: text got %q want %q", got, execAnswer)
	}
	if strings.Contains(collected.Text.String(), interleavedthinking.MemoOpenTag) {
		t.Fatal("stream output: memo wrapper must not reach client")
	}

	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("stored state: fetch failed: %v", err)
	}
	if postState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("stored state: SelectorKey got %q want %q", postState.Cycle.SelectorKey, selectorKey)
	}
	if postState.Cycle.IsEmpty() {
		t.Fatal("stored state: cycle must persist through continuation")
	}

	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo capture: lookup ok=%v err=%v", ok, err)
	}
	if stored.Memo != memoBody {
		t.Fatalf("memo capture: got %q want %q", stored.Memo, memoBody)
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceFull {
		t.Fatalf("memo capture: extraction source got %q want %q", stored.ExtractionSource, interleavedthinking.ExtractionSourceFull)
	}
	if stored.VisibleToClient {
		t.Fatal("memo capture: hidden mode must not mark memo VisibleToClient")
	}

	attempts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
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
		t.Fatalf("lineage: thinker attempts got %d want 1", thinkerAttempts)
	}
	if execAttempts < 2 {
		t.Fatalf("lineage: executor attempts got %d want at least 2", execAttempts)
	}

	var suppressedThinker bool
	for _, entry := range postState.Cycle.Sequence {
		if entry.Role == interleavedstate.RoleThinker {
			suppressedThinker = true
		}
	}
	if !suppressedThinker {
		t.Fatalf("stored state: cycle must retain thinker entry for suppression semantics: %+v", postState.Cycle.Sequence)
	}
}

// TestExecutor_VisibleInterleavedEndToEnd proves task 8.2: one composed visible-interleaved
// continuation exercises visible thinker output, memo sanitization, memo storage, executor
// continuation, and final stream termination; wrapper tags must not surface and executor
// output must follow thinker reasoning.
func TestExecutor_VisibleInterleavedEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		selector    = "[thinker]thinker-be:m^exec-be:m"
		selectorKey = "thinker-be:m^exec-be:m"
		memoBody    = "e2e visible plan"
		execAnswer  = "e2e executor answer"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call aipapi.Call) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, call: call})
		opensMu.Unlock()
	}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, func(c aipapi.Call) {
			recordOpen("exec-be", c)
		}, func() aipapi.ManagedEventStream {
			return executorTextStream(execAnswer)
		}),
		"thinker-be": *interleavedBackendWithStream(caps, func(c aipapi.Call) {
			recordOpen("thinker-be", c)
		}, func() aipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "visible",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	wireInterleavedTestSteering(ex)

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

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	var sawReasoningBeforeExec bool
	var sawResponseFinished bool
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
				t.Fatal("visible thinker reasoning must stream before executor text")
			}
			if strings.Contains(ev.Delta, interleavedthinking.MemoOpenTag) ||
				strings.Contains(ev.Delta, interleavedthinking.MemoCloseTag) {
				t.Fatalf("memo sanitization: wrapper tags leaked into visible reasoning: %q", ev.Delta)
			}
			gotReasoning.WriteString(ev.Delta)
			sawReasoningBeforeExec = true
		case aipapi.EventTextDelta:
			if strings.Contains(ev.Delta, interleavedthinking.MemoOpenTag) ||
				strings.Contains(ev.Delta, interleavedthinking.MemoCloseTag) {
				t.Fatalf("memo sanitization: wrapper tags leaked into executor text: %q", ev.Delta)
			}
			gotText.WriteString(ev.Delta)
		case aipapi.EventResponseFinished:
			sawResponseFinished = true
			if !sawReasoningBeforeExec {
				t.Fatal("stream termination: expected visible thinker reasoning before finish")
			}
			if gotText.String() != execAnswer {
				t.Fatalf("stream termination: text at finish got %q want %q", gotText.String(), execAnswer)
			}
		}
	}
	_ = stream.Close()

	if !sawResponseFinished {
		t.Fatal("stream termination: must emit ResponseFinished")
	}
	if got := gotReasoning.String(); got != memoBody {
		t.Fatalf("visible thinker output: got %q want %q", got, memoBody)
	}
	if got := gotText.String(); got != execAnswer {
		t.Fatalf("executor output: got %q want %q", got, execAnswer)
	}

	opensMu.Lock()
	secondTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	if len(secondTurnOpens) < 2 {
		t.Fatalf("continuation: want thinker then executor opens, got %d: %+v", len(secondTurnOpens), secondTurnOpens)
	}
	if secondTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("thinker selection: first open got %q want thinker-be", secondTurnOpens[0].backend)
	}
	thinkerCall := secondTurnOpens[0].call
	if len(thinkerCall.Tools) != 0 {
		t.Fatalf("thinker selection: shaped call must suppress tools, got %d", len(thinkerCall.Tools))
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range secondTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("executor continuation: exec-be must open after visible thinker capture")
	}
	if len(continuationExec.call.Tools) != 1 {
		t.Fatalf("executor continuation: must keep tools, got %d", len(continuationExec.call.Tools))
	}
	for _, msg := range continuationExec.call.Messages {
		txt := textOf(msg)
		if strings.Contains(txt, memoBody) || strings.Contains(txt, interleavedthinking.SessionSteeringGuidanceHeader) {
			t.Fatalf("executor continuation: visible memo must not be re-injected: %q", txt)
		}
	}

	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("stored state: fetch failed: %v", err)
	}
	if postState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("stored state: SelectorKey got %q want %q", postState.Cycle.SelectorKey, selectorKey)
	}

	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo storage: lookup ok=%v err=%v", ok, err)
	}
	if stored.Memo != memoBody {
		t.Fatalf("memo storage: got %q want %q", stored.Memo, memoBody)
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceFull {
		t.Fatalf("memo storage: extraction source got %q want %q", stored.ExtractionSource, interleavedthinking.ExtractionSourceFull)
	}
	if !stored.VisibleToClient {
		t.Fatal("memo storage: visible mode must mark memo VisibleToClient")
	}
	if stored.InjectedCount != 0 {
		t.Fatalf("memo storage: visible continuation must not inject memo, InjectedCount=%d", stored.InjectedCount)
	}

	attempts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
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
		t.Fatalf("lineage: thinker attempts got %d want 1", thinkerAttempts)
	}
	if execAttempts < 2 {
		t.Fatalf("lineage: executor attempts got %d want at least 2", execAttempts)
	}
}

func resumeInterleavedSession(origin, prev, next *aipapi.Call) {
	resumeInterleavedCall(prev, next)
	if origin != nil && strings.TrimSpace(next.Session.ResumeToken) == "" {
		next.Session.ResumeToken = origin.Session.ResumeToken
	}
}

func TestExecutor_VisibleMemoReinjectsOnLaterNormalExecutorTurn(t *testing.T) {
	t.Parallel()

	const (
		selector = "[thinker]thinker-be:m^[weight=2]exec-be:m"
		memoBody = "visible memo for later inject"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call aipapi.Call, cand routing.AttemptCandidate) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, role: cand.InterleavedRole, call: call})
		opensMu.Unlock()
	}

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	openWithCapture := func(backend string, newStream func() aipapi.ManagedEventStream) execbackend.Backend {
		return execbackend.Backend{
			Caps: caps,
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				recordOpen(backend, call, cand)
				return newStream(), nil
			},
		}
	}
	backends := map[string]execbackend.Backend{
		"exec-be": openWithCapture("exec-be", func() aipapi.ManagedEventStream {
			return executorTextStream("exec answer")
		}),
		"thinker-be": openWithCapture("thinker-be", func() aipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "visible",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	runtime.RegisterTestMemoStore(ex, memoStore)
	wireInterleavedTestSteering(ex)

	collectTurn := func(origin, prev, call *aipapi.Call) {
		t.Helper()
		if prev != nil {
			resumeInterleavedSession(origin, prev, call)
		}
		stream, err := ex.Execute(principalCtx("visible-memo-resume"), call)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if _, err := aipapi.Collect(context.Background(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}

	turn1 := interleavedBaseCall(selector)
	collectTurn(nil, nil, turn1)
	aLegID := turn1.Session.ALegID

	preThinker, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch pre-thinker cycle: %v", err)
	}
	preThinker.Cycle.NextIndex = 2
	if err := st.SetInterleavedState(context.Background(), aLegID, preThinker); err != nil {
		t.Fatalf("seed thinker cycle cursor: %v", err)
	}

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	turn2 := interleavedBaseCall(selector)
	collectTurn(turn1, turn1, turn2)

	opensMu.Lock()
	visibleTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	turnOpens = nil
	opensMu.Unlock()

	if len(visibleTurnOpens) < 2 {
		t.Fatalf("visible turn: want thinker then continuation executor opens, got %d: %+v", len(visibleTurnOpens), visibleTurnOpens)
	}
	if visibleTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("visible turn: first open got %q want thinker-be", visibleTurnOpens[0].backend)
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range visibleTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("visible turn: continuation executor open missing")
	}
	for _, msg := range continuationExec.call.Messages {
		txt := textOf(msg)
		if strings.Contains(txt, interleavedthinking.SessionSteeringGuidanceHeader) || strings.Contains(txt, memoBody) {
			t.Fatalf("immediate continuation executor must not inject visible memo: %q", txt)
		}
	}

	turn3 := interleavedBaseCall(selector)
	collectTurn(turn1, turn2, turn3)

	opensMu.Lock()
	laterTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	var laterExec *interleavedOpenRecord
	for _, open := range laterTurnOpens {
		if open.backend == "exec-be" {
			laterExec = &open
			break
		}
	}
	if laterExec == nil {
		t.Fatal("later normal turn: executor open missing")
	}
	if len(laterExec.call.Messages) == 0 {
		t.Fatal("later normal executor must carry conversation messages")
	}
	steeringMsg := findMemoSteeringMessage(t, laterExec.call)
	if steeringMsg == nil {
		t.Fatal("later normal executor must project the memo steering overlay")
	}
	projected := textOf(*steeringMsg)
	if !strings.Contains(projected, interleavedthinking.SessionSteeringGuidanceHeader) {
		t.Fatalf("later normal executor steering must carry guidance header: %q", projected)
	}
	if !strings.Contains(projected, memoBody) {
		t.Fatalf("later normal executor steering must carry memo body: %q", projected)
	}
	if countMemoSteeringMessages(t, laterExec.call) != 1 {
		t.Fatal("later normal executor: steering overlay must appear exactly once")
	}
	assertStandaloneMemoSteering(t, laterExec.call, memoBody)

	stored, ok, err := memoStore.Latest(context.Background(), interleavedthinking.Scope(aLegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if !stored.VisibleToClient {
		t.Fatal("memo must remain marked VisibleToClient")
	}
	if stored.InjectedCount != 1 {
		t.Fatalf("later normal turn must consume injection budget once, InjectedCount=%d", stored.InjectedCount)
	}
}

// findMemoSteeringMessage returns the projected synthetic steering user message
// in an opened backend call (#391), or nil when absent.
func findMemoSteeringMessage(t *testing.T, call aipapi.Call) *aipapi.Message {
	t.Helper()
	for i := range call.Messages {
		m := &call.Messages[i]
		if strings.Contains(textOf(*m), interleavedthinking.SessionSteeringGuidanceHeader) {
			if m.Role != aipapi.RoleUser {
				t.Fatalf("memo steering must project as a standalone user message, got role %q", m.Role)
			}
			return m
		}
	}
	return nil
}

// countMemoSteeringMessages counts messages carrying the steering guidance
// header; projection must present the memo exactly once per call.
func countMemoSteeringMessages(t *testing.T, call aipapi.Call) int {
	t.Helper()
	count := 0
	for _, m := range call.Messages {
		if strings.Contains(textOf(m), interleavedthinking.SessionSteeringGuidanceHeader) {
			count++
		}
	}
	return count
}

func assertStandaloneMemoSteering(t *testing.T, call aipapi.Call, memo string) {
	t.Helper()
	if len(call.Messages) < 2 {
		t.Fatalf("memo projection must add a standalone message: %+v", call.Messages)
	}
	wantOriginal := interleavedBaseCall(call.Route.Selector).Messages
	if len(wantOriginal) != 1 || textOf(call.Messages[0]) != textOf(wantOriginal[0]) || call.Messages[0].Role != wantOriginal[0].Role {
		t.Fatalf("memo projection mutated ingress message: got %+v want %+v", call.Messages[0], wantOriginal[0])
	}
	steering := findMemoSteeringMessage(t, call)
	if steering == nil || steering.Role != aipapi.RoleUser || !strings.Contains(textOf(*steering), memo) {
		t.Fatalf("memo projection missing standalone user steering: %+v", call.Messages)
	}
}
