package extensions_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func sampleViews() execctx.Views {
	return execctx.Views{
		Principal: execview.PrincipalView{
			ID:          "legacy-principal",
			DisplayName: "Legacy Display",
			Roles:       []string{"operator"},
			Claims:      map[string]string{"team": "platform"},
		},
		Scope: scope.PrincipalScopeView{
			SubjectKind:   scope.SubjectHuman,
			PrincipalID:   scope.Known("auth-principal"),
			Origin:        scope.OriginClient,
			TenantID:      scope.Known("acme"),
			SafeClaims:    map[string]string{"tenant": "acme"},
			PolicyLabels:  map[string]string{"env": "prod"},
			Roles:         []string{"r1"},
			ParentTraceID: scope.Known("parent-trace-9"),
		},
		Session: session.SessionView{
			AuthoritativeSessionID: "s1",
			ALegID:                 "a-leg-1",
			Labels:                 map[string]string{"treatment": "canary"},
		},
		Attempt: execview.AttemptView{
			TraceID:    "trace-1",
			BLegID:     "b-leg-2",
			AttemptSeq: 3,
			BackendID:  "bedrock",
			RouteRole:  "primary",
		},
		Workspace: workspace.WorkspaceView{
			ID:      "w1",
			Markers: []string{"m1"},
			Labels:  map[string]string{"region": "eu"},
		},
		Annotations: map[string]string{"source": "builder-test"},
	}
}

func TestDecisionContext_IncludesSafeScopeAndLegacyPrincipal(t *testing.T) {
	t.Parallel()
	ctx := extensions.BuildDecisionContext(sampleViews(), feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})

	if ctx.Stage != feature.StageIDPreRequest {
		t.Fatalf("stage = %q", ctx.Stage)
	}
	if ctx.ProviderID != "p1" {
		t.Fatalf("provider id = %q", ctx.ProviderID)
	}
	// Authoritative scope is carried.
	if ctx.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("scope principal id = %q", ctx.Scope.PrincipalID.String())
	}
	if ctx.Scope.TenantID.String() != "acme" {
		t.Fatalf("scope tenant id = %q", ctx.Scope.TenantID.String())
	}
	// Legacy principal projection is carried separately (requirement 2.6).
	if ctx.Principal.ID != "legacy-principal" {
		t.Fatalf("legacy principal id = %q", ctx.Principal.ID)
	}
	if ctx.Principal.DisplayName != "Legacy Display" {
		t.Fatalf("legacy principal display name = %q", ctx.Principal.DisplayName)
	}
}

func TestDecisionContext_CarriesLineageAndLifecycleMetadata(t *testing.T) {
	t.Parallel()
	ctx := extensions.BuildDecisionContext(sampleViews(), feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})

	if ctx.TraceID != "trace-1" {
		t.Fatalf("trace id = %q", ctx.TraceID)
	}
	if ctx.ALegID != "a-leg-1" {
		t.Fatalf("a-leg id = %q", ctx.ALegID)
	}
	if ctx.BLegID != "b-leg-2" {
		t.Fatalf("b-leg id = %q", ctx.BLegID)
	}
	if ctx.AttemptSeq != 3 {
		t.Fatalf("attempt seq = %d", ctx.AttemptSeq)
	}
	if ctx.Session.AuthoritativeSessionID != "s1" {
		t.Fatalf("session id = %q", ctx.Session.AuthoritativeSessionID)
	}
	if ctx.Workspace.ID != "w1" {
		t.Fatalf("workspace id = %q", ctx.Workspace.ID)
	}
	if ctx.Annotations["source"] != "builder-test" {
		t.Fatalf("annotations = %#v", ctx.Annotations)
	}
}

func TestDecisionContext_PreservesInternalOriginAndParentTrace(t *testing.T) {
	t.Parallel()
	views := sampleViews()
	views.Scope.Origin = scope.OriginInternal
	views.Scope.ParentTraceID = scope.Known("parent-trace-42")

	ctx := extensions.BuildDecisionContext(views, feature.StageIDRequestWide, "rtx", extensions.DecisionContextOptions{})

	if ctx.Scope.Origin != scope.OriginInternal {
		t.Fatalf("internal origin must be preserved: %q", ctx.Scope.Origin)
	}
	if ctx.Scope.ParentTraceID.String() != "parent-trace-42" {
		t.Fatalf("parent trace id must be preserved: %q", ctx.Scope.ParentTraceID.String())
	}
}

func TestDecisionContext_PreservesUnknownScopeValues(t *testing.T) {
	t.Parallel()
	views := sampleViews()
	// Wipe optional scope attribution fields to unknown/empty; principal id stays authoritative.
	views.Scope.TenantID = scope.Unknown()
	views.Scope.ProjectID = scope.Unknown()
	views.Scope.DepartmentID = scope.Unknown()
	views.Scope.CostCenterID = scope.Unknown()
	views.Scope.WorkspaceID = scope.Unknown()

	ctx := extensions.BuildDecisionContext(views, feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})

	if ctx.Scope.TenantID.IsKnown() {
		t.Fatalf("unknown tenant must be preserved as unknown, got %q", ctx.Scope.TenantID.String())
	}
	if ctx.Scope.ProjectID.IsKnown() || ctx.Scope.DepartmentID.IsKnown() || ctx.Scope.CostCenterID.IsKnown() {
		t.Fatalf("unknown optional scope fields must stay unknown")
	}
}

