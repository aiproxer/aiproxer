package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Stubs with configurable ID and Order ---

type testPolicyStub struct {
	id  string
	ord int
}

func (p testPolicyStub) ID() string                      { return p.id }
func (p testPolicyStub) Order() int                      { return p.ord }
func (testPolicyStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPolicyStub) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type testFinalizerStub struct {
	id  string
	ord int
}

func (f testFinalizerStub) ID() string { return f.id }
func (f testFinalizerStub) Order() int { return f.ord }
func (testFinalizerStub) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type testPreReqStub struct {
	id  string
	ord int
}

func (p testPreReqStub) ID() string                      { return p.id }
func (p testPreReqStub) Order() int                      { return p.ord }
func (testPreReqStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPreReqStub) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type testAttemptStub struct {
	id  string
	ord int
}

func (a testAttemptStub) ID() string                      { return a.id }
func (a testAttemptStub) Order() int                      { return a.ord }
func (testAttemptStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (testAttemptStub) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type testStreamObsStub struct {
	id  string
	ord int
}

func (s testStreamObsStub) ID() string                      { return s.id }
func (s testStreamObsStub) Order() int                      { return s.ord }
func (testStreamObsStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testStreamObsStub) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type testRedactorStub struct{ id string }

func (r testRedactorStub) ID() string { return r.id }
func (testRedactorStub) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type testGuardStub struct {
	id  string
	ord int
}

func (g testGuardStub) ID() string                         { return g.id }
func (g testGuardStub) Order() int                         { return g.ord }
func (testGuardStub) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (testGuardStub) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type testLocalTurnStub struct {
	id  string
	ord int
}

func (l testLocalTurnStub) ID() string                      { return l.id }
func (l testLocalTurnStub) Order() int                      { return l.ord }
func (testLocalTurnStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (testLocalTurnStub) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (testLocalTurnStub) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "test"}, nil
}

type testSessionOpenerStub struct{ id string }

func (s testSessionOpenerStub) ID() string { return s.id }
func (testSessionOpenerStub) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type testTransformStub struct{ id string }

func (t testTransformStub) ID() string                      { return t.id }
func (t testTransformStub) Order() int                      { return 0 }
func (testTransformStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testTransformStub) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type testRouteHintStub struct{ id string }

func (r testRouteHintStub) ID() string                      { return r.id }
func (testRouteHintStub) Order() int                        { return 0 }
func (testRouteHintStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testRouteHintStub) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type testGateStub struct{ id string }

func (g testGateStub) ID() string                      { return g.id }
func (testGateStub) Order() int                        { return 0 }
func (testGateStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testGateStub) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type testCatFilterStub struct{ id string }

func (c testCatFilterStub) ID() string                      { return c.id }
func (testCatFilterStub) Order() int                        { return 0 }
func (testCatFilterStub) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testCatFilterStub) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type testCompactionObserverStub struct{ id string }

func (o testCompactionObserverStub) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type testCompactionPreserverStub struct{ id string }

func (p testCompactionPreserverStub) ID() string { return p.id }
func (testCompactionPreserverStub) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserverStub) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserverStub) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

