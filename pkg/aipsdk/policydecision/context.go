package policydecision

import (
	"maps"
	"slices"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Context is the safe, request-scoped attribution and lifecycle metadata for one
// decision evaluation or emitted record (requirements 2.1-2.6, 7.5). It is
// safe-by-construction: raw credentials, headers, resume tokens, and unvetted
// claims are never fields here. Authority comes from the accepted request's
// authoritative scope view; legacy principal projection is carried separately so
// policy code can distinguish authoritative scope from compatibility fields.
type Context struct {
	TraceID            string                   `json:"trace_id"`
	ALegID             string                   `json:"a_leg_id"`
	BLegID             string                   `json:"b_leg_id"`
	AttemptSeq         int                      `json:"attempt_seq"`
	Stage              string                   `json:"stage"`
	ProviderID         string                   `json:"provider_id"`
	Scope              scope.PrincipalScopeView `json:"scope"`
	Principal          execview.PrincipalView   `json:"principal"`
	Session            session.SessionView      `json:"session"`
	Workspace          workspace.WorkspaceView  `json:"workspace"`
	Annotations        map[string]string        `json:"annotations,omitempty"`
	OutputCommitted    bool                     `json:"output_committed"`
	EvaluationTimeout  time.Duration            `json:"evaluation_timeout,omitempty"`
	EvaluationDeadline time.Time                `json:"evaluation_deadline,omitzero"`
}

// Clone returns a deep copy of the context so callers and observers cannot mutate
// shared context state through slices, maps, or the embedded scope view
// (requirements 1.1, 2.1, 7.7). Nil slices and maps are preserved as nil.
func (c Context) Clone() Context {
	out := c
	out.Scope = c.Scope.Clone()
	out.Principal = c.Principal
	out.Principal.Roles = slices.Clone(c.Principal.Roles)
	out.Principal.Claims = maps.Clone(c.Principal.Claims)
	out.Session = c.Session
	out.Session.Labels = maps.Clone(c.Session.Labels)
	out.Workspace = c.Workspace
	out.Workspace.Markers = slices.Clone(c.Workspace.Markers)
	out.Workspace.Labels = maps.Clone(c.Workspace.Labels)
	out.Annotations = maps.Clone(c.Annotations)
	return out
}
