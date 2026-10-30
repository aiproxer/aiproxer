package runtimebundle_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	dto "github.com/prometheus/client_model/go"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestBuild_productionClockAndRNG(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	ex := b.Executor()
	if runtimebundle.CandidateUpstreamHTTP(b) == nil {
		t.Fatal("expected shared upstream HTTP client")
	}
	if b.PluginRegistry() == nil {
		t.Fatal("expected PluginRegistry")
	}
	if ex.Now == nil {
		t.Fatal("expected non-nil Now")
	}
	if ex.Rand == nil {
		t.Fatal("expected non-nil Rand")
	}
	if ex.ALegLifecycle == nil {
		t.Fatal("expected A-leg lifecycle coordinator wired")
	}
	if ex.CandidateHealth == nil {
		t.Fatal("expected CandidateHealth wired")
	}
	if ex.RouteObserver == nil {
		t.Fatal("expected RouteObserver wired")
	}
	if b.RuntimeSnapshot() == nil {
		t.Fatal("expected RuntimeSnapshot on Built")
	}
	if ex.RuntimeSnapshot != b.RuntimeSnapshot() {
		t.Fatal("executor snapshot should match CandidateRuntime.RuntimeSnapshot")
	}
	ctx := context.Background()
	if err := b.RuntimeSnapshot().State().Put(ctx, aipstate.ScopeGlobal, "rtbundle", "probe", "1", 0); err != nil {
		t.Fatalf("runtime snapshot state: %v", err)
	}
	var out string
	found, err := b.RuntimeSnapshot().State().Get(ctx, aipstate.ScopeGlobal, "rtbundle", "probe", &out)
	if err != nil || !found || out != "1" {
		t.Fatalf("state get found=%v out=%q err=%v", found, out, err)
	}
}

func TestBuild_respectsHTTPClientInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	custom := &http.Client{}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		Infra:          runtimebundle.InfraOptions{HTTPClient: custom},
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if runtimebundle.CandidateUpstreamHTTP(b) != custom {
		t.Fatalf("UpstreamHTTP: got %p want %p", runtimebundle.CandidateUpstreamHTTP(b), custom)
	}
}

func TestBuild_wrapsCustomHTTPClientForUpstreamMetrics(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		Infra:          runtimebundle.InfraOptions{HTTPClient: custom},
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if runtimebundle.CandidateUpstreamHTTP(b) == custom {
		t.Fatal("expected wrapped client clone when metrics are enabled")
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runtimebundle.CandidateUpstreamHTTP(b).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	families, err := b.Metrics().Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	assertHistogramCount(t, families, "aip_upstream_request_duration_seconds", map[string]string{
		"host_bucket": "openai",
		"method":      http.MethodGet,
	}, 1)
}

func TestBuild_setsEffectiveDefaultRoute_defaultWireModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	// The frontends fall back to the configured route; the executor applies the
	// model aliases to it, exactly as it does to a client selector.
	want := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	if b.DefaultRouteSelector() != want {
		t.Fatalf("DefaultRouteSelector: got %q want %q", b.DefaultRouteSelector(), want)
	}
}

func TestBuild_derivesRoutePrefixesFromEnabledBackends(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "local-stub", Enabled: true}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if !slices.Contains(b.RoutePrefixes(), "local-stub") {
		t.Fatalf("RoutePrefixes = %v, want local-stub", b.RoutePrefixes())
	}
}

func TestBuild_respectsWireModelInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		WireModel:      func(string) string { return "wm-override" },
	})
	if want := "openai-responses:wm-override"; b.DefaultRouteSelector() != want {
		t.Fatalf("DefaultRouteSelector: got %q want %q", b.DefaultRouteSelector(), want)
	}
}

func TestBuild_defaultRouteAliasExpandsBeforeDefaultBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "friendly-name",
		},
		ModelAliases: []config.ModelAliasConfig{
			{Pattern: `^friendly-name$`, Replacement: "openai-responses:gpt-4o-mini"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	// The frontends fall back to the configured route; the executor expands it
	// once, so the alias still decides the backend the request is routed to.
	if b.DefaultRouteSelector() != "friendly-name" {
		t.Fatalf("DefaultRouteSelector: got %q", b.DefaultRouteSelector())
	}
	if b.Executor().DefaultBackend != "openai-responses" {
		t.Fatalf("DefaultBackend: got %q want openai-responses", b.Executor().DefaultBackend)
	}
}

func assertHistogramCount(t *testing.T, families []*dto.MetricFamily, name string, wantLabels map[string]string, wantCount uint64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !hasLabels(metric.GetLabel(), wantLabels) {
				continue
			}
			if metric.GetHistogram().GetSampleCount() != wantCount {
				t.Fatalf("%s sample count = %d want %d", name, metric.GetHistogram().GetSampleCount(), wantCount)
			}
			return
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, wantLabels)
}

func hasLabels(labels []*dto.LabelPair, want map[string]string) bool {
	if len(labels) != len(want) {
		return false
	}
	for _, label := range labels {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
