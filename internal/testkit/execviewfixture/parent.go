// Package execviewfixture provides conflicting parent authority for seam tests.
package execviewfixture

import (
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// PoisonedParent seeds each request view with distinguishable upstream state.
// Tests decide which fields survive a boundary; the fixture asserts no policy.
func PoisonedParent(aLegID string) execctx.Views {
	trusted := scope.PrincipalScopeView{Origin: scope.OriginClient, SubjectKind: scope.SubjectLocal, PrincipalID: scope.Known("trusted-principal"), SafeClaims: map[string]string{"trusted": "retained"}}
	return execctx.Views{
		Principal: trusted.Principal(), Scope: trusted,
		Session: session.SessionView{
			AuthoritativeSessionID: "parent-session", ClientSessionHint: "parent-hint", ALegID: aLegID,
			IsNew: true, WorkspaceID: "parent-workspace", ResumeEligible: true,
			Labels: map[string]string{"parent_claim": "poison"}, TurnID: "parent-turn",
			Classification: session.Classification{Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity, Confidence: session.ConfidenceHigh, Evidence: "parent", Revision: 1},
		},
		Workspace:   workspace.WorkspaceView{ID: "parent-workspace", ProjectRoot: "/parent-only", DirtyTree: true, Markers: []string{"parent-marker"}, Labels: map[string]string{"parent": "workspace-poison"}},
		Attempt:     execview.AttemptView{TraceID: "parent-trace", BLegID: "parent-b-leg", AttemptSeq: 9, BackendID: "parent-backend", RouteRole: "parent-role"},
		Annotations: map[string]string{"parent": "annotation-poison"},
	}
}
