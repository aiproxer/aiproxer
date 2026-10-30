package dbparity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestCatalog_Completeness(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()
	if len(cat.Components) != 8 {
		t.Fatalf("expected exactly 8 candidate components, got %d", len(cat.Components))
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
		if cat.Components[i].ID != exp {
			t.Errorf("component[%d].ID = %q; want %q", i, cat.Components[i].ID, exp)
		}
	}
}

func TestCatalog_Validate(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()
	if err := cat.Validate(); err != nil {
		t.Fatalf("DefaultCatalog failed Validate(): %v", err)
	}
}

func TestCatalog_ValidatePaths(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	cat := dbparity.DefaultCatalog()
	if err := cat.ValidatePaths(repoRoot); err != nil {
		t.Fatalf("DefaultCatalog failed ValidatePaths(%s): %v", repoRoot, err)
	}
}

func TestCatalog_SourceAndTestPathsExist(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	cat := dbparity.DefaultCatalog()

	for _, comp := range cat.Components {
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

	for _, infra := range cat.SharedInfra {
		abs := filepath.Join(repoRoot, filepath.FromSlash(infra))
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			t.Errorf("SharedInfra %q does not exist as dir (abs: %s)", infra, abs)
		}
	}
}

func TestCatalog_DiscoveredMigrationRootsMatch(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	cat := dbparity.DefaultCatalog()

	registeredMigrationRoots := make(map[string]bool)
	for _, comp := range cat.Components {
		for _, mr := range comp.MigrationRoots {
			registeredMigrationRoots[mr] = true
		}
	}

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
			t.Errorf("discovered migration root %q is not registered in catalog", root)
		}
	}

	for root := range registeredMigrationRoots {
		if !discoveredRoots[root] {
			t.Errorf("registered migration root %q has no discovered migration files", root)
		}
	}
}

func TestCatalog_CapabilitiesRationaleAndEvidence(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()

	for _, comp := range cat.Components {
		if len(comp.Capabilities) == 0 {
			t.Errorf("component %q has no capabilities declared", comp.ID)
		}
		hasCommon := false
		for _, cap := range comp.Capabilities {
			if cap.ID == "" {
				t.Errorf("component %q has capability with empty ID", comp.ID)
			}
			if !cap.Class.IsValid() {
				t.Errorf("component %q capability %q has invalid BackendClass %q", comp.ID, cap.ID, cap.Class)
			}
			if strings.TrimSpace(cap.Evidence) == "" {
				t.Errorf("component %q capability %q (class %q) missing evidence anchor", comp.ID, cap.ID, cap.Class)
			}
			if cap.Class == dbparity.Common {
				hasCommon = true
			} else {
				if strings.TrimSpace(cap.Rationale) == "" {
					t.Errorf("component %q capability %q (class %q) missing rationale", comp.ID, cap.ID, cap.Class)
				}
			}
		}
		if !hasCommon {
			t.Errorf("component %q has no common capabilities declared", comp.ID)
		}
	}
}

