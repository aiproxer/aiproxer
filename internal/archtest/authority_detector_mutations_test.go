package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // mutates source fixtures in a shared test repository.
func TestContributionAndDiagnosticsAuthorityMutationFixtures(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/standardplugins/contrib.go": `package standardplugins
var ContosoBackendContribution = []string{"contoso"}
var PoolRegistry = []string{"pool"}`,
		"internal/core/diag/central.go": `package diag
type ContosoDiagnosticRow struct { ID string }
type FabrikamProjector func()
type NeutralDiagnostic struct { ID string }
type GenericDiagnostic struct { ID string }
type NeutralDiagnosticProjector func()
type ContributionOwnedProjector func()`,
		"internal/infra/runtimebundle/config.go": `package runtimebundle
var DefaultModel = "contoso"`,
	}
	for name, source := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := DetectDuplicateAuthoritativeRegistries(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := regs["ContosoBackendContribution"]; !ok {
		t.Fatalf("arbitrary contribution authority not found: %+v", regs)
	}
	if _, ok := regs["PoolRegistry"]; ok {
		t.Fatalf("runtime PoolRegistry incorrectly treated as contribution authority: %+v", regs)
	}
	debt, err := DetectCentralProtocolDiagnosticsDebt(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ContosoDiagnosticRow", "FabrikamProjector"} {
		if _, ok := debt[name]; !ok {
			t.Fatalf("arbitrary central diagnostic debt %s not found: %+v", name, debt)
		}
	}
	for _, name := range []string{"DefaultModel", "NeutralDiagnostic", "GenericDiagnostic", "NeutralDiagnosticProjector", "ContributionOwnedProjector"} {
		if _, ok := debt[name]; ok {
			t.Fatalf("false-positive diagnostics debt %s: %+v", name, debt)
		}
	}
}

//nolint:paralleltest // mutates source fixtures in a shared test repository.
func TestAuthorityDetectors_IgnoreOwnedProjectorsAndState(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/standardplugins/owned.go": `package standardplugins
func ProjectContosoRows() []string { return []string{"x"} }`,
		"internal/infra/runtimebundle/state.go": `package runtimebundle
var RouteRegistry = map[string]string{"x":"y"}
var HealthState = map[string]string{"x":"y"}`,
	}
	for name, source := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := DetectDuplicateAuthoritativeRegistries(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("owned projector/state leaked into authority findings: %+v", regs)
	}
}

//nolint:paralleltest // mutates source fixtures in a shared test repository.
func TestDuplicateAuthoritativeRegistries_PreservesDistinctPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"internal/standardplugins/one.go", "internal/pluginreg/two.go"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(`package fixture
var SharedContribution = []string{"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := DetectDuplicateAuthoritativeRegistries(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := regs["SharedContribution"]
	if len(paths) != 2 {
		t.Fatalf("same-named authority paths collapsed: %+v", regs)
	}
}
