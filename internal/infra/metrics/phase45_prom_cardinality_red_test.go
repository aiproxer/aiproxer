package metrics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPhase45_TerminalWorkPromProviderLabelBoundedCardinality(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterTerminalWorkProm(reg)
	if m == nil {
		t.Fatal("expected TerminalWorkProm")
	}
	secret := "SECRET_PROVIDER_TOKEN_xyz"
	for i := range 500 {
		m.ObserveTransition("pending", "settle_request_provider", fmt.Sprintf("hostile-%d-%s", i, secret))
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var series int
	for _, f := range families {
		if f.GetName() != "aip_terminal_work_transitions_total" {
			continue
		}
		series = len(f.GetMetric())
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() != "provider_id" {
					continue
				}
				v := lp.GetValue()
				if strings.Contains(v, secret) || strings.Contains(v, "hostile-") {
					t.Fatalf("raw provider_id leaked into label: %q", v)
				}
			}
		}
	}
	const maxSeries = 64 // fixed bucket cap (state×kind×provider_bucket)
	if series > maxSeries {
		t.Fatalf("transition series=%d exceeds bounded cap %d", series, maxSeries)
	}
	if series == 0 {
		t.Fatal("expected some transition series")
	}
}

func TestPhase45_MetricsBundleWiresTerminalWorkProm(t *testing.T) {
	t.Parallel()
	b := NewBundle(nil, nil, nil)
	if b == nil || b.TerminalWork == nil {
		t.Fatal("metrics.Bundle must own TerminalWorkProm")
	}
	b.TerminalWork.ApplySnapshot(TerminalWorkSnapshot{
		Backlog:      7,
		OldestAgeSec: 12.5,
		Pending:      4,
		Retrying:     1,
		Quarantined:  1,
		Completed:    2,
		Claimed:      1,
	})
	families, err := b.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		switch f.GetName() {
		case "aip_terminal_work_backlog",
			"aip_terminal_work_oldest_age_seconds",
			"aip_terminal_work_pending",
			"aip_terminal_work_retrying",
			"aip_terminal_work_quarantined",
			"aip_terminal_work_completed",
			"aip_terminal_work_claimed":
			got[f.GetName()] = gaugeVal(f)
		}
	}
	if got["aip_terminal_work_backlog"] != 7 {
		t.Fatalf("backlog=%v want 7", got["aip_terminal_work_backlog"])
	}
	if got["aip_terminal_work_pending"] != 4 {
		t.Fatalf("pending=%v want 4", got["aip_terminal_work_pending"])
	}
	if got["aip_terminal_work_completed"] != 2 {
		t.Fatalf("completed=%v want 2", got["aip_terminal_work_completed"])
	}
}

func gaugeVal(f *dto.MetricFamily) float64 {
	if f == nil || len(f.Metric) == 0 || f.Metric[0].Gauge == nil {
		return -1
	}
	return f.Metric[0].Gauge.GetValue()
}

func TestPhase45_PromptCacheCollapseModelLabelBoundedCardinality(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := RegisterPromptCacheCollapseProm(reg)
	for i := range 500 {
		m.Observe("configured-backend", fmt.Sprintf("hostile-model-%d-SECRET", i))
	}
	m.Observe("configured-backend", "")
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "aip_prompt_cache_collapse_total" {
			continue
		}
		if len(f.GetMetric()) != 2 {
			t.Fatalf("series=%d want 2", len(f.GetMetric()))
		}
		for _, metric := range f.GetMetric() {
			if len(metric.GetLabel()) != 2 {
				t.Fatalf("unexpected labels: %v", metric.GetLabel())
			}
			for _, label := range metric.GetLabel() {
				if label.GetName() == "model" && label.GetValue() != "specified" && label.GetValue() != "unknown" {
					t.Fatalf("raw model: %s", label.GetValue())
				}
				if label.GetName() != "model" && label.GetName() != "backend" {
					t.Fatalf("unbounded label: %s", label.GetName())
				}
			}
		}
		return
	}
	t.Fatal("missing collapse counter")
}