func TestDecisionContext_CarriesOutputCommittedAndTimeoutBudget(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(250 * time.Millisecond)
	ctx := extensions.BuildDecisionContext(sampleViews(), feature.StageIDCompletionGating, "g1", extensions.DecisionContextOptions{
		OutputCommitted:    true,
		EvaluationTimeout:  250 * time.Millisecond,
		EvaluationDeadline: deadline,
	})

	if !ctx.OutputCommitted {
		t.Fatal("output committed must be carried through")
	}
	if ctx.EvaluationTimeout != 250*time.Millisecond {
		t.Fatalf("evaluation timeout = %v", ctx.EvaluationTimeout)
	}
	if !ctx.EvaluationDeadline.Equal(deadline) {
		t.Fatalf("evaluation deadline = %v, want %v", ctx.EvaluationDeadline, deadline)
	}
}

func TestDecisionContext_ZeroTimeoutPreservesLegacyDefault(t *testing.T) {
	t.Parallel()
	ctx := extensions.BuildDecisionContext(sampleViews(), feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})
	if ctx.EvaluationTimeout != 0 {
		t.Fatalf("zero timeout must be preserved as legacy default, got %v", ctx.EvaluationTimeout)
	}
	if !ctx.EvaluationDeadline.IsZero() {
		t.Fatalf("zero deadline must be preserved, got %v", ctx.EvaluationDeadline)
	}
}

func TestDecisionContext_BuildIsDefensivelyCloned(t *testing.T) {
	t.Parallel()
	views := sampleViews()
	ctx := extensions.BuildDecisionContext(views, feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})

	// Mutating the returned context's maps/slices must not affect the source views.
	ctx.Annotations["source"] = "tampered"
	ctx.Scope.SafeClaims["tenant"] = "tampered"
	ctx.Scope.PolicyLabels["env"] = "tampered"
	ctx.Scope.Roles[0] = "tampered"
	ctx.Principal.Claims["team"] = "tampered"
	ctx.Principal.Roles[0] = "tampered"
	ctx.Session.Labels["treatment"] = "tampered"
	ctx.Workspace.Markers[0] = "tampered"
	ctx.Workspace.Labels["region"] = "tampered"

	if views.Annotations["source"] != "builder-test" {
		t.Fatalf("source views annotations mutated via context: %q", views.Annotations["source"])
	}
	if views.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("source views scope safe claims mutated: %q", views.Scope.SafeClaims["tenant"])
	}
	if views.Scope.PolicyLabels["env"] != "prod" {
		t.Fatalf("source views scope policy labels mutated: %q", views.Scope.PolicyLabels["env"])
	}
	if views.Scope.Roles[0] != "r1" {
		t.Fatalf("source views scope roles mutated: %q", views.Scope.Roles[0])
	}
	if views.Principal.Claims["team"] != "platform" {
		t.Fatalf("source views principal claims mutated: %q", views.Principal.Claims["team"])
	}
	if views.Principal.Roles[0] != "operator" {
		t.Fatalf("source views principal roles mutated: %q", views.Principal.Roles[0])
	}
	if views.Session.Labels["treatment"] != "canary" {
		t.Fatalf("source views session labels mutated: %q", views.Session.Labels["treatment"])
	}
	if views.Workspace.Markers[0] != "m1" {
		t.Fatalf("source views workspace markers mutated: %q", views.Workspace.Markers[0])
	}
	if views.Workspace.Labels["region"] != "eu" {
		t.Fatalf("source views workspace labels mutated: %q", views.Workspace.Labels["region"])
	}
}

func TestDecisionContext_NoRawCredentialsOrHeadersLeak(t *testing.T) {
	t.Parallel()
	views := sampleViews()
	// Annotations are the only free-form map; ensure the builder does not synthesize unsafe
	// fields. The Scope type is safe-by-construction (no credentials/headers), so we assert the
	// context only carries what the views carried plus safe empty defaults.
	ctx := extensions.BuildDecisionContext(views, feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})

	if ctx.Annotations["source"] != "builder-test" {
		t.Fatalf("annotations dropped: %#v", ctx.Annotations)
	}
	// The context must not invent extra annotation entries beyond the source.
	if len(ctx.Annotations) != len(views.Annotations) {
		t.Fatalf("annotation count changed: got %d want %d", len(ctx.Annotations), len(views.Annotations))
	}
}

func TestDecisionContext_EmptyViewsProducesZeroSafeContext(t *testing.T) {
	t.Parallel()
	ctx := extensions.BuildDecisionContext(execctx.Views{}, feature.StageIDPreRequest, "", extensions.DecisionContextOptions{})

	if ctx.Stage != feature.StageIDPreRequest {
		t.Fatalf("stage = %q", ctx.Stage)
	}
	// Empty views produce a zero-scope context that preserves local/anonymous semantics.
	if ctx.Scope.PrincipalID.IsKnown() {
		t.Fatalf("empty views scope principal must be unknown, got %q", ctx.Scope.PrincipalID.String())
	}
	if ctx.Principal.ID != "" {
		t.Fatalf("empty views legacy principal must be empty, got %q", ctx.Principal.ID)
	}
}

func TestDecisionContext_ReturnsPolicyDecisionContextType(t *testing.T) {
	t.Parallel()
	ctx := extensions.BuildDecisionContext(sampleViews(), feature.StageIDPreRequest, "p1", extensions.DecisionContextOptions{})
	var _ policydecision.Context = ctx //nolint:staticcheck // QF1011: intentional compile-time return-type assertion
}
