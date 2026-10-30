package extensions_test

import (
	"context"
	"slices"
	"sync"
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/stretchr/testify/assert"
)

// --- Stubs for Extension Planes in Pinned Concurrency Characterization Tests ---

type charPinnedSessionOpener struct{ id string }

func (s charPinnedSessionOpener) ID() string { return s.id }
func (charPinnedSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charPinnedToolCatalogFilter struct{ id string }

func (c charPinnedToolCatalogFilter) ID() string                      { return c.id }
func (charPinnedToolCatalogFilter) Order() int                        { return 0 }
func (charPinnedToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedToolCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charPinnedToolCallPolicy struct {
	id  string
	ord int
}

func (p charPinnedToolCallPolicy) ID() string                      { return p.id }
func (p charPinnedToolCallPolicy) Order() int                      { return p.ord }
func (charPinnedToolCallPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedToolCallPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charPinnedToolCallFinalizer struct {
	id  string
	ord int
}

func (f charPinnedToolCallFinalizer) ID() string { return f.id }
func (f charPinnedToolCallFinalizer) Order() int { return f.ord }
func (charPinnedToolCallFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charPinnedRequestTransform struct{ id string }

func (r charPinnedRequestTransform) ID() string                      { return r.id }
func (charPinnedRequestTransform) Order() int                        { return 0 }
func (charPinnedRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedRequestTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charPinnedPreRequestHandler struct {
	id  string
	ord int
}

func (p charPinnedPreRequestHandler) ID() string                      { return p.id }
func (p charPinnedPreRequestHandler) Order() int                      { return p.ord }
func (charPinnedPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedPreRequestHandler) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charPinnedRouteHintProvider struct{ id string }

func (rh charPinnedRouteHintProvider) ID() string                     { return rh.id }
func (charPinnedRouteHintProvider) Order() int                        { return 0 }
func (charPinnedRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedRouteHintProvider) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charPinnedCompletionGate struct{ id string }

func (g charPinnedCompletionGate) ID() string                      { return g.id }
func (charPinnedCompletionGate) Order() int                        { return 0 }
func (charPinnedCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPinnedCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charPinnedAttemptTransform struct {
	id  string
	ord int
}

func (a charPinnedAttemptTransform) ID() string                      { return a.id }
func (a charPinnedAttemptTransform) Order() int                      { return a.ord }
func (charPinnedAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charPinnedAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charPinnedStreamObserverFactory struct {
	id  string
	ord int
}

func (s charPinnedStreamObserverFactory) ID() string                      { return s.id }
func (s charPinnedStreamObserverFactory) Order() int                      { return s.ord }
func (charPinnedStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (charPinnedStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charPinnedTrafficRedactor struct{ id string }

func (tr charPinnedTrafficRedactor) ID() string { return tr.id }
func (charPinnedTrafficRedactor) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charPinnedCompactionObserver struct{ id string }

func (co charPinnedCompactionObserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type charPinnedCompactionPreserver struct{ id string }

func (cp charPinnedCompactionPreserver) ID() string { return cp.id }
func (charPinnedCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPinnedCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charPinnedCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charPinnedSecretGuard struct {
	id  string
	ord int
}

func (sg charPinnedSecretGuard) ID() string { return sg.id }
func (sg charPinnedSecretGuard) Order() int { return sg.ord }
func (charPinnedSecretGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}

func (charPinnedSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charPinnedLocalTurnHandler struct {
	id  string
	ord int
}

func (lt charPinnedLocalTurnHandler) ID() string { return lt.id }
func (lt charPinnedLocalTurnHandler) Order() int { return lt.ord }
func (charPinnedLocalTurnHandler) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (charPinnedLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (charPinnedLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "char-pinned-local-turn"}, nil
}

type charPinnedTerminalDecisionProvider struct{ id string }

func (p *charPinnedTerminalDecisionProvider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *charPinnedTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "char-done"}, nil
}

// buildPopulatedSnapshot creates a snapshot with all extension planes populated
// with at least two entries per plane (including sorting ties for materialized planes),
// and immediately mutates the retained source slices after snapshot construction (Finding 6).
func buildPopulatedSnapshot(gen int64, label string) *extensions.RequestRuntimeSnapshot {
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()

	// Retained source slices with extra capacity so append does not reallocate
	sourceSessionOpeners := make([]session.Opener, 0, 10)
	sourceSessionOpeners = append(sourceSessionOpeners,
		charPinnedSessionOpener{id: label + "-session-1"},
		charPinnedSessionOpener{id: label + "-session-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "plugin-"+label, sourceSessionOpeners)

	sourceCatalogFilters := make([]toolcatalog.Filter, 0, 10)
	sourceCatalogFilters = append(sourceCatalogFilters,
		charPinnedToolCatalogFilter{id: label + "-catalog-1"},
		charPinnedToolCatalogFilter{id: label + "-catalog-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCatalogFilters, "plugin-"+label, sourceCatalogFilters)

	// Materialized sorted: pol-c (30), pol-a (10), pol-b (10 tie) -> sorted: [pol-a, pol-b, pol-c]
	sourcePolicies := make([]toolpolicy.Policy, 0, 10)
	sourcePolicies = append(sourcePolicies,
		charPinnedToolCallPolicy{id: label + "-policy-c", ord: 30},
		charPinnedToolCallPolicy{id: label + "-policy-b", ord: 10},
		charPinnedToolCallPolicy{id: label + "-policy-a", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "plugin-"+label, sourcePolicies)

	sourceFinalizers := make([]toolcall.Finalizer, 0, 10)
	sourceFinalizers = append(sourceFinalizers,
		charPinnedToolCallFinalizer{id: label + "-finalizer-b", ord: 20},
		charPinnedToolCallFinalizer{id: label + "-finalizer-a", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "plugin-"+label, sourceFinalizers)

	sourceReqTransforms := make([]request.Transform, 0, 10)
	sourceReqTransforms = append(sourceReqTransforms,
		charPinnedRequestTransform{id: label + "-reqxform-1"},
		charPinnedRequestTransform{id: label + "-reqxform-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneRequestTransforms, "plugin-"+label, sourceReqTransforms)

	sourcePreReq := make([]prerequest.Handler, 0, 10)
	sourcePreReq = append(sourcePreReq,
		charPinnedPreRequestHandler{id: label + "-prereq-2", ord: 20},
		charPinnedPreRequestHandler{id: label + "-prereq-1", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlanePreRequestHandlers, "plugin-"+label, sourcePreReq)

	sourceRouteHints := make([]routehint.Provider, 0, 10)
	sourceRouteHints = append(sourceRouteHints,
		charPinnedRouteHintProvider{id: label + "-routehint-1"},
		charPinnedRouteHintProvider{id: label + "-routehint-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneRouteHintProviders, "plugin-"+label, sourceRouteHints)

	sourceGates := make([]completion.Gate, 0, 10)
	sourceGates = append(sourceGates,
		charPinnedCompletionGate{id: label + "-gate-1"},
		charPinnedCompletionGate{id: label + "-gate-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "plugin-"+label, sourceGates)

	sourceAttempt := make([]request.AttemptTransform, 0, 10)
	sourceAttempt = append(sourceAttempt,
		charPinnedAttemptTransform{id: label + "-attxform-2", ord: 20},
		charPinnedAttemptTransform{id: label + "-attxform-1", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneAttemptTransforms, "plugin-"+label, sourceAttempt)

	sourceStreamObs := make([]response.StreamObserverFactory, 0, 10)
	sourceStreamObs = append(sourceStreamObs,
		charPinnedStreamObserverFactory{id: label + "-streamobs-2", ord: 20},
		charPinnedStreamObserverFactory{id: label + "-streamobs-1", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneStreamObserverFactories, "plugin-"+label, sourceStreamObs)

	sourceRedactors := make([]sdktraffic.Redactor, 0, 10)
	sourceRedactors = append(sourceRedactors,
		charPinnedTrafficRedactor{id: label + "-redactor-b"},
		charPinnedTrafficRedactor{id: label + "-redactor-a"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneTrafficRedactors, "plugin-"+label, sourceRedactors)

	sourceCompObs := make([]compaction.Observer, 0, 10)
	sourceCompObs = append(sourceCompObs,
		charPinnedCompactionObserver{id: label + "-compobs-1"},
		charPinnedCompactionObserver{id: label + "-compobs-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompactionObservers, "plugin-"+label, sourceCompObs)

	sourceCompPres := make([]compaction.Preserver, 0, 10)
	sourceCompPres = append(sourceCompPres,
		charPinnedCompactionPreserver{id: label + "-comppres-1"},
		charPinnedCompactionPreserver{id: label + "-comppres-2"},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompactionPreservers, "plugin-"+label, sourceCompPres)

	sourceSecretGuards := make([]secretguard.Guard, 0, 10)
	sourceSecretGuards = append(sourceSecretGuards,
		charPinnedSecretGuard{id: label + "-sg-2", ord: 20},
		charPinnedSecretGuard{id: label + "-sg-1", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSecretGuards, "plugin-"+label, sourceSecretGuards)

	sourceLocalTurn := make([]localturn.Handler, 0, 10)
	sourceLocalTurn = append(sourceLocalTurn,
		charPinnedLocalTurnHandler{id: label + "-localturn-2", ord: 20},
		charPinnedLocalTurnHandler{id: label + "-localturn-1", ord: 10},
	)
	_ = aipfeature.Contribute(cset, aipfeature.PlaneLocalTurnHandlers, "plugin-"+label, sourceLocalTurn)

	_ = aipfeature.Contribute(cset, aipfeature.PlaneTerminalDecisionProvider, "plugin-"+label, terminaldecision.Provider(&charPinnedTerminalDecisionProvider{id: label + "-terminal"}))

	frozen := cset.Freeze()

	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Generation:    gen,
		FeaturePlanes: frozen,
		SecretGuardPlane: extensions.SecretGuardPlane{
			ConfigVersion:      label + "-sg-v",
			AccessMode:         "single_user",
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
	})

	// Immediate source slice mutations (replace, reorder, append within capacity) to prove isolation
	sourceSessionOpeners[0] = charPinnedSessionOpener{id: "mutated-session"}
	_ = append(sourceSessionOpeners, charPinnedSessionOpener{id: "leaked-session"})

	sourcePolicies[0] = charPinnedToolCallPolicy{id: "mutated-policy", ord: 999}
	sourcePolicies[1], sourcePolicies[2] = sourcePolicies[2], sourcePolicies[1]
	_ = append(sourcePolicies, charPinnedToolCallPolicy{id: "leaked-policy", ord: 1})

	sourceFinalizers[0] = charPinnedToolCallFinalizer{id: "mutated-finalizer", ord: 999}
	sourceReqTransforms[0] = charPinnedRequestTransform{id: "mutated-reqxform"}
	sourcePreReq[0] = charPinnedPreRequestHandler{id: "mutated-prereq", ord: 999}
	sourceRouteHints[0] = charPinnedRouteHintProvider{id: "mutated-routehint"}
	sourceGates[0] = charPinnedCompletionGate{id: "mutated-gate"}
	sourceAttempt[0] = charPinnedAttemptTransform{id: "mutated-attxform", ord: 999}
	sourceStreamObs[0] = charPinnedStreamObserverFactory{id: "mutated-streamobs", ord: 999}
	sourceRedactors[0] = charPinnedTrafficRedactor{id: "mutated-redactor"}
	sourceCompObs[0] = charPinnedCompactionObserver{id: "mutated-compobs"}
	sourceCompPres[0] = charPinnedCompactionPreserver{id: "mutated-comppres"}
	sourceSecretGuards[0] = charPinnedSecretGuard{id: "mutated-sg", ord: 999}
	sourceLocalTurn[0] = charPinnedLocalTurnHandler{id: "mutated-localturn", ord: 999}

	return snap
}

// assertSnapshotPlanesMatch asserts that every accessible plane on snap matches expectedGen and expectedLabel.
func assertSnapshotPlanesMatch(t *testing.T, snap *extensions.RequestRuntimeSnapshot, expectedGen int64, expectedLabel string) bool {
	t.Helper()
	if !assert.NotNil(t, snap) {
		return false
	}
	assert.Equal(t, expectedGen, snap.Generation())

	sess := snap.SessionOpeners()
	if assert.Len(t, sess, 2) {
		assert.Equal(t, expectedLabel+"-session-1", sess[0].ID())
		assert.Equal(t, expectedLabel+"-session-2", sess[1].ID())
	}

	cat := snap.ToolCatalogFilters()
	if assert.Len(t, cat, 2) {
		assert.Equal(t, expectedLabel+"-catalog-1", cat[0].ID())
		assert.Equal(t, expectedLabel+"-catalog-2", cat[1].ID())
	}

	pols := snap.ToolCallPolicies()
	if assert.Len(t, pols, 3) {
		assert.Equal(t, expectedLabel+"-policy-a", pols[0].ID())
		assert.Equal(t, expectedLabel+"-policy-b", pols[1].ID())
		assert.Equal(t, expectedLabel+"-policy-c", pols[2].ID())
	}

	polsExec := snap.ToolCallPoliciesExecution()
	if assert.Len(t, polsExec, 3) {
		assert.Equal(t, expectedLabel+"-policy-a", polsExec[0].ID())
		assert.Equal(t, expectedLabel+"-policy-b", polsExec[1].ID())
		assert.Equal(t, expectedLabel+"-policy-c", polsExec[2].ID())
	}

	fins := snap.ToolCallFinalizers()
	if assert.Len(t, fins, 2) {
		assert.Equal(t, expectedLabel+"-finalizer-a", fins[0].ID())
		assert.Equal(t, expectedLabel+"-finalizer-b", fins[1].ID())
	}

	finsExec := snap.ToolCallFinalizersExecution()
	if assert.Len(t, finsExec, 2) {
		assert.Equal(t, expectedLabel+"-finalizer-a", finsExec[0].ID())
		assert.Equal(t, expectedLabel+"-finalizer-b", finsExec[1].ID())
	}

	rx := snap.RequestTransforms()
	if assert.Len(t, rx, 2) {
		assert.Equal(t, expectedLabel+"-reqxform-1", rx[0].ID())
		assert.Equal(t, expectedLabel+"-reqxform-2", rx[1].ID())
	}

	pr := snap.PreRequestHandlers()
	if assert.Len(t, pr, 2) {
		assert.Equal(t, expectedLabel+"-prereq-1", pr[0].ID())
		assert.Equal(t, expectedLabel+"-prereq-2", pr[1].ID())
	}

	rh := snap.RouteHintProviders()
	if assert.Len(t, rh, 2) {
		assert.Equal(t, expectedLabel+"-routehint-1", rh[0].ID())
		assert.Equal(t, expectedLabel+"-routehint-2", rh[1].ID())
	}

	cg := snap.CompletionGates()
	if assert.Len(t, cg, 2) {
		assert.Equal(t, expectedLabel+"-gate-1", cg[0].ID())
		assert.Equal(t, expectedLabel+"-gate-2", cg[1].ID())
	}

	at := snap.AttemptTransforms()
	if assert.Len(t, at, 2) {
		assert.Equal(t, expectedLabel+"-attxform-1", at[0].ID())
		assert.Equal(t, expectedLabel+"-attxform-2", at[1].ID())
	}

	so := snap.StreamObserverFactories()
	if assert.Len(t, so, 2) {
		assert.Equal(t, expectedLabel+"-streamobs-1", so[0].ID())
		assert.Equal(t, expectedLabel+"-streamobs-2", so[1].ID())
	}

	tr := snap.TrafficRedactors()
	if assert.Len(t, tr, 2) {
		assert.Equal(t, expectedLabel+"-redactor-a", tr[0].ID())
		assert.Equal(t, expectedLabel+"-redactor-b", tr[1].ID())
	}

	co := snap.CompactionObservers()
	assert.Len(t, co, 2)

	cp := snap.CompactionPreservers()
	if assert.Len(t, cp, 2) {
		assert.Equal(t, expectedLabel+"-comppres-1", cp[0].ID())
		assert.Equal(t, expectedLabel+"-comppres-2", cp[1].ID())
	}

	sg := snap.SecretGuardPlane()
	assert.Equal(t, expectedLabel+"-sg-v", sg.ConfigVersion)
	if assert.Len(t, sg.Guards, 2) {
		assert.Equal(t, expectedLabel+"-sg-1", sg.Guards[0].ID())
		assert.Equal(t, expectedLabel+"-sg-2", sg.Guards[1].ID())
	}

	sgExec := snap.SecretGuardExecutionPlane()
	assert.Equal(t, expectedLabel+"-sg-v", sgExec.ConfigVersion)
	if assert.Len(t, sgExec.Guards, 2) {
		assert.Equal(t, expectedLabel+"-sg-1", sgExec.Guards[0].ID())
		assert.Equal(t, expectedLabel+"-sg-2", sgExec.Guards[1].ID())
	}

	lt := snap.LocalTurnHandlers()
	if assert.Len(t, lt, 2) {
		assert.Equal(t, expectedLabel+"-localturn-1", lt[0].ID())
		assert.Equal(t, expectedLabel+"-localturn-2", lt[1].ID())
	}

	ltExec := snap.LocalTurnHandlersExecution()
	if assert.Len(t, ltExec, 2) {
		assert.Equal(t, expectedLabel+"-localturn-1", ltExec[0].ID())
		assert.Equal(t, expectedLabel+"-localturn-2", ltExec[1].ID())
	}

	td := snap.TerminalDecisionProvider()
	if assert.NotNil(t, td) {
		assert.Equal(t, expectedLabel+"-terminal", td.ID())
	}
	return !t.Failed()
}

// TestRequestRuntimeSnapshot_PublishPinned_ConcurrentIsolation pins Requirement 1.5:
//   - When Request 1 acquires a snapshot pinned from Generation 1, and Generation 2 publishes
//     concurrently at a controlled barrier, Request 1 continues observing Generation 1's frozen
//     extension surface end-to-end across multiple execution stages without drift, while new
//     requests concurrently see Generation 2.
func TestRequestRuntimeSnapshot_PublishPinned_ConcurrentIsolation(t *testing.T) {
	t.Parallel()

	// 1. Prepare Gen 1 snapshot
	snapGen1 := buildPopulatedSnapshot(1, "gen1")

	// Shared atomic pointer representing the active/published snapshot in the system
	var activeSnapshot sync.Map
	activeSnapshot.Store("current", snapGen1)

	// Synchronization barriers (no sleeps)
	req1Started := make(chan struct{})
	req1Stage1Done := make(chan struct{})
	gen2Published := make(chan struct{})
	req1Completed := make(chan struct{})
	req2Completed := make(chan struct{})

	// 2. In-flight Request 1 goroutine
	go func() {
		defer func() {
			close(req1Completed)
		}()

		// Acquire snapshot from Generation 1 and bind to request context
		rawSnap, loaded := activeSnapshot.Load("current")
		if !loaded {
			t.Error("generation 1 snapshot missing from publisher store")
			return
		}
		snap1, ok := rawSnap.(*extensions.RequestRuntimeSnapshot)
		if !ok {
			t.Errorf("unexpected snapshot type %T", rawSnap)
			return
		}
		reqCtx1 := extensions.WithRequestRuntimeSnapshot(context.Background(), snap1)

		close(req1Started)

		// Stage 1: Verify Gen 1 planes before candidate publish
		observedSnap1 := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
		assertSnapshotPlanesMatch(t, observedSnap1, 1, "gen1")

		close(req1Stage1Done)

		// Wait until Generation 2 is published
		<-gen2Published

		// Stage 2: In-flight request continues executing AFTER Gen 2 publication.
		// Invariant: The pinned request context MUST still observe Generation 1's frozen surface.
		observedSnap2 := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
		assertSnapshotPlanesMatch(t, observedSnap2, 1, "gen1")

		// Stage 3: Verify execution slice accessors (hot-path non-cloning methods)
		if len(observedSnap2.ToolCallPoliciesExecution()) > 0 {
			assert.Equal(t, "gen1-policy-a", observedSnap2.ToolCallPoliciesExecution()[0].ID())
		}
		if len(observedSnap2.ToolCallFinalizersExecution()) > 0 {
			assert.Equal(t, "gen1-finalizer-a", observedSnap2.ToolCallFinalizersExecution()[0].ID())
		}
		if len(observedSnap2.SecretGuardExecutionPlane().Guards) > 0 {
			assert.Equal(t, "gen1-sg-1", observedSnap2.SecretGuardExecutionPlane().Guards[0].ID())
		}
		if len(observedSnap2.LocalTurnHandlersExecution()) > 0 {
			assert.Equal(t, "gen1-localturn-1", observedSnap2.LocalTurnHandlersExecution()[0].ID())
		}

		// Stage 4: Defensive copy isolation — mutations to returned slices must not corrupt the snapshot
		clonedPolicies := observedSnap2.ToolCallPolicies()
		if len(clonedPolicies) > 0 {
			clonedPolicies[0] = charPinnedToolCallPolicy{id: "caller-mutated", ord: 999}
			recheckedPolicies := observedSnap2.ToolCallPolicies()
			if len(recheckedPolicies) > 0 {
				assert.Equal(t, "gen1-policy-a", recheckedPolicies[0].ID(), "snapshot must be isolated from caller slice mutations")
			}
		}
	}()

	// 3. Publisher goroutine: publishes Generation 2 concurrently
	go func() {
		defer func() {
			close(gen2Published)
		}()

		<-req1Stage1Done

		// Build and publish Generation 2
		snapGen2 := buildPopulatedSnapshot(2, "gen2")
		activeSnapshot.Store("current", snapGen2)
	}()

	// 4. Request 2 goroutine: starts AFTER Gen 2 publication and sees Gen 2
	go func() {
		defer func() {
			close(req2Completed)
		}()

		<-gen2Published

		rawSnap, loaded := activeSnapshot.Load("current")
		if !loaded {
			t.Error("generation 2 snapshot missing from publisher store")
			return
		}
		snap2, ok := rawSnap.(*extensions.RequestRuntimeSnapshot)
		if !ok {
			t.Errorf("unexpected snapshot type %T", rawSnap)
			return
		}
		reqCtx2 := extensions.WithRequestRuntimeSnapshot(context.Background(), snap2)

		observedSnap2 := extensions.RequestRuntimeSnapshotFromContext(reqCtx2)
		assertSnapshotPlanesMatch(t, observedSnap2, 2, "gen2")
	}()

	// Wait for all goroutines to complete
	<-req1Started
	<-req1Completed
	<-req2Completed
}

// TestRequestRuntimeSnapshot_FrozenGeneration_RepeatedPublishStability pins Requirement 1.5:
//   - Proves that repeated publishes (Gen 2 .. Gen 20) never live-rebind an already-pinned
//     snapshot; the pinned observer asserts frozen-set stability throughout across all
//     interleaving points.
func TestRequestRuntimeSnapshot_FrozenGeneration_RepeatedPublishStability(t *testing.T) {
	t.Parallel()

	snapGen1 := buildPopulatedSnapshot(1, "gen1")
	reqCtx1 := extensions.WithRequestRuntimeSnapshot(context.Background(), snapGen1)

	var activeSnapshot sync.Map
	activeSnapshot.Store("current", snapGen1)

	const iterations = 20
	var wg sync.WaitGroup

	// Publisher loop
	wg.Go(func() {
		for i := 2; i <= iterations; i++ {
			snapGenI := buildPopulatedSnapshot(int64(i), "gen-candidate")
			activeSnapshot.Store("current", snapGenI)
		}
	})

	// Pinned observer loop running concurrently
	wg.Go(func() {
		for range iterations * 5 {
			snap := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
			assertSnapshotPlanesMatch(t, snap, 1, "gen1")

			// Also verify defensive copies returned in tight loop maintain integrity
			copiedCat := snap.ToolCatalogFilters()
			if len(copiedCat) > 0 {
				_ = slices.Clone(copiedCat)
			}
		}
	})

	wg.Wait()

	// Final stability check on pinned snapshot after all publisher iterations finish
	finalSnap := extensions.RequestRuntimeSnapshotFromContext(reqCtx1)
	assertSnapshotPlanesMatch(t, finalSnap, 1, "gen1")
}
