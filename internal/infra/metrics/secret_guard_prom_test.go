package metrics

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSecretGuardDecisionSink_boundedLabelsAndCounters(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	p := RegisterSecretGuardProm(r)
	sink := NewSecretGuardDecisionSink(p)
	if sink == nil {
		t.Fatal("sink")
	}
	sink.IncDecision("block", "block", "proxy_env")
	sink.IncMatch("block", "block", "proxy_env")
	sink.IncQuarantine("block", "block", "unknown")
	sink.IncFailure("unknown", "failed", "unknown")
	sink.IncScanLimit("block", "scan_limit", "proxy_env")

	if got := testutil.ToFloat64(p.decisions.WithLabelValues("block", "block", "proxy_env")); got != 1 {
		t.Fatalf("decisions: %v", got)
	}
	if got := testutil.ToFloat64(p.matches.WithLabelValues("block", "block", "proxy_env")); got != 1 {
		t.Fatalf("matches: %v", got)
	}
	if got := testutil.ToFloat64(p.quarantines.WithLabelValues("block", "block", "unknown")); got != 1 {
		t.Fatalf("quarantines: %v", got)
	}
	if got := testutil.ToFloat64(p.failures.WithLabelValues("unknown", "failed", "unknown")); got != 1 {
		t.Fatalf("failures: %v", got)
	}
	if got := testutil.ToFloat64(p.scanLimits.WithLabelValues("block", "scan_limit", "proxy_env")); got != 1 {
		t.Fatalf("scan_limits: %v", got)
	}

	mfs, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"action": true, "outcome": true, "source_category": true}
	for _, mf := range mfs {
		name := mf.GetName()
		if !strings.HasPrefix(name, "aip_secret_guard_") {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if !allowed[label.GetName()] {
					t.Fatalf("metric %q has unexpected label %q", name, label.GetName())
				}
			}
		}
	}
	for _, want := range []string{
		"aip_secret_guard_decisions_total",
		"aip_secret_guard_matches_total",
		"aip_secret_guard_quarantines_total",
		"aip_secret_guard_failures_total",
		"aip_secret_guard_scan_limits_total",
	} {
		found := false
		for _, mf := range mfs {
			if mf.GetName() == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing metric %q", want)
		}
	}
}

func TestNewBundle_includesSecretGuardDecisionSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Observability: config.ObservabilityConfig{Metrics: config.MetricsConfig{Enabled: true}}}
	b := NewBundle(cfg, nil, nil)
	if b.SecretGuard == nil {
		t.Fatal("expected SecretGuard prom")
	}
	if b.SecretGuardDecisionSink() == nil {
		t.Fatal("expected decision sink")
	}
}
