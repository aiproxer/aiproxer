package auxiliary

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
)

// NewProductionBackgroundScheduler creates the process-lifetime bounded pool with generic scheduler bounds.
// Bounds come from the initial configuration and remain immutable across reload.
func NewProductionBackgroundScheduler(ctx context.Context, bounds auxreq.SchedulerConfig) *auxreq.BackgroundScheduler {
	scheduler, err := auxreq.NewBackgroundScheduler(ctx, nil, bounds)
	if err == nil {
		return scheduler
	}
	scheduler, _ = auxreq.NewBackgroundScheduler(ctx, nil, auxreq.SchedulerConfig{})
	return scheduler
}
