package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// The coverage and the inertness of the pre-open guard (design §5.4; B7,
// B11).
//
// The auxiliary client always builds a canonical call, so the wire fast path
// is unreachable for a governed phase and the guard is consulted on the
// canonical path. The role label is captured once, at the assembly boundary,
// into the typed frozen facts, so a request-part hook cannot drop it.

// guardLabelDroppingHook is a request-part hook that tries to drop every
// label the call carries and to corrupt the lineage copy of the auxiliary
// role. The role label is not one of them: it is a typed frozen fact,
// captured before the hook runs.
type guardLabelDroppingHook struct{}

func (guardLabelDroppingHook) ID() string { return "guard-label-dropping" }

func (guardLabelDroppingHook) Order() int { return 1 }

func (guardLabelDroppingHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (guardLabelDroppingHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	call.Extensions = nil
	// The lineage copy is observability only. The hook corrupts it, and the
	// guard must still see the original label from the typed frozen fact.
	call.Extensions = map[string]json.RawMessage{
		"aip.aux.lineage.v1": json.RawMessage(`{"role":"corrupted-by-hook"}`),
	}
	return nil
}

// guardRoleRecorder records the inputs of the pre-open checks it received.
type guardRoleRecorder struct {
	mu    sync.Mutex
	seen  []AttemptGuardInput
	fail  bool
	calls int
}

func (g *guardRoleRecorder) CheckAttempt(_ context.Context, in AttemptGuardInput) error {
	g.mu.Lock()
	g.calls++
	g.seen = append(g.seen, in)
	fail := g.fail
	g.mu.Unlock()
	if fail {
		return errGuardPolicyRefusal{}
	}
	return nil
}

func (g *guardRoleRecorder) inputs() []AttemptGuardInput {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]AttemptGuardInput(nil), g.seen...)
}

// errGuardPolicyRefusal is the content-free refusal of the recorded guard.
type errGuardPolicyRefusal struct{}

func (errGuardPolicyRefusal) Error() string { return "the policy refuses this attempt" }

// guardAuxiliaryRequest is one canonical auxiliary request of the given
// role: the auxiliary client always builds a canonical call, so the request
// carries no wire payload.
func guardAuxiliaryRequest(role string) auxiliary.Request {
	return guardAuxiliaryRequestFor(role, "backend:aux-model")
}

// guardAuxiliaryRequestFor is one canonical auxiliary request of the given
// role and route selector.
func guardAuxiliaryRequestFor(role, selector string) auxiliary.Request {
	return auxiliary.Request{
		Role:        role,
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: selector},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
		},
	}
}

// guardRoleExecutor wires one executor with a recording guard, the
// label-dropping request-part hook and the given backends.
func guardRoleExecutor(t *testing.T, principalID string, backends map[string]execbackend.Backend) (*Executor, *guardRoleRecorder) {
	t.Helper()
	ex := newReasoningCompressorExecutor(t, &reasoningCompressorCapture{}, "acct:"+principalID)
	recorder := &guardRoleRecorder{}
	ex.AttemptGuard = recorder
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{guardLabelDroppingHook{}}})
	ex.Backends = backends
	return ex, recorder
}

// guardRoleScope is the scope of one auxiliary attempt.
func guardRoleScope(principalID string) context.Context {
	return scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
}

