package request

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// RequestMeta is read-only policy and routing context for request-wide transforms.
// AttemptView is unset at this pipeline position (BLeg not allocated yet).
// Scope is the authoritative principal/scope attribution, carried separately from the
// legacy Principal projection (requirement 2.6). A zero Scope preserves local/anonymous
// identity semantics.
type RequestMeta struct {
	TraceID     string
	Annotations map[string]string
	Principal   execview.PrincipalView
	Scope       scope.PrincipalScopeView
	Session     session.SessionView
	Workspace   workspace.WorkspaceView
}

// Services exposes narrow capabilities for transforms (design §2).
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Transform mutates the canonical call with full visibility over messages, tools, and options.
// It is distinct from [sdkhooks.SubmitHook] (early reject/annotate) and from per-part hooks.
type Transform interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, call *aipapi.Call, meta RequestMeta, svc Services) error
}
