package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestOpenPlannedCandidate_MaxAttemptsDoesNotPersistCycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "cycle-budget")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("[thinker]thinker:m^exec:m")
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"exec": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("backend must not open when attempt budget is exhausted")
				return nil, nil
			},
		},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "think"}, interleavedthinking.NewMemoStore(4096))
	ttft := newTTFTBudget(ex.now(), sel)
	progress := &recoveryController{
		budget:   &attemptBudget{max: 0},
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
	}
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:  "cycle-budget-test",
				aLegID:   aLeg.ALegID,
				baseline: aipapi.Call{Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}, Messages: testMinimalUserMessages()},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(1),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}

	_, err = ex.openNext(ctx, req)
	if !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
		t.Fatalf("want ErrMaxRouteAttempts, got %v", err)
	}
	state, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsEmpty() {
		t.Fatalf("cycle must not persist when budget blocks open, got %+v", state)
	}
}

func TestTryPlanOpenOnce_ParallelAllLegsFailPreservesInterleavedState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "parallel-fail-state")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]thinker-be:m^fail1:m!fail2:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"fail1": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return ParallelPreWinFailStream{}, nil
		}},
		"fail2": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return ParallelPreWinFailStream{}, nil
		}},
		"good": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Fatal("good backend must not open when parallel soft-fails without failover arm")
			return nil, nil
		}},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, interleavedthinking.NewMemoStore(4096))
	seededCycle := interleavedstate.CycleState{
		SelectorKey: "thinker-be:m^parallel:fail1:m!fail2:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "parallel:fail1:m!fail2:m", Role: interleavedstate.RoleExecutor},
			{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}
	interleaved := interleavedstate.State{Cycle: seededCycle}
	excluded := map[string]struct{}{}
	ttft := newTTFTBudget(ex.now(), sel)
	progress := &recoveryController{
		budget:   &attemptBudget{max: 8},
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: excluded,
		failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
	}
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:  "parallel-fail-state",
				aLegID:   aLeg.ALegID,
				baseline: aipapi.Call{Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: testMinimalUserMessages()},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(2),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleaved,
	}
	out1, err := ex.openNext(ctx, req)
	if err != nil {
		t.Fatalf("first plan/open: %v", err)
	}
	if out1.ready != nil && out1.ready.session != nil {
		t.Fatal("first iteration must soft-fail parallel race without winner")
	}
	if out1.interleaved.Cycle.IsEmpty() {
		t.Fatal("parallel all-legs-fail must preserve advanced interleaved cycle in result")
	}
	stored, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Cycle.IsEmpty() {
		t.Fatalf("parallel race failure must not persist/commit cycle progress, got %+v", stored.Cycle)
	}
}

func TestTryPlanOpenOnce_ParallelAllLegsFailFailoverToPrimaryInSamePass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "parallel-failover-primary")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]thinker-be:m^fail1:m!fail2:m|good:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	var goodOpens int
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"fail1": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return ParallelPreWinFailStream{}, nil
		}},
		"fail2": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return ParallelPreWinFailStream{}, nil
		}},
		"good": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			goodOpens++
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "good-wins"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		}},
		"thinker-be": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Fatal("thinker branch must not open when parallel soft-fails into primary failover")
			return nil, nil
		}},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, interleavedthinking.NewMemoStore(4096))
	interleaved := interleavedstate.State{Cycle: interleavedstate.CycleState{
		SelectorKey: "thinker-be:m^parallel:fail1:m!fail2:m|good:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "parallel:fail1:m!fail2:m", Role: interleavedstate.RoleExecutor},
			{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}}
	excluded := map[string]struct{}{}
	ttft := newTTFTBudget(ex.now(), sel)
	progress := &recoveryController{
		budget:   &attemptBudget{max: 8},
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: excluded,
		failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
	}
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:  "parallel-failover-primary",
				aLegID:   aLeg.ALegID,
				baseline: aipapi.Call{Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: testMinimalUserMessages()},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(2),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleaved,
	}
	out, err := ex.openNext(ctx, req)
	if err != nil {
		t.Fatalf("plan/open: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("same-pass tryPlanOpenOnce must open primary after parallel soft-fail")
	}
	if req.progress.failures.ParallelFailure != nil {
		t.Fatal("parallel failure context must clear after successful primary open")
	}
	if goodOpens != 1 {
		t.Fatalf("good backend opens: got %d want 1", goodOpens)
	}
	if out.ready.Candidate().Primary.Backend != "good" {
		t.Fatalf("opened backend: got %q want good", out.ready.Candidate().Primary.Backend)
	}
}

var errInjectedCyclePersist = errors.New("injected: cycle persist failed")

type failInterleavedPersistStore struct {
	*b2bua.MemoryStore
}

func (s *failInterleavedPersistStore) SetInterleavedState(context.Context, string, interleavedstate.State) error {
	return errInjectedCyclePersist
}

