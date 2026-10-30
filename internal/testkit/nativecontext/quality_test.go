package nativecontext

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportDeterministicSchemaAndBreakEven(t *testing.T) {
	results := make([]Result, 0, 8)
	for _, mode := range Modes {
		metric := Metrics{TaskQualityPass: true, TestsPassed: true, Turns: 4, ProviderInputTokens: 100}
		if mode == ModeFull {
			metric.ProviderInputTokens = 60
			metric.CompactionCostTokens = 80
			metric.CheckpointReuse = 2
		}
		results = append(results, Result{TaskID: "repo-task-1", Seed: 41, Mode: mode, Metric: metric})
	}
	report := NewReport(41, map[string]string{"emulator": "fixed-v1", "toolchain": "go1.26"}, false, results)
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if report.Summary.BreakEvenTurns != 2 {
		t.Fatalf("break-even turns=%d, want 2", report.Summary.BreakEvenTurns)
	}
	if len(report.Comparisons) != 1 || report.Comparisons[0].InputSavingsPerTurn != 40 {
		t.Fatalf("comparisons=%+v", report.Comparisons)
	}
	first, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("report JSON is not deterministic")
	}
	if strings.Contains(string(first), "repo-like") || strings.Contains(string(first), "cipher") {
		t.Fatal("report contains content-like fixture data")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != "native-context-evaluation.v1" {
		t.Fatalf("schema=%v", decoded["schema_version"])
	}
}

func TestReportRejectsUnpairedOrNegativeResults(t *testing.T) {
	report := NewReport(1, nil, false, []Result{{TaskID: "task", Seed: 1, Mode: ModeBaseline}})
	if err := report.Validate(); err == nil {
		t.Fatal("expected unpaired report error")
	}
	results := make([]Result, 0, 4)
	for _, mode := range Modes {
		results = append(results, Result{TaskID: "task", Seed: 1, Mode: mode, Metric: Metrics{Failures: -1}})
	}
	if err := NewReport(1, nil, false, results).Validate(); err == nil {
		t.Fatal("expected negative metric error")
	}
}
