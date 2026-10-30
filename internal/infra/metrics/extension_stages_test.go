package metrics

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gatherLabelPairs(t *testing.T, g prometheus.Gatherer, metric string) []map[string]string {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, mf := range mfs {
		if mf.GetName() != metric {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			out = append(out, labels)
		}
	}
	return out
}

func counterValue(t *testing.T, g prometheus.Gatherer, metric string, want map[string]string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != metric {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			match := true
			for k, v := range want {
				if labels[k] != v {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				return m.GetCounter().GetValue()
			case dto.MetricType_HISTOGRAM:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	t.Fatalf("metric %q labels %#v not found", metric, want)
	return 0
}

func TestExtensionStageSink_collapsesUnknownLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	prom := RegisterExtensionStageProm(reg)
	sink := NewExtensionStageSink(prom)
	sink.ObserveStage("gpt-4o", "sess_abc", 0.01)
	sink.IncFailOpenSkip("openai/chat")

	for _, labels := range gatherLabelPairs(t, reg, "aip_extension_stage_duration_seconds") {
		if labels["stage"] != "unknown" || labels["outcome"] != "unknown" {
			t.Fatalf("duration labels=%#v", labels)
		}
		if labels["stage"] == "gpt-4o" || labels["outcome"] == "sess_abc" {
			t.Fatalf("sensitive labels leaked: %#v", labels)
		}
	}
	for _, labels := range gatherLabelPairs(t, reg, "aip_extension_stage_fail_open_skips_total") {
		if labels["stage"] != "unknown" {
			t.Fatalf("fail_open labels=%#v", labels)
		}
	}
	if counterValue(t, reg, "aip_extension_stage_duration_seconds", map[string]string{
		"stage": "unknown", "outcome": "unknown",
	}) != 1 {
		t.Fatal("expected one collapsed duration sample")
	}
}

func TestExtensionStageSink_recordsCountsAndBytes(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	prom := RegisterExtensionStageProm(reg)
	sink := NewExtensionStageSink(prom)
	if _, ok := sink.(extensions.StageCountByteMetrics); !ok {
		t.Fatal("extension stage sink must implement StageCountByteMetrics")
	}
	extensions.RecordStageObservation(sink, extensions.MetricsStageFinalStreamObservation, extensions.StageOutcomeOK, 0.001, 2, 64)
	if got := counterValue(t, reg, "aip_extension_stage_runs_total", map[string]string{
		"stage": "final_stream_observation", "outcome": "ok",
	}); got != 2 {
		t.Fatalf("runs=%v want 2", got)
	}
	if got := counterValue(t, reg, "aip_extension_stage_bytes_total", map[string]string{
		"stage": "final_stream_observation", "outcome": "ok",
	}); got != 64 {
		t.Fatalf("bytes=%v want 64", got)
	}
}

func TestNewExtensionStageSink_nilPromNoOp(t *testing.T) {
	t.Parallel()
	if got := NewExtensionStageSink(nil); got != nil {
		t.Fatalf("nil prom must yield nil sink, got %#v", got)
	}
}
