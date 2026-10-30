package archtest

import (
	"fmt"
	"strings"
	"testing"
)

// TestForbiddenImports_RuntimeBundleConcreteFeaturesRuleEnforced verifies that ForbiddenImports
// contains a permanent rule forbidding any production imports from internal/infra/runtimebundle
// to internal/plugins/features/*, with zero whitelist / exceptions (Requirements 5.2, 7.2, 7.8).
func TestForbiddenImports_RuntimeBundleConcreteFeaturesRuleEnforced(t *testing.T) {
	t.Parallel()

	var matched []ForbiddenImportRule
	for _, rule := range ForbiddenImports {
		if rule.SourcePattern == "internal/infra/runtimebundle" &&
			rule.TargetPattern == "/internal/plugins/features/" {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		t.Fatal("ForbiddenImports missing rule forbidding internal/infra/runtimebundle -> /internal/plugins/features/")
	}
	if len(matched) > 1 {
		t.Fatalf("expected exactly 1 runtimebundle -> features rule, got %d", len(matched))
	}
	rule := matched[0]
	if len(rule.ExceptPrefix) != 0 {
		t.Fatalf("runtimebundle forbidden feature rule must have zero whitelist exceptions, got %v", rule.ExceptPrefix)
	}
}

// TestForbiddenImports_RuntimeBundleConcreteFeaturesRenamedOrNestedBypassRejected verifies that
// renamed files, nested subpackages, or indirect filenames in runtimebundle cannot bypass the rule.
func TestForbiddenImports_RuntimeBundleConcreteFeaturesRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "runtimebundle root imports feature root package",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports toolcallrepair feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports secretguard feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports compactioncontinuity feature",
			relPath:    "internal/infra/runtimebundle/compaction_continuity_result_adapter.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports reasoningpreservation feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation",
			wantForbid: true,
		},
		{
			name:       "runtimebundle nested subpackage imports feature",
			relPath:    "internal/infra/runtimebundle/nested/sub/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard",
			wantForbid: true,
		},
		{
			name:       "runtimebundle deeply nested subpackage imports feature",
			relPath:    "internal/infra/runtimebundle/a/b/c/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/partsnoop",
			wantForbid: true,
		},
		{
			name:       "runtimebundle imports retired compactioncompose adapter (forbidden)",
			relPath:    "internal/infra/runtimebundle/compaction_continuity_generation.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/compactioncompose",
			wantForbid: true,
		},
		{
			name:       "runtimebundle imports retired reasoningcompose adapter (forbidden)",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/reasoningcompose",
			wantForbid: true,
		},
		{
			name:       "runtimebundle imports retired secretguardcompose adapter (forbidden)",
			relPath:    "internal/infra/runtimebundle/secret_guard_runtime.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/secretguardcompose",
			wantForbid: true,
		},
		{
			name:       "runtimebundle imports standardplugins distribution (allowed)",
			relPath:    "internal/infra/runtimebundle/bootstrap_effective.go",
			importPath: "github.com/aiproxer/aiproxer/internal/standardplugins",
			wantForbid: false,
		},
		{
			name:       "featurehost compaction dedicated adapter imports feature (allowed)",
			relPath:    "internal/standardplugins/featurehost/compaction/parent_port.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity",
			wantForbid: false,
		},
		{
			name:       "featurehost reasoning dedicated adapter imports feature (allowed)",
			relPath:    "internal/standardplugins/featurehost/reasoning/compose.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation",
			wantForbid: false,
		},
		{
			name:       "featurehost secretguard dedicated adapter imports feature (allowed)",
			relPath:    "internal/standardplugins/featurehost/secretguard/compose.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package test\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports(%q): %v", tc.relPath, err)
			}
			isForbidden := len(findings) > 0
			if isForbidden != tc.wantForbid {
				t.Fatalf("ScanFileForbiddenImports(%q, %q): got forbidden=%v, want %v (findings: %v)",
					tc.relPath, tc.importPath, isForbidden, tc.wantForbid, findings)
			}
		})
	}
}

// TestProductionRuntimeBundleHasZeroSecretguardhostImports scans the live
// production tree of internal/infra/runtimebundle and asserts zero imports of
// the concrete secretguardhost SDK contract (Req 8.3/9.3/13.4). Default
// environment binding construction plus presence/precedence handling live in
// standard-distribution composition (internal/standardplugins/featurehost);
// generic runtimebundle forwards only a generic environment capability.
// Test files may still construct explicit bindings to prove precedence.
func TestProductionRuntimeBundleHasZeroSecretguardhostImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	const target = "/pkg/aipsdk/secretguardhost"
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/infra/runtimebundle") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, target) {
				violations = append(violations, fmt.Sprintf("%s: imports %s", rel, imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production internal/infra/runtimebundle has forbidden secretguardhost imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestProductionRuntimeBundleHasZeroConcreteFeatureImports scans the live production tree
// of internal/infra/runtimebundle and asserts zero imports of internal/plugins/features/*.
func TestProductionRuntimeBundleHasZeroConcreteFeatureImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/infra/runtimebundle") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, "/internal/plugins/features/") {
				violations = append(violations, fmt.Sprintf("%s: imports %s", rel, imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production internal/infra/runtimebundle has forbidden feature imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
