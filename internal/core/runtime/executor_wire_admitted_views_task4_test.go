package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/testkit/execviewfixture"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Task 4 RED: wire execution must hand its authoritative bound session to
// shared routing. Today ExecuteLargeBody builds empty views tied to principal
// presence (recvViewsOK = pOK), so a fresh session is lost before route
// planning: ignore policy silently drops affinity, fail-closed reports
// missing identity for a session that was actually created.

type task4AssessorBackend struct {
	compatible bool
	universal  bool
}

func (b task4AssessorBackend) ResolveWireRequest(_ context.Context, _ largebody.WireRequestFacts, _ routing.AttemptCandidate) largebody.WireRequestSupport {
	return largebody.WireRequestSupport{Compatible: b.compatible}
}

func (b task4AssessorBackend) ResolveWireDomain(_ context.Context, _ largebody.WireDomainFacts) largebody.WireDomainSupport {
	return largebody.WireDomainSupport{Compatible: b.compatible, AnyAcceptedModel: b.universal}
}

func task4EligibleAssessor(t *testing.T, genID string) *ProductionLargeBodyAssessor {
	t.Helper()
	census := largebody.NewStandardDependencyCensus(genID)
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	wb := task4AssessorBackend{compatible: true, universal: true}
	resolver := largebody.WireBackendMap{"test-backend": wb}
	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"test-backend",
		map[string]struct{}{"test-backend": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"test-backend",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		resolver,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(nil, validator, resolver)
	wireGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, resolver)
	// Candidate-domain generation must match setupTestExecutor's live
	// LargeBodyCandidateDomainGeneration ("dom-gen-1") or execution rejects
	// the stamp as drifted before routing is reached.
	return NewProductionLargeBodyAssessor(genID, "dom-gen-1", authGate, wireGate, StandardLaneDomainPolicies())
}

func task4FreshProof(src *testSource) largebody.Proof {
	return largebody.Proof{
		ProfileID:       "openresponses_v1",
		Operation:       aipapi.OperationOpenResponsesCreate,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 64,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest(sha256.Sum256([]byte("wire-task4-identity"))),
		Source:    src.digest,
		BodyBytes: src.size,
		Session:   largebody.SessionInput{NewSessionRequested: true},
	}
}

func task4Accept(t *testing.T, assessor *ProductionLargeBodyAssessor, ctx context.Context, proof largebody.Proof) largebody.Assessment { //nolint:revive // test helper keeps t first
	t.Helper()
	accepted, err := assessor.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("AssessLargeBody: %v", err)
	}
	if accepted.Decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("production assessment must accept fresh proof, got decision=%v reason=%v", accepted.Decision, accepted.Reason)
	}
	if !accepted.WireDomain.UniversalModel {
		t.Fatal("assessed domain must be universal so the sticky live override stays inside the envelope")
	}
	return accepted
}

func task4AffinityExecutor(t *testing.T, policy affinity.MissingIdentityPolicy, overrideSelector string) (*Executor, map[string]int, affinity.Store) {
	t.Helper()
	ex, _, _ := setupTestExecutor(t)
	affStore := memorystore.New()
	ex.AffinityStore = affStore
	ex.AffinityMissingIdentity = policy
	ex.Rand = routing.NewSeededRng(0)
	opens := map[string]int{}
	backends := map[string]execbackend.Backend{}
	for _, name := range []string{"wire-a", "wire-b"} {
		backends[name] = execbackend.Backend{
			OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens[name]++
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: name},
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		}
	}
	ex.Backends = backends
	if overrideSelector != "" {
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(context.Context, string) (routeoverride.State, error) {
				return routeoverride.State{Active: true, Selector: overrideSelector}, nil
			},
		}
	}
	return ex, opens, affStore
}

func task4PrincipalCtx() context.Context {
	return execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "wire-task4-principal"})
}

func task4Collect(t *testing.T, res largebody.ExecutionResult) {
	t.Helper()
	if _, err := aipapi.Collect(context.Background(), res.Stream); err != nil {
		t.Fatalf("collect wire stream: %v", err)
	}
	if err := res.Stream.Close(); err != nil {
		t.Fatalf("close wire stream: %v", err)
	}
}

func task4OpenedBackend(opens map[string]int) string {
	for name, n := range opens {
		if n > 0 {
			return name
		}
	}
	return ""
}

