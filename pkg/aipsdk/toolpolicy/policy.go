package toolpolicy

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

// Decision is the policy outcome for a canonical tool-call lifecycle event.
type Decision int

const (
	DecisionUnspecified Decision = iota
	DecisionAllow
	DecisionDeny
)

// Meta carries read-only execution context for tool-call policy decisions.
// Scope is the authoritative principal/scope attribution, carried separately from the
// legacy Principal projection (requirement 2.6). A zero Scope preserves local/anonymous
// identity semantics.
type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int

	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// Services exposes narrow capabilities for policy hooks.
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Policy decides whether a canonical model-emitted tool event is allowed to continue.
// It is distinct from toolcatalog.Filter, which shapes the advertised catalog before
// backend execution, and from hooks.ToolReactor, which can rewrite tool events.
type Policy interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, event aipapi.ToolEvent, meta Meta, svc Services) (Decision, error)
}
