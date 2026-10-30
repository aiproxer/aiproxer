package observers

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// PolicyObserverAdapter records normalized policy decisions into the control-
// plane recorder and is always fail-open (design "Source Event Mapping"; task
// 4.2, requirements 1.5, 3.4, 3.5, 3.7, 5.2, 5.5, 8.2, 8.4, 8.5).
//
// Behavior:
//   - The original policy decision outcome is never changed. The adapter only
//     records a normalized copy of the decision (requirement 1.5, 10.7).
//   - Recording is always best-effort via [controlplane.RecorderService.RecordBestEffort]:
//     observer failures update degraded status only and never change request
//     execution (requirement 5.2, 8.4). The adapter always returns nil so it
//     cannot introduce new errors into an existing observer chain.
//   - When the control-plane capability is disabled (recorder returns
//     [controlplane.ErrDisabled]) the adapter is a no-op (requirement 8.4).
//   - Normalization errors (unsafe free text) are swallowed to preserve fail-
//     open behavior; they never reach the observer chain.
//
// The adapter is a leaf observer intended to be composed into an existing
// [policydecision.ChainObserver] by the composition root; it does not wrap or
// replace existing observers.
type PolicyObserverAdapter struct {
	normalizer *controlplane.Normalizer
	recorder   *controlplane.RecorderService
}

// PolicyObserverAdapterConfig configures a [PolicyObserverAdapter].
type PolicyObserverAdapterConfig struct {
	Normalizer *controlplane.Normalizer
	// Recorder appends control-plane events. It is concrete because this adapter
	// requires RecordBestEffort; may be nil to disable recording.
	Recorder *controlplane.RecorderService
}

// NewPolicyObserverAdapter returns a PolicyObserverAdapter.
func NewPolicyObserverAdapter(cfg PolicyObserverAdapterConfig) *PolicyObserverAdapter {
	return &PolicyObserverAdapter{normalizer: cfg.Normalizer, recorder: cfg.Recorder}
}

// OnPolicyDecision implements [policydecision.Observer] by recording a normalized
// copy of the decision. It is always fail-open and never returns an error.
func (a *PolicyObserverAdapter) OnPolicyDecision(ctx context.Context, record policydecision.Record) error {
	if a.recorder == nil || a.normalizer == nil {
		return nil
	}
	normEv, err := a.normalizer.FromPolicyDecision(record)
	if err != nil {
		return nil
	}
	_, recErr := a.recorder.RecordBestEffort(ctx, normEv)
	ignoreBestEffortRecorderErr(recErr)
	return nil
}

var _ policydecision.Observer = (*PolicyObserverAdapter)(nil)
