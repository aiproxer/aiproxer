package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeepwarmFeatureRemainsProviderNeutral(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "internal", "plugins", "features", "keepwarm")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		source := strings.ToLower(string(data))
		for _, forbidden := range []string{"anthropic", "openai", "gemini", "codex", "aipapi.call", "backend.open", "time.newticker", "prompt_cache_key"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains provider/request-routing leakage %q", entry.Name(), forbidden)
			}
		}
	}
}

func TestPromptCacheMaintenanceDoesNotReenterNormalExecution(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "core", "runtime", "prompt_cache_maintenance.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, forbidden := range []string{".Open(", ".Execute(", "route", "failover", "NewCall"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("prompt cache maintenance contains normal execution/routing token %q", forbidden)
		}
	}
}

func TestKeepwarmManagerHasNoPerTargetTicker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "plugins", "features", "keepwarm", "manager.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "newticker") {
		t.Fatal("keep-warm manager must not allocate a ticker per target")
	}
}

// TestForbiddenImports_KeepwarmTreeAdversarialBypassRejected verifies that
// renamed files and nested subpackages in the keepwarm feature tree cannot
// bypass boundaries to import stdhttp, pluginreg, core, runtimebundle, or sibling features.
func TestForbiddenImports_KeepwarmTreeAdversarialBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "keepwarm nested imports stdhttp",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/stdhttp",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports pluginreg",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/pluginreg",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports sibling secretguard feature",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports core runtime",
			relPath:    "internal/plugins/features/keepwarm/nested/deep/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports runtimebundle",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports frontends",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "keepwarm nested imports backends",
			relPath:    "internal/plugins/features/keepwarm/nested/bypass.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat",
			wantForbid: true,
		},
		{
			name:       "keepwarm root imports own subpackage (allowed)",
			relPath:    "internal/plugins/features/keepwarm/manager.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm/subpkg",
			wantForbid: false,
		},
		{
			name:       "keepwarm imports pkg/aipapi (allowed)",
			relPath:    "internal/plugins/features/keepwarm/manager.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "keepwarm imports pkg/aipsdk/feature (allowed)",
			relPath:    "internal/plugins/features/keepwarm/manager.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/feature",
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
