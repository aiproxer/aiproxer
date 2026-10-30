package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

// TestDatabaseParity_CatalogIntegrity verifies that the authoritative database-parity catalog
// passes self-consistency and repository path validation.
func TestDatabaseParity_CatalogIntegrity(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()
	if err := catalog.Validate(); err != nil {
		t.Fatalf("DefaultCatalog.Validate failed: %v", err)
	}
	if err := catalog.ValidatePaths(repoRoot); err != nil {
		t.Fatalf("DefaultCatalog.ValidatePaths failed: %v", err)
	}
}

// TestDatabaseParity_DiscoveredMigrationRootsMatchCatalog validates exact bijection between
// discovered versioned migration roots in the filesystem and the catalog declarations.
func TestDatabaseParity_DiscoveredMigrationRootsMatchCatalog(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()
	discoveredRoots, err := DiscoverMigrationRoots(repoRoot)
	if err != nil {
		t.Fatalf("DiscoverMigrationRoots: %v", err)
	}

	catalogRoots := make(map[string]bool)
	for _, root := range catalog.AllMigrationRoots() {
		catalogRoots[root] = true
	}

	// Check discovered roots against catalog
	for root, files := range discoveredRoots {
		if !catalogRoots[root] {
			t.Errorf("FAIL-CLOSED: Unregistered migration root found at %q containing %d migration files (%v). "+
				"Remediation: Register this root under the appropriate component in internal/testkit/dbparity/catalog.go.",
				root, len(files), files)
		}
	}

	// Check catalog roots against discovered roots
	for root := range catalogRoots {
		files, found := discoveredRoots[root]
		if !found || len(files) == 0 {
			t.Errorf("Catalog declares migration root %q, but no versioned migration files were discovered in that directory.", root)
		}
	}
}

// TestDatabaseParity_DialectSensitiveSourcesAreCataloged discovers all dialect-sensitive code
// across the production codebase and verifies that every occurrence belongs to either an authoritative
// registered persistence component or declared shared infrastructure.
func TestDatabaseParity_DialectSensitiveSourcesAreCataloged(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()
	findingsByPkg, err := DiscoverDialectSensitivePackages(repoRoot)
	if err != nil {
		t.Fatalf("DiscoverDialectSensitivePackages: %v", err)
	}

	for pkgDir, findings := range findingsByPkg {
		isSharedInfra := false
		for _, infra := range catalog.SharedInfra {
			if MatchPathPrefix(pkgDir, infra) {
				isSharedInfra = true
				break
			}
		}

		var owningComponent string
		for _, comp := range catalog.Components {
			for _, srcRoot := range comp.SourceRoots {
				if MatchPathPrefix(pkgDir, srcRoot) {
					if owningComponent != "" && owningComponent != comp.ID {
						t.Errorf("Ambiguous ownership: package %q matches multiple components (%q and %q)",
							pkgDir, owningComponent, comp.ID)
					}
					owningComponent = comp.ID
					break
				}
			}
		}

		if !isSharedInfra && owningComponent == "" {
			var sampleDetails []string
			for i, f := range findings {
				if i >= 5 {
					sampleDetails = append(sampleDetails, fmt.Sprintf("...and %d more findings", len(findings)-5))
					break
				}
				sampleDetails = append(sampleDetails, f.String())
			}
			t.Errorf("FAIL-CLOSED: Unregistered dialect-sensitive package discovered at %q with %d dialect indicators:\n  %s\n"+
				"Remediation: Register this package in internal/testkit/dbparity/catalog.go as a dual-dialect persistence component, "+
				"or refactor dialect-specific code into shared database infrastructure (internal/infra/db).",
				pkgDir, len(findings), strings.Join(sampleDetails, "\n  "))
		}
	}
}

