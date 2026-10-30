package reasoningpreservation

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// EgressAction is the bounded egress decision.
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

const (
	EgressPurposeReasoningSemanticCompression = "reasoning_semantic_compression"
	EgressSourceClassSemanticText             = "semantic_reasoning_text"
)

// TrustedTextSanitizer is a small interface for an existing secret/redaction authority.
type TrustedTextSanitizer interface {
	SanitizeText(ctx context.Context, text string) (string, error)
}

// EgressPrincipalView is an opaque minimal principal-scope view not interpreted by this package.
// It holds the full scope.PrincipalScopeView for policy enforcement (tenant/org/workspace/project/cost-center/policy-labels)
// but remains opaque to preservation internals. Call Scope() for a defensive clone.
type EgressPrincipalView struct {
	view scope.PrincipalScopeView
}

// NewEgressPrincipalView constructs an opaque view from a legacy principal ID string.
// Compatibility is preserved by constructing a scope with Known PrincipalID.
func NewEgressPrincipalView(opaque string) EgressPrincipalView {
	return EgressPrincipalView{view: scope.PrincipalScopeView{PrincipalID: scope.Known(opaque)}}
}

// NewEgressPrincipalScopeView constructs a view from the full trusted scope.
// The scope is defensively cloned on construction.
func NewEgressPrincipalScopeView(v scope.PrincipalScopeView) EgressPrincipalView {
	return EgressPrincipalView{view: v.Clone()}
}

// Scope returns a defensive clone of the underlying principal scope.
func (v EgressPrincipalView) Scope() scope.PrincipalScopeView { return v.view.Clone() }

// PrincipalID returns the string form of the principal ID (bounded accessor).
func (v EgressPrincipalView) PrincipalID() string { return v.view.PrincipalID.String() }

// CompressionEgressInput is the narrow egress policy input.
type CompressionEgressInput struct {
	Route       string
	Purpose     string
	SourceClass string
	Principal   EgressPrincipalView
}

// CompressionEgressDecision is the trusted policy decision.
type CompressionEgressDecision struct {
	Action        EgressAction
	PolicyVersion string
	Sanitizer     TrustedTextSanitizer
}

// EgressPolicy is the trusted data-egress decision seam.
type EgressPolicy interface {
	Decide(ctx context.Context, in CompressionEgressInput) (CompressionEgressDecision, error)
}

// EvaluateEgress enforces the hard rules: nil/missing policy => missing-policy deny-equivalent;
// redact without sanitizer => deny; route string alone never approves (policy always consulted).
func EvaluateEgress(ctx context.Context, policy EgressPolicy, in CompressionEgressInput) CompressionEgressDecision {
	if policy == nil {
		return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}
	}
	dec, err := policy.Decide(ctx, in)
	if err != nil {
		return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}
	}
	if dec.PolicyVersion == "" {
		// missing policy version is treated as missing-policy deny
		return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}
	}
	if dec.Action == EgressRedactThenAllow && dec.Sanitizer == nil {
		return CompressionEgressDecision{Action: EgressDeny, PolicyVersion: dec.PolicyVersion}
	}
	// Route alone never approves: decision must come from policy, which we already delegated.
	// No additional route-based allow is applied.
	return dec
}
