package proxymemory

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// TestMetricsIncOnNilIsSafe pins that a nil *Metrics is usable: call sites may
// hold a nil collector when metrics are not wired.
func TestMetricsIncOnNilIsSafe(t *testing.T) {
	t.Parallel()
	var m *Metrics
	m.Inc(EventCaptureRecorded) // must not panic
}

// TestMetricsReportsCaptureRecorded pins the counter name, the bounded event
// label and the counted value.
func TestMetricsReportsCaptureRecorded(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := NewPrometheusCollector()
	if err := reg.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m.Inc(EventCaptureRecorded)
	if got := testutil.ToFloat64(m.events.WithLabelValues(EventCaptureRecorded)); got != 1 {
		t.Fatalf("aip_proxy_memory_events_total{event=%q} = %v, want 1", EventCaptureRecorded, got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	assertMetricCounter(t, families, "aip_proxy_memory_events_total", "event", EventCaptureRecorded, 1)
}

// TestMetricsSummaryName pins the design §7.3 document name: the UTC timestamp
// of GeneratedAtMS formatted "2006-01-02T15-04-05", then SessionRef[8:14] (the
// first six hex characters after the "session-" prefix), then the slug, joined
// by "-". Hand-computed: "session-0123456789ab"[8:14] is "012345".
func TestMetricsSummaryName(t *testing.T) {
	t.Parallel()
	e := Extraction{SessionRef: "session-0123456789ab", GeneratedAtMS: 0, RolloutSlug: "fix-tests"}
	if got, want := SummaryName(e), "1970-01-01T00-00-00-012345-fix-tests"; got != want {
		t.Fatalf("SummaryName() = %q, want %q", got, want)
	}
}

func assertMetricCounter(t *testing.T, families []*dto.MetricFamily, name, label, labelValue string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == label && pair.GetValue() == labelValue {
					if got := metric.GetCounter().GetValue(); got != want {
						t.Fatalf("%s{%s=%q} = %v, want %v", name, label, labelValue, got, want)
					}
					return
				}
			}
		}
		t.Fatalf("%s has no series with %s=%q", name, label, labelValue)
	}
	t.Fatalf("metric family %s not found", name)
}
