package runtime_test

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 6.2. Requirements 4.1, 5.1, 5.7 and
// design.md "5. Completed Tool-Call Finalizer Metadata" ("Runtime populates
// these from the same authoritative request views already used for tool
// policy/reactor metadata. Existing finalizers remain source compatible. No
// client-provided raw metadata becomes authority."), plus "Existing
// Architecture and Placement" steps 11/12 and the model->client expansion
// bullets ("model->client expansion uses completed tool-call finalization, not
// raw ToolCallArgsDelta mutation"; "expansion completes before existing tool
// policy/reactors").
//
// Task 6.1 added the three view fields to toolcall.Meta; this task is the
// runtime side that populates them. The file pins three consequences:
//
//   - the values a completed-tool-call finalizer observes are the very same
//     authoritative request views the tool policy, tool reactor, and completion
//     gate planes observe, not a parallel derivation;
//   - they are detached per finalization call, so a finalizer that mutates the
//     maps and slices it is handed cannot reach the request's producer snapshot
//     or a sibling consumer;
//   - nothing a client supplied is promoted to authority.
//
// The producer-side half of the detachment proof, plus the absent-snapshot
// semantics of the population site, live in
// tool_call_finalization_meta_internal_test.go, which needs package-internal
// access to recvTurnFacts.viewsFor and responsePipeline.prepareRecvEvent.
//
// Every fixture value below is synthetic, and no assertion message prints a
// path, alias, workspace tag, session id, or principal value: failures report
// booleans, counts, lengths, and ordinals only.

const (
	// fcmAuthoritativeRoot is what the proxy-side workspace resolver publishes.
	// It is the only project root a finalizer may treat as authority.
	fcmAuthoritativeRoot = "/synthetic/authoritative/workspace/root"

	// fcmClientClaimedRoot is the lookalike a client tries to smuggle in through
	// its own raw session metadata. It must never become finalization authority.
	fcmClientClaimedRoot = "/synthetic/client/claimed/workspace/root"

	// fcmClientClaimedPrincipal and fcmClientClaimedSession are the identity
	// lookalikes carried by that same client-supplied metadata.
	fcmClientClaimedPrincipal = "synthetic-client-claimed-principal"
	fcmClientClaimedSession   = "synthetic-client-claimed-session"

	// fcmClientMetadataKeys are client-declared metadata keys. They are prefixed
	// so they cannot be confused with, or collide with, the label keys the proxy
	// legitimately publishes from validated state.
	fcmClientKeyRoot      = "client_declared_workspace_root"
	fcmClientKeyPrincipal = "client_declared_principal_id"
	fcmClientKeySession   = "client_declared_session_id"
	fcmClientKeyLabel     = "client_declared_effective_treatment"

	// fcmMutationSentinel is the private marker a misbehaving finalizer writes
	// into every reference-typed payload it is handed. Its absence everywhere
	// else is the detachment proof.
	fcmMutationSentinel = "fcm_mutation_sentinel"

	// fcmWorkspaceLabelKey is a proxy-published workspace label. It is not a
	// client-declared key and carries proxy state, not client input.
	fcmWorkspaceLabelKey   = "workspace_resolver_marker"
	fcmWorkspaceLabelValue = "proxy-resolver-label"

	fcmToolName   = "fcm_read_file"
	fcmToolCallID = "fcm-1"
	fcmBackendID  = "openai"
	fcmSelector   = "openai:gpt-4"

	// fcmAuthenticatedPrincipal is the principal authenticated by transport and
	// therefore the only principal a finalizer may treat as authority.
	fcmAuthenticatedPrincipal = "synthetic-authenticated-principal"
)

// fcmToolArgsDocument is the completed argument document the backend streams for
// one tool call.
const fcmToolArgsDocument = `{"path":"/synthetic/model/emitted/path.txt"}`

// fcmViews is a deep copy of the three authoritative views taken at the moment a
// probe observed them, so a later in-place write by another component cannot
// rewrite history underneath the comparison.
type fcmViews struct {
	scope     scope.PrincipalScopeView
	session   session.SessionView
	workspace aipworkspace.WorkspaceView
}