// TestSnapshot_FrozenMaterialization_ExactOrdering verifies that when a snapshot is
// constructed with FeaturePlanes containing deliberately unsorted entries (with ties),
// all materialized planes are sorted accurately and all source-order planes preserve source order.
func TestSnapshot_FrozenMaterialization_ExactOrdering(t *testing.T) {
	t.Parallel()

	cset := aipfeature.NewContributionSet()

	// 1. Tool Call Policies (sorted by Order, then ID, then stable tie-breaker)
	p3 := testPolicyStub{id: "pol-c", ord: 30}
	p1 := testPolicyStub{id: "pol-a", ord: 10}
	p2b := testPolicyStub{id: "pol-b2", ord: 20}
	p2a := testPolicyStub{id: "pol-b1", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "p1", []toolpolicy.Policy{p3, p1, p2b, p2a}))

	// 2. Tool Call Finalizers (sorted by Order, then ID)
	f3 := testFinalizerStub{id: "fin-z", ord: 50}
	f1 := testFinalizerStub{id: "fin-a", ord: 10}
	f2 := testFinalizerStub{id: "fin-m", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "p1", []toolcall.Finalizer{f3, f1, f2}))

	// 3. Pre-Request Handlers (sorted by Order, then ID)
	pr3 := testPreReqStub{id: "pre-3", ord: 300}
	pr1 := testPreReqStub{id: "pre-1", ord: 100}
	pr2 := testPreReqStub{id: "pre-2", ord: 200}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlanePreRequestHandlers, "p1", []prerequest.Handler{pr3, pr1, pr2}))

	// 4. Attempt Transforms (sorted by Order, then ID)
	at3 := testAttemptStub{id: "att-3", ord: 30}
	at1 := testAttemptStub{id: "att-1", ord: 10}
	at2 := testAttemptStub{id: "att-2", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneAttemptTransforms, "p1", []request.AttemptTransform{at3, at1, at2}))

	// 5. Stream Observer Factories (sorted by Order, then ID)
	so3 := testStreamObsStub{id: "so-3", ord: 30}
	so1 := testStreamObsStub{id: "so-1", ord: 10}
	so2 := testStreamObsStub{id: "so-2", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneStreamObserverFactories, "p1", []response.StreamObserverFactory{so3, so1, so2}))

	// 6. Traffic Redactors (sorted by ID)
	tr3 := testRedactorStub{id: "red-z"}
	tr1 := testRedactorStub{id: "red-a"}
	tr2 := testRedactorStub{id: "red-m"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneTrafficRedactors, "p1", []sdktraffic.Redactor{tr3, tr1, tr2}))

	// 7. Secret Guards (sorted by Order, then ID)
	sg3 := testGuardStub{id: "sg-3", ord: 30}
	sg1 := testGuardStub{id: "sg-1", ord: 10}
	sg2 := testGuardStub{id: "sg-2", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneSecretGuards, "p1", []secretguard.Guard{sg3, sg1, sg2}))

	// 8. Local Turn Handlers (sorted by Order, then ID)
	lt3 := testLocalTurnStub{id: "lt-3", ord: 30}
	lt1 := testLocalTurnStub{id: "lt-1", ord: 10}
	lt2 := testLocalTurnStub{id: "lt-2", ord: 20}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneLocalTurnHandlers, "p1", []localturn.Handler{lt3, lt1, lt2}))

	// Source-order planes (order MUST be preserved: 3, 1, 2)
	s3 := testSessionOpenerStub{id: "sess-3"}
	s1 := testSessionOpenerStub{id: "sess-1"}
	s2 := testSessionOpenerStub{id: "sess-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "p1", []session.Opener{s3, s1, s2}))

	rt3 := testTransformStub{id: "rt-3"}
	rt1 := testTransformStub{id: "rt-1"}
	rt2 := testTransformStub{id: "rt-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneRequestTransforms, "p1", []request.Transform{rt3, rt1, rt2}))

	rh3 := testRouteHintStub{id: "rh-3"}
	rh1 := testRouteHintStub{id: "rh-1"}
	rh2 := testRouteHintStub{id: "rh-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneRouteHintProviders, "p1", []routehint.Provider{rh3, rh1, rh2}))

	g3 := testGateStub{id: "g-3"}
	g1 := testGateStub{id: "g-1"}
	g2 := testGateStub{id: "g-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "p1", []completion.Gate{g3, g1, g2}))

	cp3 := testCompactionPreserverStub{id: "cp-3"}
	cp1 := testCompactionPreserverStub{id: "cp-1"}
	cp2 := testCompactionPreserverStub{id: "cp-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneCompactionPreservers, "p1", []compaction.Preserver{cp3, cp1, cp2}))

	cf3 := testCatFilterStub{id: "cf-3"}
	cf1 := testCatFilterStub{id: "cf-1"}
	cf2 := testCatFilterStub{id: "cf-2"}
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneToolCatalogFilters, "p1", []toolcatalog.Filter{cf3, cf1, cf2}))

	frozen := cset.Freeze()

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: frozen,
	})
	require.NotNil(t, snap)

	// Assert sorted materialization order:
	// Tool policies: [pol-a, pol-b1, pol-b2, pol-c]
	pols := snap.ToolCallPolicies()
	require.Len(t, pols, 4)
	assert.Equal(t, "pol-a", pols[0].ID())
	assert.Equal(t, "pol-b1", pols[1].ID())
	assert.Equal(t, "pol-b2", pols[2].ID())
	assert.Equal(t, "pol-c", pols[3].ID())

	polsExec := snap.ToolCallPoliciesExecution()
	require.Len(t, polsExec, 4)
	assert.Equal(t, "pol-a", polsExec[0].ID())
	assert.Equal(t, "pol-b1", polsExec[1].ID())
	assert.Equal(t, "pol-b2", polsExec[2].ID())
	assert.Equal(t, "pol-c", polsExec[3].ID())

	// Tool finalizers: [fin-a, fin-m, fin-z]
	fins := snap.ToolCallFinalizers()
	require.Len(t, fins, 3)
	assert.Equal(t, "fin-a", fins[0].ID())
	assert.Equal(t, "fin-m", fins[1].ID())
	assert.Equal(t, "fin-z", fins[2].ID())

	finsExec := snap.ToolCallFinalizersExecution()
	require.Len(t, finsExec, 3)
	assert.Equal(t, "fin-a", finsExec[0].ID())
	assert.Equal(t, "fin-m", finsExec[1].ID())
	assert.Equal(t, "fin-z", finsExec[2].ID())

	// Pre-request handlers: [pre-1, pre-2, pre-3]
	pr := snap.PreRequestHandlers()
	require.Len(t, pr, 3)
	assert.Equal(t, "pre-1", pr[0].ID())
	assert.Equal(t, "pre-2", pr[1].ID())
	assert.Equal(t, "pre-3", pr[2].ID())

	// Attempt transforms: [att-1, att-2, att-3]
	at := snap.AttemptTransforms()
	require.Len(t, at, 3)
	assert.Equal(t, "att-1", at[0].ID())
	assert.Equal(t, "att-2", at[1].ID())
	assert.Equal(t, "att-3", at[2].ID())

	// Stream observers: [so-1, so-2, so-3]
	so := snap.StreamObserverFactories()
	require.Len(t, so, 3)
	assert.Equal(t, "so-1", so[0].ID())
	assert.Equal(t, "so-2", so[1].ID())
	assert.Equal(t, "so-3", so[2].ID())

	// Traffic redactors: [red-a, red-m, red-z]
	tr := snap.TrafficRedactors()
	require.Len(t, tr, 3)
	assert.Equal(t, "red-a", tr[0].ID())
	assert.Equal(t, "red-m", tr[1].ID())
	assert.Equal(t, "red-z", tr[2].ID())

	// Secret guards: [sg-1, sg-2, sg-3]
	sg := snap.SecretGuardPlane().Guards
	require.Len(t, sg, 3)
	assert.Equal(t, "sg-1", sg[0].ID())
	assert.Equal(t, "sg-2", sg[1].ID())
	assert.Equal(t, "sg-3", sg[2].ID())

	sgExec := snap.SecretGuardExecutionPlane().Guards
	require.Len(t, sgExec, 3)
	assert.Equal(t, "sg-1", sgExec[0].ID())
	assert.Equal(t, "sg-2", sgExec[1].ID())
	assert.Equal(t, "sg-3", sgExec[2].ID())

	// Local turn handlers: [lt-1, lt-2, lt-3]
	lt := snap.LocalTurnHandlers()
	require.Len(t, lt, 3)
	assert.Equal(t, "lt-1", lt[0].ID())
	assert.Equal(t, "lt-2", lt[1].ID())
	assert.Equal(t, "lt-3", lt[2].ID())

	ltExec := snap.LocalTurnHandlersExecution()
	require.Len(t, ltExec, 3)
	assert.Equal(t, "lt-1", ltExec[0].ID())
	assert.Equal(t, "lt-2", ltExec[1].ID())
	assert.Equal(t, "lt-3", ltExec[2].ID())

	// Assert source-order planes: [3, 1, 2]
	sess := snap.SessionOpeners()
	require.Len(t, sess, 3)
	assert.Equal(t, "sess-3", sess[0].ID())
	assert.Equal(t, "sess-1", sess[1].ID())
	assert.Equal(t, "sess-2", sess[2].ID())

	rx := snap.RequestTransforms()
	require.Len(t, rx, 3)
	assert.Equal(t, "rt-3", rx[0].ID())
	assert.Equal(t, "rt-1", rx[1].ID())
	assert.Equal(t, "rt-2", rx[2].ID())

	hints := snap.RouteHintProviders()
	require.Len(t, hints, 3)
	assert.Equal(t, "rh-3", hints[0].ID())
	assert.Equal(t, "rh-1", hints[1].ID())
	assert.Equal(t, "rh-2", hints[2].ID())

	gates := snap.CompletionGates()
	require.Len(t, gates, 3)
	assert.Equal(t, "g-3", gates[0].ID())
	assert.Equal(t, "g-1", gates[1].ID())
	assert.Equal(t, "g-2", gates[2].ID())

	pres := snap.CompactionPreservers()
	require.Len(t, pres, 3)
	assert.Equal(t, "cp-3", pres[0].ID())
	assert.Equal(t, "cp-1", pres[1].ID())
	assert.Equal(t, "cp-2", pres[2].ID())

	cat := snap.ToolCatalogFilters()
	require.Len(t, cat, 3)
	assert.Equal(t, "cf-3", cat[0].ID())
	assert.Equal(t, "cf-1", cat[1].ID())
	assert.Equal(t, "cf-2", cat[2].ID())
}

