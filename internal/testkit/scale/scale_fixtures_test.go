package scale_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/scale"
)

func TestInspectDiff_UsesGitProducedDiff(t *testing.T) {
	inspector := scale.RealSharedBoundaryInspector{}
	root := t.TempDir()
	diff, err := scale.CreateDeterministicGitChange(root, "profile-contrib.go", "package p\nvar X = 1\n", "package p\nvar FrontendProfiles = make([]Pair, len(frontends)*len(profiles))\n")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := inspector.InspectDiff(root, diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.StructuralViolations) == 0 {
		t.Fatal("violating Git-produced diff was not detected")
	}
	cleanRoot := t.TempDir()
	diff, err = scale.CreateDeterministicGitChange(cleanRoot, "provider-profiles/contoso.go", "package p\nvar Profiles = []string{\"provider-profile-0999\"}\n", "package p\nvar Profiles = []string{\"provider-profile-0999\", \"provider-profile-1000\"}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+var Profiles = []string{\"provider-profile-0999\", \"provider-profile-1000\"}") || strings.Contains(diff, "-var ProfileID") {
		t.Fatalf("profile #1000 evidence is not an actual addition: %s", diff)
	}
	fp, err = inspector.InspectDiff(cleanRoot, diff)
	if err != nil {
		t.Fatal(err)
	}
	if err := fp.ValidateZeroSharedFootprint(); err != nil {
		t.Fatalf("clean Git diff rejected: %v", err)
	}
}

func TestSharedBoundaryInspector_PositiveAndNegative(t *testing.T) {
	t.Parallel()

	inspector := scale.RealSharedBoundaryInspector{}

	// Positive fixture: provider profile altering only profile catalog
	cleanFiles := []string{
		"internal/providerprofiles/catalog/custom.json",
	}
	fpClean := inspector.InspectFileChanges("clean-profile", cleanFiles, 0)
	if err := fpClean.ValidateZeroSharedFootprint(); err != nil {
		t.Fatalf("expected clean profile files to pass zero footprint validation: %v", err)
	}

	// Negative fixtures: provider profile modifying shared boundaries
	violatingFiles := []struct {
		name  string
		files []string
	}{
		{"Core Edit", []string{"internal/core/runtime/executor.go"}},
		{"API Edit", []string{"pkg/aipapi/call.go"}},
		{"Proto Edit", []string{"api/backendplugin/v1/backend.proto"}},
		{"Central Table Edit", []string{"internal/standardplugins/table.go"}},
		{"Plugin Reg Edit", []string{"internal/pluginreg/registry.go"}},
	}

	for _, tc := range violatingFiles {
		fpViolating := inspector.InspectFileChanges("bad-profile", tc.files, 0)
		if err := fpViolating.ValidateZeroSharedFootprint(); err == nil {
			t.Fatalf("expected zero footprint validation to fail for %s", tc.name)
		}
	}

	// Essential List Addition Negative
	fpEssential := inspector.InspectProfileAgainstCentralLists("openai-responses")
	if err := fpEssential.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected zero footprint validation to fail when profile is in EssentialBackendKinds")
	}
}

func TestInspectDiff_CoversEveryStructuralViolation(t *testing.T) {
	t.Parallel()
	inspector := scale.RealSharedBoundaryInspector{}
	cases := []struct {
		name     string
		source   string
		category string
	}{
		{"frontend-profile-collection", `package fixture
var frontendProfiles = make([]Pair, len(frontends)*len(profiles))`, scale.DebtFrontendProfilePairs},
		{"frontend-backend-collection", `package fixture
var frontendBackends = make([]Pair, len(frontends)*len(backends))`, scale.DebtFrontendBackendPairs},
		{"nested-materializer", `package fixture
func build() { for _, fe := range frontends { for _, be := range backends { _ = fe; _ = be } } }`, scale.DebtNestedPairMaterializer},
		{"per-profile-factory", `package fixture
func build() { for _, profile := range profiles { NewProviderFactory(profile) } }`, scale.DebtPerProfileFactory},
		{"per-profile-registration", `package fixture
func build() { for _, profile := range profiles { RegisterProvider(profile) } }`, scale.DebtPerProfileRegistration},
		{"per-profile-goroutine", `package fixture
func build() { for _, profile := range profiles { go startProvider(profile) } }`, scale.DebtPerProfileGoroutine},
		{"essential-list", `package fixture
var EssentialBackendKinds = []string{"profile"}`, scale.DebtCentralListMutation},
		{"compatible-list", `package fixture
var CompatibleBackendKinds = []string{"profile"}`, scale.DebtCentralListMutation},
		{"sentinel-growth", `package fixture
var SentinelCount = len(frontends)`, scale.DebtSentinelGrowth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "fixture.go"
			if tc.category == scale.DebtCentralListMutation {
				path = "internal/standardplugins/lists.go"
			}
			diff := "--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1,20 @@\n"
			var diffBuilder strings.Builder
			diffBuilder.WriteString(diff)
			for line := range strings.SplitSeq(strings.TrimSuffix(tc.source, "\n"), "\n") {
				diffBuilder.WriteByte('+')
				diffBuilder.WriteString(line)
				diffBuilder.WriteByte('\n')
			}
			footprint, err := inspector.InspectDiff(t.TempDir(), diffBuilder.String())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, violation := range footprint.StructuralViolations {
				if strings.HasPrefix(violation, tc.category+":") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("InspectDiff did not report %s: %+v", tc.category, footprint.StructuralViolations)
			}
		})
	}
}