// TestTask4_WireFreshSessionAffinityIgnorePolicy proves Requirement 3.2 under
// the ignore policy: the created authoritative session must be the observed
// affinity key and drive backend selection across a resumed turn. RED today:
// no binding is recorded because the bound session never reaches routing.
func TestTask4_WireFreshSessionAffinityIgnorePolicy(t *testing.T) {
	const stickySelector = "{session_sticky}wire-a:m|wire-b:m"
	ex, opens, affStore := task4AffinityExecutor(t, affinity.MissingIdentityIgnore, stickySelector)
	assessor := task4EligibleAssessor(t, "gen-1")
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"affinity-ignore"}]}`)

	ctx := largebody.WithWireIdentity(task4PrincipalCtx(), "wire-task4-req-ignore-1", "wire-task4-trace-ignore-1")
	accepted := task4Accept(t, assessor, ctx, task4FreshProof(src))
	res, err := ex.ExecuteLargeBody(ctx, accepted, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody: %v", err)
	}
	if res.Session.AuthoritativeSessionID == "" {
		t.Fatal("expected a created authoritative session")
	}
	task4Collect(t, res)

	opened := task4OpenedBackend(opens)
	if opened == "" {
		t.Fatal("expected exactly one backend open on turn 1")
	}
	wantKey := affinity.Key{Scope: affinity.ScopeSession, ID: res.Session.AuthoritativeSessionID}
	binding, ok, err := affStore.Get(context.Background(), wantKey)
	if err != nil {
		t.Fatalf("affinity store get: %v", err)
	}
	if !ok {
		t.Fatalf("missing affinity binding for created session %q: fresh-session handoff lost (ignore policy)", wantKey.ID)
	}
	if binding.BackendID != opened {
		t.Fatalf("binding backend = %q, opened backend = %q", binding.BackendID, opened)
	}

	// Turn 2 resumes the same authoritative session and must stick to it.
	for k := range opens {
		opens[k] = 0
	}
	ctx2 := largebody.WithWireIdentity(task4PrincipalCtx(), "wire-task4-req-ignore-2", "wire-task4-trace-ignore-2")
	ctx2 = largebody.WithWireSessionInput(ctx2, largebody.SessionInput{
		AuthoritativeSessionID: res.Session.AuthoritativeSessionID,
		ResumeToken:            largebody.NewSensitiveString(res.Session.ResumeToken.Reveal()),
	})
	accepted2 := task4Accept(t, assessor, ctx2, task4FreshProof(src))
	res2, err := ex.ExecuteLargeBody(ctx2, accepted2, src)
	if err != nil {
		t.Fatalf("resumed ExecuteLargeBody: %v", err)
	}
	task4Collect(t, res2)
	if got := task4OpenedBackend(opens); got != opened {
		t.Fatalf("resumed turn opened %q, want sticky backend %q", got, opened)
	}
}

// TestTask4_WireFreshSessionAffinityFailClosed proves Requirement 3.2 under
// the fail-closed policy: a freshly created session must satisfy affinity,
// not report missing identity. RED today: ErrIdentityRequired because the
// bound session never reaches routing.
func TestTask4_WireFreshSessionAffinityFailClosed(t *testing.T) {
	const stickySelector = "{session_sticky}wire-a:m|wire-b:m"
	ex, opens, affStore := task4AffinityExecutor(t, affinity.MissingIdentityFailClosed, stickySelector)
	assessor := task4EligibleAssessor(t, "gen-1")
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"affinity-fail-closed"}]}`)

	ctx := largebody.WithWireIdentity(task4PrincipalCtx(), "wire-task4-req-closed-1", "wire-task4-trace-closed-1")
	accepted := task4Accept(t, assessor, ctx, task4FreshProof(src))
	res, err := ex.ExecuteLargeBody(ctx, accepted, src)
	if err != nil {
		if errors.Is(err, affinity.ErrIdentityRequired) || strings.Contains(err.Error(), "affinity identity") {
			t.Fatalf("fail-closed rejected a freshly created session as missing identity (lost handoff): %v", err)
		}
		t.Fatalf("ExecuteLargeBody: %v", err)
	}
	task4Collect(t, res)
	wantKey := affinity.Key{Scope: affinity.ScopeSession, ID: res.Session.AuthoritativeSessionID}
	if _, ok, err := affStore.Get(context.Background(), wantKey); err != nil || !ok {
		t.Fatalf("missing affinity binding for created session %q under fail-closed (err=%v ok=%v)", wantKey.ID, err, ok)
	}
	if total := opens["wire-a"] + opens["wire-b"]; total != 1 {
		t.Fatalf("expected exactly one backend open, got %+v", opens)
	}
}

