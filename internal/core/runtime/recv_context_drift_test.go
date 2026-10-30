package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// driftFacts builds a recvTurnFacts with fully frozen request authority.
func driftFacts() recvTurnFacts {
	return testRecvTurnFacts(recvTurnFacts{
		baseline: aipapi.Call{Session: aipapi.SessionRef{ALegID: "a-1"}},
		traceID:  "trace-1",
		aLegID:   "a-1",
		recvViews: execctx.Views{
			Session: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "a-1"},
		},
		recvViewsOK:            true,
		routePrefs:             []string{"cand-a", "cand-b"},
		secureTurn:             execctx.SecureSessionTurn{SessionID: domain.SessionID("sess-1"), TurnID: domain.TurnID("turn-1")},
		secureTurnOK:           true,
		metering:               &checkpoint.RequestHolder{},
		requestAuth:            &requestAuthorityState{},
		billingCallID:          usageevidence.BillingCallID("billing-1"),
		billingCallState:       newBillingCallState(usageevidence.BillingCallID("billing-1")),
		billingAccountID:       "acct-1",
		billingIdentityStamped: true,
		nativeResolver:         &driftResolver{model: "frozen-model"},
	})
}

type driftResolver struct{ model string }

func (d *driftResolver) ResolveModelBinding(_, _ string) routing.ModelBinding {
	return routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: d.model}
}

// TestRecvContextDrift_BareContextDoesNotOverwriteFrozenFacts verifies that a
// Recv called with context.Background() still sees the frozen request facts.
func TestRecvContextDrift_BareContextDoesNotOverwriteFrozenFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	bare := context.Background()
	derived := facts.projectContext(bare, nil)

	views, ok := execctx.FromContext(derived)
	if !ok || views.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("views %+v ok=%v want sess-1", views, ok)
	}
	prefs := execctx.RouteCandidatePreferences(derived)
	if len(prefs) != 2 || prefs[0] != "cand-a" || prefs[1] != "cand-b" {
		t.Fatalf("prefs %v want [cand-a cand-b]", prefs)
	}
	st, ok := execctx.SecureSessionTurnFromContext(derived)
	if !ok || string(st.SessionID) != "sess-1" {
		t.Fatalf("secure turn %+v ok=%v", st, ok)
	}
}

// TestRecvContextDrift_ReplacementAfterGenerationRefresh ensures a generation
// refresh between initial open and retry does not change the frozen route prefs
// (proxy for bound model view). The frozen prefs must win.
func TestRecvContextDrift_ReplacementAfterGenerationRefresh(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	newPrefs := []string{"evil:model"}
	refreshCtx := execctx.WithRouteCandidatePreferences(context.Background(), newPrefs)
	derived := facts.projectContext(refreshCtx, nil)
	got := execctx.RouteCandidatePreferences(derived)
	if len(got) != 2 || got[0] != "cand-a" || got[1] != "cand-b" {
		t.Fatalf("generation refresh must not overwrite frozen prefs, got %v", got)
	}
}

// TestRecvContextDrift_AuxiliaryWithMissingAuthority ensures an auxiliary
// continuation that lacks request authority still sees the original authority
// via the frozen facts.
func TestRecvContextDrift_AuxiliaryWithMissingAuthority(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	auxCtx := context.Background()
	derived := facts.projectContext(auxCtx, nil)
	if _, ok := execctx.SecureSessionTurnFromContext(derived); !ok {
		t.Fatal("auxiliary ctx must still carry frozen secure turn")
	}
	if _, ok := execctx.FromContext(derived); !ok {
		t.Fatal("must have views")
	}
	if facts.billingCallID != "billing-1" {
		t.Fatalf("billing %q want billing-1", facts.billingCallID)
	}
}

// TestRecvContextDrift_ConflictingRoutePrefsDoesNotOverwriteFrozen ensures a
// caller context that carries conflicting route preferences or a different
// model resolver does not overwrite the frozen turn.
func TestRecvContextDrift_ConflictingRoutePrefsDoesNotOverwriteFrozen(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	conflictCtx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"evil:model"})
	conflictCtx = routing.WithNativeModelResolver(conflictCtx, &driftResolver{model: "evil-model"})
	derived := facts.projectContext(conflictCtx, nil)
	prefs := execctx.RouteCandidatePreferences(derived)
	if len(prefs) != 2 || prefs[0] != "cand-a" || prefs[1] != "cand-b" {
		t.Fatalf("prefs %v must be frozen, not evil", prefs)
	}
	resolver, ok := routing.NativeModelResolverFromContext(derived)
	if !ok {
		t.Fatal("must have resolver")
	}
	binding := resolver.ResolveModelBinding("any", "any")
	if binding.Native != "frozen-model" {
		t.Fatalf("resolver model %q want frozen-model", binding.Native)
	}
}

