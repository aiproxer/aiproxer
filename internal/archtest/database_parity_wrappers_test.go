package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

// TestDatabaseParity_StableWrapperCoverage verifies that every registered component has exactly
// the required stable SQLite and integration-tagged PostgreSQL-direct parity entry points
// in every registered test package.
func TestDatabaseParity_StableWrapperCoverage(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()

	for _, comp := range catalog.Components {
		for _, tp := range comp.TestPackages {
			wrappers, err := DiscoverPackageParityWrappers(repoRoot, tp)
			if err != nil {
				t.Errorf("Component %q test package %q discovery failed: %v", comp.ID, tp, err)
				continue
			}

			if !wrappers.HasSQLite {
				t.Errorf("Component %q test package %q is missing stable SQLite parity wrapper TestDBParity_SQLite. "+
					"Remediation: Add `func TestDBParity_SQLite(t *testing.T)` in %s/dbparity_test.go", comp.ID, tp, tp)
			} else if wrappers.SQLiteDeclCount != 1 {
				t.Errorf("Component %q test package %q has %d declarations of TestDBParity_SQLite; expected exactly 1",
					comp.ID, tp, wrappers.SQLiteDeclCount)
			}

			if !wrappers.HasPostgresDirect {
				t.Errorf("Component %q test package %q is missing stable PostgreSQL-direct parity wrapper TestDBParity_PostgresDirect. "+
					"Remediation: Add integration-tagged `func TestDBParity_PostgresDirect(t *testing.T)` in %s/dbparity_postgres_test.go", comp.ID, tp, tp)
			} else if wrappers.PostgresDirectDeclCount != 1 {
				t.Errorf("Component %q test package %q has %d declarations of TestDBParity_PostgresDirect; expected exactly 1",
					comp.ID, tp, wrappers.PostgresDirectDeclCount)
			}

			if wrappers.HasPostgresDirect && !wrappers.PostgresHasBuildTag {
				t.Errorf("Component %q test package %q defines TestDBParity_PostgresDirect in %s without `//go:build integration` header. "+
					"Remediation: Ensure the file defining TestDBParity_PostgresDirect starts with `//go:build integration`",
					comp.ID, tp, wrappers.PostgresDirectFile)
			}
		}
	}
}

// TestDatabaseParity_CapabilityEvidenceAnchors verifies that all declared capability evidence anchors
// resolve to existing files with non-empty content and valid symbol anchors (if specified),
// and that non-common capabilities have non-empty rationale.
func TestDatabaseParity_CapabilityEvidenceAnchors(t *testing.T) {
	t.Parallel()
	repoRoot := repoRoot(t)
	catalog := dbparity.DefaultCatalog()

	for _, comp := range catalog.Components {
		for _, cap := range comp.Capabilities {
			if err := ValidateCapabilityEvidence(repoRoot, cap); err != nil {
				t.Errorf("Component %q capability %q evidence validation failed: %v", comp.ID, cap.ID, err)
			}
		}
	}
}

