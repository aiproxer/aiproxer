package archtest

import (
	"fmt"
	"strings"
	"testing"
)

// TestForbiddenImports_CoreConcreteFeaturesRuleEnforced verifies that ForbiddenImports
// contains a permanent rule forbidding any production imports from internal/core
// to internal/plugins/features/*, with zero whitelist / exceptions (Requirements 7.1, 7.8).
func TestForbiddenImports_CoreConcreteFeaturesRuleEnforced(t *testing.T) {
	t.Parallel()

	var matched []ForbiddenImportRule
	for _, rule := range ForbiddenImports {
		if rule.SourcePattern == "internal/core" &&
			rule.TargetPattern == "/internal/plugins/features/" {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		t.Fatal("ForbiddenImports missing rule forbidding internal/core -> /internal/plugins/features/")
	}
	if len(matched) > 1 {
		t.Fatalf("expected exactly 1 internal/core -> features rule, got %d", len(matched))
	}
	rule := matched[0]
	if len(rule.ExceptPrefix) != 0 {
		t.Fatalf("internal/core forbidden feature rule must have zero whitelist exceptions, got %v", rule.ExceptPrefix)
	}
}

// TestForbiddenImports_RetiredCorePackagesRulesEnforced verifies that ForbiddenImports
// contains permanent rules forbidding any imports to the three retired core packages
// with zero whitelist exceptions (Requirement 7.3).
func TestForbiddenImports_RetiredCorePackagesRulesEnforced(t *testing.T) {
	t.Parallel()

	retiredTargets := []string{
		"/internal/core/toolcallrepair",
		"/internal/core/secretguard",
		"/internal/core/compactiondetect",
		"/internal/core/compactioncontinuity",
		"/internal/core/conversationview",
		"/internal/core/interleavedthinking",
		"/internal/core/keepwarm",
		"/internal/core/terminaldecisionpolicy",
	}

	for _, target := range retiredTargets {
		var matched []ForbiddenImportRule
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == "*" && rule.TargetPattern == target {
				matched = append(matched, rule)
			}
		}
		if len(matched) == 0 {
			t.Fatalf("ForbiddenImports missing rule forbidding * -> %s", target)
		}
		if len(matched) > 1 {
			t.Fatalf("expected exactly 1 rule for * -> %s, got %d", target, len(matched))
		}
		if len(matched[0].ExceptPrefix) != 0 {
			t.Fatalf("retired package rule for %s must have zero whitelist exceptions, got %v", target, matched[0].ExceptPrefix)
		}
	}
}

// TestForbiddenImports_CoreConcreteFeaturesRenamedOrNestedBypassRejected verifies that
// renamed files, nested subpackages, or indirect filenames in internal/core cannot bypass
// the rule forbidding concrete feature imports, while valid compose adapters and
// standardplugins remain allowed (Requirements 7.1, 7.7, 7.8).
func TestForbiddenImports_CoreConcreteFeaturesRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "core root imports feature root package",
			relPath:    "internal/core/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features",
			wantForbid: true,
		},
		{
			name:       "core root imports toolcallrepair feature",
			relPath:    "internal/core/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "core root imports secretguard feature",
			relPath:    "internal/core/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "core runtime imports toolcallrepair",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "core runtime imports secretguard",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "core runtime imports compactioncontinuity",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity",
			wantForbid: true,
		},
		{
			name:       "core runtime imports interleavedthinking",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking",
			wantForbid: true,
		},
		{
			name:       "core runtime imports reasoningpreservation",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation",
			wantForbid: true,
		},
		{
			name:       "core runtime imports keepwarm",
			relPath:    "internal/core/runtime/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm",
			wantForbid: true,
		},
		{
			name:       "core routing nested subpackage imports agentloopguard",
			relPath:    "internal/core/routing/sub/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard",
			wantForbid: true,
		},
		{
			name:       "core billing deeply nested subpackage imports partsnoop",
			relPath:    "internal/core/billing/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/partsnoop",
			wantForbid: true,
		},
		{
			name:       "core extensions nested subpackage imports codexclientcompat",
			relPath:    "internal/core/extensions/a/b/c.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat",
			wantForbid: true,
		},
		{
			name:       "core streams deeply nested subpackage imports feature subpackage",
			relPath:    "internal/core/streams/a/b/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair",
			wantForbid: true,
		},
		{
			name:       "core imports aipapi (allowed)",
			relPath:    "internal/core/runtime/service.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "core imports aipsdk/feature (allowed)",
			relPath:    "internal/core/runtime/service.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/feature",
			wantForbid: false,
		},
		{
			name:       "core imports internal/core/routeoverride (allowed)",
			relPath:    "internal/core/runtime/service.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/routeoverride",
			wantForbid: false,
		},
		{
			name:       "standardplugins distribution imports toolcallrepair (allowed)",
			relPath:    "internal/standardplugins/features_install.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: false,
		},
		{
			name:       "standardplugins distribution imports secretguard (allowed)",
			relPath:    "internal/standardplugins/features_install.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: false,
		},
		{
			name:       "standardplugins distribution imports keepwarm (allowed)",
			relPath:    "internal/standardplugins/features_install.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm",
			wantForbid: false,
		},
		{
			name:       "featurehost compaction dedicated adapter imports compactioncontinuity (allowed)",
			relPath:    "internal/standardplugins/featurehost/compaction/parent_port.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity",
			wantForbid: false,
		},
		{
			name:       "reasoningcompose dedicated adapter imports reasoningpreservation (allowed)",
			relPath:    "internal/infra/reasoningcompose/bind.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation",
			wantForbid: false,
		},
		{
			name:       "secretguardcompose dedicated adapter imports secretguard (allowed)",
			relPath:    "internal/infra/secretguardcompose/compose.go",
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

// TestProductionCoreHasZeroConcreteFeatureImports scans the live production tree
// of internal/core and asserts zero imports of internal/plugins/features/*.
func TestProductionCoreHasZeroConcreteFeatureImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/core") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, "/internal/plugins/features") {
				violations = append(violations, fmt.Sprintf("%s: imports %s", rel, imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production internal/core has forbidden feature imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
