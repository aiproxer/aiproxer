package metrics

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestReloadProm_CardinalityBoundedFixedLabels(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterReloadProm(reg)
	if m == nil {
		t.Fatal("expected ReloadProm")
	}

	secret := "sk-live-reload-secret-TOKEN-9f3a"
	path := "/etc/aip/config-with-" + secret + ".yaml"
	modelID := "gpt-hostile-" + secret
	backendID := "backend-" + secret

	for i := range 200 {
		trig := "api"
		if i%2 == 0 {
			trig = "sighup"
		}
		// Hostile callers may try to inject unbounded / secret label values.
		m.ObserveAttempt(trig, fmt.Sprintf("published/%s/%s", path, modelID), time.Millisecond)
		m.ObserveStage("compile", fmt.Sprintf("preparation-failed:%s:%s", backendID, secret), time.Millisecond)
		m.ObserveAttempt(path, modelID, time.Millisecond)                       // invalid trigger → other
		m.ObserveStage(secret, "error:"+fmt.Sprint(i)+secret, time.Millisecond) // invalid stage → other
	}
	m.ApplyGenerationSnapshot(ReloadGenerationSnapshot{
		Active:            1,
		Retired:           2,
		Pinned:            4,
		RetentionPressure: 1,
	})

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var attemptSeries, stageSeries int
	forbiddenLabelNames := map[string]bool{
		"generation_id": true,
		"path":          true,
		"model_id":      true,
		"backend_id":    true,
		"error":         true,
		"secret":        true,
	}
	for _, f := range families {
		switch f.GetName() {
		case "aip_reload_attempts_total":
			attemptSeries = len(f.GetMetric())
		case "aip_reload_stage_duration_seconds":
			stageSeries = len(f.GetMetric())
		case "aip_reload_duration_seconds",
			"aip_reload_active_generations",
			"aip_reload_retired_generations",
			"aip_reload_pinned_references",
			"aip_reload_retention_pressure":
			// present
		default:
			if strings.HasPrefix(f.GetName(), "aip_reload_") {
				t.Fatalf("unexpected reload metric %q", f.GetName())
			}
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				name := lp.GetName()
				val := lp.GetValue()
				if forbiddenLabelNames[name] {
					t.Fatalf("forbidden label name %q on %s", name, f.GetName())
				}
				if strings.Contains(val, secret) || strings.Contains(val, path) ||
					strings.Contains(val, modelID) || strings.Contains(val, backendID) ||
					strings.Contains(val, "hostile-") || strings.Contains(val, "/etc/") {
					t.Fatalf("%s label %s=%q leaked unbounded/secret material", f.GetName(), name, val)
				}
			}
		}
	}
	// Fixed vocabularies: trigger×result and stage×result only.
	const maxAttemptSeries = 32
	const maxStageSeries = 64
	if attemptSeries == 0 || attemptSeries > maxAttemptSeries {
		t.Fatalf("attempt series=%d want 1..%d", attemptSeries, maxAttemptSeries)
	}
	if stageSeries == 0 || stageSeries > maxStageSeries {
		t.Fatalf("stage series=%d want 1..%d", stageSeries, maxStageSeries)
	}

	gauges := map[string]float64{}
	for _, f := range families {
		switch f.GetName() {
		case "aip_reload_active_generations",
			"aip_reload_retired_generations",
			"aip_reload_pinned_references",
			"aip_reload_retention_pressure":
			gauges[f.GetName()] = reloadGaugeVal(f)
		}
	}
	if gauges["aip_reload_active_generations"] != 1 {
		t.Fatalf("active=%v want 1", gauges["aip_reload_active_generations"])
	}
	if gauges["aip_reload_retired_generations"] != 2 {
		t.Fatalf("retired=%v want 2", gauges["aip_reload_retired_generations"])
	}
	if gauges["aip_reload_pinned_references"] != 4 {
		t.Fatalf("pinned=%v want 4", gauges["aip_reload_pinned_references"])
	}
	if gauges["aip_reload_retention_pressure"] != 1 {
		t.Fatalf("pressure=%v want 1", gauges["aip_reload_retention_pressure"])
	}
}

func TestBundle_WiresReloadProm(t *testing.T) {
	t.Parallel()
	b := NewBundle(nil, nil, nil)
	if b == nil || b.Reload == nil {
		t.Fatal("metrics.Bundle must own ReloadProm")
	}
}