// TestRecvContextDrift_CancellationWithDeadlineStillHasBusinessFacts ensures a
// context that only carries a deadline/cancellation but no business metadata
// still gets the frozen business facts.
func TestRecvContextDrift_CancellationWithDeadlineStillHasBusinessFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	derived := facts.projectContext(ctx, nil)
	if _, ok := execctx.FromContext(derived); !ok {
		t.Fatal("must have views even on timeout ctx")
	}
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	derived2 := facts.projectContext(cancelled, nil)
	if _, ok := execctx.FromContext(derived2); !ok {
		t.Fatal("cancelled ctx must still get frozen views")
	}
}

// TestRecvContextDrift_ConcurrentGenerationReload ensures that a concurrent
// generation reload does not race the frozen turn's view.
func TestRecvContextDrift_ConcurrentGenerationReload(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	var wg sync.WaitGroup
	errCh := make(chan string, 10)
	for range 10 {
		wg.Go(func() {
			ctx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"evil:model"})
			derived := facts.projectContext(ctx, nil)
			got := execctx.RouteCandidatePreferences(derived)
			if len(got) != 2 || got[0] != "cand-a" || got[1] != "cand-b" {
				errCh <- "drift"
			}
		})
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}

// TestRecvContextDrift_ConflictingContextDoesNotOverrideFrozenFacts verifies that
// when a conflicting context (carrying different principal, scope, session, and workspace)
// is projected, the frozen facts win, and caller context values cannot overwrite them.
func TestRecvContextDrift_ConflictingContextDoesNotOverrideFrozenFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()

	// Build a conflicting context.
	conflictingScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("evil-principal"),
		Origin:      scope.OriginClient,
	}
	conflictingViews := execctx.Views{
		Principal: execview.PrincipalView{
			ID:     "evil-principal",
			Claims: map[string]string{"role": "evil-role"},
		},
		Scope: conflictingScope,
		Session: session.SessionView{
			AuthoritativeSessionID: "evil-sess-id",
		},
		Workspace: aipworkspace.WorkspaceView{
			ID: "evil-ws-id",
		},
	}
	conflictCtx := execctx.WithViews(context.Background(), conflictingViews)

	derived := facts.projectContext(conflictCtx, nil)
	gotViews, ok := execctx.FromContext(derived)
	if !ok {
		t.Fatal("views missing in derived context")
	}

	if gotViews.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("session overridden: got %q, want sess-1", gotViews.Session.AuthoritativeSessionID)
	}
	if gotViews.Principal.ID != "" { // driftFacts principal is empty/zero
		t.Fatalf("principal overridden: got %q, want empty", gotViews.Principal.ID)
	}
}

// TestRecvContextDrift_BoundViewsReloadPinning verifies that once views are captured
// at preparation, subsequent generation reloads do not affect the pinned registry, catalog,
// and native resolver in the projected context.
func TestRecvContextDrift_BoundViewsReloadPinning(t *testing.T) {
	t.Parallel()

	// Pre-captured views
	regView := modelregistry.EmptyBoundView()
	catView := modelcatalog.EmptyBoundView()
	resolver := &driftResolver{model: "frozen-model"}

	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		baseline:        aipapi.Call{Session: aipapi.SessionRef{ALegID: "a-1"}},
		traceID:         "trace-1",
		aLegID:          "a-1",
		boundRegistry:   regView,
		boundRegistryOK: true,
		boundCatalog:    catView,
		boundCatalogOK:  true,
		nativeResolver:  resolver,
	})

	// Simulate reloaded views in the caller context (representing generation reload)
	reloadedRegView := modelregistry.EmptyBoundView()
	reloadedCatView := modelcatalog.EmptyBoundView()
	reloadedResolver := &driftResolver{model: "reloaded-model"}

	reloadCtx := modelregistry.WithBoundView(context.Background(), reloadedRegView)
	reloadCtx = modelcatalog.WithBoundView(reloadCtx, reloadedCatView)
	reloadCtx = routing.WithNativeModelResolver(reloadCtx, reloadedResolver)

	derived := facts.projectContext(reloadCtx, nil)

	// Verify pinned resolver is used
	gotResolver, ok := routing.NativeModelResolverFromContext(derived)
	if !ok {
		t.Fatal("resolver missing in derived context")
	}
	binding := gotResolver.ResolveModelBinding("any", "any")
	if binding.Native != "frozen-model" {
		t.Fatalf("resolver re-derived after reload: got %q, want frozen-model", binding.Native)
	}
}

