package streamrecovery_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPolicy_AllowPostOutputContinuation_PostOutputEOF(t *testing.T) {
	t.Parallel()

	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:                     true,
		EmitWarning:                 true,
		AllowPostOutputContinuation: true,
	}, time.Unix(1, 0))

	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))

	if dec.Kind != streamrecovery.DecisionContinuePostOutput {
		t.Fatalf("expected DecisionContinuePostOutput, got %s", dec.Kind)
	}
	if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
		t.Fatalf("post-output decision must never have retry/replacement semantics")
	}
	if dec.Kind == streamrecovery.DecisionFinishPostOutput {
		t.Fatalf("post-output continuation mode must not produce synthetic finish")
	}
	if dec.Reason == "" {
		t.Fatalf("expected non-empty reason")
	}
	if !errors.Is(dec.Err, io.EOF) {
		t.Fatalf("expected io.EOF error, got %v", dec.Err)
	}
	if dec.Finish.Kind != "" {
		t.Fatalf("expected no Finish event, got %#v", dec.Finish)
	}
	if dec.Warning.Kind != "" {
		t.Fatalf("expected no Warning event on continuation, got %#v", dec.Warning)
	}
}

func TestPolicy_AllowPostOutputContinuation_PostOutputGenericError(t *testing.T) {
	t.Parallel()

	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:                     true,
		EmitWarning:                 true,
		AllowPostOutputContinuation: true,
	}, time.Unix(1, 0))

	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

	upstreamErr := errors.New("upstream connection reset")
	dec := p.DecideEOF(upstreamErr, time.Unix(3, 0))

	if dec.Kind != streamrecovery.DecisionContinuePostOutput {
		t.Fatalf("expected DecisionContinuePostOutput, got %s", dec.Kind)
	}
	if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
		t.Fatalf("post-output decision must never have retry/replacement semantics")
	}
	if dec.Reason == "" {
		t.Fatalf("expected non-empty reason")
	}
	if !errors.Is(dec.Err, upstreamErr) {
		t.Fatalf("expected upstream error, got %v", dec.Err)
	}
	if dec.Finish.Kind != "" {
		t.Fatalf("expected no Finish event, got %#v", dec.Finish)
	}
	if dec.Warning.Kind != "" {
		t.Fatalf("expected no Warning event on continuation, got %#v", dec.Warning)
	}
}

func TestPolicy_AllowPostOutputContinuation_PostOutputIdleTimeout(t *testing.T) {
	t.Parallel()

	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:                     true,
		IdleTimeout:                 45 * time.Second,
		GracePeriod:                 3 * time.Second,
		EmitWarning:                 true,
		AllowPostOutputContinuation: true,
	}, time.Unix(1, 0))

	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(10, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(10, 0))

	dec := p.DecideIdle(time.Unix(58, 0))

	if dec.Kind != streamrecovery.DecisionContinuePostOutput {
		t.Fatalf("expected DecisionContinuePostOutput, got %s", dec.Kind)
	}
	if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
		t.Fatalf("post-output decision must never have retry/replacement semantics")
	}
	if dec.Reason == "" {
		t.Fatalf("expected non-empty reason")
	}
	if dec.Finish.Kind != "" {
		t.Fatalf("expected no Finish event, got %#v", dec.Finish)
	}
	if dec.Warning.Kind != "" {
		t.Fatalf("expected no Warning event on continuation, got %#v", dec.Warning)
	}
}

func TestPolicy_AllowPostOutputContinuation_PreOutputInterruptionUntouched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(p *streamrecovery.Policy) streamrecovery.Decision
	}{
		{
			name: "EOF before output",
			call: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideEOF(io.EOF, time.Unix(3, 0))
			},
		},
		{
			name: "generic error before output",
			call: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideEOF(errors.New("upstream connection reset"), time.Unix(3, 0))
			},
		},
		{
			name: "idle timeout before output",
			call: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideIdle(time.Unix(58, 0))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				IdleTimeout:                 45 * time.Second,
				GracePeriod:                 3 * time.Second,
				AllowPostOutputContinuation: true,
			}, time.Unix(1, 0))

			p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}, time.Unix(10, 0))

			dec := tt.call(p)
			if dec.Kind != streamrecovery.DecisionRecoverPreOutput {
				t.Fatalf("pre-output failure must return DecisionRecoverPreOutput even when AllowPostOutputContinuation=true, got %s", dec.Kind)
			}
			if dec.Reason == "" {
				t.Fatalf("expected non-empty reason")
			}
			if dec.Err == nil {
				t.Fatalf("expected non-nil error for pre-output recovery")
			}
		})
	}
}