// fcmDetachViews deep-copies every reference-typed payload reachable through the
// three views. Scope uses the SDK's own [scope.PrincipalScopeView.Clone]; Session
// labels and Workspace labels/markers are detached with the standard library
// copies. This is exactly the discipline the runtime producer applies before
// handing views to a consumer, and it is what lets a probe report what it saw
// rather than what a later writer left behind.
func fcmDetachViews(s scope.PrincipalScopeView, se session.SessionView, w aipworkspace.WorkspaceView) fcmViews {
	se.Labels = maps.Clone(se.Labels)
	w.Labels = maps.Clone(w.Labels)
	w.Markers = slices.Clone(w.Markers)
	return fcmViews{scope: s.Clone(), session: se, workspace: w}
}

// fcmViewsAgree reports whether two observations carry the same scope, session,
// and workspace views by value.
func fcmViewsAgree(a, b fcmViews) bool {
	return reflect.DeepEqual(a.scope, b.scope) &&
		reflect.DeepEqual(a.session, b.session) &&
		reflect.DeepEqual(a.workspace, b.workspace)
}

// fcmContainsSentinel counts the reference-typed payloads in a view triple that
// carry the mutation sentinel, i.e. how far an in-place write actually spread.
func fcmContainsSentinel(v fcmViews) int {
	found := 0
	for _, m := range []map[string]string{v.scope.SafeClaims, v.scope.PolicyLabels, v.session.Labels, v.workspace.Labels} {
		if _, ok := m[fcmMutationSentinel]; ok {
			found++
		}
	}
	for _, role := range v.scope.Roles {
		if role == fcmMutationSentinel {
			found++
		}
	}
	for _, marker := range v.workspace.Markers {
		if marker == fcmMutationSentinel {
			found++
		}
	}
	return found
}

// fcmAuthoritativeScope is the trusted principal/scope snapshot an
// authenticated transport layer attaches to the request context. It carries
// reference-typed roles, safe claims, and policy labels so the detachment test
// covers every writable payload the three views expose, not only the workspace
// ones.
func fcmAuthoritativeScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		Origin:       scope.OriginClient,
		SubjectKind:  scope.SubjectService,
		PrincipalID:  scope.Known(fcmAuthenticatedPrincipal),
		AuthMethod:   scope.Known("synthetic_transport_auth"),
		Roles:        []string{"synthetic-role-a", "synthetic-role-b"},
		SafeClaims:   map[string]string{"synthetic_claim": "synthetic-value"},
		PolicyLabels: map[string]string{"synthetic_label": "synthetic-value"},
	}
}

// fcmAuthContext builds the authenticated request context. The trusted scope
// takes precedence over any legacy principal projection, exactly as the HTTP
// auth bridge establishes it in production.
func fcmAuthContext() context.Context {
	s := fcmAuthoritativeScope()
	return scope.WithScope(execview.WithPrincipal(context.Background(), s.Principal()), s)
}

// fcmBackendEvents is one complete tool call streamed as canonical events.
func fcmBackendEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: fcmToolCallID, ToolName: fcmToolName},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: fcmToolCallID, ToolName: fcmToolName, Delta: fcmToolArgsDocument},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: fcmToolCallID, ToolName: fcmToolName},
		{Kind: aipapi.EventResponseFinished},
	}
}

