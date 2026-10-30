package completion

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Meta carries attempt-scoped identifiers and safe decision context for completion gates
// (no transport types). Scope is the authoritative principal/scope attribution; Session and
// Workspace provide the lifecycle context needed for completion-gate decision evidence
// (requirement 2.1, 2.6). Existing identifiers are preserved; a zero Scope preserves
// local/anonymous identity semantics.
type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int

	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}
