package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func (e *Executor) candidateAttemptMeta(ctx context.Context, rf requestFacts, attempt aipapi.Call, c routing.AttemptCandidate, be execbackend.Backend) request.AttemptMeta {
	meta := request.AttemptMeta{
		TraceID:         rf.traceID,
		ALegID:          rf.aLegID,
		CandidateKey:    c.Key,
		BackendID:       strings.TrimSpace(c.Primary.Backend),
		BackendPrefixes: execbackend.CloneBackendPrefixes(be),
		Model:           strings.TrimSpace(c.Primary.Model),
		ReplaySupport:   execbackend.EffectiveReplaySupport(ctx, be, attempt, c),
		// The attempt policy unifies restored and client-sent reasoning:
		// under log_skip, transforms withhold unrepresentable restores
		// instead of excluding the candidate.
		AttemptPolicy: e.attemptPolicy(),
		Scope:         rf.recvViews.Scope,
		Session: session.SessionView{
			AuthoritativeSessionID: strings.TrimSpace(attempt.Session.AuthoritativeSessionID),
			ClientSessionHint:      strings.TrimSpace(attempt.Session.ClientSessionID),
			ALegID:                 rf.aLegID,
			// Attempt views keep the turn's already-decided classification so a
			// per-candidate consumer never re-runs the classifier (requirement 4.2).
			Classification: rf.recvViews.Session.Classification,
		},
		Workspace: cloneWorkspaceView(rf.recvViews.Workspace),
	}
	if rf.recvViews.Session.AuthoritativeSessionID != "" || rf.recvViews.Session.ClientSessionHint != "" {
		meta.Session = cloneSessionView(rf.recvViews.Session)
		meta.Session.ALegID = rf.aLegID
	}
	return meta
}

func cloneSessionView(src session.SessionView) session.SessionView {
	src.Labels = maps.Clone(src.Labels)
	return src
}

func cloneWorkspaceView(src aipworkspace.WorkspaceView) aipworkspace.WorkspaceView {
	src.Markers = slices.Clone(src.Markers)
	src.Labels = maps.Clone(src.Labels)
	return src
}

func (e *Executor) noteAttemptTransformExclude(ctx context.Context, traceID string, c routing.AttemptCandidate, res extensions.AttemptTransformStageResult, failures *candidateFailureHistory) {
	diag.LogDecision(ctx, e.Log, "attempt_transform_exclude", diag.AttrOpts{CallID: traceID},
		slog.String("decision", "exclude_candidate"), slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend), slog.String("reason_code", res.ReasonCode),
		slog.String("provider_id", res.ProviderID))
	e.notePlanCandidate(ctx, traceID, c.Key, nil)
	if failures != nil && failures.TransformExcludes != nil {
		failures.TransformExcludes.noteTransform(res.ReasonCode)
	}
}

func pinCandidateRouteIdentity(attempt *aipapi.Call, baseline aipapi.Call) {
	if attempt != nil {
		attempt.Route = baseline.Route
	}
}

type postHookRederiveResult struct {
	excluded    bool
	facts       modelcatalog.EffectiveFacts
	preflight   accountingpreflight.Decision
	preflightOK bool
}