func TestTryPlanOpenOnce_ThinkerRecoverableOpenFailureDoesNotPersistCycleAdvance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "thinker-open-fail-cycle")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]bad-thinker:m^exec-be:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	var opensMu sync.Mutex
	var opened []string
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"bad-thinker": {
			Caps: caps, TransportCaps: transport,
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if cand.InterleavedRole != interleavedstate.RoleThinker {
					t.Fatalf("first open role: got %q want thinker", cand.InterleavedRole)
				}
				opensMu.Lock()
				opened = append(opened, "bad-thinker")
				opensMu.Unlock()
				return nil, aipapi.RecoverablePreOutputError(errors.New("thinker down"))
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensMu.Lock()
				opened = append(opened, "exec-be")
				opensMu.Unlock()
				t.Fatal("executor must not open in same tryPlanOpenOnce pass after thinker recoverable open failure")
				return nil, nil
			},
		},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, interleavedthinking.NewMemoStore(4096))
	thinkerIdx := 1
	seededCycle := interleavedstate.CycleState{
		SelectorKey: "bad-thinker:m^exec-be:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "exec-be:m", Role: interleavedstate.RoleExecutor},
			{Key: "bad-thinker:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: thinkerIdx,
	}
	seeded := interleavedstate.State{Cycle: seededCycle}
	if err := st.SetInterleavedState(ctx, aLeg.ALegID, seeded); err != nil {
		t.Fatal(err)
	}
	ttft := newTTFTBudget(ex.now(), sel)
	progress := &recoveryController{
		budget:   &attemptBudget{max: 8},
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
	}
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID: "thinker-open-fail-cycle",
				aLegID:  aLeg.ALegID,
				baseline: aipapi.Call{
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("plan this")},
					}},
					Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
				},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(2),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: seeded,
	}
	out, err := ex.openNext(ctx, req)
	if err != nil {
		t.Fatalf("openNext: %v", err)
	}
	if out.ready != nil && out.ready.session != nil {
		t.Fatal("thinker recoverable open failure must not open a stream")
	}
	opensMu.Lock()
	gotOpens := append([]string(nil), opened...)
	opensMu.Unlock()
	if len(gotOpens) != 1 || gotOpens[0] != "bad-thinker" {
		t.Fatalf("opens: got %+v want [bad-thinker]", gotOpens)
	}
	if out.interleaved.Cycle.NextIndex != thinkerIdx {
		t.Fatalf("threaded cycle NextIndex: got %d want %d", out.interleaved.Cycle.NextIndex, thinkerIdx)
	}
	stored, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cycle.NextIndex != thinkerIdx {
		t.Fatalf("persisted cycle NextIndex: got %d want %d (must not advance past failed thinker)", stored.Cycle.NextIndex, thinkerIdx)
	}
	if stored.Cycle.Sequence[thinkerIdx].Role != interleavedstate.RoleThinker {
		t.Fatalf("persisted cycle cursor must still point at thinker entry, got %+v", stored.Cycle.Sequence[stored.Cycle.NextIndex])
	}
}

func TestTryPlanOpenOnce_InterleavedCyclePersistFailureFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &failInterleavedPersistStore{MemoryStore: base}
	aLeg, err := st.CreateALeg(ctx, "persist-fail")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("[thinker]thinker:m^exec:m")
	if err != nil {
		t.Fatal(err)
	}
	var backendOpens int
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"exec": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendOpens++
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "think"}, interleavedthinking.NewMemoStore(4096))
	ttft := newTTFTBudget(ex.now(), sel)
	budget := &attemptBudget{max: 8}
	failures := budget.getFailures()
	progress := &recoveryController{
		budget:   budget,
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		failures: failures,
	}
	budget.failures = failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:  "persist-fail",
				aLegID:   aLeg.ALegID,
				baseline: aipapi.Call{Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}, Messages: testMinimalUserMessages()},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(1),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}
	_, err = ex.openNext(ctx, req)
	if !errors.Is(err, errInjectedCyclePersist) {
		t.Fatalf("want errInjectedCyclePersist, got %v", err)
	}
	if budget.usedNow() != 1 {
		t.Fatalf("attempt budget used: got %d want 1", budget.usedNow())
	}
	if backendOpens != 1 {
		t.Fatalf("backend must open before cycle persist failure, opens=%d", backendOpens)
	}
	state, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsEmpty() {
		t.Fatalf("cycle must not persist when SetInterleavedState fails, got %+v", state)
	}
}

func TestTryPlanOpenOnce_ParallelBudgetRejectsAllPreservesCycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "parallel-budget-cycle")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("[thinker]thinker-be:m^a:m!b:m")
	if err != nil {
		t.Fatal(err)
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"a": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Fatal("parallel leg must not open when budget rejects all entries")
			return nil, nil
		}},
		"b": {Caps: caps, TransportCaps: transport, Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Fatal("parallel leg must not open when budget rejects all entries")
			return nil, nil
		}},
	}
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{Instructions: "Think step by step."}, interleavedthinking.NewMemoStore(4096))
	seededCycle := interleavedstate.CycleState{
		SelectorKey: "thinker-be:m^a:m!b:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "a:m!b:m", Role: interleavedstate.RoleExecutor},
			{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}
	if err := st.SetInterleavedState(ctx, aLeg.ALegID, interleavedstate.State{Cycle: seededCycle}); err != nil {
		t.Fatal(err)
	}
	ttft := newTTFTBudget(ex.now(), sel)
	budget := &attemptBudget{max: 0}
	failures := budget.getFailures()
	progress := &recoveryController{
		budget:   budget,
		ttft:     ttft,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		failures: failures,
	}
	budget.failures = failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:  "parallel-budget-cycle",
				aLegID:   aLeg.ALegID,
				baseline: aipapi.Call{Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming}, Messages: testMinimalUserMessages()},

				billingCallID:    usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(2),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{Cycle: seededCycle},
	}
	_, err = ex.openNext(ctx, req)
	if !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
		t.Fatalf("want ErrMaxRouteAttempts, got %v", err)
	}
	stored, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cycle.NextIndex != seededCycle.NextIndex {
		t.Fatalf("cycle NextIndex: got %d want %d when budget rejects all parallel legs", stored.Cycle.NextIndex, seededCycle.NextIndex)
	}
}
