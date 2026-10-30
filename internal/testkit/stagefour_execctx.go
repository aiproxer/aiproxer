package testkit

import (
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// ExecCtxViewsFixture returns deterministic plugin-facing views for ordering and snapshot tests (task 4.3).
func ExecCtxViewsFixture() execctx.Views {
	return execctx.Views{
		Principal: execview.PrincipalView{
			ID: "fixture-principal", DisplayName: "Fixture",
			Roles:  []string{"fixture-role"},
			Claims: map[string]string{"fixture": "1"},
		},
		Session: session.SessionView{
			ClientSessionHint: "fixture-session", ALegID: "fixture-aleg", IsNew: true,
			Labels: map[string]string{"fixture": "session"},
		},
		Attempt: execview.AttemptView{
			TraceID: "fixture-trace", BLegID: "fixture-bleg", AttemptSeq: 3,
			BackendID: "fixture-backend", RouteRole: "primary",
		},
		Workspace: workspace.WorkspaceView{
			ProjectRoot: "/fixture/root", DirtyTree: false,
			Markers: []string{".fixture"},
			Labels:  map[string]string{"fixture": "ws"},
		},
		Annotations: map[string]string{"fixture": "ann"},
	}
}

// ExecCtxViewsFromSubmit wraps [execctx.ViewsFromSubmit] with a fixed clock-friendly A-leg row.
func ExecCtxViewsFromSubmit(traceID string, aLeg b2bua.ALegRecord, call aipapi.Call, ann map[string]string) execctx.Views {
	return execctx.ViewsFromSubmit(traceID, aLeg, call, ann)
}
