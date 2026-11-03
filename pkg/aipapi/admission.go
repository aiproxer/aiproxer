package aipapi

import "slices"

// CandidateAdmissionInput carries everything required to reject incompatible candidates before upstream work.
type CandidateAdmissionInput struct {
	Call              Call
	Invocation        Invocation
	BackendCaps       BackendCaps
	TransportCaps     BackendTransportCaps
	TransportPolicy   TransportFallbackPolicy
	ReplaySupport     ReasoningReplaySupport
	DialectSupport    DialectSupport
	ProjectionTarget  LegacyProjectionTarget
	RequireProjection bool
	// FrozenRequirements, when set, is the immutable baseline requirement set every
	// candidate must satisfy; transforms must not weaken it during failover.
	FrozenRequirements *ProtocolRequirements
	// AttemptPolicy gates the per-attempt strip of unrepresentable
	// client-sent reasoning. Empty (or reject) preserves today's hard reject.
	// log_skip converts a reject caused solely by unrepresentable client-sent
	// reasoning into a downgrade; the executor strips the attempt clone and
	// records the decision. The legacy hard path is unchanged for callers
	// that leave this unset.
	AttemptPolicy AttemptPolicy
}

// CandidateAdmissionResult is the deterministic admission outcome for one candidate.
type CandidateAdmissionResult struct {
	Kind            NegotiationKind
	Capability      NegotiationResult
	Transport       TransportNegotiationResult
	Requirements    RequirementsMatchResult
	ProjectionError error
}

// AdmitCandidate evaluates operation/transport, semantic capability, exact dialect, and projector feasibility.
func AdmitCandidate(in CandidateAdmissionInput) CandidateAdmissionResult {
	out := CandidateAdmissionResult{}

	out.Transport = NegotiateTransport(in.Invocation, in.TransportCaps, in.TransportPolicy)
	if out.Transport.Kind == NegotiationReject {
		out.Kind = NegotiationReject
		return out
	}

	target := in.ProjectionTarget
	target.Caps = in.BackendCaps
	target.ReplaySupport = in.ReplaySupport

	var req ProtocolRequirements
	if in.FrozenRequirements != nil {
		frozen := NormalizeProtocolRequirements(*in.FrozenRequirements)
		current, curErr := DeriveCandidateRequirements(in.Call, in.BackendCaps, target)
		if curErr != nil {
			out.ProjectionError = curErr
			out.Kind = NegotiationReject
			return out
		}
		req = UnionProtocolRequirements(frozen, current)
		out.Capability = NegotiateWithAttemptPolicy(admissionCapabilityRequirements(in.Call, in.BackendCaps, target, req), in.BackendCaps, in.AttemptPolicy)
		if out.Capability.Kind == NegotiationReject {
			out.Kind = NegotiationReject
			return maybeDowngradeUnrepresentableReasoning(in, out)
		}
	} else {
		candReq, candErr := DeriveCandidateRequirements(in.Call, in.BackendCaps, target)
		if candErr != nil {
			out.ProjectionError = candErr
			out.Kind = NegotiationReject
			return out
		}
		out.Capability = NegotiateWithAttemptPolicy(admissionCapabilityRequirements(in.Call, in.BackendCaps, target, candReq), in.BackendCaps, in.AttemptPolicy)
		if out.Capability.Kind == NegotiationReject {
			out.Kind = NegotiationReject
			return maybeDowngradeUnrepresentableReasoning(in, out)
		}
		req = candReq
	}
	req.Capabilities = nil // semantic capabilities were evaluated above; requirements match dialects/extensions only.
	supported := ProtocolRequirements{Capabilities: capabilitySlice(in.BackendCaps)}
	supported.ItemDialects = append([]DialectRequirement(nil), in.DialectSupport.ItemDialects...)
	supported.ReasoningDialects = append([]DialectRequirement(nil), in.DialectSupport.ReasoningDialects...)
	supported.CompactionDialects = append([]DialectRequirement(nil), in.DialectSupport.CompactionDialects...)
	supported.ExtensionTypes = append([]ExtensionRequirement(nil), in.DialectSupport.ExtensionTypes...)
	out.Requirements = MatchRequirements(req, supported, in.ReplaySupport)
	if out.Requirements.Kind == NegotiationReject {
		out.Kind = NegotiationReject
		return maybeDowngradeUnrepresentableReasoning(in, out)
	}

	// Projection feasibility is already established by DeriveCandidateRequirements
	// above, which projects the call into the candidate view and returns the
	// projection error on failure. Re-running CheckProjectionFeasibility here
	// would repeat the full projection (per-item field formatting, content
	// conversion, and clones) once per candidate with a discarded result.

	if out.Capability.Kind == NegotiationDowngrade {
		out.Kind = NegotiationDowngrade
		return out
	}
	out.Kind = NegotiationLossless
	return out
}

// Err returns the first hard reject error, if any.
func (r CandidateAdmissionResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	if r.ProjectionError != nil {
		return r.ProjectionError
	}
	if err := r.Requirements.Err(); err != nil {
		return err
	}
	if err := r.Transport.Err(); err != nil {
		return err
	}
	return r.Capability.Err()
}

// maybeDowngradeUnrepresentableReasoning converts a hard reject caused solely
// by unrepresentable client-sent reasoning into a single downgrade under the
// log_skip attempt policy. It replays admission on a scratch stripped clone
// (never the caller's call) with the frozen baseline re-derived from the
// stripped shape, so the failover union cannot reintroduce the waived
// reasoning obligations while every non-reasoning obligation stays exact.
// Anything the strip cannot fix stays rejected. Under reject (or unset) the
// input is returned unchanged: the legacy hard path is untouched.
func maybeDowngradeUnrepresentableReasoning(in CandidateAdmissionInput, rejected CandidateAdmissionResult) CandidateAdmissionResult {
	if !in.AttemptPolicy.allowsSkip() {
		return rejected
	}
	if rejected.Transport.Kind == NegotiationReject || rejected.ProjectionError != nil {
		return rejected
	}
	scratch := CloneCall(in.Call)
	if !StripReasoningForAttempt(&scratch) {
		return rejected
	}
	strippedFrozen := DeriveProtocolRequirements(scratch)
	recheck := AdmitCandidate(CandidateAdmissionInput{
		Call:               scratch,
		Invocation:         in.Invocation,
		BackendCaps:        in.BackendCaps,
		TransportCaps:      in.TransportCaps,
		TransportPolicy:    in.TransportPolicy,
		ReplaySupport:      in.ReplaySupport,
		DialectSupport:     in.DialectSupport,
		ProjectionTarget:   in.ProjectionTarget,
		RequireProjection:  in.RequireProjection,
		FrozenRequirements: &strippedFrozen,
	})
	if recheck.Kind == NegotiationReject || recheck.ProjectionError != nil {
		return rejected
	}
	downgraded := append([]Capability(nil), recheck.Capability.Downgraded...)
	hasReplay := slices.Contains(downgraded, CapabilityReasoningReplay)
	if !hasReplay {
		downgraded = append(downgraded, CapabilityReasoningReplay)
	}
	return CandidateAdmissionResult{
		Kind:            NegotiationDowngrade,
		Capability:      NegotiationResult{Kind: NegotiationDowngrade, Downgraded: downgraded},
		Transport:       recheck.Transport,
		Requirements:    recheck.Requirements,
		ProjectionError: nil,
	}
}

func capabilitySlice(caps BackendCaps) []Capability {
	if len(caps) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(caps))
	for c := range caps {
		out = append(out, c)
	}
	return out
}
