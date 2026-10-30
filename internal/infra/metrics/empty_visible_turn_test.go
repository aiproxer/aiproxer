package metrics_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestEmptyVisibleTurn_LabelSetRatchet pins the bounded series shape of
// aip_empty_visible_turn_total: exactly one backend label, an empty backend
// folds to the bounded "unknown" bucket, and the executor sink opts in through
// the optional runtime.EmptyVisibleTurnObserver extension so existing sinks
// keep working unchanged.
func TestEmptyVisibleTurn_LabelSetRatchet(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	if m == nil {
		t.Fatal("expected ExecutorProm")
	}
	sink := metrics.NewExecutorPromSink(m)
	if sink == nil {
		t.Fatal("expected executor sink")
	}
	obs, ok := sink.(runtime.EmptyVisibleTurnObserver)
	if !ok {
		t.Fatal("executor sink must implement runtime.EmptyVisibleTurnObserver")
	}
	obs.OnEmptyVisibleTurn("")
	obs.OnEmptyVisibleTurn("  ")
	obs.OnEmptyVisibleTurn("backend-1")
	obs.OnEmptyVisibleTurn("backend-1")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "aip_empty_visible_turn_total" {
			continue
		}
		got := map[string]float64{}
		for _, metric := range f.GetMetric() {
			if len(metric.GetLabel()) != 1 || metric.GetLabel()[0].GetName() != "backend" {
				t.Fatalf("label set must be exactly {backend}, got %v", metric.GetLabel())
			}
			got[metric.GetLabel()[0].GetValue()] = metric.GetCounter().GetValue()
		}
		if got["unknown"] != 2 {
			t.Fatalf("blank backends must fold to the bounded unknown bucket, got %v", got)
		}
		if got["backend-1"] != 2 {
			t.Fatalf("backend-1 increments = %v, want 2", got)
		}
		return
	}
	t.Fatal("missing aip_empty_visible_turn_total metric family")
}

// TestEmptyVisibleTurnRetry_LabelSetRatchet pins the bounded series shape of
// aip_empty_visible_turn_retry_total: exactly the {backend, outcome} label
// pair, the closed outcome set corrected|skipped|failed, an empty or blank
// backend folding to the bounded "unknown" bucket, the executor sink opting in
// through the optional runtime.EmptyVisibleTurnRetryObserver extension, and an
// outcome outside the closed set being dropped, never labelled.
func TestEmptyVisibleTurnRetry_LabelSetRatchet(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	require.NotNil(t, m, "expected ExecutorProm")
	sink := metrics.NewExecutorPromSink(m)
	require.NotNil(t, sink, "expected executor sink")
	obs, ok := sink.(runtime.EmptyVisibleTurnRetryObserver)
	require.True(t, ok, "executor sink must implement runtime.EmptyVisibleTurnRetryObserver")
	closedOutcomes := map[string]struct{}{"corrected": {}, "skipped": {}, "failed": {}}
	obs.OnEmptyVisibleTurnRetry("", "corrected")
	obs.OnEmptyVisibleTurnRetry("  ", "failed")
	obs.OnEmptyVisibleTurnRetry("backend-1", "skipped")
	obs.OnEmptyVisibleTurnRetry("backend-1", "skipped")
	// Outcomes outside the closed set are dropped rather than labelled.
	obs.OnEmptyVisibleTurnRetry("backend-1", "other")
	obs.OnEmptyVisibleTurnRetry("backend-1", "")

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "aip_empty_visible_turn_retry_total" {
			continue
		}
		got := map[string]map[string]float64{}
		seenOutcomes := map[string]struct{}{}
		for _, metric := range f.GetMetric() {
			labels := metric.GetLabel()
			require.Truef(t, len(labels) == 2 && labels[0].GetName() == "backend" && labels[1].GetName() == "outcome",
				"label set must be exactly {backend, outcome}, got %v", labels)
			backend, outcome := labels[0].GetValue(), labels[1].GetValue()
			_, closed := closedOutcomes[outcome]
			require.Truef(t, closed, "outcome label %q is outside corrected|skipped|failed", outcome)
			seenOutcomes[outcome] = struct{}{}
			if got[backend] == nil {
				got[backend] = map[string]float64{}
			}
			got[backend][outcome] = metric.GetCounter().GetValue()
		}
		require.Len(t, seenOutcomes, len(closedOutcomes), "outcome label set")
		require.Equal(t, float64(1), got["unknown"]["corrected"], "unknown corrected")
		require.Equal(t, float64(1), got["unknown"]["failed"], "unknown failed")
		require.Equal(t, map[string]float64{"skipped": 2}, got["backend-1"], "backend-1 series")
		return
	}
	t.Fatal("missing aip_empty_visible_turn_retry_total metric family")
}