func TestScanRepository_IsolatedStructuralCategoryMutations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		path       string
		source     string
		categories []string
	}{
		{"pair-collection", "pairs.go", `package fixture
var frontendProfiles = make([]Pair, len(frontends)*len(profiles))`, []string{scale.DebtFrontendProfilePairs}},
		// The nested-loop detector intentionally reports both the direct pair
		// dimension and its nested materializer as one unavoidable evidence set.
		{"nested-pair-materializer", "pairs.go", `package fixture
func build() { for _, fe := range frontends { for _, be := range backends { _ = fe; _ = be } } }`, []string{scale.DebtFrontendBackendPairs, scale.DebtNestedPairMaterializer}},
		{"per-profile-factory", "factory.go", `package fixture
func build() { for _, profile := range profiles { NewProviderFactory(profile) } }`, []string{scale.DebtPerProfileFactory}},
		{"registration", "registration.go", `package fixture
func build() { for _, profile := range profiles { RegisterProvider(profile) } }`, []string{scale.DebtPerProfileRegistration}},
		{"goroutine", "goroutine.go", `package fixture
func build() { for _, profile := range profiles { go startProvider(profile) } }`, []string{scale.DebtPerProfileGoroutine}},
		{"essential-list", "internal/standardplugins/lists.go", `package standardplugins
var EssentialBackendKinds = []string{"profile"}`, []string{scale.DebtCentralListMutation}},
		{"compatible-list", "internal/standardplugins/lists.go", `package standardplugins
var CompatibleBackendKinds = []string{"profile"}`, []string{scale.DebtCentralListMutation}},
		{"sentinel-growth", "integration/sentinel.go", `package integration
var SentinelCount = len(frontends)`, []string{scale.DebtSentinelGrowth}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			report, err := scale.ScanRepository(root)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, finding := range report.Findings {
				got[finding.Category]++
			}
			for _, category := range tc.categories {
				if got[category] == 0 {
					t.Fatalf("missing %s: %+v", category, report.Findings)
				}
			}
			if len(got) != len(tc.categories) {
				t.Fatalf("isolated %s leaked categories: %+v", tc.name, got)
			}
		})
	}
}

func TestScanRepository_CleanRepositoryHasNoStructuralDebt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "clean.go"), []byte("package clean\nfunc Stable() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := scale.ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("clean repository findings: %+v", report.Findings)
	}
}

func TestScanRepository_ReportsEveryStructuralCategory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"pairs.go": `package fixture
var frontendProfiles = make([]Pair, len(frontends)*len(profiles))
var frontendBackends = make([]Pair, len(frontends)*len(backends))
func build() { for _, fe := range frontends { for _, be := range backends { _ = fe; _ = be } } }`,
		"factory.go": `package fixture
func build() { for _, profile := range profiles { NewProviderFactory(profile); RegisterProvider(profile); go startProvider(profile) } }`,
		"internal/standardplugins/lists.go": `package standardplugins
var EssentialBackendKinds = []string{"x"}
var CompatibleBackendKinds = []string{"x"}`,
		"sentinel.go": `package fixture
var SentinelCount = len(frontends)`,
	}
	for path, source := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report, err := scale.ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{scale.DebtFrontendProfilePairs, scale.DebtFrontendBackendPairs, scale.DebtNestedPairMaterializer, scale.DebtPerProfileFactory, scale.DebtPerProfileRegistration, scale.DebtPerProfileGoroutine, scale.DebtCentralListMutation, scale.DebtSentinelGrowth} {
		found := false
		for _, f := range report.Findings {
			if f.Category == category {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ScanRepository missed %s: %+v", category, report.Findings)
		}
	}
}

func TestScanCurrentCartesianDebt_SyntheticAST(t *testing.T) {
	t.Parallel()

	// Parse clean AST snippet (no AllCells calls or loops)
	cleanSrc := `package test
func DoSomething() {
	_ = "clean"
}`
	fset := token.NewFileSet()
	cleanFile, err := parser.ParseFile(fset, "clean.go", cleanSrc, 0)
	if err != nil {
		t.Fatalf("failed to parse clean snippet: %v", err)
	}

	allCellsCalls := 0
	ast.Inspect(cleanFile, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "AllCells" {
				allCellsCalls++
			}
		}
		return true
	})
	if allCellsCalls != 0 {
		t.Fatalf("expected 0 AllCells calls in clean snippet, got %d", allCellsCalls)
	}

	// Parse violating AST snippet with AllCells loop
	violatingSrc := `package test
func TestMatrix() {
	for _, cell := range AllCells() {
		_ = cell
	}
}`
	violatingFile, err := parser.ParseFile(fset, "violating.go", violatingSrc, 0)
	if err != nil {
		t.Fatalf("failed to parse violating snippet: %v", err)
	}

	violatingCalls := 0
	ast.Inspect(violatingFile, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "AllCells" {
				violatingCalls++
			}
		}
		return true
	})
	if violatingCalls != 1 {
		t.Fatalf("expected 1 AllCells call in violating snippet, got %d", violatingCalls)
	}
}
