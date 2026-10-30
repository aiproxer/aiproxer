package runtime

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type contextAuthoritySpyStream struct{}

func (contextAuthoritySpyStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (contextAuthoritySpyStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (contextAuthoritySpyStream) Close() error {
	return nil
}

type testContextAuthorityHook struct {
	fn func(context.Context, *aipapi.Call, sdk.PartMeta) error
}

func (h testContextAuthorityHook) ID() string                   { return "test-context-authority-hook" }
func (h testContextAuthorityHook) Order() int                   { return 1 }
func (h testContextAuthorityHook) FailureMode() sdk.FailureMode { return sdk.FailClosed }

func (h testContextAuthorityHook) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	return h.fn(ctx, call, meta)
}

func TestTDD_ContextAuthorityPinning(t *testing.T) {
	// 1. Setup original context with authoritative values
	pinnedMetering := &checkpoint.RequestHolder{}
	pinnedScope := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("p-original"),
		AuthMethod:  scope.Known("token"),
	}
	pinnedPrefs := []string{"backend-B:model-B"}

	// Bind these to prepCtx using WithDetachedSession
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{})
	ctx = withMeteringHolder(ctx, pinnedMetering)
	ctx = scope.WithScope(ctx, pinnedScope)
	ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "p-original"})
	ctx = execctx.WithRouteCandidatePreferences(ctx, pinnedPrefs)

	// Setup Executor
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.Store = st
	ex.UsageAuthority = &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "tdd-res"},
	}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	// Register dummy backends
	ex.Backends = map[string]execbackend.Backend{
		"backend-A": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return contextAuthoritySpyStream{}, nil
			},
		},
		"backend-B": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return contextAuthoritySpyStream{}, nil
			},
		},
	}

	// Setup a Hook to verify context projection during attempt open
	var hookCalled bool
	var hookRoutePrefs []string
	var hookScope scope.PrincipalScopeView
	var hookMetering *checkpoint.RequestHolder

	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			testContextAuthorityHook{
				fn: func(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
					hookCalled = true
					hookRoutePrefs = execctx.RouteCandidatePreferences(ctx)
					hookScope = scopeFromCtx(ctx)
					hookMetering = meteringHolderFrom(ctx)
					return nil
				},
			},
		},
	})

	call := &aipapi.Call{
		ID:    "call-123",
		Route: aipapi.RouteIntent{Selector: "backend-A:model-A|backend-B:model-B"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}

	// Run prepareRequest
	prep, prepCtx, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_ = prepCtx // prepCtx has the correct pinned values

	// Set pinned preferences manually because detached mode strips them from the context
	prep.routePrefs = pinnedPrefs

	// 2. Setup a poisoned/omitted context for the attempt open loop
	poisonedMetering := &checkpoint.RequestHolder{}
	poisonedScope := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("p-poisoned"),
		AuthMethod:  scope.Known("poisoned"),
	}
	poisonedPrefs := []string{"backend-A:model-A"}

	poisonedCtx := context.Background()
	poisonedCtx = withMeteringHolder(poisonedCtx, poisonedMetering)
	poisonedCtx = scope.WithScope(poisonedCtx, poisonedScope)
	poisonedCtx = execview.WithPrincipal(poisonedCtx, execview.PrincipalView{ID: "p-poisoned"})
	poisonedCtx = execctx.WithRouteCandidatePreferences(poisonedCtx, poisonedPrefs)

	// Run route planning
	sel, err := routing.Parse("backend-A:model-A|backend-B:model-B")
	if err != nil {
		t.Fatal(err)
	}
	progress := newRecoveryController(recoveryControllerInput{
		budget:   &attemptBudget{max: 3},
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      routing.NewSeededRng(1),
	})
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(1),
		},
		progress: progress,
	}

	// 3. Invoke the open loop with the poisoned context
	out, err := ex.openInitialAttempt(poisonedCtx, prep, plan)
	if err != nil {
		t.Fatalf("openInitialAttempt failed: %v", err)
	}

	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected opened attempt session")
	}

	// PROVE 1: Planner preferences (PreferredCandidateKeys) used pinned RoutePrefs (backend-B), not poisoned (backend-A)
	// Since backend-B is preferred, it should be selected first.
	if out.ready.Candidate().Primary.Backend != "backend-B" {
		t.Fatalf("planner preferred candidate: want backend-B, got %s", out.ready.Candidate().Primary.Backend)
	}

	// PROVE 2: Metering holder used pinned metering holder, not poisoned metering holder
	if len(pinnedMetering.BackendIngress) == 0 {
		t.Fatal("expected pinned metering holder to receive backend ingress snapshot")
	}
	if len(poisonedMetering.BackendIngress) > 0 {
		t.Fatal("expected poisoned metering holder NOT to receive backend ingress snapshot")
	}

	// PROVE 3: Backend ingress scope used pinned principal scope
	var recordedScope scope.PrincipalScopeView
	for _, snap := range pinnedMetering.BackendIngress {
		recordedScope = snap.Public.Scope
	}
	if recordedScope.PrincipalID.String() != "p-original" {
		t.Fatalf("recorded ingress scope: want p-original, got %s", recordedScope.PrincipalID.String())
	}

	// PROVE 4: Hooks got projected compatibility values from pinned facts
	if !hookCalled {
		t.Fatal("expected request hook to be called")
	}
	if len(hookRoutePrefs) != 1 || hookRoutePrefs[0] != "backend-B:model-B" {
		t.Fatalf("hook route prefs: want backend-B:model-B, got %v", hookRoutePrefs)
	}
	if hookScope.PrincipalID.String() != "p-original" {
		t.Fatalf("hook scope principal ID: want p-original, got %s", hookScope.PrincipalID.String())
	}
	if hookMetering != pinnedMetering {
		t.Fatalf("hook metering holder: want %p, got %p", pinnedMetering, hookMetering)
	}
}

