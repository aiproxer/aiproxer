package capabilities

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CandidateFacts carries resolved backend facts used for pre-network candidate admission.
type CandidateFacts struct {
	Caps               aipapi.BackendCaps
	TransportCaps      aipapi.BackendTransportCaps
	ReplaySupport      aipapi.ReasoningReplaySupport
	DialectSupport     aipapi.DialectSupport
	ProjectionTarget   aipapi.LegacyProjectionTarget
	FrozenRequirements *aipapi.ProtocolRequirements
	// AttemptPolicy gates the per-attempt log_skip strip of
	// unrepresentable client-sent reasoning. Empty stays hard (reject).
	AttemptPolicy aipapi.AttemptPolicy
}

// AdmitCandidate evaluates one candidate against call requirements before upstream work.
func AdmitCandidate(ctx context.Context, call aipapi.Call, inv aipapi.Invocation, cand routing.AttemptCandidate, facts CandidateFacts) aipapi.CandidateAdmissionResult {
	_ = ctx
	_ = cand
	target := facts.ProjectionTarget
	target.Caps = facts.Caps
	target.ReplaySupport = facts.ReplaySupport
	return aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:               call,
		Invocation:         inv,
		BackendCaps:        facts.Caps,
		TransportCaps:      facts.TransportCaps,
		TransportPolicy:    aipapi.TransportFallbackCompatibility,
		ReplaySupport:      facts.ReplaySupport,
		DialectSupport:     facts.DialectSupport,
		ProjectionTarget:   target,
		FrozenRequirements: facts.FrozenRequirements,
		AttemptPolicy:      facts.AttemptPolicy,
	})
}

// FailoverRequirementSet retains the complete requirement set that every failover candidate must satisfy.
type FailoverRequirementSet struct {
	Required aipapi.ProtocolRequirements
}

// NewFailoverRequirementSet derives the requirement set from the baseline call.
func NewFailoverRequirementSet(call aipapi.Call) FailoverRequirementSet {
	return FailoverRequirementSet{Required: aipapi.DeriveProtocolRequirements(call)}
}

// NewFailoverRequirementSetFromRequirements constructs a FailoverRequirementSet directly
// from static metadata ProtocolRequirements without requiring a aipapi.Call (Requirements 7, 8, 19).
func NewFailoverRequirementSetFromRequirements(req aipapi.ProtocolRequirements) FailoverRequirementSet {
	return FailoverRequirementSet{Required: req}
}

// CandidateMatchesFailoverRequirements reports whether candidate support satisfies the baseline requirement set.
func (f FailoverRequirementSet) CandidateMatchesFailoverRequirements(supported aipapi.ProtocolRequirements, replay aipapi.ReasoningReplaySupport) bool {
	res := aipapi.MatchRequirements(f.Required, supported, replay)
	return res.Kind != aipapi.NegotiationReject
}