// fcmCall is the canonical client request carrying one path-bearing tool.
func fcmCall() *aipapi.Call {
	call := pdBaseCall(fcmSelector)
	call.Tools = []aipapi.ToolDef{{
		Name:       fcmToolName,
		Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	return call
}

// fcmCallWithClientLookalikes is the same request plus the raw client-supplied
// metadata a hostile or confused client would attach. SessionRef.Metadata is
// documented as validated client session metadata that is never proxy authority
// unless a separate trusted carrier establishes the field.
func fcmCallWithClientLookalikes() *aipapi.Call {
	call := fcmCall()
	call.Session.ClientSessionID = fcmClientClaimedSession
	call.Session.Metadata = map[string]string{
		fcmClientKeyRoot:      fcmClientClaimedRoot,
		fcmClientKeyPrincipal: fcmClientClaimedPrincipal,
		fcmClientKeySession:   fcmClientClaimedSession,
		fcmClientKeyLabel:     "client-declared-treatment",
	}
	return call
}

// fcmFinalizer is a completed-tool-call finalizer probe. With mutate set it
// writes every reference-typed field reachable through Meta in place, exactly as
// a misbehaving plugin could.
type fcmFinalizer struct {
	id     string
	order  int
	mutate bool

	mu                  sync.Mutex
	calls               int
	seen                fcmViews
	writesLanded        int
	traceIDsOK          bool
	attemptSeqPopulated bool
}

func (f *fcmFinalizer) ID() string { return f.id }

func (f *fcmFinalizer) Order() int { return f.order }

func (f *fcmFinalizer) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.seen = fcmDetachViews(meta.Scope, meta.Session, meta.Workspace)
	f.traceIDsOK = len(meta.TraceID) > 0 && len(meta.ALegID) > 0 && len(meta.BLegID) > 0
	f.attemptSeqPopulated = meta.AttemptSeq > 0
	if f.mutate {
		fcmWriteEveryReferenceField(&meta)
		f.writesLanded = fcmContainsSentinel(fcmDetachViews(meta.Scope, meta.Session, meta.Workspace))
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// fcmWriteEveryReferenceField writes the mutation sentinel in place through every
// reference-typed field reachable from Meta. Absent (nil or empty) payloads are
// skipped rather than allocated, so the probe never creates the state it later
// claims to have mutated.
func fcmWriteEveryReferenceField(meta *toolcall.Meta) {
	if meta.Scope.SafeClaims != nil {
		meta.Scope.SafeClaims[fcmMutationSentinel] = fcmMutationSentinel
	}
	if meta.Scope.PolicyLabels != nil {
		meta.Scope.PolicyLabels[fcmMutationSentinel] = fcmMutationSentinel
	}
	if len(meta.Scope.Roles) > 0 {
		meta.Scope.Roles[0] = fcmMutationSentinel
	}
	if meta.Session.Labels != nil {
		meta.Session.Labels[fcmMutationSentinel] = fcmMutationSentinel
	}
	if meta.Workspace.Labels != nil {
		meta.Workspace.Labels[fcmMutationSentinel] = fcmMutationSentinel
	}
	if len(meta.Workspace.Markers) > 0 {
		meta.Workspace.Markers[0] = fcmMutationSentinel
	}
}

// fcmMutableFieldCount counts the reference-typed payloads a view triple
// actually exposes. The mutating probe writes only into these, so a landed-write
// count that equals this number proves every writable field was reached.
func fcmMutableFieldCount(v fcmViews) int {
	n := 0
	for _, m := range []map[string]string{v.scope.SafeClaims, v.scope.PolicyLabels, v.session.Labels, v.workspace.Labels} {
		if m != nil {
			n++
		}
	}
	if len(v.scope.Roles) > 0 {
		n++
	}
	if len(v.workspace.Markers) > 0 {
		n++
	}
	return n
}

func (f *fcmFinalizer) snapshot() fcmFinalizerSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fcmFinalizerSnapshot{
		calls:               f.calls,
		seen:                f.seen,
		writesLanded:        f.writesLanded,
		traceIDsOK:          f.traceIDsOK,
		attemptSeqPopulated: f.attemptSeqPopulated,
	}
}

type fcmFinalizerSnapshot struct {
	calls               int
	seen                fcmViews
	writesLanded        int
	traceIDsOK          bool
	attemptSeqPopulated bool
}

// fcmObserver is one read-only probe of a sibling consumer plane.
type fcmObserver struct {
	mu       sync.Mutex
	calls    int
	recorded bool
	seen     fcmViews
}

func (o *fcmObserver) record(s scope.PrincipalScopeView, se session.SessionView, w aipworkspace.WorkspaceView) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if !o.recorded {
		o.seen = fcmDetachViews(s, se, w)
		o.recorded = true
	}
}

func (o *fcmObserver) snapshot() (int, fcmViews) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.seen
}

