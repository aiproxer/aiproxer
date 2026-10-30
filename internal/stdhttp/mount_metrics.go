package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
)

// mountMetricsInput carries inputs for [mountMetrics].
type mountMetricsInput struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountMetrics mounts the Prometheus metrics endpoint when observability.metrics.enabled is
// true and returns the HTTP metrics handle (nil when disabled) for the outer middleware stack.
// On misconfiguration (enabled without Operations.Metrics) it returns the same error the inline
// block previously returned so ComposeStandardHTTP's error wrapping stays identical.
func mountMetrics(in mountMetricsInput) (*metrics.HTTPMetrics, error) {
	cfg, log, ops, mux := in.Cfg, in.Log, in.Operations, in.Mux
	if !cfg.Observability.Metrics.Enabled {
		return nil, nil
	}
	if ops.Metrics == nil || ops.Metrics.Registry == nil {
		return nil, fmt.Errorf("stdhttp: observability.metrics.enabled requires Operations.Metrics from generation composition")
	}
	mp := strings.TrimSpace(cfg.Observability.Metrics.Path)
	if mp == "" {
		mp = "/metrics"
	}
	om := cfg.Observability.Metrics.ExemplarsEnabled
	mux.Handle(mp, wrapDiagnostics(cfg, metrics.MetricsHandler(ops.Metrics.Registry, om)))
	log.InfoContext(in.LogCtx, "prometheus metrics mounted", "path", mp, "open_metrics", om)
	return ops.Metrics.HTTP, nil
}
