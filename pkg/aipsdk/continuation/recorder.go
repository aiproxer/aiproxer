package continuation

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Recorder accepts incremental terminal output for one reserved response ID.
type Recorder interface {
	RecordTerminal(ctx context.Context, record ContinuationRecord) error
}

// StreamObserver is a best-effort canonical-stream observation seam. Observe
// must not return an error: persistence is intentionally outside downstream
// commitment and failover decisions.
type StreamObserver interface {
	Observe(ctx context.Context, event aipapi.Event)
	Close() error
}

// TerminalRecorder adapts a Store to the Recorder port for one response lifecycle.
type TerminalRecorder struct {
	Store Store
}

// RecordTerminal validates terminal state and persists through the store.
func (r TerminalRecorder) RecordTerminal(ctx context.Context, record ContinuationRecord) error {
	if r.Store == nil {
		return ErrPreviousResponseNotFound
	}
	if !record.Terminal {
		return ErrRecordNotReady
	}
	if EffectiveStatus(record) == RecordStatusFailed {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.Store.Delete(cleanupCtx, record.Scope, record.ID)
		return ErrRecordNotEligible
	}
	if err := r.Store.PutTerminal(ctx, record); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.Store.Delete(cleanupCtx, record.Scope, record.ID)
		return err
	}
	return nil
}
