package archtest

import (
	"fmt"
	"strings"
	"testing"
)

// TestForbiddenImports_FeatureTreeRulesEnforced verifies that ForbiddenImports contains
// the complete recursive boundary rules for toolcallrepair and secretguard feature trees (Requirements 7.1, 7.7).
func TestForbiddenImports_FeatureTreeRulesEnforced(t *testing.T) {
	t.Parallel()

	featureTrees := []struct {
		source      string
		ownPrefix   string
		expectRules []string
	}{
		{
			source:    "internal/plugins/features/toolcallrepair",
			ownPrefix: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			expectRules: []string{
				"/internal/core",
				"/internal/infra/runtimebundle",
				"/internal/plugins/frontends",
				"/internal/plugins/backends",
				"/internal/plugins/features/",
				"/internal/stdhttp",
				"/internal/pluginreg",
			},
		},
		{
			source:    "internal/plugins/features/secretguard",
			ownPrefix: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			expectRules: []string{
				"/internal/core",
				"/internal/infra/runtimebundle",
				"/internal/plugins/frontends",
				"/internal/plugins/backends",
				"/internal/plugins/features/",
				"/internal/stdhttp",
				"/internal/pluginreg",
			},
		},
		{
			source:    "internal/plugins/features/compactioncontinuity",
			ownPrefix: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity",
			expectRules: []string{
				"/internal/core",
				"/internal/infra/runtimebundle",
				"/internal/plugins/frontends",
				"/internal/plugins/backends",
				"/internal/plugins/features/",
				"/internal/stdhttp",
				"/internal/pluginreg",
			},
		},
		{
			source:    "internal/plugins/features/interleavedthinking",
			ownPrefix: "github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking",
			expectRules: []string{
				"/internal/core",
				"/internal/infra/runtimebundle",
				"/internal/plugins/frontends",
				"/internal/plugins/backends",
				"/internal/plugins/features/",
				"/internal/stdhttp",
				"/internal/pluginreg",
			},
		},
		{
			source:    "internal/plugins/features/keepwarm",
			ownPrefix: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm",
			expectRules: []string{
				"/internal/core",
				"/internal/infra/runtimebundle",
				"/internal/plugins/frontends",
				"/internal/plugins/backends",
				"/internal/plugins/features/",
				"/internal/stdhttp",
				"/internal/pluginreg",
			},
		},
	}

	for _, ft := range featureTrees {
		for _, target := range ft.expectRules {
			var matched []ForbiddenImportRule
			for _, rule := range ForbiddenImports {
				if rule.SourcePattern == ft.source && rule.TargetPattern == target {
					matched = append(matched, rule)
				}
			}
			if len(matched) == 0 {
				t.Fatalf("ForbiddenImports missing rule for source %q target %q", ft.source, target)
			}
			if target == "/internal/plugins/features/" {
				if len(matched[0].ExceptPrefix) != 1 || matched[0].ExceptPrefix[0] != ft.ownPrefix {
					t.Fatalf("%s rule for features must exempt only own prefix %q, got %v", ft.source, ft.ownPrefix, matched[0].ExceptPrefix)
				}
			} else if len(matched[0].ExceptPrefix) != 0 {
				t.Fatalf("%s rule for %s must have zero whitelist exceptions, got %v", ft.source, target, matched[0].ExceptPrefix)
			}
		}
	}
}

