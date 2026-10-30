package nativecontext

import "testing"

func TestDeterministicReportHasFourPairedModes(t *testing.T) {
	one := DeterministicReport()
	two := DeterministicReport()
	first, err := one.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := two.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("deterministic report changed between runs")
	}
	if one.Summary.TaskCount != 2 || one.Summary.ModeCount != 4 || !one.Summary.Paired {
		t.Fatalf("summary=%+v", one.Summary)
	}
	if one.Summary.BreakEvenTurns != 1 {
		t.Fatalf("break-even turns=%d, want 1", one.Summary.BreakEvenTurns)
	}
}
