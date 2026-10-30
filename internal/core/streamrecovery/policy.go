package streamrecovery

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type Config struct {
	Enabled                     bool
	IdleTimeout                 time.Duration
	GracePeriod                 time.Duration
	EmitWarning                 bool
	PostOutputPolicy            string
	AllowPostOutputContinuation bool
}

type DecisionKind string

const (
	DecisionPassThrough        DecisionKind = "pass_through"
	DecisionRecoverPreOutput   DecisionKind = "recover_pre_output"
	DecisionFinishPostOutput   DecisionKind = "finish_post_output"
	DecisionSurfaceFailure     DecisionKind = "surface_failure"
	DecisionContinuePostOutput DecisionKind = "continue_post_output"
)

type Decision struct {
	Kind    DecisionKind
	Err     error
	Warning aipapi.Event
	Finish  aipapi.Event
	Reason  string
}

type Policy struct {
	cfg              Config
	lastActivityAt   time.Time
	clientCommitted  bool
	responseFinished bool
}

func NewPolicy(cfg Config, start time.Time) *Policy {
	return &Policy{cfg: cfg, lastActivityAt: start}
}

func (p *Policy) ObserveBackendEvent(ev aipapi.Event, at time.Time) {
	if p == nil {
		return
	}
	p.lastActivityAt = at
	if ev.Kind == aipapi.EventResponseFinished {
		p.responseFinished = true
	}
}

func (p *Policy) ObserveClientEvent(ev aipapi.Event, at time.Time) {
	if p == nil {
		return
	}
	p.lastActivityAt = at
	if aipapi.OutputCommitted(ev) {
		p.clientCommitted = true
	}
	if ev.Kind == aipapi.EventResponseFinished {
		p.responseFinished = true
	}
}

func (p *Policy) DecideEOF(err error, now time.Time) Decision {
	if p == nil || !p.cfg.Enabled || err == nil || p.responseFinished {
		return Decision{Kind: DecisionPassThrough, Err: err}
	}
	if !errors.Is(err, io.EOF) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Decision{Kind: DecisionSurfaceFailure, Err: err}
		}
		if p.clientCommitted {
			if p.cfg.AllowPostOutputContinuation {
				return Decision{Kind: DecisionContinuePostOutput, Err: err, Reason: "upstream error after client output"}
			}
			return p.finishPostOutput("upstream error after client output")
		}
		return Decision{Kind: DecisionRecoverPreOutput, Err: aipapi.RecoverablePreOutputError(err), Reason: "upstream error before response_finished"}
	}
	if p.clientCommitted {
		if p.cfg.AllowPostOutputContinuation {
			return Decision{Kind: DecisionContinuePostOutput, Err: io.EOF, Reason: "upstream EOF before response_finished"}
		}
		return p.finishPostOutput("upstream EOF before response_finished")
	}
	_ = now
	return Decision{Kind: DecisionRecoverPreOutput, Err: aipapi.RecoverablePreOutputError(io.EOF), Reason: "upstream EOF before response_finished"}
}

func (p *Policy) DecideIdle(now time.Time) Decision {
	if p == nil || !p.cfg.Enabled || p.responseFinished {
		return Decision{Kind: DecisionPassThrough}
	}
	deadline, ok := p.IdleDeadline()
	if !ok || now.Before(deadline) {
		return Decision{Kind: DecisionPassThrough}
	}
	if p.clientCommitted {
		if p.cfg.AllowPostOutputContinuation {
			return Decision{Kind: DecisionContinuePostOutput, Reason: "upstream idle timeout after client output"}
		}
		return p.finishPostOutput("upstream idle timeout after client output")
	}
	return Decision{Kind: DecisionRecoverPreOutput, Err: aipapi.RecoverablePreOutputError(context.DeadlineExceeded), Reason: "upstream idle timeout before response_finished"}
}

func (p *Policy) IdleDeadline() (time.Time, bool) {
	if p == nil || !p.cfg.Enabled || p.responseFinished {
		return time.Time{}, false
	}
	limit := p.cfg.IdleTimeout + p.cfg.GracePeriod
	if limit <= 0 || p.lastActivityAt.IsZero() {
		return time.Time{}, false
	}
	return p.lastActivityAt.Add(limit), true
}

func (p *Policy) finishPostOutput(reason string) Decision {
	dec := Decision{
		Kind:   DecisionFinishPostOutput,
		Reason: reason,
		Finish: aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "proxy_stream_recovered"},
	}
	if p.cfg.EmitWarning {
		dec.Warning = aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "proxy_stream_recovery",
			WarningMessage: reason,
		}
	}
	return dec
}