// fcmPolicyProbe observes the tool policy plane, which design.md ordering places
// at step 12, immediately after completed tool-call finalization at step 11.
type fcmPolicyProbe struct{ fcmObserver }

func (p *fcmPolicyProbe) ID() string { return "fcm-path-policy-probe" }

func (p *fcmPolicyProbe) Order() int { return 0 }

func (p *fcmPolicyProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (p *fcmPolicyProbe) Handle(
	_ context.Context,
	_ aipapi.ToolEvent,
	meta toolpolicy.Meta,
	_ toolpolicy.Services,
) (toolpolicy.Decision, error) {
	p.record(meta.Scope, meta.Session, meta.Workspace)
	return toolpolicy.DecisionAllow, nil
}

// fcmReactorProbe observes the tool reactor plane, the other design.md step-12
// consumer of the same authoritative request views.
type fcmReactorProbe struct{ fcmObserver }

func (r *fcmReactorProbe) ID() string { return "fcm-tool-reactor-probe" }

func (r *fcmReactorProbe) Order() int { return 0 }

func (r *fcmReactorProbe) HandleToolEvent(
	_ context.Context,
	ev aipapi.ToolEvent,
	meta sdkhooks.ToolMeta,
) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	r.record(meta.Scope, meta.Session, meta.Workspace)
	return sdkhooks.ToolPass, ev, nil
}

// fcmGateProbe observes the completion-gate plane, which runs after both
// tool-call finalization and tool policy/reactors and re-derives its views from
// the same frozen request facts. It therefore observes the producer snapshot.
type fcmGateProbe struct{ fcmObserver }

func (g *fcmGateProbe) ID() string { return "fcm-completion-gate-probe" }

func (g *fcmGateProbe) Order() int { return 0 }

func (g *fcmGateProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *fcmGateProbe) Handle(
	_ context.Context,
	meta completion.Meta,
	_ completion.Buffered,
	_ completion.Services,
) (completion.Outcome, error) {
	g.record(meta.Scope, meta.Session, meta.Workspace)
	return completion.PassOriginalOutcome(), nil
}

// fcmPreRequestProbe records whether the client lookalike metadata really was
// present on the request. Without it the client-metadata regression could pass
// simply because nothing client-supplied ever reached the runtime.
type fcmPreRequestProbe struct {
	mu            sync.Mutex
	calls         int
	clientMetaLen int
}

func (p *fcmPreRequestProbe) ID() string { return "fcm-client-metadata-probe" }

func (p *fcmPreRequestProbe) Order() int { return 0 }

func (p *fcmPreRequestProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (p *fcmPreRequestProbe) Handle(
	_ context.Context,
	call *aipapi.Call,
	_ prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.clientMetaLen = len(call.Session.Metadata)
	return prerequest.Decision{}, nil
}

func (p *fcmPreRequestProbe) snapshot() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.clientMetaLen
}

// fcmResolver publishes the authoritative workspace view the same way a
// production resolver would.
type fcmResolver struct{}

func (fcmResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{
		ID:          "synthetic-workspace-id",
		ProjectRoot: fcmAuthoritativeRoot,
		Markers:     []string{"proxy-resolver-marker"},
		Labels:      map[string]string{fcmWorkspaceLabelKey: fcmWorkspaceLabelValue},
	}, nil
}

// fcmFixture wires one secure-session executor whose response planes observe the
// same authoritative request views, from completed tool-call finalization
// outward.
type fcmFixture struct {
	ex         *runtime.Executor
	finalizer  *fcmFinalizer
	policy     *fcmPolicyProbe
	reactor    *fcmReactorProbe
	gate       *fcmGateProbe
	preRequest *fcmPreRequestProbe
}

