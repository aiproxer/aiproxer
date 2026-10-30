package traffic

import (
	"context"
	"time"
)

// PortBundle is the raw/redactor/observer triple used at each traffic leg (design sections 10-11).
// [Emit] maps [CaptureMeta] and the leg/protocol/content-type arguments into [Observation] only;
// it does not attach transport handles or provider-specific values beyond those string fields.
type PortBundle struct {
	Raw RawCaptureSink
	Obs Observer
	Red []Redactor
}

// EmitIsNoop reports whether [PortBundle.Emit] returns without doing work.
// Hot-path callers (e.g. per-stream event encoding) can use this to skip expensive payload preparation.
// Disabled raw ports and [NoopObserver] match what [Emit] effectively does without touching payloads.
func (p PortBundle) EmitIsNoop() bool {
	if len(p.Red) > 0 {
		return false
	}
	rawNoop := p.Raw == nil
	if !rawNoop {
		if _, disabled := p.Raw.(DisabledRawCapture); disabled {
			rawNoop = true
		}
	}
	if !rawNoop {
		return false
	}
	obsNoop := p.Obs == nil
	if !obsNoop {
		if _, noop := p.Obs.(NoopObserver); noop {
			obsNoop = true
		}
	}
	return obsNoop
}

// Emit runs privileged raw capture, redactors, then the general observer (fail-open on observer errors).
// A zero or empty bundle is a no-op. The observer sees only metadata copied from meta plus leg,
// protocol, contentType, redacted body, and a recorded timestamp—see [Observation] (task 5.2).
func (p PortBundle) Emit(ctx context.Context, leg Leg, meta CaptureMeta, protocol, contentType string, payload []byte) {
	if p.EmitIsNoop() {
		return
	}
	if p.Raw != nil {
		if _, disabled := p.Raw.(DisabledRawCapture); !disabled {
			_ = p.Raw.WriteRaw(ctx, leg, meta, payload)
		}
	}
	obs := p.Obs
	if obs == nil {
		obs = NoopObserver{}
	}
	out := ApplyRedactors(ctx, leg, meta, payload, p.Red)
	_ = obs.OnObservation(ctx, Observation{
		Leg:         leg,
		TraceID:     meta.TraceID,
		ALegID:      meta.ALegID,
		BLegID:      meta.BLegID,
		PrincipalID: meta.PrincipalID,
		SessionID:   meta.SessionID,
		AttemptSeq:  meta.AttemptSeq,
		BackendID:   meta.BackendID,
		FrontendID:  meta.FrontendID,
		Protocol:    protocol,
		ContentType: contentType,
		Body:        out,
		Scope:       meta.Scope.Clone(),
		RecordedAt:  time.Now(),
	})
}
