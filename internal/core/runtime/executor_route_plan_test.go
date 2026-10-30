package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func primaryBackend(sel *routing.Selector) string {
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Primary == nil {
		return ""
	}
	return sel.Alternatives[0].Primary.Backend
}

func testPreparedRequest(traceID string, call aipapi.Call, aLeg b2bua.ALegRecord, recvViews execctx.Views, recvViewsOK bool) *preparedRequest {
	if traceID == "" {
		traceID = "trace-test"
	}
	if aLeg.ALegID == "" {
		aLeg.ALegID = "a-test"
	}
	call.Session.ALegID = aLeg.ALegID
	preSession := session.SessionView{
		ALegID: aLeg.ALegID,
	}
	ibt, err := newIdentityBoundTurn(
		traceID,
		&call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		aLeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		panic(err)
	}
	return &preparedRequest{
		identity: ibt,
		call:     ibt.call,
		recvTurnFacts: recvTurnFacts{
			recvViews:   recvViews,
			recvViewsOK: recvViewsOK,
		},
	}
}

func TestBuildRoutePlan_selectorAliasRewritesBeforeParse(t *testing.T) {
	t.Parallel()
	ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^alias$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.SelectorAliases = ar
	ex.DefaultBackend = "backendA"
	prep := testPreparedRequest("trace-1", aipapi.Call{Route: aipapi.RouteIntent{Selector: "alias"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got := primaryBackend(plan.sel); got != "backendB" {
		t.Fatalf("alias rewrite: want backendB, got %q", got)
	}
}

func TestBuildRoutePlan_modelOnlyAppliesDefaultBackend(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.DefaultBackend = "defaultBE"
	prep := testPreparedRequest("trace-1", aipapi.Call{Route: aipapi.RouteIntent{Selector: "gpt-4"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got := primaryBackend(plan.sel); got != "defaultBE" {
		t.Fatalf("model-only default: want defaultBE, got %q", got)
	}
}

func TestBuildRoutePlan_unresolvedModelOnlyFails(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	prep := testPreparedRequest("trace-1", aipapi.Call{Route: aipapi.RouteIntent{Selector: "gpt-4"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	_, err := ex.buildRoutePlan(context.Background(), prep)
	if err == nil || !errors.Is(err, aipapi.ErrUnresolvedModelOnlySelector) {
		t.Fatalf("want ErrUnresolvedModelOnlySelector, got %v", err)
	}
}

func TestBuildRoutePlan_affinityIdentityError(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.AffinityMissingIdentity = affinity.MissingIdentityFailClosed
	prep := testPreparedRequest("trace-1", aipapi.Call{Route: aipapi.RouteIntent{Selector: "{affinity=session}backendA:model-x"}}, b2bua.ALegRecord{}, execctx.Views{}, true)
	_, err := ex.buildRoutePlan(context.Background(), prep)
	if err == nil || !strings.Contains(err.Error(), "affinity identity") {
		t.Fatalf("want affinity identity error, got %v", err)
	}
}

func TestBuildRoutePlan_initializesBudgetAndSession(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.MaxAttempts = 5
	ex.DefaultBackend = "backendA"
	prep := testPreparedRequest("trace-1", aipapi.Call{Route: aipapi.RouteIntent{Selector: "backendA:model-x"}}, b2bua.ALegRecord{WeightedFirstConsumed: true}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if plan.progress.budget == nil || plan.progress.budget.max != 5 || plan.progress.budget.usedNow() != 0 {
		t.Fatalf("budget: got %+v", plan.progress.budget)
	}
	if plan.progress.session == nil || !plan.progress.session.FirstRequestConsumed {
		t.Fatalf("session state: got %+v", plan.progress.session)
	}
	if plan.progress.excluded == nil {
		t.Fatal("expected non-nil excluded set")
	}
}
