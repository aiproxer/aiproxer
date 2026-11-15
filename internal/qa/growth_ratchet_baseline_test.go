//go:build precommit

package qa

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestGrowthRatchet_RoutesEnterpriseCountsByPrefix pins that Enterprise
// measurements land in the export-ignored record and that a public tree
// neither requires nor creates that record.
func TestGrowthRatchet_RoutesEnterpriseCountsByPrefix(t *testing.T) {
	t.Parallel()

	// A path that merely contains "enterprise" is public: routing is by prefix.
	publicEntry := growthEntry{Path: "internal/qa/enterprise_scope_test.go", Measure: measureTestSleep, Count: 1}
	enterpriseEntry := growthEntry{Path: "enterprise/internal/compose/example_test.go", Measure: measureTestSleep, Count: 2}

	publicEntries, enterpriseEntries := splitGrowthEntries([]growthEntry{publicEntry, enterpriseEntry})
	if !slices.Equal(publicEntries, []growthEntry{publicEntry}) || !slices.Equal(enterpriseEntries, []growthEntry{enterpriseEntry}) {
		t.Fatalf("routed public %+v, enterprise %+v", publicEntries, enterpriseEntries)
	}

	privateRoot := t.TempDir()
	writeGrowthBaselinesAt(t, privateRoot, true, []growthEntry{publicEntry, enterpriseEntry})
	public, enterprise, err := loadGrowthBaselines(privateRoot)
	if err != nil {
		t.Fatalf("load private baselines: %v", err)
	}
	if !slices.Equal(public.Entries, []growthEntry{publicEntry}) || !slices.Equal(enterprise.Entries, []growthEntry{enterpriseEntry}) {
		t.Fatalf("private records: public %+v, enterprise %+v", public.Entries, enterprise.Entries)
	}

	publicRoot := t.TempDir()
	writeGrowthBaselinesAt(t, publicRoot, false, []growthEntry{publicEntry})
	if _, err := os.Stat(growthBaselinePath(publicRoot, growthEnterpriseBaselineRelPath)); !os.IsNotExist(err) {
		t.Fatalf("a public tree created an Enterprise baseline: %v", err)
	}
	if err := writeGrowthBaselines(publicRoot, []growthEntry{enterpriseEntry}); err == nil {
		t.Fatal("a public tree wrote an Enterprise count")
	}
}

// TestGrowthRatchet_MissingEnterpriseBaseline pins when the Enterprise record
// may be absent, and that a misrouted Enterprise path fails the export.
func TestGrowthRatchet_MissingEnterpriseBaseline(t *testing.T) {
	t.Parallel()

	publicEntry := growthEntry{Path: "internal/qa/example_test.go", Measure: measureTestSleep, Count: 1}
	enterpriseEntry := growthEntry{Path: "enterprise/internal/compose/example_test.go", Measure: measureTestSleep, Count: 2}
	measure := func(t *testing.T, root string, measured []growthEntry) growthDiff {
		t.Helper()
		if err := writeGrowthBaseline(growthBaselinePath(root, growthBaselineRelPath), []growthEntry{publicEntry}); err != nil {
			t.Fatalf("write public baseline: %v", err)
		}
		public, enterprise, err := loadGrowthBaselines(root)
		if err != nil {
			t.Fatalf("load baselines: %v", err)
		}
		return diffGrowth(measured, slices.Concat(public.Entries, enterprise.Entries))
	}

	t.Run("private tree reports measured growth", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
			t.Fatal(err)
		}
		if diff := measure(t, root, []growthEntry{publicEntry, enterpriseEntry}); !diff.growth() {
			t.Fatal("a missing Enterprise record must report its measured counts as growth")
		}
	})

	t.Run("public tree passes", func(t *testing.T) {
		t.Parallel()
		if diff := measure(t, t.TempDir(), []growthEntry{publicEntry}); diff.growth() {
			t.Fatalf("public tree reported growth: %s", diff)
		}
	})

	t.Run("enterprise path in the public record", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := writeGrowthBaseline(growthBaselinePath(root, growthBaselineRelPath), []growthEntry{enterpriseEntry}); err != nil {
			t.Fatalf("write public baseline: %v", err)
		}
		if _, _, err := loadGrowthBaselines(root); err == nil {
			t.Fatal("an Enterprise path in the exported record must fail")
		}
	})
}

func writeGrowthBaselinesAt(t *testing.T, root string, enterpriseTree bool, measured []growthEntry) {
	t.Helper()

	if enterpriseTree {
		if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeGrowthBaselines(root, measured); err != nil {
		t.Fatalf("write baselines: %v", err)
	}
}