func TestPolicy_AllowPostOutputContinuation_CancellationSurfacesFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "context deadline exceeded", err: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				AllowPostOutputContinuation: true,
			}, time.Unix(1, 0))

			p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
			p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

			dec := p.DecideEOF(tt.err, time.Unix(3, 0))
			if dec.Kind != streamrecovery.DecisionSurfaceFailure {
				t.Fatalf("cancellation must return DecisionSurfaceFailure, got %s", dec.Kind)
			}
			if !errors.Is(dec.Err, tt.err) {
				t.Fatalf("expected %v, got %v", tt.err, dec.Err)
			}
		})
	}
}

func TestPolicy_AllowPostOutputContinuation_AlreadyFinishedResponsePassesThrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		observeEvent func(p *streamrecovery.Policy)
	}{
		{
			name: "finished via backend event",
			observeEvent: func(p *streamrecovery.Policy) {
				p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}, time.Unix(2, 0))
			},
		},
		{
			name: "finished via client event",
			observeEvent: func(p *streamrecovery.Policy) {
				p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}, time.Unix(2, 0))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				IdleTimeout:                 45 * time.Second,
				GracePeriod:                 3 * time.Second,
				AllowPostOutputContinuation: true,
			}, time.Unix(1, 0))

			p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(1, 500))
			tt.observeEvent(p)

			decEOF := p.DecideEOF(io.EOF, time.Unix(3, 0))
			if decEOF.Kind != streamrecovery.DecisionPassThrough {
				t.Fatalf("DecideEOF after finish must pass through, got %s", decEOF.Kind)
			}

			decErr := p.DecideEOF(errors.New("upstream err"), time.Unix(3, 0))
			if decErr.Kind != streamrecovery.DecisionPassThrough {
				t.Fatalf("DecideEOF with err after finish must pass through, got %s", decErr.Kind)
			}

			decIdle := p.DecideIdle(time.Unix(58, 0))
			if decIdle.Kind != streamrecovery.DecisionPassThrough {
				t.Fatalf("DecideIdle after finish must pass through, got %s", decIdle.Kind)
			}
		})
	}
}

func TestPolicy_AllowPostOutputContinuation_DisabledPolicyPassesThrough(t *testing.T) {
	t.Parallel()

	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:                     false,
		AllowPostOutputContinuation: true,
	}, time.Unix(1, 0))

	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

	decEOF := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if decEOF.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("disabled policy DecideEOF must pass through, got %s", decEOF.Kind)
	}

	decIdle := p.DecideIdle(time.Unix(58, 0))
	if decIdle.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("disabled policy DecideIdle must pass through, got %s", decIdle.Kind)
	}
}

func TestPolicy_AllowPostOutputContinuation_NilPolicyAndNilError(t *testing.T) {
	t.Parallel()

	var nilPolicy *streamrecovery.Policy
	decNilEOF := nilPolicy.DecideEOF(io.EOF, time.Unix(1, 0))
	if decNilEOF.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("nil policy DecideEOF must pass through, got %s", decNilEOF.Kind)
	}
	decNilIdle := nilPolicy.DecideIdle(time.Unix(1, 0))
	if decNilIdle.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("nil policy DecideIdle must pass through, got %s", decNilIdle.Kind)
	}

	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:                     true,
		IdleTimeout:                 45 * time.Second,
		GracePeriod:                 3 * time.Second,
		AllowPostOutputContinuation: true,
	}, time.Unix(1, 0))

	decNilErr := p.DecideEOF(nil, time.Unix(2, 0))
	if decNilErr.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("nil error DecideEOF must pass through, got %s", decNilErr.Kind)
	}

	decBeforeDeadline := p.DecideIdle(time.Unix(10, 0))
	if decBeforeDeadline.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("DecideIdle before deadline must pass through, got %s", decBeforeDeadline.Kind)
	}
}