func TestCatalog_StoreContractsNonEmpty(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()

	for _, comp := range cat.Components {
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

func TestCatalog_SharedInfraSeparation(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()

	if len(cat.SharedInfra) == 0 {
		t.Fatal("shared infrastructure roots must not be empty")
	}

	sharedMap := make(map[string]bool)
	for _, infra := range cat.SharedInfra {
		sharedMap[infra] = true
	}

	for _, comp := range cat.Components {
		for _, src := range comp.SourceRoots {
			if sharedMap[src] {
				t.Errorf("component %q SourceRoot %q is registered as shared infrastructure", comp.ID, src)
			}
		}
		for _, mig := range comp.MigrationRoots {
			if sharedMap[mig] {
				t.Errorf("component %q MigrationRoot %q is registered as shared infrastructure", comp.ID, mig)
			}
		}
	}
}

func TestCatalog_ComponentByID(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()
	comp, ok := cat.ComponentByID("concurrency-authority")
	if !ok {
		t.Fatal("expected ComponentByID('concurrency-authority') to return true")
	}
	if comp.ID != "concurrency-authority" {
		t.Fatalf("comp.ID = %q; want concurrency-authority", comp.ID)
	}

	_, ok = cat.ComponentByID("non-existent")
	if ok {
		t.Fatal("expected ComponentByID('non-existent') to return false")
	}
}

func TestCatalog_ComponentIDs(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()
	ids := cat.ComponentIDs()
	if len(ids) != len(cat.Components) {
		t.Fatalf("len(ids) = %d; want %d", len(ids), len(cat.Components))
	}
	for i, id := range ids {
		if id != cat.Components[i].ID {
			t.Errorf("ids[%d] = %q; want %q", i, id, cat.Components[i].ID)
		}
	}
}

func TestCatalog_Accessors(t *testing.T) {
	t.Parallel()

	cat := dbparity.DefaultCatalog()

	srcRoots := cat.AllSourceRoots()
	if len(srcRoots) == 0 {
		t.Error("expected non-empty AllSourceRoots()")
	}

	testPkgs := cat.AllTestPackages()
	if len(testPkgs) == 0 {
		t.Error("expected non-empty AllTestPackages()")
	}

	migRoots := cat.AllMigrationRoots()
	if len(migRoots) == 0 {
		t.Error("expected non-empty AllMigrationRoots()")
	}

	commonCaps := cat.CommonCapabilities("concurrency-authority")
	if len(commonCaps) == 0 {
		t.Error("expected non-empty CommonCapabilities for concurrency-authority")
	}
	for _, cap := range commonCaps {
		if cap.Class != dbparity.Common {
			t.Errorf("expected Common class, got %q", cap.Class)
		}
	}

	nonCommonCaps := cat.NonCommonCapabilities("concurrency-authority")
	if len(nonCommonCaps) == 0 {
		t.Error("expected non-empty NonCommonCapabilities for concurrency-authority")
	}
	for _, cap := range nonCommonCaps {
		if cap.Class == dbparity.Common {
			t.Errorf("expected non-Common class, got %q", cap.Class)
		}
	}
}

func TestCatalog_ValidationErrors(t *testing.T) {
	t.Parallel()

	t.Run("empty components", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.Catalog{}
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty components")
		}
	})

	t.Run("blank component ID", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].ID = "   "
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank component ID")
		}
	})

	t.Run("duplicate component ID", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components = append(cat.Components, cat.Components[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate component ID")
		}
	})

	t.Run("nondeterministic component ordering", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0], cat.Components[1] = cat.Components[1], cat.Components[0]
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on unordered components")
		}
	})

	t.Run("empty source roots", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].SourceRoots = nil
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty source roots")
		}
	})

	t.Run("blank source root", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].SourceRoots = append(cat.Components[0].SourceRoots, " ")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank source root")
		}
	})

	t.Run("duplicate source root within component", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].SourceRoots = append(cat.Components[0].SourceRoots, cat.Components[0].SourceRoots[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate source root within component")
		}
	})

	t.Run("duplicate source root ownership across components", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[1].SourceRoots = append(cat.Components[1].SourceRoots, cat.Components[0].SourceRoots[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate source root ownership across components")
		}
	})

	t.Run("source root conflicts with shared infra", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].SourceRoots = append(cat.Components[0].SourceRoots, "internal/infra/db")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on source root conflicting with shared infra")
		}
	})

	t.Run("empty test packages", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].TestPackages = nil
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty test packages")
		}
	})

	t.Run("blank test package", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].TestPackages = append(cat.Components[0].TestPackages, "")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank test package")
		}
	})

	t.Run("duplicate test package within component", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].TestPackages = append(cat.Components[0].TestPackages, cat.Components[0].TestPackages[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate test package within component")
		}
	})

	t.Run("empty store contracts", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].StoreContracts = nil
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty store contracts")
		}
	})

	t.Run("blank store contract", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].StoreContracts = append(cat.Components[0].StoreContracts, "")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank store contract")
		}
	})

	t.Run("duplicate store contract within component", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].StoreContracts = append(cat.Components[0].StoreContracts, cat.Components[0].StoreContracts[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate store contract")
		}
	})

	t.Run("empty migration roots", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].MigrationRoots = nil
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty migration roots")
		}
	})

	t.Run("blank migration root", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].MigrationRoots = append(cat.Components[0].MigrationRoots, "")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank migration root")
		}
	})

	t.Run("duplicate migration root within component", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].MigrationRoots = append(cat.Components[0].MigrationRoots, cat.Components[0].MigrationRoots[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate migration root within component")
		}
	})

	t.Run("duplicate migration root ownership across components", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[1].MigrationRoots = append(cat.Components[1].MigrationRoots, cat.Components[0].MigrationRoots[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate migration root ownership across components")
		}
	})

	t.Run("migration root conflicts with shared infra", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].MigrationRoots = append(cat.Components[0].MigrationRoots, "internal/infra/db")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on migration root conflicting with shared infra")
		}
	})

	t.Run("empty capabilities", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities = nil
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on empty capabilities")
		}
	})

	t.Run("blank capability ID", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[0].ID = "  "
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank capability ID")
		}
	})

	t.Run("duplicate capability ID within component", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities = append(cat.Components[0].Capabilities, cat.Components[0].Capabilities[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate capability ID")
		}
	})

	t.Run("invalid backend class", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[0].Class = "invalid-class"
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on invalid backend class")
		}
	})

	t.Run("common capability missing evidence", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[0].Evidence = "" // index 0 is Common
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on missing evidence for common capability")
		}
	})

	t.Run("non-common capability missing evidence", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[2].Evidence = "" // index 2 is PostgresDirect
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on missing evidence for non-common capability")
		}
	})

	t.Run("non-common capability missing rationale", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[2].Rationale = "" // index 2 is PostgresDirect
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on missing rationale for non-common capability")
		}
	})

	t.Run("no common capabilities", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		for i := range cat.Components[0].Capabilities {
			cat.Components[0].Capabilities[i].Class = dbparity.SQLiteSpecific
			cat.Components[0].Capabilities[i].Rationale = "some rationale"
		}
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error when component has no common capabilities")
		}
	})

	t.Run("blank shared infra entry", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.SharedInfra = append(cat.SharedInfra, "   ")
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on blank shared infra entry")
		}
	})

	t.Run("duplicate shared infra entry", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.SharedInfra = append(cat.SharedInfra, cat.SharedInfra[0])
		if err := cat.Validate(); err == nil {
			t.Fatal("expected error on duplicate shared infra entry")
		}
	})
}