func (e *Executor) rederiveAfterRequestHooks(
	ctx context.Context,
	rf requestFacts,
	route routeFacts,
	attempt *aipapi.Call,
	c routing.AttemptCandidate,
	be execbackend.Backend,
	stickyBackendID string,
	stickyBinding bool,
	failures *candidateFailureHistory,
	parallel bool,
) (postHookRederiveResult, error) {
	var out postHookRederiveResult
	if attempt == nil {
		return out, fmt.Errorf("executor: nil attempt after hooks")
	}
	pinCandidateRouteIdentity(attempt, rf.baseline)
	if vErr := attempt.Validate(); vErr != nil {
		return out, fmt.Errorf("executor: post-hook validate: %w", vErr)
	}
	admitOut, admitPanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_candidate_admission",
		func() (candidateAdmissionOutcome, error) {
			return e.evaluateCandidateAdmission(ctx, rf.traceID, *attempt, c, be, capabilities.NewFailoverRequirementSet(*attempt)), nil
		},
	)
	if admitPanicErr != nil {
		if pe, ok := errors.AsType[*safety.PanicError](admitPanicErr); ok {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: rf.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(ctx, slog.LevelError, "isolated_panic_candidate_admission", attrs...)
			}
			diag.LogDecision(
				ctx, e.Log, "candidate_admission_panic_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			out.excluded = true
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
			return out, nil
		}
		return out, admitPanicErr
	}
	out.facts = admitOut.facts
	if admitOut.admitRes.Kind == aipapi.NegotiationReject {
		if !parallel {
			e.noteCandidateAdmissionReject(ctx, rf.traceID, route.affinityKey, route.affinitySet, c, stickyBackendID, stickyBinding, admitOut, "post_request_hooks", failures)
		} else {
			reason := "admission_reject"
			if admitOut.admitRes.Transport.Kind == aipapi.NegotiationReject {
				reason = "transport_reject"
				if failures != nil {
					failures.TransportReject = admitOut.admitRes.Transport
				}
			} else if admitOut.admitRes.Capability.Kind == aipapi.NegotiationReject {
				reason = "capability_reject"
				if failures != nil {
					failures.CapabilityReject = admitOut.admitRes.Capability
				}
			} else if admitOut.admitRes.Requirements.Kind == aipapi.NegotiationReject {
				reason = "requirements_reject"
				if failures != nil {
					failures.AdmissionErr = admitOut.admitRes.Requirements.Err()
				}
			} else if admitOut.admitRes.ProjectionError != nil {
				reason = "projection_reject"
				if failures != nil {
					failures.AdmissionErr = admitOut.admitRes.ProjectionError
				}
			}
			if stickyBinding && c.Primary.Backend == stickyBackendID && failures != nil {
				failures.AffinityReset = reason
			}
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
		}
		out.excluded = true
		return out, nil
	}
	if admitOut.admitRes.Capability.Kind == aipapi.NegotiationDowngrade {
		// A bound feature guard may refuse the strip before it is applied,
		// so a governed call is never served with the effort negotiated
		// away (design §2; Requirement 1.4).
		if derr := e.checkAttemptDowngradeGuard(ctx, rf.baseline, rf.auxiliaryRole, c, attempt.Options.ReasoningEffort, admitOut.admitRes.Capability); derr != nil {
			return out, derr
		}
		aipapi.ApplyNegotiatedDowngrades(attempt, admitOut.admitRes.Capability)
		e.stripDowngradedReasoningReplay(ctx, rf.traceID, attempt, c, admitOut.admitRes.Capability)
	}
	attempt.Invocation.TransportMode = admitOut.admitRes.Transport.Selected
	facts := admitOut.facts
	if e != nil && e.EligibilityResolver != nil {
		facts = e.effectiveFactsForAttempt(ctx, be, *attempt, c)
		out.facts = facts
		d := e.EligibilityResolver.Check(ctx, c, *attempt, facts)
		if !d.IsEligible {
			if stickyBinding && c.Primary.Backend == stickyBackendID {
				if !parallel {
					e.clearAffinityBinding(ctx, rf.traceID, route.affinityKey, route.affinitySet, string(d.Reason))
				} else if failures != nil {
					failures.AffinityReset = string(d.Reason)
				}
			}
			if failures != nil && d.Reason == modelcatalog.EligibilityContextLimitExceeded {
				failures.ContextLimit = true
			}
			diag.LogDecision(ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", c.Key), slog.String("backend", c.Primary.Backend),
				slog.String("phase", "post_request_hooks"))
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
			out.excluded = true
			return out, nil
		}
	}
	if decision, ok := e.runPreflight(ctx, rf.traceID, *attempt, c, facts.Facts); ok {
		out.preflight, out.preflightOK = decision, true
		if !decision.Allowed {
			return out, fmt.Errorf("executor: token accounting preflight: %w", decision.Err)
		}
		if decision.AdjustedMaxOutputTokens != nil {
			adjusted := *decision.AdjustedMaxOutputTokens
			attempt.Options.MaxOutputTokens = &adjusted
		}
	}
	return out, nil
}

// controlToolActivation is the trusted, attempt-local owner of one projected
// proxy-owned model control tool. It exists exactly once per attempt, on the
// attempt transaction and then on the attempt session, and is discarded with the
// attempt. It is never a client-writable canonical extension, never serialized
// backend or frontend metadata, and never derived from a tool name the model
// emitted. Candidate, race, and replacement attempts therefore hold independent
// activations pinned to the generation each one was admitted with.
type controlToolActivation struct {
	// providerID is the generation-frozen identity read from cached validated
	// metadata. It never comes from a live provider call.
	providerID string
	// provider is the pinned generation provider instance.
	provider controltool.Provider
	// projection is the private, immutable approved projection. It is the only
	// source of proxy-owned control truth for the rest of the attempt.
	projection controltool.Projection
	// meta is the request/attempt provenance the control provider receives with a
	// completed call. It is captured here so later response handling cannot read
	// a mutable request or context for the same facts.
	meta controltool.Meta
}

