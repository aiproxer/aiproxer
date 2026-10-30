package hooks_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestToolMeta_ScopeFieldAuthoritativeAndClonesSafely(t *testing.T) {
	t.Parallel()
	meta := hooks.ToolMeta{
		TraceID:    "trace-1",
		ALegID:     "a-leg",
		BLegID:     "b-leg",
		AttemptSeq: 3,
		Principal: execview.PrincipalView{
			ID:     "legacy-principal",
			Roles:  []string{"a"},
			Claims: map[string]string{"team": "platform"},
		},
		Session:   session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace: workspace.WorkspaceView{ID: "w1"},
		Scope: scope.PrincipalScopeView{
			SubjectKind:   scope.SubjectHuman,
			PrincipalID:   scope.Known("auth-principal"),
			Origin:        scope.OriginInternal,
			ParentTraceID: scope.Known("parent-trace"),
			SafeClaims:    map[string]string{"tenant": "acme"},
			PolicyLabels:  map[string]string{"env": "prod"},
		},
	}

	if meta.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("authoritative scope principal id = %q", meta.Scope.PrincipalID.String())
	}
	if meta.Principal.ID != "legacy-principal" {
		t.Fatalf("legacy principal id must still project as before: %q", meta.Principal.ID)
	}
	// Existing ToolDecision enum stays untouched.
	if hooks.ToolPass == hooks.ToolDecisionUnspecified {
		t.Fatal("ToolPass must remain distinct from ToolDecisionUnspecified")
	}

	clone := meta.Scope.Clone()
	clone.SafeClaims["tenant"] = "tampered"
	if meta.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("meta scope safe claims mutated via clone: %q", meta.Scope.SafeClaims["tenant"])
	}
	if meta.Scope.Origin != scope.OriginInternal {
		t.Fatalf("internal origin must be preserved: %q", meta.Scope.Origin)
	}
	if meta.Scope.ParentTraceID.String() != "parent-trace" {
		t.Fatalf("parent trace id must be preserved: %q", meta.Scope.ParentTraceID.String())
	}
}

func TestToolMeta_ZeroScopePreservesLegacyPrincipal(t *testing.T) {
	t.Parallel()
	meta := hooks.ToolMeta{Principal: execview.PrincipalView{ID: "anon"}}
	if meta.Scope.SubjectKind != "" {
		t.Fatalf("zero scope subject kind = %q, want empty zero value", meta.Scope.SubjectKind)
	}
	if meta.Principal.ID != "anon" {
		t.Fatalf("legacy principal id = %q", meta.Principal.ID)
	}
}
