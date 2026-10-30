package auth

import (
	"context"
	"sync"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// EventDispatcher delivers auth and session-start events to an [EventSink] and applies
// [EventFailurePolicy] when the sink returns an error.
//
// Event DTO additions remain subject to non-secret classification; see [EventSink] and
// [sdkauth.AuthDecisionEvent] / [sdkauth.SessionStartEvent] package docs.
type EventDispatcher struct {
	mu     sync.Mutex
	sink   EventSink
	policy EventFailurePolicy
}

// NewEventDispatcher constructs a dispatcher. sink may be nil (explicit no delivery).
func NewEventDispatcher(sink EventSink, policy EventFailurePolicy) *EventDispatcher {
	return &EventDispatcher{sink: sink, policy: policy}
}

// DispatchAuthDecision invokes the sink when non-nil; applies failure policy on sink error.
// Challenge summary is sanitized before any sink (including custom sinks) for defense in depth.
func (d *EventDispatcher) DispatchAuthDecision(ctx context.Context, ev sdkauth.AuthDecisionEvent) error {
	if d == nil || d.sink == nil {
		return nil
	}
	ev2 := ev
	ev2.ChallengeSummary = sdkauth.SanitizePublicChallengeSummary(ev.ChallengeSummary, "", sdkauth.PublicChallengeSummaryMaxRunes)
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.sink.OnAuthDecision(ctx, ev2)
	return d.handleSinkError(err)
}

// DispatchSessionStart invokes the sink when non-nil; applies failure policy on sink error.
func (d *EventDispatcher) DispatchSessionStart(ctx context.Context, ev sdkauth.SessionStartEvent) error {
	if d == nil || d.sink == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.sink.OnSessionStart(ctx, ev)
	return d.handleSinkError(err)
}

func (d *EventDispatcher) handleSinkError(err error) error {
	if err == nil {
		return nil
	}
	if d.policy == EventFailureFailClosed {
		return err
	}
	return nil
}