// TestDatabaseParity_SyntheticMissingPostgresWrapperFails verifies that a test package
// missing TestDBParity_PostgresDirect or missing the //go:build integration tag fails validation.
func TestDatabaseParity_SyntheticMissingPostgresWrapperFails(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	// Case 1: Package with only SQLite wrapper, missing Postgres wrapper
	sqliteOnlyDir := filepath.Join(tempDir, "sqliteonly")
	if err := os.MkdirAll(sqliteOnlyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sqliteOnlyCode := "package sqliteonly_test\n\nimport \"testing\"\n\nfunc TestDBParity_SQLite(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(sqliteOnlyDir, "dbparity_test.go"), []byte(sqliteOnlyCode), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	wrappers, err := DiscoverPackageParityWrappers(tempDir, "sqliteonly")
	if err != nil {
		t.Fatalf("DiscoverPackageParityWrappers: %v", err)
	}
	if !wrappers.HasSQLite {
		t.Errorf("expected HasSQLite=true, got false")
	}
	if wrappers.HasPostgresDirect {
		t.Errorf("expected HasPostgresDirect=false for package missing Postgres wrapper, got true")
	}

	// Case 2: Package with Postgres wrapper but missing //go:build integration header
	untaggedPGDir := filepath.Join(tempDir, "untaggedpg")
	if err := os.MkdirAll(untaggedPGDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	untaggedPGCode := "package untaggedpg_test\n\nimport \"testing\"\n\nfunc TestDBParity_PostgresDirect(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(untaggedPGDir, "dbparity_postgres_test.go"), []byte(untaggedPGCode), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	wrappersUntagged, err := DiscoverPackageParityWrappers(tempDir, "untaggedpg")
	if err != nil {
		t.Fatalf("DiscoverPackageParityWrappers: %v", err)
	}
	if !wrappersUntagged.HasPostgresDirect {
		t.Errorf("expected HasPostgresDirect=true, got false")
	}
	if wrappersUntagged.PostgresHasBuildTag {
		t.Errorf("expected PostgresHasBuildTag=false for untagged postgres test, got true")
	}
}

// TestDatabaseParity_SyntheticMissingSQLiteWrapperFails verifies that a test package
// missing TestDBParity_SQLite fails validation.
func TestDatabaseParity_SyntheticMissingSQLiteWrapperFails(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	pgOnlyDir := filepath.Join(tempDir, "pgonly")
	if err := os.MkdirAll(pgOnlyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pgOnlyCode := "//go:build integration\n\npackage pgonly_test\n\nimport \"testing\"\n\nfunc TestDBParity_PostgresDirect(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(pgOnlyDir, "dbparity_postgres_test.go"), []byte(pgOnlyCode), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	wrappers, err := DiscoverPackageParityWrappers(tempDir, "pgonly")
	if err != nil {
		t.Fatalf("DiscoverPackageParityWrappers: %v", err)
	}
	if wrappers.HasSQLite {
		t.Errorf("expected HasSQLite=false for package missing SQLite wrapper, got true")
	}
	if !wrappers.HasPostgresDirect {
		t.Errorf("expected HasPostgresDirect=true, got false")
	}
	if !wrappers.PostgresHasBuildTag {
		t.Errorf("expected PostgresHasBuildTag=true, got false")
	}
}

// TestDatabaseParity_SyntheticInvalidEvidenceAnchorFails verifies that various invalid evidence
// configurations fail closed during capability evidence validation.
func TestDatabaseParity_SyntheticInvalidEvidenceAnchorFails(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	validFileRel := "pkg/valid_test.go"
	validFileAbs := filepath.Join(tempDir, "pkg", "valid_test.go")
	if err := os.MkdirAll(filepath.Dir(validFileAbs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	validCode := "package pkg\n\nfunc DeclaredHelper() {}\n"
	if err := os.WriteFile(validFileAbs, []byte(validCode), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	emptyFileRel := "pkg/empty_test.go"
	emptyFileAbs := filepath.Join(tempDir, "pkg", "empty_test.go")
	if err := os.WriteFile(emptyFileAbs, []byte(""), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	cases := []struct {
		name string
		cap  dbparity.Capability
	}{
		{
			name: "blank evidence string",
			cap: dbparity.Capability{
				ID:       "blank-evidence",
				Class:    dbparity.Common,
				Evidence: "   ",
			},
		},
		{
			name: "non-existent file",
			cap: dbparity.Capability{
				ID:       "non-existent-file",
				Class:    dbparity.Common,
				Evidence: "non/existent/file_test.go",
			},
		},
		{
			name: "evidence is a directory",
			cap: dbparity.Capability{
				ID:       "directory-evidence",
				Class:    dbparity.Common,
				Evidence: "pkg",
			},
		},
		{
			name: "evidence file is empty (0 bytes)",
			cap: dbparity.Capability{
				ID:       "empty-file-evidence",
				Class:    dbparity.Common,
				Evidence: emptyFileRel,
			},
		},
		{
			name: "symbol anchor does not exist in file",
			cap: dbparity.Capability{
				ID:       "missing-symbol",
				Class:    dbparity.Common,
				Evidence: validFileRel + ":NonExistentSymbol",
			},
		},
		{
			name: "non-common capability missing rationale",
			cap: dbparity.Capability{
				ID:        "missing-rationale",
				Class:     dbparity.PostgresDirect,
				Evidence:  validFileRel,
				Rationale: "  ",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCapabilityEvidence(tempDir, tc.cap)
			if err == nil {
				t.Fatalf("Expected ValidateCapabilityEvidence to fail for case %q, but got nil error", tc.name)
			}
		})
	}
}
