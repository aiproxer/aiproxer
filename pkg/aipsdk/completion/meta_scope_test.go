package completion_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestCompletionMeta_ScopeSessionWorkspaceForEvidence(t *testing.T) {
	t.Parallel()
	meta := completion.Meta{
		TraceID:    "trace-1",
		ALegID:     "a-leg",
		BLegID:     "b-leg",
		AttemptSeq: 3,
		Scope: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectHuman,
			PrincipalID:  scope.Known("auth-principal"),
			Origin:       scope.OriginClient,
			SafeClaims:   map[string]string{"tenant": "acme"},
			PolicyLabels: map[string]string{"env": "prod"},
		},
		Session:   session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace: workspace.WorkspaceView{ID: "w1"},
	}

	if meta.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("authoritative scope principal id = %q", meta.Scope.PrincipalID.String())
	}
	if meta.Session.AuthoritativeSessionID != "s1" {
		t.Fatalf("session id = %q", meta.Session.AuthoritativeSessionID)
	}
	if meta.Workspace.ID != "w1" {
		t.Fatalf("workspace id = %q", meta.Workspace.ID)
	}

	clone := meta.Scope.Clone()
	clone.SafeClaims["tenant"] = "tampered"
	if meta.Scope.SafeClaims["tenant"] != "acme" {
		t.Fatalf("meta scope safe claims mutated via clone: %q", meta.Scope.SafeClaims["tenant"])
	}
}

func TestCompletionMeta_ZeroScopePreservesIdentifiers(t *testing.T) {
	t.Parallel()
	meta := completion.Meta{TraceID: "trace-1", ALegID: "a-leg"}
	if meta.Scope.SubjectKind != "" {
		t.Fatalf("zero scope subject kind = %q, want empty zero value", meta.Scope.SubjectKind)
	}
	if meta.TraceID != "trace-1" || meta.ALegID != "a-leg" {
		t.Fatalf("existing identifiers must be preserved: trace=%q a-leg=%q", meta.TraceID, meta.ALegID)
	}
}
