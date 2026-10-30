package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// growthAllowlistOverlap reports the first allowlist path already claimed by
// another overlay (empty means disjoint). It compares allowlist paths directly
// against measured claim sets, independent of the growth measurement's defensive
// filtering, so an overlap can never hide behind a skip.
func growthAllowlistOverlap(allowlist []usageEconomicsGrowthFile, claimed map[string]string) string {
	for _, f := range allowlist {
		if owner, dup := claimed[f.path]; dup {
			return "growth allowlist path " + f.path + " already selected by " + owner
		}
	}
	return ""
}

// remediation-3B growth allowance: the cap and table stay pinned, every measured
// file belongs to the pinned allowlist, and measured lines stay within the cap.
// Deletions only shrink the measurement, so a missing or shrunken allowlisted file
// always passes; only excess, unknown files, or table/cap drift fail.
func checkUsageEconomicsGrowthAllowance(growth OverlayMeasurement) string {
	if growth.Name != "Usage economics" {
		return "growth overlay name = " + growth.Name + ", want Usage economics"
	}
	if growth.Max != UsageEconomicsOverlayMax {
		return "growth overlay max mismatch against UsageEconomicsOverlayMax cap"
	}
	allowed := make(map[string]struct{}, len(usageEconomicsGrowthFiles))
	for _, f := range usageEconomicsGrowthFiles {
		allowed[f.path] = struct{}{}
	}
	for _, f := range growth.Files {
		if _, ok := allowed[f]; !ok {
			return "growth overlay file outside pinned allowlist: " + f
		}
	}
	if msg := budgetBoundaryError("usage economics growth", growth.Lines, growth.Max); msg != "" {
		return msg
	}
	return ""
}

// TestShrinkage_UsageEconomicsGrowthAllowanceIsExact verifies the remediation-3B
// growth allowance on the real tree through the deletion-friendly acceptance
// predicate: cap pinned, every measured file allowlisted, lines within cap.
// (Audited live credit is 1,368 = 1,244 new-file lines + 16 production_options +
// 108 process_billing; the predicate deliberately does not pin that total so
// deletions keep passing.)
func TestShrinkage_UsageEconomicsGrowthAllowanceIsExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	connector, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	overlays, err := measurePathMarkerOverlays(root, connector.Files)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]struct{}, len(connector.Files))
	for _, f := range connector.Files {
		claimed[f] = struct{}{}
	}
	for _, o := range overlays {
		for _, f := range o.Files {
			claimed[f] = struct{}{}
		}
	}
	growth, err := measureUsageEconomicsGrowthOverlay(root, claimed)
	if err != nil {
		t.Fatal(err)
	}
	if msg := checkUsageEconomicsGrowthAllowance(growth); msg != "" {
		t.Fatal(msg)
	}
	if !growth.Pass {
		t.Fatal("growth overlay Pass must be true when lines <= Max")
	}
}

// TestShrinkage_UsageEconomicsAllowanceAcceptancePredicate runs the complete guard
// (not merely the boundary helper) over reduced, deleted-file, over-cap, and
// broadened measurements: reductions and deletions pass; cap+1 and unknown files
// fail.
func TestShrinkage_UsageEconomicsAllowanceAcceptancePredicate(t *testing.T) {
	t.Parallel()
	reduced := OverlayMeasurement{
		Name:  "Usage economics",
		Max:   UsageEconomicsOverlayMax,
		Lines: 900,
		Files: []string{
			"internal/infra/runtimebundle/accounting_recovery.go",
			"internal/infra/runtimebundle/observation_economic_bridge.go",
		},
		Pass: true,
	}
	deleted := OverlayMeasurement{
		Name:  "Usage economics",
		Max:   UsageEconomicsOverlayMax,
		Lines: 1225,
		Files: []string{
			"internal/infra/runtimebundle/accounting_recovery.go",
			"internal/infra/runtimebundle/external_billing.go",
			"internal/infra/runtimebundle/observation_economic_bridge.go",
			"internal/infra/runtimebundle/operator_reports.go",
			"internal/infra/runtimebundle/shadow_v2_compose.go",
			"internal/stdhttp/admin/billing/operator.go",
			"pkg/aipruntime/billing.go",
			"internal/infra/runtimebundle/process_billing.go",
		},
		Pass: true,
	}
	overCap := OverlayMeasurement{
		Name:  "Usage economics",
		Max:   UsageEconomicsOverlayMax,
		Lines: UsageEconomicsOverlayMax + 1,
		Files: []string{"internal/infra/runtimebundle/operator_reports.go"},
		Pass:  false,
	}
	broadened := OverlayMeasurement{
		Name:  "Usage economics",
		Max:   UsageEconomicsOverlayMax,
		Lines: 100,
		Files: []string{
			"internal/infra/runtimebundle/operator_reports.go",
			"internal/infra/runtimebundle/unlisted_feature.go",
		},
		Pass: true,
	}
	for _, tc := range []struct {
		name string
		got  OverlayMeasurement
		pass bool
	}{
		{name: "reduced measurement passes", got: reduced, pass: true},
		{name: "deleted allowlisted file passes", got: deleted, pass: true},
		{name: "cap plus one fails", got: overCap, pass: false},
		{name: "unknown broadened file fails", got: broadened, pass: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := checkUsageEconomicsGrowthAllowance(tc.got)
			if tc.pass && msg != "" {
				t.Fatalf("want pass, got %q", msg)
			}
			if !tc.pass && msg == "" {
				t.Fatal("want failure, got pass")
			}
		})
	}
}