// TestReloadProm_CanonicalCategoryLabelsUnchanged locks exact trigger/result label
// strings after direct pkg/aipsdk/configreload migration (Task 2.2).
func TestReloadProm_CanonicalCategoryLabelsUnchanged(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := RegisterReloadProm(reg)
	if m == nil {
		t.Fatal("expected ReloadProm")
	}

	wantPairs := []struct {
		trigger, result string
	}{
		{string(sdkreload.TriggerAPI), string(sdkreload.ResultPublished)},
		{string(sdkreload.TriggerSIGHUP), string(sdkreload.ResultBusy)},
		{string(sdkreload.TriggerAPI), string(sdkreload.ResultNoop)},
		{string(sdkreload.TriggerAPI), string(sdkreload.ResultRestartRequired)},
		{string(sdkreload.TriggerAPI), string(sdkreload.ResultSourceIntegrity)},
	}
	for _, p := range wantPairs {
		m.ObserveAttempt(p.trigger, p.result, time.Millisecond)
		m.ObserveStage("publish", p.result, time.Millisecond)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seenAttempt := map[string]bool{}
	seenStage := map[string]bool{}
	for _, f := range families {
		switch f.GetName() {
		case "aip_reload_attempts_total":
			for _, metric := range f.GetMetric() {
				trig, res := "", ""
				for _, lp := range metric.GetLabel() {
					switch lp.GetName() {
					case "trigger":
						trig = lp.GetValue()
					case "result":
						res = lp.GetValue()
					}
				}
				seenAttempt[trig+"|"+res] = true
			}
		case "aip_reload_stage_duration_seconds":
			for _, metric := range f.GetMetric() {
				stage, res := "", ""
				for _, lp := range metric.GetLabel() {
					switch lp.GetName() {
					case "stage":
						stage = lp.GetValue()
					case "result":
						res = lp.GetValue()
					}
				}
				seenStage[stage+"|"+res] = true
			}
		}
	}
	for _, p := range wantPairs {
		key := p.trigger + "|" + p.result
		if !seenAttempt[key] {
			t.Fatalf("missing attempt labels %q (canonical category strings must remain stable)", key)
		}
		if !seenStage["publish|"+p.result] {
			t.Fatalf("missing stage labels publish|%s", p.result)
		}
	}
	// Closed vocabulary hyphenation must not drift to underscored forms.
	for key := range seenAttempt {
		if strings.Contains(key, "restart_required") || strings.Contains(key, "source_integrity") ||
			strings.Contains(key, "no_op") {
			t.Fatalf("underscored category label leaked: %q", key)
		}
	}
}

// TestReloadProm_ResultAllowInventoryFromCanonicalConstants locks the allow-map
// inventory to declared ResultCategory constants plus the extra observer labels,
// and proves production source does not range over mutable AllResultCategories.
func TestReloadProm_ResultAllowInventoryFromCanonicalConstants(t *testing.T) {
	t.Parallel()

	canonical := []sdkreload.ResultCategory{
		sdkreload.ResultPublished,
		sdkreload.ResultNoop,
		sdkreload.ResultBusy,
		sdkreload.ResultRestartRequired,
		sdkreload.ResultRetentionBlocked,
		sdkreload.ResultInvalid,
		sdkreload.ResultSourceIntegrity,
		sdkreload.ResultCanceled,
		sdkreload.ResultPreparationFailed,
		sdkreload.ResultInternalFailed,
	}
	for _, c := range canonical {
		if got := boundReloadResult(string(c)); got != string(c) {
			t.Fatalf("boundReloadResult(%q)=%q want self", c, got)
		}
	}
	for _, extra := range []string{"quiesce_failed", "cleanup_failed", "other"} {
		if got := boundReloadResult(extra); got != extra {
			t.Fatalf("extra label boundReloadResult(%q)=%q", extra, got)
		}
	}
	if got := boundReloadResult("decoy-from-mutable-enum"); got != "other" {
		t.Fatalf("unknown result=%q want other", got)
	}

	src, err := os.ReadFile("reload_prom.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "AllResultCategories") {
		t.Fatal("reload_prom.go must not consult mutable AllResultCategories for allow-map init")
	}
}

func reloadGaugeVal(f *dto.MetricFamily) float64 {
	if f == nil || len(f.Metric) == 0 || f.Metric[0].Gauge == nil {
		return -1
	}
	return f.Metric[0].Gauge.GetValue()
}
