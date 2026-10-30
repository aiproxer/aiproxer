package dbparity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 16 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("go.mod not found from %s", dir)
	return ""
}

func TestInventory_Completeness(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()
	if len(inv.Components) != 8 {
		t.Fatalf("expected exactly 8 candidate components, got %d", len(inv.Components))
	}

	expectedIDs := []string{
		"concurrency-authority",
		"continuity",
		"control-plane-ledger",
		"metering-journal",
		"proxy-memory",
		"secure-sessions",
		"session-classification",
		"terminal-work",
	}

	for i, exp := range expectedIDs {
		if inv.Components[i].ID != exp {
			t.Errorf("component[%d].ID = %q; want %q", i, inv.Components[i].ID, exp)
		}
	}
}

func TestInventory_SourceAndTestPathsExist(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	inv := dbparity.DefaultInventory()

	for _, comp := range inv.Components {
		for _, src := range comp.SourceRoots {
			abs := filepath.Join(repoRoot, filepath.FromSlash(src))
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				t.Errorf("component %q SourceRoot %q does not exist as dir (abs: %s)", comp.ID, src, abs)
			}
		}
		for _, testPkg := range comp.TestPackages {
			abs := filepath.Join(repoRoot, filepath.FromSlash(testPkg))
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				t.Errorf("component %q TestPackage %q does not exist as dir (abs: %s)", comp.ID, testPkg, abs)
			}
		}
		for _, mig := range comp.MigrationRoots {
			abs := filepath.Join(repoRoot, filepath.FromSlash(mig))
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				t.Errorf("component %q MigrationRoot %q does not exist as dir (abs: %s)", comp.ID, mig, abs)
			}
		}
	}

	for _, infra := range inv.SharedInfra {
		abs := filepath.Join(repoRoot, filepath.FromSlash(infra))
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			t.Errorf("SharedInfra %q does not exist as dir (abs: %s)", infra, abs)
		}
	}
}