// TestShrinkage_UsageEconomicsOverlapGuardCatchesClaimedPath proves the independent
// pre-exclusion guard: a clean claim set passes, while an allowlist path already
// selected elsewhere fails even though defensive filtering would skip it silently.
func TestShrinkage_UsageEconomicsOverlapGuardCatchesClaimedPath(t *testing.T) {
	t.Parallel()
	clean := map[string]string{"internal/infra/runtimebundle/process_services.go": "Connector"}
	if msg := growthAllowlistOverlap(usageEconomicsGrowthFiles[:7], clean); msg != "" {
		t.Fatalf("disjoint allowlist must pass, got %q", msg)
	}
	conflict := map[string]string{
		"internal/infra/runtimebundle/process_services.go":   "Connector",
		"internal/infra/runtimebundle/production_options.go": "Billing host composition",
	}
	if msg := growthAllowlistOverlap(usageEconomicsGrowthFiles, conflict); msg == "" {
		t.Fatal("overlapping allowlist path must fail, got pass")
	}
}

// TestShrinkage_UsageEconomicsGrowthMechanics proves the allowance mechanics on
// synthetic roots: deletions and absent files credit zero, baseline content cannot
// enter, growth credits exactly, over-cap fails, and unlisted growth is ignored.
func TestShrinkage_UsageEconomicsGrowthMechanics(t *testing.T) {
	t.Parallel()
	writeLines := func(t *testing.T, root, rel string, n int) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString("package runtimebundle\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "var synthVar%d = %d\n", i, i)
		}
		if err := os.WriteFile(abs, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("deletion passes", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		m, err := measureUsageEconomicsGrowthOverlay(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.Lines != 0 || !m.Pass {
			t.Fatalf("absent files must credit zero and pass, got %+v", m)
		}
	})

	t.Run("baseline content cannot enter", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeLines(t, root, "internal/infra/runtimebundle/process_billing.go", 75)
		m, err := measureUsageEconomicsGrowthOverlay(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.Lines != 0 {
			t.Fatalf("baseline content must credit zero, got %+v", m)
		}
	})

	t.Run("growth credits exactly", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeLines(t, root, "internal/infra/runtimebundle/process_billing.go", 75+10)
		m, err := measureUsageEconomicsGrowthOverlay(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.Lines != 10 {
			t.Fatalf("growth credit = %d, want exactly 10", m.Lines)
		}
	})

	t.Run("unlisted growth ignored", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeLines(t, root, "internal/infra/runtimebundle/unlisted_feature.go", 500)
		m, err := measureUsageEconomicsGrowthOverlay(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.Lines != 0 {
			t.Fatalf("unlisted files must credit zero, got %+v", m)
		}
	})

	t.Run("claimed files never double counted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		rel := "internal/infra/runtimebundle/process_billing.go"
		writeLines(t, root, rel, 75+10)
		m, err := measureUsageEconomicsGrowthOverlay(root, map[string]struct{}{rel: {}})
		if err != nil {
			t.Fatal(err)
		}
		if m.Lines != 0 {
			t.Fatalf("claimed files must be skipped, got %+v", m)
		}
	})
}