// TestForbiddenImports_ToolCallRepairTreeAdversarialBypassRejected verifies that
// renamed files and nested subpackages in the toolcallrepair feature tree cannot
// bypass boundaries to import stdhttp, pluginreg, core, runtimebundle, or sibling features.
func TestForbiddenImports_ToolCallRepairTreeAdversarialBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "repair nested imports stdhttp",
			relPath:    "internal/plugins/features/toolcallrepair/repair/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/stdhttp",
			wantForbid: true,
		},
		{
			name:       "repair nested imports pluginreg",
			relPath:    "internal/plugins/features/toolcallrepair/repair/jsonshape/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/pluginreg",
			wantForbid: true,
		},
		{
			name:       "repair nested imports sibling secretguard feature",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "repair nested imports prefix-colliding sibling toolcallrepairmalicious rejected",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepairmalicious",
			wantForbid: true,
		},
		{
			name:       "repair nested imports sibling secretguardextra feature rejected",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguardextra",
			wantForbid: true,
		},
		{
			name:       "repair nested imports core runtime",
			relPath:    "internal/plugins/features/toolcallrepair/repair/nested/deep/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "repair nested imports runtimebundle",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "repair nested imports frontends",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "repair nested imports backends",
			relPath:    "internal/plugins/features/toolcallrepair/repair/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses",
			wantForbid: true,
		},
		{
			name:       "repair imports own jsonshape subpackage (allowed)",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape",
			wantForbid: false,
		},
		{
			name:       "repair imports pkg/aipapi (allowed)",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "repair imports pkg/aipsdk/toolcall (allowed)",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall",
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

// TestForbiddenImports_SecretGuardTreeAdversarialBypassRejected verifies that
// renamed files and nested subpackages in the secretguard feature tree cannot
// bypass boundaries to import stdhttp, pluginreg, core, runtimebundle, or sibling features.
func TestForbiddenImports_SecretGuardTreeAdversarialBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "engine nested imports stdhttp",
			relPath:    "internal/plugins/features/secretguard/engine/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/stdhttp",
			wantForbid: true,
		},
		{
			name:       "engine nested imports pluginreg",
			relPath:    "internal/plugins/features/secretguard/engine/nested/deep/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/pluginreg",
			wantForbid: true,
		},
		{
			name:       "engine nested imports sibling toolcallrepair feature",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "engine nested imports prefix-colliding sibling secretguardextra rejected",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguardextra",
			wantForbid: true,
		},
		{
			name:       "engine nested imports sibling toolcallrepairmalicious feature rejected",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepairmalicious",
			wantForbid: true,
		},
		{
			name:       "engine nested imports core runtime",
			relPath:    "internal/plugins/features/secretguard/engine/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "engine nested imports runtimebundle",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "engine nested imports frontends",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "engine nested imports backends",
			relPath:    "internal/plugins/features/secretguard/engine/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses",
			wantForbid: true,
		},
		{
			name:       "secretguard root imports own engine subpackage (allowed)",
			relPath:    "internal/plugins/features/secretguard/guard.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine",
			wantForbid: false,
		},
		{
			name:       "engine imports pkg/aipapi (allowed)",
			relPath:    "internal/plugins/features/secretguard/engine/matcher.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "engine imports pkg/aipsdk/secretguard (allowed)",
			relPath:    "internal/plugins/features/secretguard/engine/matcher.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard",
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

// TestForbiddenImports_InterleavedThinkingTreeAdversarialBypassRejected verifies that
// renamed files and nested subpackages in the interleavedthinking feature tree cannot
// bypass boundaries to import stdhttp, pluginreg, core, runtimebundle, or sibling features.
func TestForbiddenImports_InterleavedThinkingTreeAdversarialBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "interleaved nested imports stdhttp",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/stdhttp",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports pluginreg",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/pluginreg",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports sibling secretguard feature",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports core runtime",
			relPath:    "internal/plugins/features/interleavedthinking/nested/deep/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports runtimebundle",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports frontends",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "interleaved nested imports backends",
			relPath:    "internal/plugins/features/interleavedthinking/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat",
			wantForbid: true,
		},
		{
			name:       "interleaved root imports own state subpackage (allowed)",
			relPath:    "internal/plugins/features/interleavedthinking/processor.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state",
			wantForbid: false,
		},
		{
			name:       "interleaved imports pkg/aipapi (allowed)",
			relPath:    "internal/plugins/features/interleavedthinking/processor.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
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

// TestProductionFeatureTreesHaveZeroForbiddenImports scans the live production trees
// of toolcallrepair, secretguard, compactioncontinuity, interleavedthinking, and keepwarm and asserts zero forbidden imports.
func TestProductionFeatureTreesHaveZeroForbiddenImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/plugins/features/toolcallrepair") &&
			!MatchPathPrefix(pkg, "internal/plugins/features/secretguard") &&
			!MatchPathPrefix(pkg, "internal/plugins/features/compactioncontinuity") &&
			!MatchPathPrefix(pkg, "internal/plugins/features/interleavedthinking") &&
			!MatchPathPrefix(pkg, "internal/plugins/features/keepwarm") {
			return nil
		}
		findings, err := ScanFileForbiddenImports(rel, abs, src)
		if err != nil {
			return err
		}
		for _, f := range findings {
			violations = append(violations, f.String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production feature trees have forbidden imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
