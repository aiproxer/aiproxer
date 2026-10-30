package request_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestRequestMeta_ScopeFieldAuthoritativeAndClonesSafely(t *testing.T) {
	t.Parallel()
	meta := request.RequestMeta{
		TraceID: "trace-1",
		Principal: execview.PrincipalView{
			ID:     "legacy-principal",
			Roles:  []string{"a"},
			Claims: map[string]string{"team": "platform"},
		},
		Session:   session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace: workspace.WorkspaceView{ID: "w1"},
		Scope: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectService,
			PrincipalID:  scope.Known("auth-service"),
			Origin:       scope.OriginClient,
			SafeClaims:   map[string]string{"tenant": "acme"},
			PolicyLabels: map[string]string{"env": "prod"},
		},
	}

	if meta.Scope.PrincipalID.String() != "auth-service" {
		t.Fatalf("authoritative scope principal id = %q", meta.Scope.PrincipalID.String())
	}
	if meta.Principal.ID != "legacy-principal" {
		t.Fatalf("legacy principal id must still project as before: %q", meta.Principal.ID)
	}

	clone := meta.Scope.Clone()
	clone.SafeClaims["tenant"] = "tampered"
	if meta.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("meta scope safe claims mutated via clone: %q", meta.Scope.SafeClaims["tenant"])
	}
}

func TestRequestMeta_ZeroScopePreservesLegacyPrincipal(t *testing.T) {
	t.Parallel()
	meta := request.RequestMeta{Principal: execview.PrincipalView{ID: "anon"}}
	if meta.Scope.SubjectKind != "" {
		t.Fatalf("zero scope subject kind = %q, want empty zero value", meta.Scope.SubjectKind)
	}
	if meta.Principal.ID != "anon" {
		t.Fatalf("legacy principal id = %q", meta.Principal.ID)
	}
}