func fcmNewFixture(t *testing.T) *fcmFixture {
	t.Helper()
	f := &fcmFixture{
		finalizer:  &fcmFinalizer{id: "fcm-finalizer-probe", order: 0},
		policy:     &fcmPolicyProbe{},
		reactor:    &fcmReactorProbe{},
		gate:       &fcmGateProbe{},
		preRequest: &fcmPreRequestProbe{},
	}
	var opens atomic.Int32
	backends := map[string]execbackend.Backend{
		fcmBackendID: recordingBackend(fcmBackendID, &opens, aipapi.NewFixedEventStream(fcmBackendEvents())),
	}
	ex, _ := interleavedSecureExecutor(t, backends)
	ex.InterleavedProcessor = nil
	ex.Bus = corehooks.New(corehooks.Config{ToolReactors: []sdkhooks.ToolReactor{f.reactor}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: fcmResolver{},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{f.preRequest},
			CompletionGates:    []completion.Gate{f.gate},
			ToolCallPolicies:   []toolpolicy.Policy{f.policy},
			ToolCallFinalizers: []toolcall.Finalizer{f.finalizer},
		}),
	})
	f.ex = ex
	return f
}

// run drives one request through the executor and drains the canonical stream.
func (f *fcmFixture) run(t *testing.T, call *aipapi.Call) {
	t.Helper()
	stream, err := f.ex.Execute(fcmAuthContext(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	tcrCollect(t, stream)
	_ = stream.Close()
}

// TestToolCallFinalizationMeta_SharesAuthoritativeRequestViewsWithToolPolicyAndReactor
// proves design.md "5. Completed Tool-Call Finalizer Metadata": the values a
// completed tool-call finalizer observes are the same authoritative request
// views tool policy and tool reactors observe for the same call, and the
// pre-existing trace identity fields are unchanged.
func TestToolCallFinalizationMeta_SharesAuthoritativeRequestViewsWithToolPolicyAndReactor(t *testing.T) {
	t.Parallel()

	f := fcmNewFixture(t)
	f.run(t, fcmCall())

	fin := f.finalizer.snapshot()
	policyCalls, policyViews := f.policy.snapshot()
	reactorCalls, reactorViews := f.reactor.snapshot()

	if fin.calls != 1 {
		t.Fatalf("completed tool call must reach tool-call finalization exactly once: invocations=%d", fin.calls)
	}
	if !fin.traceIDsOK {
		t.Error("pre-existing trace identity fields must stay populated alongside the additive views")
	}
	if !fin.attemptSeqPopulated {
		t.Error("the pre-existing attempt sequence field must stay populated alongside the additive views")
	}
	if policyCalls < 1 || reactorCalls < 1 {
		t.Fatalf("scaffolding guard: both sibling consumers must have run: policy_observations=%d reactor_observations=%d", policyCalls, reactorCalls)
	}
	if fin.seen.workspace.ProjectRoot != fcmAuthoritativeRoot {
		t.Fatalf("finalization metadata must carry the authoritative workspace view (workspace_root_len=%d)",
			len(fin.seen.workspace.ProjectRoot))
	}
	if !fcmViewsAgree(fin.seen, policyViews) {
		t.Error("finalization metadata and tool policy metadata must carry the same authoritative scope/session/workspace views by value")
	}
	if !fcmViewsAgree(fin.seen, reactorViews) {
		t.Error("finalization metadata and tool reactor metadata must carry the same authoritative scope/session/workspace views by value")
	}
}

// TestToolCallFinalizationMeta_DetachedFromProducerAndSiblingConsumers proves the
// ownership boundary the additive fields promise: every finalization call gets
// its own detached copy of the reference-typed payloads, so a finalizer that
// writes them in place cannot reach the request's producer snapshot or the tool
// policy / tool reactor / completion gate views derived from the same request
// facts.
func TestToolCallFinalizationMeta_DetachedFromProducerAndSiblingConsumers(t *testing.T) {
	t.Parallel()

	f := fcmNewFixture(t)
	f.finalizer.mutate = true
	f.run(t, fcmCall())

	fin := f.finalizer.snapshot()
	policyCalls, policyViews := f.policy.snapshot()
	reactorCalls, reactorViews := f.reactor.snapshot()
	gateCalls, gateViews := f.gate.snapshot()

	if fin.calls != 1 {
		t.Fatalf("completed tool call must reach tool-call finalization exactly once: invocations=%d", fin.calls)
	}
	// Non-vacuity guard for every detachment assertion below: each writable
	// reference-typed field the metadata exposes must really have received the
	// finalizer's in-place write.
	mutable := fcmMutableFieldCount(fin.seen)
	if mutable < 3 {
		t.Fatalf("fixture guard: finalization metadata must expose several writable payloads for this detachment test to mean anything: writable_payloads=%d", mutable)
	}
	if fin.writesLanded != mutable {
		t.Fatalf("non-vacuity guard: the finalizer's in-place writes must reach every payload it was handed: writable_payloads=%d sentinel_payloads=%d", mutable, fin.writesLanded)
	}
	if policyCalls < 1 || reactorCalls < 1 || gateCalls < 1 {
		t.Fatalf("scaffolding guard: every sibling consumer must have run: policy=%d reactor=%d gate=%d", policyCalls, reactorCalls, gateCalls)
	}

	for _, c := range []struct {
		name  string
		after fcmViews
	}{
		{name: "tool_policy", after: policyViews},
		{name: "tool_reactor", after: reactorViews},
		{name: "completion_gate", after: gateViews},
	} {
		if !fcmViewsAgree(fin.seen, c.after) {
			t.Errorf("%s: a finalizer's in-place writes must not change the views a sibling consumer observes (finalizer_scope_roles=%d finalizer_workspace_markers=%d after_scope_roles=%d after_workspace_markers=%d)",
				c.name, len(fin.seen.scope.Roles), len(fin.seen.workspace.Markers), len(c.after.scope.Roles), len(c.after.workspace.Markers))
		}
		if got := fcmContainsSentinel(c.after); got != 0 {
			t.Errorf("%s: a finalizer's in-place writes must not reach a sibling consumer's payloads (sentinel_payloads=%d)", c.name, got)
		}
	}

	// The producer snapshot itself is witnessed by the completion gate: it runs
	// after finalization and policy/reactors and re-derives its views from the
	// same frozen request facts, so a sentinel reaching it would mean the finalizer
	// had written into the snapshot every consumer shares. Equal values plus zero
	// sentinel payloads across all three consumers is what "detached, one request
	// state" means here; sharing a payload would have leaked the sentinel into at
	// least one of them.
}

// TestToolCallFinalizationMeta_ClientSuppliedMetadataNeverBecomesAuthority pins
// design.md "No client-provided raw metadata becomes authority" together with
// requirements 5.1 and 5.7: raw client session metadata that looks like a
// workspace root, a principal, a session id, or a policy label must not reach
// finalization metadata as authority. The client hint fields the views document
// as non-authoritative keep carrying the client value, which is the precise
// statement of "hint, never proof".
func TestToolCallFinalizationMeta_ClientSuppliedMetadataNeverBecomesAuthority(t *testing.T) {
	t.Parallel()

	f := fcmNewFixture(t)
	f.run(t, fcmCallWithClientLookalikes())

	preCalls, clientMetaLen := f.preRequest.snapshot()
	fin := f.finalizer.snapshot()

	if fin.calls != 1 {
		t.Fatalf("completed tool call must reach tool-call finalization exactly once: invocations=%d", fin.calls)
	}
	// Non-vacuity guard: the lookalikes really were on the request.
	if preCalls < 1 || clientMetaLen != 4 {
		t.Fatalf("client lookalike metadata must be present on the request for this regression to mean anything: pre_request_observations=%d client_metadata_keys=%d", preCalls, clientMetaLen)
	}

	// Authoritative workspace comes from the proxy-side resolver only.
	if fin.seen.workspace.ProjectRoot == fcmClientClaimedRoot {
		t.Error("client-declared workspace root must never become the finalizer's authoritative workspace view")
	}
	if fin.seen.workspace.ProjectRoot != fcmAuthoritativeRoot {
		t.Errorf("finalizer workspace root must come from the proxy-side resolver: authoritative_root_len=%d observed_root_len=%d matches=%t",
			len(fcmAuthoritativeRoot), len(fin.seen.workspace.ProjectRoot), fin.seen.workspace.ProjectRoot == fcmAuthoritativeRoot)
	}

	// Authoritative principal comes from authenticated transport context only.
	if fin.seen.scope.PrincipalID.String() == fcmClientClaimedPrincipal {
		t.Error("client-declared principal must never become the finalizer's authoritative scope principal")
	}
	if fin.seen.scope.PrincipalID.String() != fcmAuthenticatedPrincipal {
		t.Errorf("finalizer scope principal must come from authenticated transport context: authenticated_principal_len=%d observed_principal_len=%d matches=%t",
			len(fcmAuthenticatedPrincipal), len(fin.seen.scope.PrincipalID.String()),
			fin.seen.scope.PrincipalID.String() == fcmAuthenticatedPrincipal)
	}

	// The client session hint stays visible as the documented hint; the
	// authoritative session identity stays proxy-owned.
	if fin.seen.session.ClientSessionHint != fcmClientClaimedSession {
		t.Errorf("client session hint must remain visible as a non-authoritative hint: hint_len=%d matches=%t",
			len(fin.seen.session.ClientSessionHint), fin.seen.session.ClientSessionHint == fcmClientClaimedSession)
	}
	if fin.seen.session.AuthoritativeSessionID == fcmClientClaimedSession {
		t.Error("client-declared session id must never become the finalizer's authoritative session identity")
	}

	if got := fcmClientDeclaredLabelsInViews(fin.seen); got != 0 {
		t.Errorf("client-declared metadata keys must never surface as finalization view labels: offending_label_views=%d", got)
	}
	if got := fcmClientDeclaredValuesInViews(fin.seen); got != 0 {
		t.Errorf("no client-declared value may appear in finalization metadata: offending_views=%d", got)
	}
	// The proxy-published workspace label must still be present, proving the
	// assertion above is filtering client keys rather than demanding empty maps.
	if fin.seen.workspace.Labels[fcmWorkspaceLabelKey] != fcmWorkspaceLabelValue {
		t.Errorf("proxy-published workspace labels must reach finalization metadata: workspace_labels=%d", len(fin.seen.workspace.Labels))
	}
}

// fcmClientDeclaredLabelsInViews counts how many client-declared metadata keys
// appear as label keys in any of the three views.
func fcmClientDeclaredLabelsInViews(v fcmViews) int {
	keys := []string{fcmClientKeyRoot, fcmClientKeyPrincipal, fcmClientKeySession, fcmClientKeyLabel}
	labels := []map[string]string{v.scope.SafeClaims, v.scope.PolicyLabels, v.session.Labels, v.workspace.Labels}
	found := 0
	for _, m := range labels {
		for _, k := range keys {
			if _, ok := m[k]; ok {
				found++
			}
		}
	}
	return found
}

// fcmClientDeclaredValuesInViews counts how many client-declared scalar values
// appear anywhere in the three views.
func fcmClientDeclaredValuesInViews(v fcmViews) int {
	clientValues := []string{fcmClientClaimedRoot, fcmClientClaimedPrincipal, fcmClientClaimedSession}
	found := 0
	for _, want := range clientValues {
		if v.workspace.ProjectRoot == want || v.workspace.ID == want {
			found++
		}
		if v.scope.PrincipalID.String() == want {
			found++
		}
		if v.session.AuthoritativeSessionID == want {
			found++
		}
	}
	for _, m := range []map[string]string{v.scope.SafeClaims, v.scope.PolicyLabels, v.session.Labels, v.workspace.Labels} {
		for _, val := range m {
			for _, want := range clientValues {
				if val == want {
					found++
				}
			}
		}
	}
	return found
}