var (
	sinkPolicies   []toolpolicy.Policy
	sinkFinalizers []toolcall.Finalizer
	sinkGuards     []secretguard.Guard
	sinkHandlers   []localturn.Handler
)

// TestSnapshot_ExecutionAccessors_ZeroAllocations asserts that all four narrow execution
// accessors perform zero heap allocations on the hot path (Finding 4).
func TestSnapshot_ExecutionAccessors_ZeroAllocations(t *testing.T) { //nolint:paralleltest // uses package-level sink variables for AllocsPerRun
	cset := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "p1", []toolpolicy.Policy{
		testPolicyStub{id: "p1", ord: 1},
		testPolicyStub{id: "p2", ord: 2},
	}))
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "p1", []toolcall.Finalizer{
		testFinalizerStub{id: "f1", ord: 1},
	}))
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneSecretGuards, "p1", []secretguard.Guard{
		testGuardStub{id: "g1", ord: 1},
	}))
	require.NoError(t, aipfeature.Contribute(cset, aipfeature.PlaneLocalTurnHandlers, "p1", []localturn.Handler{
		testLocalTurnStub{id: "l1", ord: 1},
	}))

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})

	allocsPolicies := testing.AllocsPerRun(1000, func() {
		sinkPolicies = snap.ToolCallPoliciesExecution()
	})
	assert.Equal(t, float64(0), allocsPolicies, "ToolCallPoliciesExecution must have 0 allocations")

	allocsFinalizers := testing.AllocsPerRun(1000, func() {
		sinkFinalizers = snap.ToolCallFinalizersExecution()
	})
	assert.Equal(t, float64(0), allocsFinalizers, "ToolCallFinalizersExecution must have 0 allocations")

	allocsGuards := testing.AllocsPerRun(1000, func() {
		sinkGuards = snap.SecretGuardsExecution()
	})
	assert.Equal(t, float64(0), allocsGuards, "SecretGuardsExecution must have 0 allocations")

	allocsHandlers := testing.AllocsPerRun(1000, func() {
		sinkHandlers = snap.LocalTurnHandlersExecution()
	})
	assert.Equal(t, float64(0), allocsHandlers, "LocalTurnHandlersExecution must have 0 allocations")
}
