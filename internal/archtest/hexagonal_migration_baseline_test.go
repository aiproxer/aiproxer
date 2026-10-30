package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	hexagonalBaselineRelPath = "testdata/architecture/hexagonal_migration_baseline.json"
	internalCoreImportPrefix = "github.com/aiproxer/aiproxer/internal/core/"
)

type hexagonalBaselineFile struct {
	SchemaVersion     int                      `json:"schema_version"`
	RetiredExceptions []string                 `json:"retired_exceptions"`
	Packages          []hexagonalBaselineEntry `json:"packages"`
}

type hexagonalBaselineEntry struct {
	GoListPattern              string                    `json:"go_list_pattern"`
	Classification             string                    `json:"classification"`
	Role                       string                    `json:"role,omitempty"`
	Justification              string                    `json:"justification"`
	RetirementTrigger          string                    `json:"retirement_trigger"`
	AllowedInternalCoreImports []string                  `json:"allowed_internal_core_imports"`
	Backlog                    *hexagonalBaselineBacklog `json:"backlog,omitempty"`
}

type hexagonalBaselineBacklog struct {
	Owner                string   `json:"owner"`
	NextExtraction       string   `json:"next_extraction"`
	RetirementTarget     string   `json:"retirement_target"`
	BlockingDependencies []string `json:"blocking_dependencies"`
	Status               string   `json:"status"`
}

// TestHexagonalMigrationBaselineIncludesAllClassifications ensures the migration
// register still models the closure target: aligned packages present and zero
// exception entries after arch review final closure.
func TestHexagonalMigrationBaselineIncludesAllClassifications(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var doc hexagonalBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if doc.SchemaVersion != 3 {
		t.Fatalf("unsupported schema_version %d (expected 3)", doc.SchemaVersion)
	}
	var aligned, extract, exception int
	for _, row := range doc.Packages {
		switch row.Classification {
		case "aligned":
			aligned++
		case "extract":
			extract++
		case "exception":
			exception++
		default:
			t.Fatalf("unexpected classification %q for %s", row.Classification, row.GoListPattern)
		}
	}
	if aligned == 0 {
		t.Fatalf("baseline must include at least one aligned package (got aligned=%d extract=%d exception=%d)",
			aligned, extract, exception)
	}
	if exception != 0 {
		t.Fatalf("baseline must have zero exception packages after closure (got aligned=%d extract=%d exception=%d)",
			aligned, extract, exception)
	}
}

// TestHexagonalMigrationBaselineMatchesGoList locks the migration register from
// introduce-hexagonal-architecture: direct internal/core imports per listed package
// must match the committed baseline (intentional edits only).
func TestHexagonalMigrationBaselineMatchesGoList(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	var doc hexagonalBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if doc.SchemaVersion != 3 {
		t.Fatalf("unsupported schema_version %d (expected 3)", doc.SchemaVersion)
	}

	for _, row := range doc.Packages {
		t.Run(strings.TrimPrefix(row.GoListPattern, "./"), func(t *testing.T) {
			t.Parallel()
			if row.GoListPattern == "" {
				t.Fatal("empty go_list_pattern")
			}
			validClass := map[string]struct{}{
				"aligned":   {},
				"extract":   {},
				"exception": {},
			}
			if _, ok := validClass[row.Classification]; !ok {
				t.Fatalf("invalid classification %q for %s", row.Classification, row.GoListPattern)
			}
			validRoles := map[string]struct{}{"": {}, "composition_root": {}}
			if _, ok := validRoles[row.Role]; !ok {
				t.Fatalf("%s: invalid role %q (allowed: empty or \"composition_root\")", row.GoListPattern, row.Role)
			}
			if row.Role == "composition_root" && row.Classification != "aligned" {
				t.Fatalf("%s: role composition_root requires classification aligned, got %q", row.GoListPattern, row.Classification)
			}
			if row.Classification == "exception" {
				if strings.TrimSpace(row.RetirementTrigger) == "" {
					t.Fatalf("%s: classification exception requires non-empty retirement_trigger", row.GoListPattern)
				}
			}
			if row.Classification == "extract" || row.Classification == "exception" {
				if row.Backlog == nil {
					t.Fatalf("%s: classification %s requires a non-empty backlog", row.GoListPattern, row.Classification)
				}
				if strings.TrimSpace(row.Backlog.Owner) == "" {
					t.Fatalf("%s: backlog.owner must be non-empty", row.GoListPattern)
				}
				if strings.TrimSpace(row.Backlog.NextExtraction) == "" {
					t.Fatalf("%s: backlog.next_extraction must be non-empty", row.GoListPattern)
				}
				if strings.TrimSpace(row.Backlog.RetirementTarget) == "" {
					t.Fatalf("%s: backlog.retirement_target must be non-empty", row.GoListPattern)
				}
				if strings.TrimSpace(row.Backlog.Status) == "" {
					t.Fatalf("%s: backlog.status must be non-empty", row.GoListPattern)
				}
			}

			got := directInternalCoreImports(t, root, row.GoListPattern)
			want := slices.Clone(row.AllowedInternalCoreImports)
			slices.Sort(want)

			if !slices.Equal(got, want) {
				t.Fatalf("%s (%s): internal/core direct import mismatch\n  got:  %v\n  want: %v",
					row.GoListPattern, row.Classification, got, want)
			}
		})
	}
}

func directInternalCoreImports(t *testing.T, repoRootDir, listPattern string) []string {
	t.Helper()

	out, err := cachedGoList(t, "-e", "-json", "-test=false", listPattern)
	if err != nil {
		t.Fatalf("go list %s: %v", listPattern, err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	if !dec.More() {
		t.Fatalf("go list %s: empty output", listPattern)
	}
	var meta struct {
		ImportPath string   `json:"ImportPath"`
		Imports    []string `json:"Imports"`
	}
	if err := dec.Decode(&meta); err != nil {
		t.Fatalf("decode go list json for %s: %v", listPattern, err)
	}
	if dec.More() {
		t.Fatalf("go_list_pattern %q matched multiple packages; baseline register expects one main module package per row (use a non-recursive path or split rows)", listPattern)
	}

	var core []string
	for _, imp := range meta.Imports {
		if strings.HasPrefix(imp, internalCoreImportPrefix) {
			core = append(core, imp)
		}
	}
	slices.Sort(core)
	return core
}
