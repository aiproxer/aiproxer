package observers

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// UsageObserverAdapter records safe usage observations into the control-plane
// recorder and is always fail-open (design "Source Event Mapping"; task 4.2,
// requirements 1.4, 3.4, 3.5, 3.7, 5.2, 5.5, 8.2, 8.4, 8.5).
//
// Behavior:
//   - Raw usage JSON from the source event is never carried into the normalized
//     control-plane event; only typed safe token/cost fields and explicit
//     observed availability are projected (requirement 1.4, 9.2). The normalizer
//     enforces this.
//   - Recording is always best-effort via
//     [controlplane.RecorderService.RecordBestEffort] and the adapter always
//     returns nil so it cannot introduce new errors into an existing usage
//     observer chain (requirement 5.2, 8.4). When the existing chain returns an
//     error from another observer, that behavior is preserved because this
//     adapter never adds one.
//   - When the control-plane capability is disabled (recorder returns
//     [controlplane.ErrDisabled]) the adapter is a no-op (requirement 8.4).
//   - Normalization errors are swallowed to preserve fail-open behavior.
//
// The adapter is a leaf observer intended to be composed into an existing
// [usage.ChainObserver] by the composition root; it does not wrap or replace
// existing observers.
type UsageObserverAdapter struct {
	normalizer *controlplane.Normalizer
	recorder   *controlplane.RecorderService
}

// UsageObserverAdapterConfig configures a [UsageObserverAdapter].
type UsageObserverAdapterConfig struct {
	Normalizer *controlplane.Normalizer
	// Recorder appends control-plane events. It is concrete because this adapter
	// requires RecordBestEffort; may be nil to disable recording.
	Recorder *controlplane.RecorderService
}

// NewUsageObserverAdapter returns a UsageObserverAdapter.
func NewUsageObserverAdapter(cfg UsageObserverAdapterConfig) *UsageObserverAdapter {
	return &UsageObserverAdapter{normalizer: cfg.Normalizer, recorder: cfg.Recorder}
}

// OnUsage implements [usage.Observer] by recording a normalized usage event. It
// is always fail-open and never returns an error.
func (a *UsageObserverAdapter) OnUsage(ctx context.Context, ev usage.Event) error {
	if a.recorder == nil || a.normalizer == nil {
		return nil
	}
	normEv, err := a.normalizer.FromUsage(ev)
	if err != nil {
		return nil
	}
	_, recErr := a.recorder.RecordBestEffort(ctx, normEv)
	ignoreBestEffortRecorderErr(recErr)
	return nil
}

var _ usage.Observer = (*UsageObserverAdapter)(nil)