func TestInventory_DiscoveredMigrationRootsMatch(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	inv := dbparity.DefaultInventory()

	registeredMigrationRoots := make(map[string]bool)
	for _, comp := range inv.Components {
		for _, mr := range comp.MigrationRoots {
			registeredMigrationRoots[mr] = true
		}
	}

	// Walk repo looking for Go migration files (timestamp prefix e.g. 20250426... or 2026...)
	discoveredRoots := make(map[string]bool)
	err := filepath.Walk(filepath.Join(repoRoot, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := info.Name()
		if strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go") && len(base) > 14 {
			isTimestamp := true
			for i := range 14 {
				if base[i] < '0' || base[i] > '9' {
					isTimestamp = false
					break
				}
			}
			if isTimestamp {
				relDir, relErr := filepath.Rel(repoRoot, filepath.Dir(path))
				if relErr == nil {
					discoveredRoots[filepath.ToSlash(relDir)] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}

	for root := range discoveredRoots {
		if !registeredMigrationRoots[root] {
			t.Errorf("discovered migration root %q is not registered in inventory", root)
		}
	}

	for root := range registeredMigrationRoots {
		if !discoveredRoots[root] {
			t.Errorf("registered migration root %q has no discovered migration files", root)
		}
	}
}

func TestInventory_CapabilitiesRationale(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()

	for _, comp := range inv.Components {
		if len(comp.Capabilities) == 0 {
			t.Errorf("component %q has no capabilities declared", comp.ID)
		}
		hasCommon := false
		for _, cap := range comp.Capabilities {
			if cap.ID == "" {
				t.Errorf("component %q has capability with empty ID", comp.ID)
			}
			if cap.Class == dbparity.Common {
				hasCommon = true
			} else {
				if strings.TrimSpace(cap.Rationale) == "" {
					t.Errorf("component %q capability %q (class %q) missing rationale", comp.ID, cap.ID, cap.Class)
				}
				if strings.TrimSpace(cap.Evidence) == "" {
					t.Errorf("component %q capability %q (class %q) missing evidence anchor", comp.ID, cap.ID, cap.Class)
				}
			}
		}
		if !hasCommon {
			t.Errorf("component %q has no common capabilities declared", comp.ID)
		}
	}
}

func TestInventory_StoreContractsNonEmpty(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()

	for _, comp := range inv.Components {
		if len(comp.StoreContracts) == 0 {
			t.Errorf("component %q has no store contracts declared", comp.ID)
		}
		for _, contract := range comp.StoreContracts {
			if strings.TrimSpace(contract) == "" {
				t.Errorf("component %q has blank store contract entry", comp.ID)
			}
		}
	}
}

func TestInventory_SharedInfraSeparation(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()

	if len(inv.SharedInfra) == 0 {
		t.Fatal("shared infrastructure roots must not be empty")
	}

	sharedMap := make(map[string]bool)
	for _, infra := range inv.SharedInfra {
		sharedMap[infra] = true
	}

	for _, comp := range inv.Components {
		for _, src := range comp.SourceRoots {
			if sharedMap[src] {
				t.Errorf("component %q SourceRoot %q is registered as shared infrastructure", comp.ID, src)
			}
		}
	}
}

func TestInventory_Validate(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()
	if err := inv.Validate(); err != nil {
		t.Fatalf("DefaultInventory failed Validate(): %v", err)
	}
}

func TestInventory_ComponentByID(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()
	comp, ok := inv.ComponentByID("concurrency-authority")
	if !ok {
		t.Fatal("expected ComponentByID('concurrency-authority') to return true")
	}
	if comp.ID != "concurrency-authority" {
		t.Fatalf("comp.ID = %q; want concurrency-authority", comp.ID)
	}

	_, ok = inv.ComponentByID("non-existent")
	if ok {
		t.Fatal("expected ComponentByID('non-existent') to return false")
	}
}

func TestInventory_ComponentIDs(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()
	ids := inv.ComponentIDs()
	if len(ids) != len(inv.Components) {
		t.Fatalf("len(ids) = %d; want %d", len(ids), len(inv.Components))
	}
	for i, id := range ids {
		if id != inv.Components[i].ID {
			t.Errorf("ids[%d] = %q; want %q", i, id, inv.Components[i].ID)
		}
	}
}

func TestInventory_ValidationErrors(t *testing.T) {
	t.Parallel()

	t.Run("empty components", func(t *testing.T) {
		t.Parallel()
		inv := dbparity.Inventory{}
		if err := inv.Validate(); err == nil {
			t.Fatal("expected error on empty components")
		}
	})

	t.Run("duplicate component ID", func(t *testing.T) {
		t.Parallel()
		inv := dbparity.DefaultInventory()
		inv.Components = append(inv.Components, inv.Components[0])
		if err := inv.Validate(); err == nil {
			t.Fatal("expected error on duplicate component ID")
		}
	})

	t.Run("nondeterministic ordering", func(t *testing.T) {
		t.Parallel()
		inv := dbparity.DefaultInventory()
		inv.Components[0], inv.Components[1] = inv.Components[1], inv.Components[0]
		if err := inv.Validate(); err == nil {
			t.Fatal("expected error on unordered components")
		}
	})

	t.Run("non-common capability missing rationale", func(t *testing.T) {
		t.Parallel()
		inv := dbparity.DefaultInventory()
		inv.Components[0].Capabilities[2].Rationale = "" // index 2 is PostgresDirect
		if err := inv.Validate(); err == nil {
			t.Fatal("expected error on missing rationale for non-common capability")
		}
	})

	t.Run("source root conflicts with shared infra", func(t *testing.T) {
		t.Parallel()
		inv := dbparity.DefaultInventory()
		inv.Components[0].SourceRoots = append(inv.Components[0].SourceRoots, "internal/infra/db")
		if err := inv.Validate(); err == nil {
			t.Fatal("expected error on source root conflicting with shared infra")
		}
	})
}
