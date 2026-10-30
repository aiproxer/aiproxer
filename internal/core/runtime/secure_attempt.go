package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func buildAttemptTrace(
	st execctx.SecureSessionTurn,
	aLegID string,
	bleg b2bua.BLegRecord,
	cand routing.AttemptCandidate,
	openCall aipapi.Call,
	startedAt time.Time,
) domain.AttemptTrace {
	reqModel := strings.TrimSpace(openCall.Route.Selector)
	if reqModel == "" {
		reqModel = strings.TrimSpace(cand.Primary.Model)
	}
	settings := domain.AttemptSettings{
		ReasoningEffort: strings.TrimSpace(openCall.Options.ReasoningEffort),
		Verbosity:       string(openCall.Options.Verbosity),
		Streaming:       true,
	}
	if openCall.Options.Temperature != nil {
		t := *openCall.Options.Temperature
		settings.Temperature = &t
	}
	if openCall.Options.MaxOutputTokens != nil {
		m := *openCall.Options.MaxOutputTokens
		settings.MaxTokens = &m
	}
	return domain.AttemptTrace{
		SessionID:       st.SessionID,
		TurnID:          st.TurnID,
		ALegID:          strings.TrimSpace(aLegID),
		BLegID:          strings.TrimSpace(bleg.BLegID),
		AttemptSeq:      bleg.Seq,
		RequestedModel:  reqModel,
		RequestedAlias:  "",
		ResolvedBackend: strings.TrimSpace(cand.Primary.Backend),
		ResolvedModel:   strings.TrimSpace(cand.Primary.Model),
		RouteSource:     "routing_candidate",
		RouteReason:     strings.TrimSpace(cand.Key),
		Settings:        settings,
		StartedAt:       startedAt,
	}
}

func secureAttemptOutcome(
	st execctx.SecureSessionTurn,
	bleg b2bua.BLegRecord,
	p recordAttemptParams,
	endedAt time.Time,
) domain.AttemptOutcome {
	out := domain.AttemptOutcome{
		SessionID:   st.SessionID,
		TurnID:      st.TurnID,
		BLegID:      strings.TrimSpace(bleg.BLegID),
		EndedAt:     endedAt,
		DebugReason: strings.TrimSpace(p.Reason),
	}
	switch p.Outcome {
	case aipapi.AttemptSuccess:
		out.Success = true
		out.SurfaceState = domain.SurfaceSurfaced
	case aipapi.AttemptSwallowedFailure:
		out.Success = false
		out.SurfaceState = domain.SurfaceSwallowed
	case aipapi.AttemptSurfacedFailure:
		out.Success = false
		out.SurfaceState = domain.SurfaceSurfaced
	case aipapi.AttemptCancelled:
		out.Success = false
		out.SurfaceState = domain.SurfaceFailed
		if strings.TrimSpace(out.DebugReason) == "" {
			out.DebugReason = "cancelled"
		}
	default:
		out.Success = false
		out.SurfaceState = domain.SurfaceFailed
	}
	applyErrDetailToOutcome(&out, p.DetailErr)
	if p.OpenFailed {
		applyOpenFailure(&out, p.DetailErr, p.Reason)
	}
	if p.BindFailed {
		applyBindFailure(&out)
	}
	return out
}

func applyErrDetailToOutcome(out *domain.AttemptOutcome, err error) {
	if err == nil || out == nil {
		return
	}
	if uf, ok := errors.AsType[*aipapi.UpstreamFailureError](err); ok {
		if strings.TrimSpace(uf.CandidateKey) != "" {
			out.ErrorCode = strings.TrimSpace(uf.CandidateKey)
		} else {
			out.ErrorCode = "upstream_failure"
		}
		if uf.Phase == aipapi.PhasePostOutput {
			out.ProviderStatus = "post_output"
		} else {
			out.ProviderStatus = "pre_output"
		}
	}
	if errors.Is(err, context.Canceled) {
		out.TimeoutClass = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		out.TimeoutClass = "deadline"
	}
}

// applyOpenFailure completes the outcome of a failed backend Open. An Open
// that failed is pre-output whatever phase the backend reported, carries a
// stable failure category when the error names none, and keeps the failure
// detail beside the generic classification reason.
func applyOpenFailure(oc *domain.AttemptOutcome, err error, reason string) {
	oc.ProviderStatus = "pre_output"
	if uf, ok := errors.AsType[*aipapi.UpstreamFailureError](err); ok && uf.Reason == nilStreamOpenReason {
		// The category outranks the candidate key, which the trace already names.
		oc.ErrorCode = "backend_open_nil_stream"
	}
	if oc.ErrorCode == "" {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			oc.ErrorCode = "backend_open_deadline"
		case errors.Is(err, context.Canceled):
			oc.ErrorCode = "backend_open_canceled"
		default:
			oc.ErrorCode = "backend_open_error"
		}
	}
	if detail := attemptReasonDetail(err); detail != "" && !strings.Contains(reason, detail) {
		oc.DebugReason = strings.TrimSpace(reason + ": " + detail)
	}
}

// applyBindFailure completes the outcome of an evidence-binding failure after
// a successful Open: pre-output, but not a failed open.
func applyBindFailure(oc *domain.AttemptOutcome) {
	oc.ProviderStatus = "pre_output"
	oc.ErrorCode = "backend_evidence_binding_failed"
}