// active reports whether this attempt approved a proxy-owned control tool. An
// inactive or absent activation never claims a control call.
func (a *controlToolActivation) active() bool {
	return a != nil && a.projection.Active()
}

// projectCandidateControlTool is the single generic, bounded control-projection
// stage. It runs after ordinary candidate request mutation and before the
// authoritative post-hook capability, context, and accounting rederive, so
// downstream sizing, requirements, and admission observe the real model-facing
// request.
//
// The stage is inert unless the request's own generation admitted a control-tool
// provider. Absence is the ordinary case and costs nothing: no provider method
// call, no additional capability resolution, and no projection clone. When a
// provider is present, its frozen identity is read from cached validated
// metadata, an unsuppressed provider is resolved once, and the pure projection
// decides eligibility from that candidate's own effective capabilities.
//
// An occupied plane without a frozen identity, an unavailable spec, and an
// isolated provider or capability panic all fail closed before the authoritative
// rederive, with bounded content-free errors.
func (e *Executor) projectCandidateControlTool(
	hookCtx context.Context,
	tx *attemptTx,
	be execbackend.Backend,
	attempt *aipapi.Call,
	c routing.AttemptCandidate,
) error {
	snap := extensions.RequestRuntimeSnapshotFromContext(hookCtx)
	if snap == nil {
		snap = e.RuntimeSnapshot
	}
	if snap == nil {
		return nil
	}
	provider := snap.ControlToolProvider()
	if provider == nil {
		return nil
	}
	providerID, ok := snap.ControlToolProviderIdentity()
	if !ok {
		return fmt.Errorf("executor: control tool provider identity unavailable: %w", controltool.ErrInvalidProvider)
	}
	if execctx.IsSuppressedPluginID(hookCtx, providerID) {
		return nil
	}

	// Eligibility input only. The authoritative rederive below revalidates the
	// projected candidate; this lookup exists because eligibility depends on the
	// candidate's actual backend capabilities.
	caps, err := safety.CallValue(safety.BoundaryBackend, "control_tool_candidate_caps", func() (aipapi.BackendCaps, error) {
		return e.effectiveFactsForAttempt(hookCtx, be, *attempt, c).EffectiveCaps, nil
	})
	if err != nil {
		return fmt.Errorf("executor: control tool candidate capabilities: %w", err)
	}

	projected, activation, err := extensions.ProjectControlTool(extensions.ControlToolProjectionRequest{
		ProviderID: providerID,
		Provider:   provider,
		Call:       *attempt,
		Caps:       caps,
	})
	if err != nil {
		return fmt.Errorf("executor: control tool projection: %w", err)
	}
	*attempt = projected
	tx.controlTool = &controlToolActivation{
		providerID: activation.ProviderID,
		provider:   activation.Provider,
		projection: activation.Projection,
		meta:       controlToolMeta(tx, c),
	}
	logControlToolProjection(hookCtx, e.Log, tx, c, activation)
	return nil
}

// controlToolMeta captures the request/attempt provenance handed to the control
// provider later in the attempt. It is built from frozen request facts, never
// from live context state.
func controlToolMeta(tx *attemptTx, c routing.AttemptCandidate) controltool.Meta {
	views := tx.reqFacts.recvViews
	meta := controltool.Meta{
		TraceID:      tx.reqFacts.traceID,
		ALegID:       tx.reqFacts.aLegID,
		BLegID:       tx.bleg.BLegID,
		CandidateKey: c.Key,
		AttemptSeq:   tx.bleg.Seq,
		Scope:        views.Scope.Clone(),
		Workspace:    cloneWorkspaceView(views.Workspace),
	}
	meta.Session = cloneSessionView(views.Session)
	meta.Session.ALegID = tx.reqFacts.aLegID
	return meta
}

// logControlToolProjection records the bounded activation outcome. Only the
// provider identity and the content-free reason code are logged; no instruction
// text, tool arguments, or raw identifiers are ever attached.
func logControlToolProjection(ctx context.Context, log *slog.Logger, tx *attemptTx, c routing.AttemptCandidate, activation extensions.ControlToolProjection) {
	diag.LogDecision(ctx, log, "control_tool_projection", diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("provider_id", activation.ProviderID),
		slog.String("reason_code", activation.Reason()))
}