func TestRecvTurnFactsProjectContextMaskingSemantics(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		facts  recvTurnFacts
		logger *slog.Logger
		assert func(t *testing.T, derived context.Context, parent context.Context, facts recvTurnFacts, parentHolder *checkpoint.RequestHolder, parentAuth *requestAuthorityState, parentViews execctx.Views, parentResolver routing.NativeModelResolver)
	}

	parentHolder := &checkpoint.RequestHolder{}
	parentAuth := &requestAuthorityState{}
	parentViews := execctx.Views{
		Session: session.SessionView{AuthoritativeSessionID: "parent-sess-view", ALegID: "parent-a-leg"},
	}
	parentResolver := &driftResolver{model: "parent-model"}
	parentID := modelview.Derive(1, "parent-fp", "parent-reg", "parent-cat")

	buildParent := func() context.Context {
		ctx := context.Background()
		ctx = diag.WithCallDiag(ctx, "parent-trace", "parent-aleg")
		ctx = execctx.WithSecureSessionTurn(ctx, execctx.SecureSessionTurn{
			SessionID: domain.SessionID("parent-sess"),
			TurnID:    domain.TurnID("parent-turn"),
			Policy:    domain.PolicyMetadata{TranscriptEnabled: true},
		})
		ctx = execctx.WithRouteCandidatePreferences(ctx, []string{"parent-backend:parent-model"})
		ctx = modelregistry.WithBoundView(ctx, modelregistry.EmptyBoundView())
		ctx = modelcatalog.WithBoundView(ctx, modelcatalog.EmptyBoundView())
		ctx = modelview.WithIdentity(ctx, parentID)
		ctx = withMeteringHolder(ctx, parentHolder)
		ctx = withRequestAuthority(ctx, parentAuth)
		ctx = execctx.WithViews(ctx, parentViews)
		ctx = routing.WithNativeModelResolver(ctx, parentResolver)
		return ctx
	}

	frozenHolder := &checkpoint.RequestHolder{}
	frozenAuth := &requestAuthorityState{}
	frozenViews := execctx.Views{
		Session: session.SessionView{AuthoritativeSessionID: "frozen-sess-view", ALegID: "frozen-a-leg"},
	}
	frozenResolver := &driftResolver{model: "frozen-model"}
	frozenID := modelview.Derive(2, "frozen-fp", "frozen-reg", "frozen-cat")
	testLogger := slog.Default()

	tests := []testCase{
		{
			name:  "ZeroValueFacts_MasksAuthoritativeKeysAndPreservesPassiveParentKeys",
			facts: recvTurnFacts{},
			assert: func(t *testing.T, derived context.Context, parent context.Context, facts recvTurnFacts, parentHolder *checkpoint.RequestHolder, parentAuth *requestAuthorityState, parentViews execctx.Views, parentResolver routing.NativeModelResolver) {
				t.Helper()
				st, ok := execctx.SecureSessionTurnFromContext(derived)
				if !ok || st.SessionID != "" || st.TurnID != "" {
					t.Fatalf("secure turn must be overwritten with empty struct, got ok=%v, st=%+v", ok, st)
				}
				if _, ok := session.SecureTurnPolicyFromContext(derived); ok {
					t.Fatal("secure turn policy must be masked by WithoutSecureTurnPolicy")
				}

				if prefs := execctx.RouteCandidatePreferences(derived); len(prefs) != 0 {
					t.Fatalf("route prefs must be masked, got %v", prefs)
				}

				reg, ok := modelregistry.BoundViewFromContext(derived)
				if !ok || reg.Active() || reg.Generation() != "" {
					t.Fatalf("bound registry must be EmptyBoundView, got ok=%v, reg=%+v", ok, reg)
				}

				cat, ok := modelcatalog.BoundViewFromContext(derived)
				if !ok || cat.Active() || cat.Generation() != "" {
					t.Fatalf("bound catalog must be EmptyBoundView, got ok=%v, cat=%+v", ok, cat)
				}

				if id, ok := modelview.FromContext(derived); ok {
					t.Fatalf("model view identity must be masked, got ok=%v, id=%+v", ok, id)
				}

				if got := meteringHolderFrom(derived); got != parentHolder {
					t.Fatalf("metering holder must survive, got %v, want %v", got, parentHolder)
				}
				if got := requestAuthorityFrom(derived); got != parentAuth {
					t.Fatalf("request authority must survive, got %v, want %v", got, parentAuth)
				}

				gotViews, ok := execctx.FromContext(derived)
				if !ok || gotViews.Session.AuthoritativeSessionID != parentViews.Session.AuthoritativeSessionID {
					t.Fatalf("parent views must survive in projected context, got ok=%v, views=%+v", ok, gotViews)
				}
				vFor, ok := facts.viewsFor(parent)
				if ok {
					t.Fatalf("viewsFor on zero-value facts must not fall back to context, got %+v", vFor)
				}

				res, ok := routing.NativeModelResolverFromContext(derived)
				if !ok || res != parentResolver {
					t.Fatalf("parent native resolver must survive, got ok=%v, res=%v", ok, res)
				}

				if traceID := diag.TraceID(derived); traceID != "" {
					t.Fatalf("trace id must be overwritten with facts value, got %q", traceID)
				}
			},
		},
		{
			name: "PopulatedFacts_FrozenValuesWinOverParentContext",
			facts: recvTurnFacts{
				traceID: "frozen-trace",
				aLegID:  "frozen-aleg",
				secureTurn: execctx.SecureSessionTurn{
					SessionID: domain.SessionID("frozen-sess"),
					TurnID:    domain.TurnID("frozen-turn"),
					Policy:    domain.PolicyMetadata{TranscriptEnabled: true},
				},
				secureTurnOK:    true,
				routePrefs:      []string{"frozen-cand"},
				boundRegistry:   modelregistry.EmptyBoundView(),
				boundRegistryOK: true,
				boundCatalog:    modelcatalog.EmptyBoundView(),
				boundCatalogOK:  true,
				modelViewID:     frozenID,
				modelViewIDOK:   true,
				metering:        frozenHolder,
				requestAuth:     frozenAuth,
				recvViews:       frozenViews,
				recvViewsOK:     true,
				nativeResolver:  frozenResolver,
			},
			logger: testLogger,
			assert: func(t *testing.T, derived context.Context, parent context.Context, facts recvTurnFacts, parentHolder *checkpoint.RequestHolder, parentAuth *requestAuthorityState, parentViews execctx.Views, parentResolver routing.NativeModelResolver) {
				t.Helper()
				st, ok := execctx.SecureSessionTurnFromContext(derived)
				if !ok || st.SessionID != "frozen-sess" || st.TurnID != "frozen-turn" {
					t.Fatalf("frozen secure turn must win, got ok=%v, st=%+v", ok, st)
				}
				pol, ok := session.SecureTurnPolicyFromContext(derived)
				if !ok || !pol.TranscriptEnabled {
					t.Fatalf("frozen secure turn policy must win, got ok=%v, pol=%+v", ok, pol)
				}

				prefs := execctx.RouteCandidatePreferences(derived)
				if len(prefs) != 1 || prefs[0] != "frozen-cand" {
					t.Fatalf("frozen route prefs must win, got %v", prefs)
				}

				reg, ok := modelregistry.BoundViewFromContext(derived)
				if !ok || reg != facts.boundRegistry {
					t.Fatalf("frozen bound registry must win, got ok=%v, reg=%+v", ok, reg)
				}

				cat, ok := modelcatalog.BoundViewFromContext(derived)
				if !ok || cat != facts.boundCatalog {
					t.Fatalf("frozen bound catalog must win, got ok=%v, cat=%+v", ok, cat)
				}

				id, ok := modelview.FromContext(derived)
				if !ok || id != frozenID {
					t.Fatalf("frozen model view identity must win, got ok=%v, id=%+v", ok, id)
				}

				if got := meteringHolderFrom(derived); got != frozenHolder {
					t.Fatalf("frozen metering holder must win, got %v, want %v", got, frozenHolder)
				}
				if got := requestAuthorityFrom(derived); got != frozenAuth {
					t.Fatalf("frozen request authority must win, got %v, want %v", got, frozenAuth)
				}

				gotViews, ok := execctx.FromContext(derived)
				if !ok || gotViews.Session.AuthoritativeSessionID != "frozen-sess-view" {
					t.Fatalf("frozen views must win in projected context, got ok=%v, views=%+v", ok, gotViews)
				}
				vFor, ok := facts.viewsFor(parent)
				if !ok || vFor.Session.AuthoritativeSessionID != "frozen-sess-view" {
					t.Fatalf("viewsFor must prioritize frozen facts over parent context views, got ok=%v, vFor=%+v", ok, vFor)
				}

				res, ok := routing.NativeModelResolverFromContext(derived)
				if !ok || res != frozenResolver {
					t.Fatalf("frozen native resolver must win, got ok=%v, res=%v", ok, res)
				}

				if traceID := diag.TraceID(derived); traceID != "frozen-trace" {
					t.Fatalf("trace id must match frozen facts, got %q", traceID)
				}
				if aLegID := diag.ALegID(derived); aLegID != "frozen-aleg" {
					t.Fatalf("aleg id must match frozen facts, got %q", aLegID)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := buildParent()
			derived := tc.facts.projectContext(parent, tc.logger)
			tc.assert(t, derived, parent, tc.facts, parentHolder, parentAuth, parentViews, parentResolver)
		})
	}
}

