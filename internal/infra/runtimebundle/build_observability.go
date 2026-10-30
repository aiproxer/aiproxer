package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/prometheus/client_golang/prometheus"
)

// observabilityRuntime holds the metrics bundle and the shared upstream HTTP
// client produced by [buildGenerationObservability]. Neither resource owns a closer.
type observabilityRuntime struct {
	Bundle   *metrics.Bundle
	Upstream *http.Client
}

// buildProcessMetricsBundle constructs the process-owned Prometheus metrics bundle
// when metrics are enabled. Call site for metrics.NewBundle remains here so the
// live uniqueness gate keeps a single process construction site. The bounded
// ingress self-defense adaptive state is the second process-owned scrape source,
// exactly like the PostgreSQL pool registry: the entry-count gauge reads it live
// on every scrape, so no self-defense request path pays for it.
func buildProcessMetricsBundle(cfg *config.Config, poolStats func() []sql.DBStats, selfDefense *ingressdefense.State) *metrics.Bundle {
	if cfg == nil || !cfg.Observability.Metrics.Enabled {
		return nil
	}
	return metrics.NewBundle(cfg, poolStats, ingressDefenseEntryCount(selfDefense))
}

// registerStartupMetricsCollectors invokes the optional startup-only
// registration once, while the process is unpublished: nil register or nil
// bundle (disabled metrics) skip, and an error aborts construction so the
// partially populated process-owned registry is discarded before any host
// serves (prometheus has no unregister). Reload reuses the bundle.
func registerStartupMetricsCollectors(register func(prometheus.Registerer) error, bundle *metrics.Bundle) error {
	if register == nil || bundle == nil || bundle.Registry == nil {
		return nil
	}
	if err := register(bundle.Registry); err != nil {
		return fmt.Errorf("runtimebundle: startup metrics registration: %w", err)
	}
	return nil
}

// buildGenerationObservability builds the generation-owned upstream HTTP client,
// wrapping it with the shared process metrics bundle and optional OTEL propagation.
// It does not construct a metrics registry.
// Internally created transports are ledgered for idle cleanup; caller-injected
// Infra.HTTPClient transports are never claimed.
func buildGenerationObservability(bctx buildContext, bundle *metrics.Bundle) observabilityRuntime {
	cfg := bctx.Cfg
	opts := bctx.Opts
	tune := httpclient.TransportTuneFromConfig(cfg)
	ownedIdle := idleTransportCloser(nil)
	var upstream *http.Client
	if opts != nil && opts.Infra.HTTPClient != nil {
		upstream = opts.Infra.HTTPClient
	} else {
		upstream = httpclient.StandardWithTune(cfg.EffectiveTrustEnvironmentProxy(), tune)
		ownedIdle = idleTransportCloser(upstream.Transport)
	}
	outbound := false
	if opts != nil {
		outbound = opts.Infra.OutboundTracing
	}
	upstream = wrapUpstreamClient(upstream, bundle, outbound)
	if ownedIdle != nil && bctx.Ledger != nil {
		bctx.Ledger.Add("upstream-idle-transport", PhaseClose, func(context.Context) error {
			ownedIdle()
			return nil
		})
	}
	return observabilityRuntime{Bundle: bundle, Upstream: upstream}
}

// wrapUpstreamClient wraps an upstream [http.Client] transport with the metrics
// bundle's upstream round-tripper (when present) and OpenTelemetry HTTP
// propagation (when outboundTracing is true). When HTTPClient is non-nil the
// caller-owned client is cloned before wrapping so it is not mutated.
func wrapUpstreamClient(client *http.Client, bundle *metrics.Bundle, outboundTracing bool) *http.Client {
	if client == nil {
		return nil
	}
	rt := client.Transport
	if rt == nil {
		rt = httpclient.DefaultTransport()
	}
	wrapped := rt
	if bundle != nil && bundle.Upstream != nil {
		wrapped = bundle.Upstream.WrapUpstreamRoundTripper(wrapped)
	}
	if outboundTracing {
		wrapped = tracing.WrapTransport(true, wrapped)
	}
	if wrapped == rt {
		return client
	}
	c := *client
	c.Transport = wrapped
	return &c
}

// idleTransportCloser returns a CloseIdleConnections callback for an owned
// transport without going through metrics/otel wrappers that may hide the hook.
func idleTransportCloser(rt http.RoundTripper) func() {
	if rt == nil {
		return nil
	}
	if tr, ok := rt.(*http.Transport); ok {
		return tr.CloseIdleConnections
	}
	type idleCloser interface{ CloseIdleConnections() }
	if c, ok := rt.(idleCloser); ok {
		return c.CloseIdleConnections
	}
	return nil
}
