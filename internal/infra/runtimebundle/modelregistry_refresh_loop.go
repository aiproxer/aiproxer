package runtimebundle

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

// runModelRegistryRefreshLoop runs a ticker-driven refresh loop until ctx is
// canceled. The loop body is owned by [startOwnedLoop], which establishes ledger
// ownership before application work begins.
func runModelRegistryRefreshLoop(ctx context.Context, rt *modelregistry.Runtime, interval time.Duration) {
	if ctx == nil || rt == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.RunRefresh(ctx)
		}
	}
}