// TestTask4_WireGenuineAbsenceKeepsPolicy proves Requirement 3.3: when the
// trusted affinity identity is genuinely absent (no authenticated principal),
// the request never reaches backend selection under either policy and no
// identity is synthesized — secure-session preparation denies first with the
// existing authorization error. This control passes both before and after
// the handoff fix, distinguishing genuine absence (denied before routing)
// from a lost handoff (fail-closed despite a created session, or a dropped
// binding under ignore).
func TestTask4_WireGenuineAbsenceKeepsPolicy(t *testing.T) {
	const stickySelector = "{session_sticky}wire-a:m|wire-b:m"

	for _, policy := range []affinity.MissingIdentityPolicy{affinity.MissingIdentityFailClosed, affinity.MissingIdentityIgnore} {
		t.Run(string(policy), func(t *testing.T) {
			ex, opens, affStore := task4AffinityExecutor(t, policy, stickySelector)
			assessor := task4EligibleAssessor(t, "gen-1")
			src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"genuine-absent-policy"}}`)
			// No principal: genuinely absent identity, never fabricated
			// from body fields.
			ctx := largebody.WithWireIdentity(context.Background(), "wire-task4-req-absent-policy", "wire-task4-trace-absent-policy")
			accepted := task4Accept(t, assessor, ctx, task4FreshProof(src))
			_, err := ex.ExecuteLargeBody(ctx, accepted, src)
			if err == nil {
				t.Fatalf("genuinely absent identity must not execute (policy %q)", policy)
			}
			if total := opens["wire-a"] + opens["wire-b"]; total != 0 {
				t.Fatalf("genuine absence must open zero backends (policy %q), got %+v", policy, opens)
			}
			// No binding may appear for an empty session key: absence stays
			// absent rather than fabricated.
			if _, ok, gerr := affStore.Get(context.Background(), affinity.Key{Scope: affinity.ScopeSession, ID: ""}); gerr != nil || ok {
				t.Fatalf("empty session key must never bind (policy %q, err=%v ok=%v)", policy, gerr, ok)
			}
		})
	}
}

// TestTask4_WireAdmittedViewsFrozenIndependentOfPrincipal proves Requirements
// 3.1 and 3.4 at the freeze boundary: kicking the views through
// newRecvTurnFacts keeps principal, scope, bound session, and workspace
// available even with no principal, and the frozen snapshot survives bare,
// conflicting, replacement, sibling, and parallel projections without reading
// new request facts.
//
// Conflict is seeded through the parent aggregate itself, including the
// existing execviewfixture.PoisonedParent: every asserted field carries a
// frozen value distinguishable from its poisoned counterpart, in both the
// execctx aggregate and the public SDK projections. A principal seeded beside
// the aggregate (execview.WithPrincipal wrapped by execctx.WithViews) would be
// erased by WithViews before projection and prove nothing.
func TestTask4_WireAdmittedViewsFrozenIndependentOfPrincipal(t *testing.T) {
	t.Parallel()
	bound := session.SessionView{
		AuthoritativeSessionID: "wire-task4-authoritative-session",
		ClientSessionHint:      "wire-task4-client-hint",
		ALegID:                 "wire-task4-a-leg",
		TurnID:                 "wire-task4-turn",
		IsNew:                  true,
		Labels:                 map[string]string{"wire-task4-label": "wire-task4-value"},
	}
	resolvedScope := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("wire-task4-principal"),
		SafeClaims:  map[string]string{"wire_task4": "frozen"},
	}
	workspaceView := aipworkspace.WorkspaceView{ID: "wire-task4-workspace", ProjectRoot: "/wire-task4-root"}

	withPrincipal := &PreparedSecureSession{
		principal:    execview.PrincipalView{ID: "wire-task4-principal"},
		hasPrincipal: true,
		scope:        resolvedScope,
		wsView:       workspaceView,
	}
	withoutPrincipal := &PreparedSecureSession{wsView: workspaceView}

	principalViews := wireAdmittedViews(bound, withPrincipal)
	if principalViews.Session.AuthoritativeSessionID != bound.AuthoritativeSessionID {
		t.Fatalf("principal views session = %q, want bound %q", principalViews.Session.AuthoritativeSessionID, bound.AuthoritativeSessionID)
	}
	if principalViews.Principal.ID != "wire-task4-principal" {
		t.Fatalf("principal views principal = %q, want resolved principal", principalViews.Principal.ID)
	}
	if got := principalViews.Scope.PrincipalID.String(); got != "wire-task4-principal" {
		t.Fatalf("principal views scope principal = %q, want resolved scope (Scope handoff omitted?)", got)
	}
	if principalViews.Scope.SubjectKind != scope.SubjectHuman || principalViews.Scope.Origin != scope.OriginClient {
		t.Fatalf("principal views scope = %+v, want frozen human/client scope", principalViews.Scope)
	}
	if principalViews.Scope.SafeClaims["wire_task4"] != "frozen" {
		t.Fatalf("principal views scope claims = %v, want frozen claims", principalViews.Scope.SafeClaims)
	}
	if principalViews.Workspace.ID != "wire-task4-workspace" || principalViews.Workspace.ProjectRoot != "/wire-task4-root" {
		t.Fatalf("principal views workspace = %+v, want frozen workspace", principalViews.Workspace)
	}

	// The bound session view stays valid with no principal at all.
	bareViews := wireAdmittedViews(bound, withoutPrincipal)
	if bareViews.Session.AuthoritativeSessionID != bound.AuthoritativeSessionID {
		t.Fatalf("principal-less views session = %q, want bound %q", bareViews.Session.AuthoritativeSessionID, bound.AuthoritativeSessionID)
	}
	if bareViews.Workspace.ID != "wire-task4-workspace" {
		t.Fatalf("principal-less views workspace = %q, want frozen workspace", bareViews.Workspace.ID)
	}

	// Freeze the COMPLETE view: principal, scope, session, and workspace are
	// all populated, each distinguishable from the poisoned parents below.
	frozen := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		traceID:     "wire-task4-trace",
		aLegID:      "wire-task4-a-leg",
		recvViews:   principalViews,
		recvViewsOK: true,
	})
	if !frozen.recvViewsOK {
		t.Fatal("frozen views must stay available without depending on principal presence")
	}

	assertFrozenViews := func(t *testing.T, what string, ctx context.Context) {
		t.Helper()
		got, ok := execctx.FromContext(ctx)
		if !ok {
			t.Fatalf("%s lost frozen views", what)
		}
		if got.Principal.ID != "wire-task4-principal" {
			t.Fatalf("%s principal = %q, want frozen wire-task4-principal", what, got.Principal.ID)
		}
		if sid := got.Scope.PrincipalID.String(); sid != "wire-task4-principal" {
			t.Fatalf("%s scope principal = %q, want frozen wire-task4-principal", what, sid)
		}
		if got.Scope.SubjectKind != scope.SubjectHuman {
			t.Fatalf("%s scope subject = %q, want frozen human", what, got.Scope.SubjectKind)
		}
		if got.Scope.SafeClaims["wire_task4"] != "frozen" {
			t.Fatalf("%s scope claims = %v, want frozen claims", what, got.Scope.SafeClaims)
		}
		if got.Session.AuthoritativeSessionID != bound.AuthoritativeSessionID {
			t.Fatalf("%s session = %q, want frozen %q", what, got.Session.AuthoritativeSessionID, bound.AuthoritativeSessionID)
		}
		if got.Workspace.ID != "wire-task4-workspace" || got.Workspace.ProjectRoot != "/wire-task4-root" {
			t.Fatalf("%s workspace = %+v, want frozen workspace", what, got.Workspace)
		}
		// Public SDK projections carry the same frozen snapshot.
		if p, ok := execview.PrincipalFromContext(ctx); !ok || p.ID != "wire-task4-principal" {
			t.Fatalf("%s SDK principal = %+v ok=%v, want frozen principal", what, p, ok)
		}
		if s, ok := scope.ScopeFromContext(ctx); !ok || s.PrincipalID.String() != "wire-task4-principal" || s.SubjectKind != scope.SubjectHuman {
			t.Fatalf("%s SDK scope = %+v ok=%v, want frozen scope", what, s, ok)
		}
		if sv, ok := session.SessionViewFromContext(ctx); !ok || sv.AuthoritativeSessionID != bound.AuthoritativeSessionID {
			t.Fatalf("%s SDK session = %+v ok=%v, want frozen session", what, sv, ok)
		}
		if wv, ok := aipworkspace.WorkspaceViewFromContext(ctx); !ok || wv.ID != "wire-task4-workspace" || wv.ProjectRoot != "/wire-task4-root" {
			t.Fatalf("%s SDK workspace = %+v ok=%v, want frozen workspace", what, wv, ok)
		}
	}

	// Bare context: frozen facts project without any live request state.
	assertFrozenViews(t, "bare projection", frozen.projectContext(context.Background(), nil))

	// Conflicting parent: every view carries distinguishable poison through
	// the aggregate itself, so only the frozen snapshot may survive.
	poisonCtx := execctx.WithViews(context.Background(), execviewfixture.PoisonedParent("wire-task4-poison-a-leg"))
	assertFrozenViews(t, "conflicting-parent projection", frozen.projectContext(poisonCtx, nil))

	// Replacement context: conflicting live views seeded in the aggregate
	// (not beside it) must not move the frozen snapshot.
	replacementCtx := execctx.WithViews(context.Background(), execctx.Views{
		Principal: execview.PrincipalView{ID: "wire-task4-replacement-principal"},
		Scope: scope.PrincipalScopeView{
			Origin:      scope.OriginInternal,
			SubjectKind: scope.SubjectService,
			PrincipalID: scope.Known("wire-task4-replacement-principal"),
			SafeClaims:  map[string]string{"wire_task4": "replacement"},
		},
		Session:   session.SessionView{AuthoritativeSessionID: "wire-task4-replacement-session"},
		Workspace: aipworkspace.WorkspaceView{ID: "wire-task4-replacement-workspace", ProjectRoot: "/replacement-only"},
	})
	assertFrozenViews(t, "replacement projection", frozen.projectContext(replacementCtx, nil))

	// Sibling projection: a second poisoned lineage still observes the same
	// frozen snapshot.
	siblingCtx := execctx.WithViews(context.Background(), execviewfixture.PoisonedParent("wire-task4-sibling-a-leg"))
	assertFrozenViews(t, "sibling projection", frozen.projectContext(siblingCtx, nil))

	// Principal-less freeze stays available: session and workspace survive
	// with no principal at all.
	bareFrozen := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		traceID:     "wire-task4-trace-principal-less",
		aLegID:      "wire-task4-a-leg",
		recvViews:   bareViews,
		recvViewsOK: true,
	})
	if !bareFrozen.recvViewsOK {
		t.Fatal("principal-less frozen views must stay available")
	}
	projected := bareFrozen.projectContext(context.Background(), nil)
	got, ok := execctx.FromContext(projected)
	if !ok {
		t.Fatal("principal-less bare context lost frozen views")
	}
	if got.Session.AuthoritativeSessionID != bound.AuthoritativeSessionID {
		t.Fatalf("principal-less bare projection session = %q, want frozen %q", got.Session.AuthoritativeSessionID, bound.AuthoritativeSessionID)
	}
	if got.Workspace.ID != "wire-task4-workspace" {
		t.Fatalf("principal-less bare projection workspace = %q, want frozen workspace", got.Workspace.ID)
	}
	if got.Principal.ID != "" {
		t.Fatalf("principal-less bare projection principal = %q, want empty", got.Principal.ID)
	}

	// Parallel projection: two projections are independent clones.
	first := frozen.projectContext(context.Background(), nil)
	second := frozen.projectContext(context.Background(), nil)
	assertFrozenViews(t, "parallel first projection", first)
	assertFrozenViews(t, "parallel second projection", second)
	mut, ok := execctx.FromContext(first)
	if !ok {
		t.Fatal("first parallel projection lost views")
	}
	mut.Session.Labels["wire-task4-label"] = "wire-task4-mutated"
	check, ok := execctx.FromContext(second)
	if !ok {
		t.Fatal("second parallel projection lost views")
	}
	if check.Session.Labels["wire-task4-label"] != "wire-task4-value" {
		t.Fatalf("parallel projections share mutable state: %q", check.Session.Labels["wire-task4-label"])
	}
	if frozen.recvViews.Session.Labels["wire-task4-label"] != "wire-task4-value" {
		t.Fatalf("projection mutated frozen facts: %q", frozen.recvViews.Session.Labels["wire-task4-label"])
	}
}
