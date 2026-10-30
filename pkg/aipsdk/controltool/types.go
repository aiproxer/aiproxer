package controltool

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Provider is one optional proxy-owned model control tool. Handle receives a
// value copy of the completed call and provenance; providers have no contract
// capability to mutate the request, execute ordinary tools, or publish client
// output.
type Provider interface {
	ID() string
	Spec() Spec
	Handle(context.Context, CompletedCall, Meta) (Outcome, error)
}

// Spec is the frozen, provider-owned model-facing contract. Name and schema are
// not operator-renamable at this layer.
type Spec struct {
	Tool         aipapi.ToolDef
	Instruction  Instruction
	MaxArgsBytes int
}

// Instruction is the bounded, stable control text projected with the tool.
// V1 accepts only system or developer roles.
type Instruction struct {
	Role aipapi.Role
	Text string
}

// CompletedCall is the captured control-tool invocation. ToolName is opaque
// data; ownership is not inferred from the name at this contract.
type CompletedCall struct {
	ToolCallID string
	ToolName   string
	ArgsJSON   []byte
}

// Meta is request-local provenance supplied by the platform. Identifiers are
// opaque; Scope, Session, and Workspace are safe views, not authority objects.
type Meta struct {
	TraceID      string
	ALegID       string
	BLegID       string
	CandidateKey string
	AttemptSeq   int
	Scope        scope.PrincipalScopeView
	Session      session.SessionView
	Workspace    workspace.WorkspaceView
}

// OutcomeKind identifies the only results a provider may return.
type OutcomeKind uint8

const (
	// OutcomeInvalid is the zero value. It means the call is not a valid
	// control completion and must not carry client output.
	OutcomeInvalid OutcomeKind = iota
	// OutcomeComplete is a valid control completion with bounded result text.
	OutcomeComplete
)

// IsKnown reports whether k is one of the two legal outcome kinds.
func (k OutcomeKind) IsKnown() bool {
	switch k {
	case OutcomeInvalid, OutcomeComplete:
		return true
	default:
		return false
	}
}

// Outcome is the bounded provider result. OutcomeComplete requires non-empty
// result text. OutcomeInvalid carries no client output.
type Outcome struct {
	Kind       OutcomeKind
	ResultText string
	ReasonCode string
}