// TestAuxiliaryPhaseGuardSeesTheFrozenRoleInAParallelRound pins the role
// freeze of design §2 on the parallel leg (R10).
//
// The real auxiliary client drives the real executor through a real parallel
// route, so the role travels the real capture into the real frozen facts and
// the arms inherit them. A request-part hook deletes the call's extensions
// and corrupts the lineage copy of the role, and the guard must still see
// the original label.
//
// The proof is conflict-seeded: the hook's corrupted copy and the frozen fact
// disagree, and the frozen fact wins. Removing the capture (the role freeze)
// makes the guard see no label at all, so the case fails.
func TestAuxiliaryPhaseGuardSeesTheFrozenRoleInAParallelRound(t *testing.T) {
	principalID := "principal-guard-role-parallel"
	backends := map[string]execbackend.Backend{}
	for _, id := range []string{"arm-a", "arm-b"} {
		backends[id] = execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// The arms produce no winning content: the round has no
				// winner, so it waits for every arm and the role proof
				// does not depend on a publication race.
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}
	}
	ex, recorder := guardRoleExecutor(t, principalID, backends)
	// The parallel group spends one attempt budget per arm and per open, so
	// the budget must cover both arms.
	ex.MaxAttempts = 4
	ctx := guardRoleScope(principalID)
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	stream, err := client.Stream(ctx, guardAuxiliaryRequestFor("proxy_memory_phase1", "arm-a:aux-model!arm-b:aux-model"))
	if err == nil {
		// The arms produce no winning content, so the round has no winner.
		// The role proof does not depend on the round's outcome.
		_, _ = aipapi.Collect(ctx, stream)
	}

	inputs := recorder.inputs()
	if len(inputs) < 2 {
		t.Fatalf("guard consultations = %d, want at least 2 (one per parallel arm)", len(inputs))
	}
	keys := map[string]struct{}{}
	for _, in := range inputs {
		if in.AuxiliaryRole != "proxy_memory_phase1" {
			t.Fatalf("auxiliary role = %q, want %q", in.AuxiliaryRole, "proxy_memory_phase1")
		}
		keys[in.CandidateKey] = struct{}{}
	}
	if len(keys) < 2 {
		t.Fatalf("distinct guard candidate keys = %d, want at least 2 (one per parallel arm): %v", len(keys), keys)
	}
}

// TestAuxiliaryPhaseGuardSeesTheFrozenRoleInTheContinuationLeg pins the role
// freeze of design §2 on the B2BUA continuation leg (R10).
//
// The continuation re-uses the pinned frozen facts of the request it
// continues, and the guard reads the typed frozen fact. The continuation's
// own context carries no auxiliary role carrier, so a late context read
// would see no label: the case fails when the role is read from the context
// instead of the frozen fact.
func TestAuxiliaryPhaseGuardSeesTheFrozenRoleInTheContinuationLeg(t *testing.T) {
	principalID := "principal-guard-role-continuation"
	ex, recorder := guardRoleExecutor(t, principalID, map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventTextDelta, Delta: "continued"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	})
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(context.Background(), "guard-role-continuation")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	ex.Store = store
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLeg.ALegID)

	sel, err := routing.Parse("backend:m")
	if err != nil {
		t.Fatalf("parse route: %v", err)
	}
	plan := &routePlanState{routeFacts: routeFacts{sel: sel, rng: routing.NewSeededRng(1)}}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend:m"},
		Session: aipapi.SessionRef{
			ALegID: aLeg.ALegID,
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	ibt, err := newIdentityBoundTurn(
		"guard-role-continuation",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		aLeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		session.SessionView{ALegID: aLeg.ALegID},
	)
	if err != nil {
		t.Fatalf("identity bound turn: %v", err)
	}
	prep := &preparedRequest{identity: ibt, call: ibt.call, aScope: aScope}

	// The initial attempt carries the auxiliary role on the core-owned
	// carrier, so the assembly boundary captures it into the frozen facts.
	initialCtx := execctx.WithAuxiliaryRole(context.Background(), "proxy_memory_phase1")
	out, err := ex.openInitialAttempt(initialCtx, prep, plan)
	if err != nil {
		t.Fatalf("open initial attempt: %v", err)
	}
	if out.ready == nil {
		t.Fatal("the initial attempt did not open")
	}
	before := len(recorder.inputs())

	// The continuation re-uses the pinned frozen facts. Its own context
	// carries no auxiliary role carrier.
	opener := plan.progress.opener
	if opener == nil {
		t.Fatal("the progress controller has no replacement opener")
	}
	res, err := opener(context.Background(), replacementOpenRequest{
		facts: requestTerminalFacts{
			call:          *prep.call,
			traceID:       "guard-role-continuation",
			aLegID:        aLeg.ALegID,
			billingCallID: prep.billingCallID,
			billingState:  prep.billingCallState,
		},
		pinnedFacts: prep.recvTurnFacts,
		recovery:    plan.progress.openSnapshot(),
		prior:       priorAttemptOutcome{attempt: out.ready.session, retired: true},
		isRetryPath: false,
	})
	if err != nil {
		t.Fatalf("continuation open: %v", err)
	}
	if !res.opened {
		t.Fatal("the continuation leg did not open")
	}
	inputs := recorder.inputs()
	if len(inputs) <= before {
		t.Fatalf("guard consultations = %d, want more than %d (the continuation leg must consult the guard)", len(inputs), before)
	}
	for _, in := range inputs[before:] {
		if in.AuxiliaryRole != "proxy_memory_phase1" {
			t.Fatalf("continuation auxiliary role = %q, want %q", in.AuxiliaryRole, "proxy_memory_phase1")
		}
	}
}