func TestFrozenFacts_StartAttemptTx_IgnoresPoisonedContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "poison-start")
	if err != nil {
		t.Fatalf("a-leg: %v", err)
	}
	ex := TestExecutor()
	ex.Store = store

	frozenHolder := &checkpoint.RequestHolder{}
	frozenAuth := &requestAuthorityState{}
	frozenViews := execctx.Views{Scope: execctx.Views{}.Scope}
	frozenViewsOK := true

	poisonHolder := &checkpoint.RequestHolder{}
	poisonAuth := &requestAuthorityState{}
	poisonViews := execctx.Views{}
	poisonCtx := context.Background()
	poisonCtx = withMeteringHolder(poisonCtx, poisonHolder)
	poisonCtx = withRequestAuthority(poisonCtx, poisonAuth)
	poisonCtx = execctx.WithViews(poisonCtx, poisonViews)
	poisonCtx = routing.WithNativeModelResolver(poisonCtx, &driftResolver{model: "poison-model"})

	rf := requestFacts{
		recvTurnFacts: recvTurnFacts{
			traceID:          "trace-poison",
			aLegID:           aLeg.ALegID,
			baseline:         aipapi.Call{ID: "req-poison", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions}, Messages: testMinimalUserMessages()},
			recvViews:        frozenViews,
			recvViewsOK:      frozenViewsOK,
			metering:         frozenHolder,
			requestAuth:      frozenAuth,
			billingCallID:    "bc_cccccccccccccccccccccccccccccccc",
			billingCallState: newBillingCallState("bc_cccccccccccccccccccccccccccccccc"),
			nativeResolver:   &driftResolver{model: "frozen-model"},
		},
	}

	tx, err := ex.startAttemptTx(poisonCtx, rf, routeFacts{}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}, Key: "b:m"}, nil, nil)
	if err != nil {
		t.Fatalf("startAttemptTx: %v", err)
	}
	if tx.reqFacts.metering != frozenHolder {
		t.Fatalf("metering was overwritten by poisoned context: got %p want %p", tx.reqFacts.metering, frozenHolder)
	}
	if tx.reqFacts.requestAuth != frozenAuth {
		t.Fatalf("requestAuth was overwritten by poisoned context: got %p want %p", tx.reqFacts.requestAuth, frozenAuth)
	}
	if tx.reqFacts.recvViewsOK != frozenViewsOK {
		t.Fatalf("recvViewsOK changed by poisoned context")
	}
	pr, okPR := tx.reqFacts.nativeResolver.(*driftResolver)
	if !okPR || pr.model != "frozen-model" {
		t.Fatalf("native resolver was overwritten by poisoned context")
	}
	if tx.reqFacts.metering == poisonHolder || tx.reqFacts.requestAuth == poisonAuth {
		t.Fatalf("poison context leaked into tx facts")
	}
}

