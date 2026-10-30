package observers

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// AuthSinkAdapter fans auth-decision and session-start events out to the
// existing auth [auth.EventSink] and the core control-plane recorder (design
// "Source Event Mapping"; task 4.1, requirements 1.1, 1.2, 3.1, 4.1, 5.2, 5.4,
// 8.4).
//
// Behavior:
//   - The existing delegate sink remains authoritative for auth event delivery.
//     When the delegate is nil the adapter records only and returns nil.
//   - Recording happens after the source event is constructed and before the
//     delegate's fan-out result is returned (design "Source Event Mapping").
//   - Recording failures are best-effort by default: the recorder degrades
//     capability status and the adapter continues to the delegate so the auth
//     path is preserved.
//   - Fail-closed applies only when the existing auth event delivery policy is
//     fail-closed (FailClosed=true) AND the control-plane recorder returns a
//     required pre-work failure (recorder.Record surfaces a non-disabled error
//     only for required pre-work + required category). In that case the adapter
//     returns the recording error before upstream work and skips the delegate,
//     so protected upstream work does not begin (requirement 5.4).
//   - When the control-plane capability is disabled (recorder returns
//     [controlplane.ErrDisabled]) the adapter delegates unchanged; existing
//     sink behavior is not affected (requirement 8.4).
//   - Normalization errors (unsafe free text) are treated as best-effort: the
//     adapter skips recording and delegates so the auth path is not broken by a
//     misclassified field. They never fail closed.
//
// The adapter performs no I/O beyond the recorder call and starts no goroutines.
type AuthSinkAdapter struct {
	delegate   auth.EventSink
	normalizer *controlplane.Normalizer
	recorder   *controlplane.RecorderService
	failClosed bool
}

// AuthSinkAdapterConfig configures an [AuthSinkAdapter].
type AuthSinkAdapterConfig struct {
	// Delegate is the existing auth event sink. May be nil to record only.
	Delegate auth.EventSink
	// Normalizer converts auth events into control-plane events. Required.
	Normalizer *controlplane.Normalizer
	// Recorder appends control-plane events. May be nil to disable recording
	// (the adapter becomes a pass-through to Delegate).
	Recorder *controlplane.RecorderService
	// FailClosed mirrors the existing auth event delivery policy
	// (auth.EventFailureFailClosed). When true, required pre-work recording
	// failures fail closed before upstream work.
	FailClosed bool
}

// NewAuthSinkAdapter returns an AuthSinkAdapter that fans auth events out to the
// delegate and the control-plane recorder.
func NewAuthSinkAdapter(cfg AuthSinkAdapterConfig) *AuthSinkAdapter {
	return &AuthSinkAdapter{
		delegate:   cfg.Delegate,
		normalizer: cfg.Normalizer,
		recorder:   cfg.Recorder,
		failClosed: cfg.FailClosed,
	}
}

// OnAuthDecision implements [auth.EventSink] by recording the decision into the
// control-plane recorder and then delegating to the existing sink.
func (a *AuthSinkAdapter) OnAuthDecision(ctx context.Context, ev sdkauth.AuthDecisionEvent) error {
	if a.recorder != nil && a.normalizer != nil {
		if normEv, err := a.normalizer.FromAuthDecision(ev); err == nil {
			if _, recErr := a.recorder.Record(ctx, normEv); recErr != nil && !errors.Is(recErr, controlplane.ErrDisabled) {
				if a.failClosed {
					return recErr
				}
			}
		}
	}
	if a.delegate == nil {
		return nil
	}
	return a.delegate.OnAuthDecision(ctx, ev)
}

// OnSessionStart implements [auth.EventSink] by recording the session-start into
// the control-plane recorder and then delegating to the existing sink.
func (a *AuthSinkAdapter) OnSessionStart(ctx context.Context, ev sdkauth.SessionStartEvent) error {
	if a.recorder != nil && a.normalizer != nil {
		if normEv, err := a.normalizer.FromSessionStart(ev); err == nil {
			if _, recErr := a.recorder.Record(ctx, normEv); recErr != nil && !errors.Is(recErr, controlplane.ErrDisabled) {
				if a.failClosed {
					return recErr
				}
			}
		}
	}
	if a.delegate == nil {
		return nil
	}
	return a.delegate.OnSessionStart(ctx, ev)
}

var _ auth.EventSink = (*AuthSinkAdapter)(nil)