// TestGovernedAuxiliaryAttemptNeverEntersTheWireLane pins the wire-lane
// unreachability of design §5.4 (B7, R9) as a factual negative on the
// admission's own observable.
//
// The wire fast path and the canonical path freeze different backend-ingress
// facts: the canonical path freezes the provider-neutral attempt call, and
// the wire path freezes the bounded wire facts with the wire attempt
// evidence attached. A governed auxiliary attempt therefore carries the
// canonical freeze and no wire evidence, and the negative is the wire
// lane's own distinguishing fact — not the presence of the guard.
func TestGovernedAuxiliaryAttemptNeverEntersTheWireLane(t *testing.T) {
	principalID := "principal-guard-wire-lane"
	ex, _ := guardRoleExecutor(t, principalID, map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	})
	holder := &checkpoint.RequestHolder{}
	ctx := withMeteringHolder(guardRoleScope(principalID), holder)
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	stream, err := client.Stream(ctx, guardAuxiliaryRequest("proxy_memory_phase1"))
	if err != nil {
		t.Fatalf("auxiliary stream: %v", err)
	}
	if _, err := aipapi.Collect(ctx, stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(holder.BackendIngress) == 0 {
		t.Fatal("the attempt froze no backend ingress: the observable is not live")
	}
	for attemptID, snap := range holder.BackendIngress {
		if snap == nil {
			t.Fatalf("attempt %q froze no backend ingress snapshot", attemptID)
		}
		if snap.Evidence != nil {
			t.Fatalf("attempt %q froze wire attempt evidence: the wire lane was entered", attemptID)
		}
	}
}

// TestAuxiliaryNonPhaseRoleLeavesTheGuardInert pins the inertness of design
// §5.4: a role that names no phase of the feature changes nothing. The core
// guard is generic, so the inertness is proven by the feature's own guard;
// this test proves that the core consults the guard for every auxiliary
// attempt, so the feature's own role switch is the only filter.
func TestAuxiliaryNonPhaseRoleLeavesTheGuardInert(t *testing.T) {
	principalID := "principal-guard-inert"
	ex := newReasoningCompressorExecutor(t, &reasoningCompressorCapture{}, "acct:"+principalID)
	recorder := &guardRoleRecorder{}
	ex.AttemptGuard = recorder
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	stream, err := client.Stream(ctx, guardAuxiliaryRequest(usageevidence.WorkloadRoleCompactionContinuityExtractor))
	if err != nil {
		t.Fatalf("auxiliary stream: %v", err)
	}
	if _, err := aipapi.Collect(ctx, stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	inputs := recorder.inputs()
	if len(inputs) == 0 {
		t.Fatal("the guard was never consulted")
	}
	for _, in := range inputs {
		if in.AuxiliaryRole != usageevidence.WorkloadRoleCompactionContinuityExtractor {
			t.Fatalf("auxiliary role = %q, want %q", in.AuxiliaryRole, usageevidence.WorkloadRoleCompactionContinuityExtractor)
		}
	}
}