func TestFrozenFacts_BuildRoutePlan_UsesTypedResolver(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	frozenResolver := &driftResolver{model: "frozen-native"}
	prep := &preparedRequest{
		recvTurnFacts: recvTurnFacts{
			traceID:        "trace-build",
			aLegID:         "a-1",
			nativeResolver: frozenResolver,
		},
		call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "b:m"}},
		identity: &identityBoundTurn{
			traceID:   "trace-build",
			aLeg:      b2bua.ALegRecord{ALegID: "a-1"},
			routeAuth: routeAuthoritySnapshot{},
		},
	}
	poisonResolver := &driftResolver{model: "poison-native"}
	ctx := routing.WithNativeModelResolver(context.Background(), poisonResolver)
	plan, err := ex.buildRoutePlan(ctx, prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if plan.sel == nil {
		t.Fatalf("plan sel nil")
	}
	var found string
	for _, alt := range plan.sel.Alternatives {
		if alt.Primary != nil && alt.Primary.Backend == "b" {
			found = alt.Primary.NativeModel
			break
		}
	}
	if found != "frozen-native" {
		t.Fatalf("buildRoutePlan used poison resolver: got %q want frozen-native", found)
	}
}

func TestFrozenFacts_TurnTerminal_ConflictingContextDoesNotAlterSettlement(t *testing.T) {
	t.Parallel()

	t.Run("typed_unsettled_wins_over_poisoned_context_settled", func(t *testing.T) {
		t.Parallel()
		frozenAuth := &requestAuthorityState{Settled: false}
		poisonAuth := &requestAuthorityState{Settled: true}
		poisonCtx := withRequestAuthority(context.Background(), poisonAuth)

		term := newTurnTerminal()
		term.settleRequestAuthority = func(ctx context.Context, facts []metering.Fact) error {
			return errors.New("settle provider failure")
		}
		p := &responsePipeline{customer: newCustomerEvidenceAccumulator()}
		rf := requestTerminalFacts{traceID: "t-unsettled-wins", requestAuth: frozenAuth}

		_ = term.settleRequestAuthorityWithFrontendEgress(poisonCtx, aipapi.Event{}, rf, p)
		if !p.markCustomerSettled() {
			t.Fatal("expected customer to be unmarked settled for retry when typed requestAuth is unsettled")
		}
	})

	t.Run("typed_settled_wins_over_poisoned_context_unsettled", func(t *testing.T) {
		t.Parallel()
		frozenAuth := &requestAuthorityState{Settled: true}
		poisonAuth := &requestAuthorityState{Settled: false}
		poisonCtx := withRequestAuthority(context.Background(), poisonAuth)

		term := newTurnTerminal()
		term.settleRequestAuthority = func(ctx context.Context, facts []metering.Fact) error {
			return nil
		}
		p := &responsePipeline{customer: newCustomerEvidenceAccumulator()}
		rf := requestTerminalFacts{traceID: "t-settled-wins", requestAuth: frozenAuth}

		_ = term.settleRequestAuthorityWithFrontendEgress(poisonCtx, aipapi.Event{}, rf, p)
		if p.markCustomerSettled() {
			t.Fatal("expected customer to remain settled when typed requestAuth is settled, ignoring poisoned unsettled context")
		}
	})
}

