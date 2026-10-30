package metrics

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Phase 4.5 RED/GREEN: backlog/oldest-age/transition metrics with bounded labels
// (requirements 8.9, 12.5, 12.8; design D14).

func TestPhase45_TerminalWorkPromBoundedLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterTerminalWorkProm(reg)
	if m == nil {
		t.Fatal("expected TerminalWorkProm")
	}
	m.SetBacklog(3)
	m.SetOldestAge(90 * time.Second)
	m.ObserveTransition("pending", "settle_request_provider", "prov-a")
	m.ObserveTransition("quarantined", "settle_request_provider", "prov-a")
	m.ObserveTransition("completed", "settle_request_provider", "prov-a")
	m.ObserveTransition("retry", "release_request_provider", "prov-b")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				v := lp.GetValue()
				if strings.Contains(v, "SECRET") || strings.Contains(v, "prompt") {
					t.Fatalf("unbounded/content label value %q on %s", v, f.GetName())
				}
			}
		}
	}
	want := []string{
		"aip_terminal_work_backlog",
		"aip_terminal_work_oldest_age_seconds",
		"aip_terminal_work_transitions_total",
	}
	for _, n := range want {
		if !slices.Contains(names, n) {
			t.Fatalf("missing metric %s in %v", n, names)
		}
	}
	// Assert backlog gauge value.
	for _, f := range families {
		if f.GetName() != "aip_terminal_work_backlog" {
			continue
		}
		if got := gaugeValue(f); got != 3 {
			t.Fatalf("backlog=%v want 3", got)
		}
	}
}

func gaugeValue(f *dto.MetricFamily) float64 {
	if f == nil || len(f.Metric) == 0 || f.Metric[0].Gauge == nil {
		return -1
	}
	return f.Metric[0].Gauge.GetValue()
}
