package archtest

import (
	"path/filepath"
	"testing"
)

// Fixed audit baselines and approved ceilings for selected general architecture
// budgets. Recursive trees use the measured-plus-headroom policy in budgets.go;
// critical files use its file policy. The live source may shrink below its audit,
// and each ceiling remains capped above so excess or unapproved inflation fails.
// ConnectorArchitectureOverlayMax is a separately measured overlay ceiling with
// fixed headroom. The audited value is a test pin; shrinkage subtraction uses the
// live measured overlay lines.
const (
	phase20AuditRuntimebundleLines    = 14059
	phase20CeilingRuntimebundle       = 22000
	phase20AuditStdhttpLines          = 8562
	phase20CeilingStdhttp             = 14000
	phase20AuditCoreLines             = 143764
	phase20CeilingCore                = 216000
	phase20AuditProcessServicesLines  = 349
	phase20CeilingProcessServices     = 550
	phase20AuditConnectorOverlayLines = 2335
	phase20CeilingConnectorOverlay    = 3550
)

// TestPhase20RefreshedBudgetHeadroomExact pins the audited baselines to their
// approved fixed ceilings, checks the live measurements stay within those ceilings,
// and rejects any unapproved ceiling change. Beneficial deletions remain allowed.
func TestPhase20RefreshedBudgetHeadroomExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	treeCeiling := func(tree string, audited, ceiling int) {
		t.Helper()
		max := -1
		for _, b := range PackageTreeBudgets {
			if b.Tree == tree {
				max = b.Max
			}
		}
		if max > ceiling {
			t.Fatalf("%s: PackageTreeBudgets Max=%d, want at most approved ceiling %d for audited baseline %d", tree, max, ceiling, audited)
		}
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(tree)))
		if err != nil {
			t.Fatal(err)
		}
		if msg := budgetBoundaryError(tree, n, max); msg != "" {
			t.Fatal(msg)
		}
	}
	lineCeiling := func(dir string, audited, ceiling int) {
		t.Helper()
		max := -1
		for _, b := range LineBudgets {
			if b.Dir == dir {
				max = b.Max
			}
		}
		if max > ceiling {
			t.Fatalf("%s: LineBudgets Max=%d, want at most approved ceiling %d for audited baseline %d", dir, max, ceiling, audited)
		}
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatal(err)
		}
		if msg := budgetBoundaryError(dir, n, max); msg != "" {
			t.Fatal(msg)
		}
	}

	treeCeiling("internal/infra/runtimebundle", phase20AuditRuntimebundleLines, phase20CeilingRuntimebundle)
	treeCeiling("internal/stdhttp", phase20AuditStdhttpLines, phase20CeilingStdhttp)
	lineCeiling("internal/core", phase20AuditCoreLines, phase20CeilingCore)

	const processServicesPath = "internal/infra/runtimebundle/process_services.go"
	fileMax := -1
	for _, b := range CriticalFileBudgets {
		if b.Path == processServicesPath {
			fileMax = b.Max
		}
	}
	if fileMax > phase20CeilingProcessServices {
		t.Fatalf("%s: CriticalFileBudgets Max=%d, want at most approved ceiling %d for audited baseline %d", processServicesPath, fileMax, phase20CeilingProcessServices, phase20AuditProcessServicesLines)
	}
	n, err := CountFileLines(filepath.Join(root, filepath.FromSlash(processServicesPath)))
	if err != nil {
		t.Fatal(err)
	}
	if msg := budgetBoundaryError(processServicesPath, n, fileMax); msg != "" {
		t.Fatal(msg)
	}

	if ConnectorArchitectureOverlayMax > phase20CeilingConnectorOverlay {
		t.Fatalf("ConnectorArchitectureOverlayMax=%d, want at most approved fixed ceiling %d for audited baseline %d", ConnectorArchitectureOverlayMax, phase20CeilingConnectorOverlay, phase20AuditConnectorOverlayLines)
	}
	overlay, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	if msg := budgetBoundaryError("connector overlay", overlay.Lines, overlay.Max); msg != "" {
		t.Fatal(msg)
	}
}

// TestPhase20BudgetBoundaryReductionPassesExcessFails is the pure boundary
// regression for the ceiling helper: deep reductions and at-cap measurements pass,
// while ceiling+1 fails.
func TestPhase20BudgetBoundaryReductionPassesExcessFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		measured int
		ceiling  int
		wantErr  bool
	}{
		{name: "deep reduction passes", measured: 0, ceiling: 100, wantErr: false},
		{name: "headroom reduction passes", measured: 74, ceiling: 100, wantErr: false},
		{name: "at cap passes", measured: 100, ceiling: 100, wantErr: false},
		{name: "ceiling plus one fails", measured: 101, ceiling: 100, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := budgetBoundaryError("boundary", tc.measured, tc.ceiling)
			if tc.wantErr && msg == "" {
				t.Fatalf("measured=%d ceiling=%d: want excess error, got none", tc.measured, tc.ceiling)
			}
			if !tc.wantErr && msg != "" {
				t.Fatalf("measured=%d ceiling=%d: want pass, got %q", tc.measured, tc.ceiling, msg)
			}
		})
	}
}
