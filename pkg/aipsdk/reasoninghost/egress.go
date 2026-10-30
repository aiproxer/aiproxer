package reasoninghost

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// EgressAction is the bounded public egress decision.
type EgressAction uint8

const (
	EgressDeny EgressAction = iota
	EgressAllow
	EgressRedactThenAllow
)

func (a EgressAction) String() string {
	switch a {
	case EgressAllow:
		return "allow"
	case EgressRedactThenAllow:
		return "redact_then_allow"
	default:
		return "deny"
	}
}

// EgressInput is the narrow public egress policy input.
// Scope is held privately and returned as a defensive clone via Scope().
type EgressInput struct {
	Route       string
	Purpose     string
	SourceClass string
	scope       scope.PrincipalScopeView
}

// Scope returns a defensive clone of the principal scope.
func (i EgressInput) Scope() scope.PrincipalScopeView {
	return i.scope.Clone()
}

// NewEgressInput constructs a public input with defensive clone of scope.
func NewEgressInput(route, purpose, sourceClass string, s scope.PrincipalScopeView) EgressInput {
	return EgressInput{
		Route:       route,
		Purpose:     purpose,
		SourceClass: sourceClass,
		scope:       s.Clone(),
	}
}

// EgressDecision is the trusted public policy decision.
type EgressDecision struct {
	Action        EgressAction
	PolicyVersion string
}

// EgressPolicy is the trusted public data-egress decision seam for host composition.
// Enabled compression without host policy fails closed.
type EgressPolicy interface {
	Decide(ctx context.Context, in EgressInput) (EgressDecision, error)
}
