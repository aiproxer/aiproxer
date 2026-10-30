package runtimebundle

import (
	"context"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

// runModelCatalogRefreshLoop starts a ticker-driven refresh worker until ctx is canceled.
// wg tracks the goroutine for shutdown ordering with [modelcatalog.CatalogRuntime.Close].
func runModelCatalogRefreshLoop(ctx context.Context, cat *modelcatalog.CatalogRuntime, interval time.Duration, wg *sync.WaitGroup) {
	if interval <= 0 || cat == nil {
		return
	}
	wg.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cat.RunRefresh(ctx)
			}
		}
	})
}