func TestCatalog_ValidatePathsErrors(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)

	t.Run("non-existent source root", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].SourceRoots = []string{"non/existent/source/root"}
		if err := cat.ValidatePaths(repoRoot); err == nil {
			t.Fatal("expected error for non-existent source root")
		}
	})

	t.Run("non-existent test package", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].TestPackages = []string{"non/existent/test/pkg"}
		if err := cat.ValidatePaths(repoRoot); err == nil {
			t.Fatal("expected error for non-existent test package")
		}
	})

	t.Run("non-existent migration root", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].MigrationRoots = []string{"non/existent/migration/root"}
		if err := cat.ValidatePaths(repoRoot); err == nil {
			t.Fatal("expected error for non-existent migration root")
		}
	})

	t.Run("non-existent shared infra", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.SharedInfra = []string{"non/existent/shared/infra"}
		if err := cat.ValidatePaths(repoRoot); err == nil {
			t.Fatal("expected error for non-existent shared infra")
		}
	})

	t.Run("non-existent evidence anchor", func(t *testing.T) {
		t.Parallel()
		cat := dbparity.DefaultCatalog()
		cat.Components[0].Capabilities[0].Evidence = "non/existent/file.go:Function"
		if err := cat.ValidatePaths(repoRoot); err == nil {
			t.Fatal("expected error for non-existent evidence anchor file")
		}
	})
}

func TestCatalog_CompatibilityWithInventory(t *testing.T) {
	t.Parallel()

	inv := dbparity.DefaultInventory()
	cat := dbparity.DefaultCatalog()

	if len(inv.Components) != len(cat.Components) {
		t.Fatalf("len(inv.Components) = %d; len(cat.Components) = %d", len(inv.Components), len(cat.Components))
	}

	for i := range inv.Components {
		if inv.Components[i].ID != cat.Components[i].ID {
			t.Errorf("inv.Components[%d].ID = %q; cat.Components[%d].ID = %q", i, inv.Components[i].ID, i, cat.Components[i].ID)
		}
	}
}
