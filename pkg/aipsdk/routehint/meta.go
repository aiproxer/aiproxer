package routehint

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Input is the read-only context for a route-hint pass (after request-wide transforms, before planning).
type Input struct {
	TraceID   string
	Call      *aipapi.Call
	Principal execview.PrincipalView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// Result is a single provider's advisory output. Empty result is a no-op.
type Result struct {
	// PreferredCandidateKeys is a core routing key per attempt in the form "backend:model" (see [routing.AttemptCandidate].Key).
	// The planner may move these keys earlier in the candidate list when they are already eligible; keys that
	// are excluded, unhealthy, or not in the route are ignored.
	PreferredCandidateKeys []string
}
