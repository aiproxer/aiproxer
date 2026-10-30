package prerequest_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestMeta_ScopeFieldAuthoritativeAndClonesSafely(t *testing.T) {
	t.Parallel()
	meta := prerequest.Meta{
		TraceID: "trace-1",
		Principal: execview.PrincipalView{
			ID:          "legacy-principal",
			DisplayName: "Legacy Display",
			Roles:       []string{"a"},
			Claims:      map[string]string{"team": "platform"},
		},
		Session:   session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace: workspace.WorkspaceView{ID: "w1"},
		Scope: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectHuman,
			PrincipalID:  scope.Known("auth-principal"),
			Origin:       scope.OriginClient,
			SafeClaims:   map[string]string{"tenant": "acme"},
			PolicyLabels: map[string]string{"env": "prod"},
		},
		Annotations:    map[string]string{"k": "v"},
		AuxiliaryDepth: 0,
	}

	// Authoritative scope is carried separately from the legacy principal projection.
	if meta.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("authoritative scope principal id = %q", meta.Scope.PrincipalID.String())
	}
	if meta.Principal.ID != "legacy-principal" {
		t.Fatalf("legacy principal id must still project as before: %q", meta.Principal.ID)
	}

	// Scope maps clone safely: mutating the clone must not affect the meta's scope.
	clone := meta.Scope.Clone()
	clone.SafeClaims["tenant"] = "tampered"
	clone.PolicyLabels["env"] = "dev"
	if meta.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("meta scope safe claims mutated via clone: %q", meta.Scope.SafeClaims["tenant"])
	}
	if meta.Scope.PolicyLabels["env"] != "prod" {
		t.Fatalf("meta scope policy labels mutated via clone: %q", meta.Scope.PolicyLabels["env"])
	}
}

func TestMeta_ZeroScopePreservesLocalAnonymousSemantics(t *testing.T) {
	t.Parallel()
	meta := prerequest.Meta{
		Principal: execview.PrincipalView{ID: "anon"},
	}
	// Zero scope is unknown/local: subject kind zero value is empty and principal id is unknown.
	if meta.Scope.SubjectKind != "" {
		t.Fatalf("zero scope subject kind = %q, want empty zero value", meta.Scope.SubjectKind)
	}
	if meta.Scope.PrincipalID.IsKnown() {
		t.Fatalf("zero scope principal id must be unknown, got known %q", meta.Scope.PrincipalID.String())
	}
	// Legacy principal projection still works for local/anonymous operation.
	if meta.Principal.ID != "anon" {
		t.Fatalf("legacy principal id = %q", meta.Principal.ID)
	}
}

func TestMeta_ScopeClonesIsolateRolesSlice(t *testing.T) {
	t.Parallel()
	meta := prerequest.Meta{
		Scope: scope.PrincipalScopeView{
			Roles: []string{"r1", "r2"},
		},
	}
	clone := meta.Scope.Clone()
	clone.Roles[0] = "mutated"
	if meta.Scope.Roles[0] != "r1" {
		t.Fatalf("meta scope roles mutated via clone: %q", meta.Scope.Roles[0])
	}
}