func TestTDD_ContextAuthorityPinningReplacement(t *testing.T) {
	// 1. Setup original context with authoritative values
	pinnedMetering := &checkpoint.RequestHolder{}
	pinnedScope := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("p-original"),
		AuthMethod:  scope.Known("token"),
	}
	pinnedPrefs := []string{"backend-B:model-B"}

	// Bind these to prepCtx using WithDetachedSession
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{})
	ctx = withMeteringHolder(ctx, pinnedMetering)
	ctx = scope.WithScope(ctx, pinnedScope)
	ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "p-original"})
	ctx = execctx.WithRouteCandidatePreferences(ctx, pinnedPrefs)

	// Setup Executor
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.Store = st
	ex.UsageAuthority = &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "tdd-res"},
	}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	// Register dummy backends
	ex.Backends = map[string]execbackend.Backend{
		"backend-A": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return contextAuthoritySpyStream{}, nil
			},
		},
		"backend-B": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return contextAuthoritySpyStream{}, nil
			},
		},
	}

	// Setup a Hook to verify context projection during attempt open
	var hookCalled bool
	var hookRoutePrefs []string
	var hookScope scope.PrincipalScopeView
	var hookMetering *checkpoint.RequestHolder

	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			testContextAuthorityHook{
				fn: func(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
					hookCalled = true
					hookRoutePrefs = execctx.RouteCandidatePreferences(ctx)
					hookScope = scopeFromCtx(ctx)
					hookMetering = meteringHolderFrom(ctx)
					return nil
				},
			},
		},
	})

	call := &aipapi.Call{
		ID:    "call-123",
		Route: aipapi.RouteIntent{Selector: "backend-A:model-A|backend-B:model-B"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}

	// Run prepareRequest
	prep, _, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Set pinned preferences manually because detached mode strips them from the context
	prep.routePrefs = pinnedPrefs

	// Run route planning
	sel, err := routing.Parse("backend-A:model-A|backend-B:model-B")
	if err != nil {
		t.Fatal(err)
	}
	progress := newRecoveryController(recoveryControllerInput{
		e:        ex,
		opener:   newReplacementOpener(ex, prep.bus, prep.aScope),
		budget:   &attemptBudget{max: 3},
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      routing.NewSeededRng(1),
	})
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: routing.NewSeededRng(1),
		},
		progress: progress,
	}

	// Invoke the initial open loop.
	out, err := ex.openInitialAttempt(context.Background(), prep, plan)
	if err != nil {
		t.Fatalf("openInitialAttempt failed: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected opened attempt session")
	}

	// Setup a poisoned/omitted context for the retry/replacement open loop
	poisonedMetering := &checkpoint.RequestHolder{}
	poisonedScope := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("p-poisoned"),
		AuthMethod:  scope.Known("poisoned"),
	}
	poisonedPrefs := []string{"backend-A:model-A"}

	poisonedCtx := context.Background()
	poisonedCtx = withMeteringHolder(poisonedCtx, poisonedMetering)
	poisonedCtx = scope.WithScope(poisonedCtx, poisonedScope)
	poisonedCtx = execview.WithPrincipal(poisonedCtx, execview.PrincipalView{ID: "p-poisoned"})
	poisonedCtx = execctx.WithRouteCandidatePreferences(poisonedCtx, poisonedPrefs)

	// Reset hook state
	hookCalled = false
	hookRoutePrefs = nil
	hookScope = scope.PrincipalScopeView{}
	hookMetering = nil

	// Retire the first attempt
	out.ready.session.authority.finalizeIncurredOrRelease(context.Background(), usagelimit.ReleaseKindSwallowed, emptyOperatorUsageShell())

	// Call openReplacement (or tryReplacementIteration) with the poisoned context
	res, err := progress.tryReplacementIteration(poisonedCtx, prep.terminalFacts(), out.ready.session, false)
	if err != nil {
		t.Fatalf("tryReplacementIteration failed: %v", err)
	}
	if !res.opened || res.next == nil {
		t.Fatal("expected opened replacement attempt session")
	}

	// PROVE 1: Planner preferences (PreferredCandidateKeys) used pinned RoutePrefs (backend-B), not poisoned (backend-A)
	if res.next.Candidate().Primary.Backend != "backend-B" {
		t.Fatalf("planner preferred candidate: want backend-B, got %s", res.next.Candidate().Primary.Backend)
	}

	// PROVE 2: Metering holder used pinned metering holder, not poisoned metering holder
	if len(pinnedMetering.BackendIngress) == 0 {
		t.Fatal("expected pinned metering holder to receive backend ingress snapshot")
	}
	if len(poisonedMetering.BackendIngress) > 0 {
		t.Fatal("expected poisoned metering holder NOT to receive backend ingress snapshot")
	}

	// PROVE 3: Backend ingress scope used pinned principal scope
	var recordedScope scope.PrincipalScopeView
	for _, snap := range pinnedMetering.BackendIngress {
		recordedScope = snap.Public.Scope
	}
	if recordedScope.PrincipalID.String() != "p-original" {
		t.Fatalf("recorded ingress scope: want p-original, got %s", recordedScope.PrincipalID.String())
	}

	// PROVE 4: Hooks got projected compatibility values from pinned facts
	if !hookCalled {
		t.Fatal("expected request hook to be called")
	}
	if len(hookRoutePrefs) != 1 || hookRoutePrefs[0] != "backend-B:model-B" {
		t.Fatalf("hook route prefs: want backend-B:model-B, got %v", hookRoutePrefs)
	}
	if hookScope.PrincipalID.String() != "p-original" {
		t.Fatalf("hook scope principal ID: want p-original, got %s", hookScope.PrincipalID.String())
	}
	if hookMetering != pinnedMetering {
		t.Fatalf("hook metering holder: want %p, got %p", pinnedMetering, hookMetering)
	}
}
