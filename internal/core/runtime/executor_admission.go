package runtime

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type candidateAdmissionOutcome struct {
	admitRes      aipapi.CandidateAdmissionResult
	facts         modelcatalog.EffectiveFacts
	transportMode aipapi.TransportMode
}

func (e *Executor) evaluateCandidateAdmission(
	ctx context.Context,
	traceID string,
	attempt aipapi.Call,
	c routing.AttemptCandidate,
	be execbackend.Backend,
	failoverReq capabilities.FailoverRequirementSet,
) candidateAdmissionOutcome {
	facts := e.effectiveFactsForAttempt(ctx, be, attempt, c)
	replay := execbackend.EffectiveReplaySupport(ctx, be, attempt, c)
	transportCaps := e.transportCapsForAttempt(ctx, be, attempt, c)
	target := aipapi.LegacyProjectionTargetFromCaps(facts.EffectiveCaps, replay)
	target.SupportedExtensions = append([]aipapi.ExtensionRequirement(nil), execbackend.EffectiveDialectSupport(ctx, be, attempt, c).ExtensionTypes...)
	frozen := failoverReq.Required
	admitRes := capabilities.AdmitCandidate(ctx, attempt, attempt.Invocation, c, capabilities.CandidateFacts{
		Caps:               facts.EffectiveCaps,
		TransportCaps:      transportCaps,
		ReplaySupport:      replay,
		DialectSupport:     execbackend.EffectiveDialectSupport(ctx, be, attempt, c),
		ProjectionTarget:   target,
		FrozenRequirements: &frozen,
		AttemptPolicy:      e.attemptPolicy(),
	})
	mode := admitRes.Transport.Selected
	if mode == "" {
		mode = admitRes.Transport.Mode
	}
	return candidateAdmissionOutcome{
		admitRes:      admitRes,
		facts:         facts,
		transportMode: mode,
	}
}

func (e *Executor) noteCandidateAdmissionReject(
	ctx context.Context,
	traceID string,
	affinityKey affinity.Key,
	affinitySet bool,
	c routing.AttemptCandidate,
	stickyBackendID string,
	stickyBinding bool,
	out candidateAdmissionOutcome,
	phase string,
	failures *candidateFailureHistory,
) {
	reason := "admission_reject"
	if out.admitRes.Transport.Kind == aipapi.NegotiationReject {
		reason = "transport_reject"
		e.recordTransportNegotiation(out.admitRes.Transport.Operation, out.admitRes.Transport.Mode, "reject")
		if failures != nil {
			failures.TransportReject = out.admitRes.Transport
		}
	} else if out.admitRes.Capability.Kind == aipapi.NegotiationReject {
		reason = "capability_reject"
		if failures != nil {
			failures.CapabilityReject = out.admitRes.Capability
		}
	} else if out.admitRes.Requirements.Kind == aipapi.NegotiationReject {
		reason = "requirements_reject"
		if failures != nil {
			failures.AdmissionErr = out.admitRes.Requirements.Err()
		}
	} else if out.admitRes.ProjectionError != nil {
		reason = "projection_reject"
		if failures != nil {
			failures.AdmissionErr = out.admitRes.ProjectionError
		}
	}
	if stickyBinding && c.Primary.Backend == stickyBackendID {
		e.clearAffinityBinding(ctx, traceID, affinityKey, affinitySet, reason)
	}
	diag.LogDecision(
		ctx, e.Log, reason, diag.AttrOpts{CallID: traceID},
		slog.String("decision", "exclude_candidate"),
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("phase", phase),
	)
	cat := catalogRouteTraceIfEnabled(e, out.facts, out.admitRes.Capability, nil, false)
	e.notePlanCandidate(ctx, traceID, c.Key, cat)
	if failures != nil {
		failures.TransformExcludes.noteOther()
	}
}

// attemptPolicy resolves the per-attempt policy for unrepresentable
// client-sent reasoning. Empty resolves to the standard-distribution default
// (log_skip); explicit reject keeps today's hard refusal. Unknown values fail
// closed to reject (config validation rejects them at load).
func (e *Executor) attemptPolicy() aipapi.AttemptPolicy {
	if e == nil {
		return aipapi.DefaultAttemptPolicy
	}
	switch aipapi.AttemptPolicy(strings.TrimSpace(strings.ToLower(string(e.AttemptPolicy)))) {
	case aipapi.AttemptLogSkip, "":
		return aipapi.AttemptLogSkip
	case aipapi.AttemptReject:
		return aipapi.AttemptReject
	default:
		return aipapi.AttemptReject
	}
}

// stripDowngradedReasoningReplay removes unrepresentable client-sent reasoning
// from the per-attempt clone after a reasoning_replay downgrade and records
// one skip per stripped attempt. It never touches the shared call: callers
// pass the attempt clone. A second call on an already-stripped attempt is a
// no-op and records nothing, so restored and client-sent reasoning share one
// accounting entry per attempt.
func (e *Executor) stripDowngradedReasoningReplay(ctx context.Context, traceID string, attempt *aipapi.Call, c routing.AttemptCandidate, res aipapi.NegotiationResult) {
	if res.Kind != aipapi.NegotiationDowngrade || !slices.Contains(res.Downgraded, aipapi.CapabilityReasoningReplay) {
		return
	}
	if !aipapi.StripReasoningForAttempt(attempt) {
		return
	}
	e.noteReasoningReplaySkip(ctx, traceID, c, e.attemptPolicy())
}

// noteReasoningReplaySkip records one bounded skip observation plus one
// content-safe decision entry per stripped attempt. Only the candidate key,
// backend, and policy are logged: never reasoning bodies, opaque envelopes,
// signatures, or session partitions.
func (e *Executor) noteReasoningReplaySkip(ctx context.Context, traceID string, c routing.AttemptCandidate, policy aipapi.AttemptPolicy) {
	backend := strings.TrimSpace(c.Primary.Backend)
	e.recordReasoningReplaySkipped(backend)
	diag.LogDecision(
		ctx, e.Log, "reasoning_replay_skipped", diag.AttrOpts{CallID: traceID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", backend),
		slog.String("policy", string(policy)),
	)
}
