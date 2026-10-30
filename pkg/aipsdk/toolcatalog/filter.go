package toolcatalog

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// CatalogMeta is read-only context for catalog filters (no provider types).
type CatalogMeta struct {
	TraceID     string
	Annotations map[string]string
	Principal   execview.PrincipalView
	Session     session.SessionView
	Workspace   workspace.WorkspaceView
}

// Services exposes narrow capabilities for catalog filters.
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Filter removes or annotates tool definitions before backend translation (R9).
type Filter interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, call *aipapi.Call, meta CatalogMeta, svc Services) error
}