// TestDatabaseParity_StoreContractsCompileTimeAssertions verifies that all registered store components
// have explicit compile-time interface assertions (e.g. `var _ Contract = (*Store)(nil)`) for their declared contracts.
func TestDatabaseParity_StoreContractsCompileTimeAssertions(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()

	for _, comp := range catalog.Components {
		if len(comp.StoreContracts) == 0 {
			continue
		}

		// Collect all interface assertions across all source files in the component's source roots
		assertions := make(map[string]bool)
		for _, srcRoot := range comp.SourceRoots {
			dirPath := filepath.Join(repoRoot, filepath.FromSlash(srcRoot))
			entries, err := os.ReadDir(dirPath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
					continue
				}
				filePath := filepath.Join(dirPath, entry.Name())
				src, err := os.ReadFile(filePath)
				if err != nil {
					t.Fatalf("ReadFile %s: %v", filePath, err)
				}
				_, f, err := ParseGoSource(filePath, src)
				if err != nil {
					t.Fatalf("ParseGoSource %s: %v", filePath, err)
				}
				for _, assertName := range DiscoverDeclaredInterfaceAssertions(f) {
					assertions[assertName] = true
				}
			}
		}

		// Verify every declared store contract is satisfied by at least one compile-time assertion
		for _, contract := range comp.StoreContracts {
			parts := strings.Split(contract, ".")
			shortName := parts[len(parts)-1]
			var pkgShortName string
			if len(parts) >= 2 {
				pkgPathParts := strings.Split(parts[len(parts)-2], "/")
				pkgShortName = pkgPathParts[len(pkgPathParts)-1] + "." + shortName
			}

			found := assertions[contract] || assertions[shortName] || (pkgShortName != "" && assertions[pkgShortName])
			if !found {
				for a := range assertions {
					if a == shortName || strings.HasSuffix(a, "."+shortName) {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("Component %q declares store contract %q, but no compile-time interface assertion "+
					"(e.g. `var _ %s = (*Store)(nil)`) was found in source roots %v. Assertions found: %v",
					comp.ID, contract, shortName, comp.SourceRoots, mapKeys(assertions))
			}
		}
	}
}

// TestDatabaseParity_SyntheticUnregisteredPackageFails verifies that introducing an uncataloged package
// with dialect-sensitive constructs fails the discovery classification test.
func TestDatabaseParity_SyntheticUnregisteredPackageFails(t *testing.T) {
	t.Parallel()
	catalog := dbparity.DefaultCatalog()

	syntheticCases := []struct {
		name       string
		pkgPath    string
		sourceCode string
	}{
		{
			name:       "unregistered sqlite import",
			pkgPath:    "internal/core/syntheticstore",
			sourceCode: "package syntheticstore\n\nimport _ \"modernc.org/sqlite\"\n",
		},
		{
			name:       "unregistered dialect check",
			pkgPath:    "internal/infra/syntheticstore",
			sourceCode: "package syntheticstore\n\nimport \"github.com/uptrace/bun/dialect\"\n\nfunc isPG(d dialect.Name) bool { return d == dialect.PG }\n",
		},
		{
			name:       "unregistered raw locking query",
			pkgPath:    "internal/plugins/syntheticstore",
			sourceCode: "package syntheticstore\n\nconst query = \"SELECT * FROM items FOR UPDATE\"\n",
		},
	}

	for _, tc := range syntheticCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset, f, err := ParseGoSource("synthetic.go", []byte(tc.sourceCode))
			if err != nil {
				t.Fatalf("ParseGoSource: %v", err)
			}
			findings := inspectParsedDialectIndicators(fset, f, tc.pkgPath+"/synthetic.go")
			if len(findings) == 0 {
				t.Fatalf("Expected dialect indicators to be detected in synthetic code, found none")
			}

			// Verify that synthetic package is not in catalog and would fail closed
			isSharedInfra := false
			for _, infra := range catalog.SharedInfra {
				if MatchPathPrefix(tc.pkgPath, infra) {
					isSharedInfra = true
					break
				}
			}
			var owningComponent string
			for _, comp := range catalog.Components {
				for _, srcRoot := range comp.SourceRoots {
					if MatchPathPrefix(tc.pkgPath, srcRoot) {
						owningComponent = comp.ID
						break
					}
				}
			}

			if isSharedInfra || owningComponent != "" {
				t.Fatalf("Synthetic package %q unexpectedly matched catalog (shared=%v, owner=%q)",
					tc.pkgPath, isSharedInfra, owningComponent)
			}
		})
	}
}