func TestFrozenFacts_CustomerUsageReconstruction_IgnoresPoisonedMetering(t *testing.T) {
	t.Parallel()

	t.Run("typed_metering_wins_over_poisoned_context_metering", func(t *testing.T) {
		t.Parallel()
		frozenHolder := &checkpoint.RequestHolder{}
		_, err := frozenHolder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
			Call:         aipapi.Call{ID: "frozen-call-id"},
			CheckpointID: "fe-frozen",
			StreamID:     "fe-stream-frozen",
		})
		if err != nil {
			t.Fatalf("capture frozen frontend ingress: %v", err)
		}
		frozenHolder.MergeFrontendIngressQuantities([]metering.Quantity{
			{Component: metering.ComponentInputToken, Value: 42, Present: true},
		})

		poisonHolder := &checkpoint.RequestHolder{}
		_, err = poisonHolder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
			Call:         aipapi.Call{ID: "poison-call-id"},
			CheckpointID: "fe-poison",
			StreamID:     "fe-stream-poison",
		})
		if err != nil {
			t.Fatalf("capture poison frontend ingress: %v", err)
		}
		poisonHolder.MergeFrontendIngressQuantities([]metering.Quantity{
			{Component: metering.ComponentInputToken, Value: 999, Present: true},
		})

		poisonCtx := withMeteringHolder(context.Background(), poisonHolder)
		facts := recvTurnFacts{
			baseline: aipapi.Call{ID: "base-call-id"},
			metering: frozenHolder,
		}

		reconstructor := accountingstream.New(nil, accountingstream.Config{})
		events := []aipapi.Event{
			{Kind: aipapi.EventUsageDelta, OutputTokens: 10},
		}
		ev := reconstructCustomerUsageForResponse(poisonCtx, reconstructor, nil, facts, nil, "hello", events)
		if ev.InputTokens != 42 {
			t.Fatalf("expected input tokens from typed frozen metering (42), got %d (poison was 999)", ev.InputTokens)
		}
	})
}
