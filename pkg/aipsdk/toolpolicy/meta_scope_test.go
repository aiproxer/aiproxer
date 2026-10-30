package toolpolicy_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestToolPolicyMeta_ScopeFieldAuthoritativeAndClonesSafely(t *testing.T) {
	t.Parallel()
	meta := toolpolicy.Meta{
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
			SubjectKind:  scope.SubjectHuman,
			PrincipalID:  scope.Known("auth-principal"),
			Origin:       scope.OriginClient,
			SafeClaims:   map[string]string{"tenant": "acme"},
			PolicyLabels: map[string]string{"env": "prod"},
			Roles:        []string{"r1"},
		},
	}

	if meta.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("authoritative scope principal id = %q", meta.Scope.PrincipalID.String())
	}
	if meta.Principal.ID != "legacy-principal" {
		t.Fatalf("legacy principal id must still project as before: %q", meta.Principal.ID)
	}
	// Existing decision enum stays untouched.
	if toolpolicy.DecisionAllow == toolpolicy.DecisionUnspecified {
		t.Fatal("DecisionAllow must remain distinct from DecisionUnspecified")
	}

	clone := meta.Scope.Clone()
	clone.SafeClaims["tenant"] = "tampered"
	clone.Roles[0] = "mutated"
	if meta.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("meta scope safe claims mutated via clone: %q", meta.Scope.SafeClaims["tenant"])
	}
	if meta.Scope.Roles[0] != "r1" {
		t.Fatalf("meta scope roles mutated via clone: %q", meta.Scope.Roles[0])
	}
}

func TestToolPolicyMeta_ZeroScopePreservesLegacyPrincipal(t *testing.T) {
	t.Parallel()
	meta := toolpolicy.Meta{Principal: execview.PrincipalView{ID: "anon"}}
	if meta.Scope.SubjectKind != "" {
		t.Fatalf("zero scope subject kind = %q, want empty zero value", meta.Scope.SubjectKind)
	}
	if meta.Principal.ID != "anon" {
		t.Fatalf("legacy principal id = %q", meta.Principal.ID)
	}
}
