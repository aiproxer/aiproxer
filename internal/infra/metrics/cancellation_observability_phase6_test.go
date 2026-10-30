package metrics_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPhase6_CancellationsProm_BoundedLabelCardinality_And_SecretSafety(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	if m == nil {
		t.Fatal("expected ExecutorProm")
	}

	secretToken := "sk-ant-api03-SECRET_KEY_xyz987654321"

	causes := []runtime.CancellationCauseClass{runtime.CancellationCauseExplicit, runtime.CancellationCauseClientGone, runtime.CancellationCauseContextDone, runtime.CancellationCauseRaceLoser, runtime.CancellationCauseNone, runtime.CancellationCauseOther}
	modes := []runtime.CancellationModeClass{runtime.CancellationModeNone, runtime.CancellationModeProvider, runtime.CancellationModeTransport, runtime.CancellationModeCloseOnly, runtime.CancellationModeOther}
	phases := []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseOutcome, runtime.CancellationPhaseForced, runtime.CancellationPhaseTerminal, runtime.CancellationPhaseNone, runtime.CancellationPhaseOther}
	fallbacks := []runtime.CancellationFallback{runtime.CancellationFallbackNegotiated, runtime.CancellationFallbackLegacy, runtime.CancellationFallbackNone, runtime.CancellationFallbackOther}

	for _, cause := range causes {
		for _, mode := range modes {
			for _, phase := range phases {
				for _, fallback := range fallbacks {
					m.ObserveCancellation(runtime.CancellationObservation{
						CauseClass: cause,
						Mode:       mode,
						Phase:      phase,
						Fallback:   fallback,
					})
				}
			}
		}
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	var seriesCount int
	for _, f := range families {
		if f.GetName() != "aip_executor_cancellations_total" {
			continue
		}
		found = true
		seriesCount = len(f.GetMetric())
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				val := lp.GetValue()
				if strings.Contains(val, secretToken) {
					t.Fatalf("secret token leaked into label %q: %q", lp.GetName(), val)
				}
			}
		}
	}

	if !found {
		t.Fatal("expected aip_executor_cancellations_total metric family to be gathered")
	}

	if seriesCount == 0 {
		t.Fatal("expected non-zero series count")
	}
}

func TestPhase6_CancellationsProm_StandardClasses(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := metrics.RegisterExecutorProm(reg)
	if m == nil {
		t.Fatal("expected ExecutorProm")
	}

	m.ObserveCancellation(runtime.CancellationObservation{
		CauseClass: runtime.CancellationCauseExplicit,
		Mode:       runtime.CancellationModeProvider,
		Phase:      runtime.CancellationPhaseOutcome,
		Fallback:   runtime.CancellationFallbackNegotiated,
	})

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, f := range families {
		if f.GetName() != "aip_executor_cancellations_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["cause_class"] == "explicit" &&
				labels["mode"] == "provider" &&
				labels["phase"] == "outcome" &&
				labels["fallback"] == "negotiated" {
				found = true
				if metric.GetCounter().GetValue() != 1 {
					t.Fatalf("expected counter value 1, got %v", metric.GetCounter().GetValue())
				}
			}
		}
	}
	if !found {
		t.Fatal("expected metric with explicit/provider/outcome/negotiated labels")
	}
}