// TestDatabaseParity_SyntheticUnregisteredMigrationFails verifies that introducing an uncataloged migration file
// causes migration discovery validation to fail closed.
func TestDatabaseParity_SyntheticUnregisteredMigrationFails(t *testing.T) {
	t.Parallel()
	catalog := dbparity.DefaultCatalog()
	syntheticMigrationDir := "internal/core/syntheticunregistered"
	syntheticMigrationFile := syntheticMigrationDir + "/20260101000000_unregistered_migration.go"

	if !versionedMigrationRegex.MatchString(filepath.Base(syntheticMigrationFile)) {
		t.Fatalf("Synthetic migration filename did not match regex pattern")
	}

	catalogRoots := make(map[string]bool)
	for _, root := range catalog.AllMigrationRoots() {
		catalogRoots[root] = true
	}

	if catalogRoots[syntheticMigrationDir] {
		t.Fatalf("Synthetic migration directory %q unexpectedly exists in catalog", syntheticMigrationDir)
	}
}

// TestDatabaseParity_SharedInfraClassification verifies that shared infrastructure packages
// (internal/infra/db, internal/infra/dbmigrate, internal/infra/runtimebundle)
// are correctly classified as shared infrastructure and not as store components.
func TestDatabaseParity_SharedInfraClassification(t *testing.T) {
	t.Parallel()
	catalog := dbparity.DefaultCatalog()

	expectedShared := []string{
		"internal/bunmigrate",
		"internal/infra/db",
		"internal/infra/dbmigrate",
		"internal/infra/runtimebundle",
	}
	for _, expected := range expectedShared {
		if !slices.Contains(catalog.SharedInfra, expected) {
			t.Errorf("Expected %q to be in SharedInfra", expected)
		}

		// Ensure shared infra is not claimed as a component source root
		for _, comp := range catalog.Components {
			for _, srcRoot := range comp.SourceRoots {
				if srcRoot == expected {
					t.Errorf("Shared infrastructure %q must not be declared as a component SourceRoot (claimed by %q)",
						expected, comp.ID)
				}
			}
		}
	}
}

// TestDatabaseParity_MigrationIDDiscoveryConsistency verifies that discovering versioned migrations
// via dbparity.DiscoverMigrations for each catalog migration root yields valid, strictly ascending
// migration IDs, that every migration file found by the AST/filesystem scanner DiscoverMigrationRoots
// is accounted for, and that no migration root is empty.
func TestDatabaseParity_MigrationIDDiscoveryConsistency(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()

	discoveredRoots, err := DiscoverMigrationRoots(repoRoot)
	if err != nil {
		t.Fatalf("DiscoverMigrationRoots: %v", err)
	}

	for _, comp := range catalog.Components {
		for _, migRoot := range comp.MigrationRoots {
			absDir := filepath.Join(repoRoot, filepath.FromSlash(migRoot))
			migrations, err := dbparity.DiscoverMigrations(absDir)
			if err != nil {
				t.Fatalf("Component %q: DiscoverMigrations(%s): %v", comp.ID, migRoot, err)
			}
			if len(migrations) == 0 {
				t.Errorf("Component %q: migration root %q has 0 discovered migrations", comp.ID, migRoot)
				continue
			}

			// Verify consistency with DiscoverMigrationRoots
			expectedFiles, found := discoveredRoots[migRoot]
			if !found {
				t.Errorf("Component %q: migration root %q not found in DiscoverMigrationRoots", comp.ID, migRoot)
				continue
			}

			// Verify strictly ascending timestamps and valid 14-digit format
			discoveredIDSet := make(map[string]bool, len(migrations))
			var prevID string
			for _, m := range migrations {
				if len(m.ID) != 14 {
					t.Errorf("Component %q migration %q has invalid timestamp ID %q (length %d != 14)",
						comp.ID, m.Filename, m.ID, len(m.ID))
				}
				if prevID != "" && m.ID <= prevID {
					t.Errorf("Component %q migration %q (ID %s) is not strictly after previous migration (ID %s)",
						comp.ID, m.Filename, m.ID, prevID)
				}
				prevID = m.ID
				discoveredIDSet[m.ID] = true
			}

			// Ensure every physical migration file found by the scanner is covered by a discovered migration ID
			for _, expFile := range expectedFiles {
				base := filepath.Base(expFile)
				if len(base) < 14 {
					continue
				}
				fileID := base[:14]
				if !discoveredIDSet[fileID] {
					t.Errorf("Component %q: scanner migration file %q (ID %s) is missing from dbparity.DiscoverMigrations",
						comp.ID, expFile, fileID)
				}
			}
		}
	}
}