func TestPolicy_AllowPostOutputContinuation_OutputCommitTracking(t *testing.T) {
	t.Parallel()

	committingEvents := []struct {
		name string
		ev   aipapi.Event
	}{
		{name: "text delta", ev: aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}},
		{name: "reasoning delta", ev: aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "x"}},
		{name: "reasoning opaque delta", ev: aipapi.Event{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking"}`)}},
		{name: "reasoning part", ev: aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{Text: "x"}}},
		{name: "tool call started", ev: aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "read"}},
		{name: "tool call args delta", ev: aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: "{}"}},
		{name: "assistant image ref", ev: aipapi.Event{Kind: aipapi.EventAssistantImageRef, AssistantRef: "img_1"}},
		{name: "assistant file ref", ev: aipapi.Event{Kind: aipapi.EventAssistantFileRef, AssistantRef: "file_1"}},
	}

	for _, tt := range committingEvents {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				AllowPostOutputContinuation: true,
			}, time.Unix(1, 0))

			p.ObserveClientEvent(tt.ev, time.Unix(2, 0))

			dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
			if dec.Kind != streamrecovery.DecisionContinuePostOutput {
				t.Fatalf("committing event %s must lead to DecisionContinuePostOutput, got %s", tt.name, dec.Kind)
			}
		})
	}

	nonCommittingEvents := []struct {
		name string
		ev   aipapi.Event
	}{
		{name: "response started", ev: aipapi.Event{Kind: aipapi.EventResponseStarted}},
		{name: "message started", ev: aipapi.Event{Kind: aipapi.EventMessageStarted}},
		{name: "reasoning signature delta", ev: aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"}},
	}

	for _, tt := range nonCommittingEvents {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				AllowPostOutputContinuation: true,
			}, time.Unix(1, 0))

			p.ObserveClientEvent(tt.ev, time.Unix(2, 0))

			dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
			if dec.Kind != streamrecovery.DecisionRecoverPreOutput {
				t.Fatalf("non-committing event %s must lead to DecisionRecoverPreOutput, got %s", tt.name, dec.Kind)
			}
		})
	}
}

func TestPolicy_AllowPostOutputContinuation_False_PreservesLegacyFinishPostOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		emitWarning     bool
		trigger         func(p *streamrecovery.Policy) streamrecovery.Decision
		expectedWarning bool
	}{
		{
			name:        "EOF with warning enabled",
			emitWarning: true,
			trigger: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideEOF(io.EOF, time.Unix(3, 0))
			},
			expectedWarning: true,
		},
		{
			name:        "EOF with warning disabled",
			emitWarning: false,
			trigger: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideEOF(io.EOF, time.Unix(3, 0))
			},
			expectedWarning: false,
		},
		{
			name:        "generic error with warning enabled",
			emitWarning: true,
			trigger: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideEOF(errors.New("connection reset"), time.Unix(3, 0))
			},
			expectedWarning: true,
		},
		{
			name:        "idle timeout with warning enabled",
			emitWarning: true,
			trigger: func(p *streamrecovery.Policy) streamrecovery.Decision {
				return p.DecideIdle(time.Unix(58, 0))
			},
			expectedWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := streamrecovery.NewPolicy(streamrecovery.Config{
				Enabled:                     true,
				IdleTimeout:                 45 * time.Second,
				GracePeriod:                 3 * time.Second,
				EmitWarning:                 tt.emitWarning,
				AllowPostOutputContinuation: false,
			}, time.Unix(1, 0))

			p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
			p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

			dec := tt.trigger(p)
			if dec.Kind != streamrecovery.DecisionFinishPostOutput {
				t.Fatalf("expected DecisionFinishPostOutput when AllowPostOutputContinuation=false, got %s", dec.Kind)
			}
			if dec.Finish.Kind != aipapi.EventResponseFinished || dec.Finish.FinishReason != "proxy_stream_recovered" {
				t.Fatalf("expected Finish event with finishReason 'proxy_stream_recovered', got %#v", dec.Finish)
			}
			if tt.expectedWarning {
				if dec.Warning.Kind != aipapi.EventWarning || dec.Warning.WarningCode != "proxy_stream_recovery" {
					t.Fatalf("expected Warning event, got %#v", dec.Warning)
				}
			} else {
				if dec.Warning.Kind != "" {
					t.Fatalf("expected no Warning event, got %#v", dec.Warning)
				}
			}
		})
	}
}

func TestPolicy_PostOutputDecisionNeverHasRetryOrReplacementSemantics(t *testing.T) {
	t.Parallel()

	for _, allowContinuation := range []bool{false, true} {
		p := streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:                     true,
			IdleTimeout:                 45 * time.Second,
			GracePeriod:                 3 * time.Second,
			AllowPostOutputContinuation: allowContinuation,
		}, time.Unix(1, 0))

		p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))
		p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}, time.Unix(2, 0))

		decEOF := p.DecideEOF(io.EOF, time.Unix(3, 0))
		if decEOF.Kind == streamrecovery.DecisionRecoverPreOutput {
			t.Fatalf("post-output EOF must never return DecisionRecoverPreOutput (allowContinuation=%v)", allowContinuation)
		}

		decErr := p.DecideEOF(errors.New("socket broken"), time.Unix(3, 0))
		if decErr.Kind == streamrecovery.DecisionRecoverPreOutput {
			t.Fatalf("post-output error must never return DecisionRecoverPreOutput (allowContinuation=%v)", allowContinuation)
		}

		decIdle := p.DecideIdle(time.Unix(58, 0))
		if decIdle.Kind == streamrecovery.DecisionRecoverPreOutput {
			t.Fatalf("post-output idle must never return DecisionRecoverPreOutput (allowContinuation=%v)", allowContinuation)
		}
	}
}
