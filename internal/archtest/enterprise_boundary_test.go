package archtest

import (
	"fmt"
	"testing"
)

// TestEnterpriseImportBoundary_RulesPresent proves the open-source to
// Enterprise one-way import rule covers every open-source root with zero
// whitelist exceptions (open-core-billing-split 1.1; Requirements 1.2, 4.3).
func TestEnterpriseImportBoundary_RulesPresent(t *testing.T) {
	t.Parallel()

	wantSources := []string{"cmd", "internal", "pkg", "tools", "connectors", "connector-support", "testdata"}
	for _, src := range wantSources {
		var matched []ForbiddenImportRule
		for _, rule := range ClosureForbiddenImports {
			if rule.SourcePattern == src && rule.TargetPattern == "/enterprise/" {
				matched = append(matched, rule)
			}
		}
		if len(matched) != 1 {
			t.Fatalf("ClosureForbiddenImports must contain exactly 1 rule for %q -> %q, got %d",
				src, "/enterprise/", len(matched))
		}
		if len(matched[0].ExceptPrefix) != 0 {
			t.Fatalf("enterprise rule %q -> %q must have zero whitelist exceptions, got %v",
				src, "/enterprise/", matched[0].ExceptPrefix)
		}
		var bare []ForbiddenImportRule
		for _, rule := range ClosureForbiddenImports {
			if rule.SourcePattern == src && rule.TargetPattern == "github.com/aiproxer/aiproxer/enterprise" {
				bare = append(bare, rule)
			}
		}
		if len(bare) != 1 {
			t.Fatalf("ClosureForbiddenImports must contain exactly 1 bare-package rule for %q, got %d",
				src, len(bare))
		}
		if len(bare[0].ExceptPrefix) != 0 {
			t.Fatalf("bare enterprise rule %q must have zero whitelist exceptions, got %v",
				src, bare[0].ExceptPrefix)
		}
	}
}

// TestEnterpriseImportBoundary_ViolationsRejected proves a deliberate
// violating import from each open-source root is rejected while the allowed
// direction (Enterprise importing open source or itself) stays silent.
func TestEnterpriseImportBoundary_ViolationsRejected(t *testing.T) {
	t.Parallel()

	const ent = "github.com/aiproxer/aiproxer/enterprise/internal/compose"
	const entRoot = "github.com/aiproxer/aiproxer/enterprise"
	const sibling = "github.com/aiproxer/aiproxer/internal/enterprisexyz"
	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{name: "cmd imports enterprise", relPath: "cmd/aipstd/main.go", importPath: ent, wantForbid: true},
		{name: "cmd imports bare enterprise package", relPath: "cmd/aipstd/main.go", importPath: entRoot, wantForbid: true},
		{name: "internal imports enterprise", relPath: "internal/core/runtime/exec.go", importPath: ent, wantForbid: true},
		{name: "internal imports bare enterprise package", relPath: "internal/core/runtime/exec.go", importPath: entRoot, wantForbid: true},
		{name: "internal nested imports enterprise subpackage", relPath: "internal/infra/runtimebundle/nested/deep.go", importPath: ent + "/billing", wantForbid: true},
		{name: "pkg imports enterprise", relPath: "pkg/aipruntime/options.go", importPath: ent, wantForbid: true},
		{name: "pkg imports bare enterprise package", relPath: "pkg/aipruntime/options.go", importPath: entRoot, wantForbid: true},
		{name: "tools imports enterprise", relPath: "tools/ossexport/main.go", importPath: ent, wantForbid: true},
		{name: "tools imports bare enterprise package", relPath: "tools/ossexport/main.go", importPath: entRoot, wantForbid: true},
		{name: "connectors imports enterprise", relPath: "connectors/acme/main.go", importPath: ent, wantForbid: true},
		{name: "connectors imports bare enterprise package", relPath: "connectors/acme/main.go", importPath: entRoot, wantForbid: true},
		{name: "connector-support imports enterprise", relPath: "connector-support/acme/helpers.go", importPath: ent, wantForbid: true},
		{name: "connector-support imports bare enterprise package", relPath: "connector-support/acme/helpers.go", importPath: entRoot, wantForbid: true},
		{name: "testdata imports enterprise", relPath: "testdata/enterprise_module/main.go", importPath: ent, wantForbid: true},
		{name: "testdata imports bare enterprise package", relPath: "testdata/enterprise_module/main.go", importPath: entRoot, wantForbid: true},
		{name: "similarly named sibling package stays silent", relPath: "internal/core/runtime/exec.go", importPath: sibling, wantForbid: false},
		{name: "enterprise imports open source allowed", relPath: "enterprise/internal/compose/compose.go", importPath: "github.com/aiproxer/aiproxer/internal/core/runtime", wantForbid: false},
		{name: "enterprise imports enterprise allowed", relPath: "enterprise/cmd/aipstd-enterprise/main.go", importPath: ent, wantForbid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package test\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports(%q): %v", tc.relPath, err)
			}
			if got := len(findings) > 0; got != tc.wantForbid {
				t.Fatalf("ScanFileForbiddenImports(%q, %q): got forbidden=%v, want %v (findings: %v)",
					tc.relPath, tc.importPath, got, tc.wantForbid, findings)
			}
		})
	}
}

// TestEnterpriseBoundary_PreexistingModuleRulesUnchanged locks the original
// semantics of the pre-existing /connectors/ and /connector-support/ rules:
// bare module imports stay allowed while nested imports stay forbidden and
// similarly named siblings stay silent. A future broadening of the shared
// instead of silently changing those rules (open-core-billing-split 1.1).
func TestEnterpriseBoundary_PreexistingModuleRulesUnchanged(t *testing.T) {
	t.Parallel()

	const mod = "github.com/aiproxer/aiproxer"
	sources := []string{"pkg/aipapi/probe.go", "pkg/aipsdk/probe.go", "internal/plugins/backends/openresponsescompat/probe.go"}
	targets := []string{"/connectors", "/connector-support"}
	for _, source := range sources {
		for _, target := range targets {
			for _, tc := range []struct {
				name       string
				importPath string
				wantForbid bool
			}{
				{name: "bare", importPath: mod + target, wantForbid: false},
				{name: "nested", importPath: mod + target + "/acme", wantForbid: true},
				{name: "sibling", importPath: mod + target + "xyz", wantForbid: false},
			} {
				t.Run(source+target+"_"+tc.name, func(t *testing.T) {
					t.Parallel()
					src := fmt.Sprintf("package probe\nimport _ %q\n", tc.importPath)
					findings, err := ScanFileForbiddenImports(source, source, []byte(src))
					if err != nil {
						t.Fatalf("ScanFileForbiddenImports(%q): %v", source, err)
					}
					if got := len(findings) > 0; got != tc.wantForbid {
						t.Fatalf("ScanFileForbiddenImports(%q, %q): got forbidden=%v, want %v (findings: %v)",
							source, tc.importPath, got, tc.wantForbid, findings)
					}
				})
			}
		}
	}
}
