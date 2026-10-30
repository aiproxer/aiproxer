package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
)

type startedModelCatalog struct {
	Runtime        *modelcatalog.CatalogRuntime
	closers        []func() error // PhaseClose: runtime/client disposal
	quiesceClosers []func() error // PhaseQuiesce: refresh cancel/wait
}

const (
	defaultModelCatalogCachePath = "data/model_catalog.json"
	defaultModelCatalogSourceURL = "https://models.dev/api.json"
	defaultModelCatalogInterval  = time.Hour
)

func startModelCatalog(parent context.Context, cfg *config.Config, upstream *http.Client) (*startedModelCatalog, error) {
	if !modelCatalogStartupNeeded(cfg) {
		return &startedModelCatalog{}, nil
	}
	catalogHTTP, closeCatalogHTTP := cloneCatalogHTTPClient(upstream)
	mc := cfg.ModelCatalog
	cachePath := strings.TrimSpace(mc.CachePath)
	if cachePath == "" {
		cachePath = filepath.Clean(defaultModelCatalogCachePath)
	}
	store := modelsdev.NewFileSnapshotStore(cachePath)
	interval, _ := mc.UpdateIntervalDuration()
	if interval <= 0 {
		interval = defaultModelCatalogInterval
	} else if interval < defaultModelCatalogInterval {
		interval = defaultModelCatalogInterval
	}
	fetchTimeout, _ := mc.FetchTimeoutDuration()
	sourceURL := strings.TrimSpace(mc.SourceURL)
	if sourceURL == "" {
		sourceURL = defaultModelCatalogSourceURL
	}
	src := modelsdev.NewHTTPSource(catalogHTTP, sourceURL, mc.ExternalUpdatesEnabled, fetchTimeout)
	cat := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  store,
	})
	if err := cat.Start(parent); err != nil {
		closeCatalogHTTP()
		_ = cat.Close()
		return nil, fmt.Errorf("runtimebundle: model catalog runtime start: %w", err)
	}
	if mc.ExternalUpdatesEnabled {
		cat.RunRefresh(parent)
	}

	out := &startedModelCatalog{
		Runtime: cat,
		closers: []func() error{func() error {
			err := cat.Close()
			closeCatalogHTTP()
			return err
		}},
	}

	var refreshWG sync.WaitGroup
	var refreshCleanup func()
	if mc.ExternalUpdatesEnabled && interval > 0 {
		refreshCtx, cancel := context.WithCancel(parent)
		runModelCatalogRefreshLoop(refreshCtx, cat, interval, &refreshWG)
		refreshCleanup = func() {
			cancel()
			refreshWG.Wait()
		}
		out.quiesceClosers = append(out.quiesceClosers, func() error {
			refreshCleanup()
			return nil
		})
	}

	return out, nil
}

func modelCatalogStartupNeeded(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.ModelCatalog.Enabled || cfg.ModelCatalog.ExternalUpdatesEnabled
}

func cloneCatalogHTTPClient(upstream *http.Client) (*http.Client, func()) {
	if upstream == nil {
		return &http.Client{}, func() {}
	}
	c := *upstream
	if tr, ok := upstream.Transport.(*http.Transport); ok {
		clone := tr.Clone()
		c.Transport = clone
		return &c, clone.CloseIdleConnections
	}
	// A non-*http.Transport cannot be cloned here. It may be a caller/process-
	// owned injected transport shared by multiple generations, so never claim
	// its CloseIdleConnections hook. Generation-owned underlying transports are
	// already ledgered by buildGenerationObservability.
	return &c, func() {}
}

func routingRuntimeFromModelCatalog(rr runtime.RoutingRuntime, cat *modelcatalog.CatalogRuntime, cfg *config.Config) runtime.RoutingRuntime {
	if cat == nil || cfg == nil || !cfg.ModelCatalog.Enabled {
		return rr
	}
	sizeEstimator := modelcatalog.DefaultSizeEstimator{}
	ovr := modelcatalog.NewOverrideResolver(OverrideSetFromModelCatalog(cfg.ModelCatalog))
	rr.CatalogResolver = modelcatalog.NewCatalogResolver(nil, ovr, true, cat)
	rr.EligibilityResolver = modelcatalog.NewEligibilityResolver(sizeEstimator)
	rr.RequestTokenEstimator = sizeEstimator
	return rr
}

// attachModelCatalog returns the catalog runtime and enriched routing runtime when model_catalog.enabled requests it.
// Catalog I/O must already be started via [startModelCatalog].
func attachModelCatalog(
	rr runtime.RoutingRuntime,
	started *startedModelCatalog,
	cfg *config.Config,
) (runtime.RoutingRuntime, *modelcatalog.CatalogRuntime) {
	if started == nil {
		return rr, nil
	}
	return routingRuntimeFromModelCatalog(rr, started.Runtime, cfg), started.Runtime
}
