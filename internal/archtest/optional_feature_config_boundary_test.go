package archtest

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

// TestForbiddenImports_CoreConfigRuleEnforced verifies that ForbiddenImports
// contains an explicit compact rule forbidding imports from internal/core/config
// to /internal/plugins/features/ with zero whitelist exceptions (Requirements 10.4, 12.1).
func TestForbiddenImports_CoreConfigRuleEnforced(t *testing.T) {
	t.Parallel()

	var matched []ForbiddenImportRule
	for _, rule := range ForbiddenImports {
		if rule.SourcePattern == "internal/core/config" &&
			rule.TargetPattern == "/internal/plugins/features/" {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		t.Fatal("ForbiddenImports missing explicit rule forbidding internal/core/config -> /internal/plugins/features/")
	}
	if len(matched) > 1 {
		t.Fatalf("expected exactly 1 internal/core/config -> features rule, got %d", len(matched))
	}
	if len(matched[0].ExceptPrefix) != 0 {
		t.Fatalf("internal/core/config forbidden features rule must have zero exceptions, got %v", matched[0].ExceptPrefix)
	}
}

// TestProductionCoreConfig_HasZeroFeatureImports asserts that no production Go
// file in internal/core/config imports any internal/plugins/features/* package.
func TestProductionCoreConfig_HasZeroFeatureImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	configPkgRel := filepath.Join("internal", "core", "config")
	var violations []string

	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if pkg != configPkgRel {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, "/internal/plugins/features") {
				violations = append(violations, fmt.Sprintf("%s imports %s", rel, imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("internal/core/config has forbidden feature imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// forbiddenCoreConfigSymbols is the set of known optional feature config/default symbols
// that must NEVER appear in internal/core/config production files.
var forbiddenCoreConfigSymbols = map[string]string{
	// Interleaved thinking feature symbols
	"StreamToClient":              "interleaved feature config field (moved to internal/plugins/features/interleavedthinking)",
	"RegularTurnsRemaining":       "interleaved feature config field (moved to internal/plugins/features/interleavedthinking)",
	"MaxMemoBytes":                "interleaved feature config field (moved to internal/plugins/features/interleavedthinking)",
	"InstructionsFile":            "interleaved feature config field (moved to internal/plugins/features/interleavedthinking)",
	"DefaultStreamToClient":       "interleaved feature default constant (moved to internal/plugins/features/interleavedthinking)",
	"DefaultRegularTurns":         "interleaved feature default constant (moved to internal/plugins/features/interleavedthinking)",
	"DefaultMaxMemoBytes":         "interleaved feature default constant (moved to internal/plugins/features/interleavedthinking)",
	"DefaultMaxInstructionsBytes": "interleaved feature default constant (moved to internal/plugins/features/interleavedthinking)",
	"DefaultInstructions":         "interleaved built-in prompt constant (moved to internal/plugins/features/interleavedthinking)",

	// Keepwarm feature symbols
	"MaxRefreshesPerIdleEpoch":        "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"MaxIdleDuration":                 "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"MaxActiveTargets":                "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"MaxConcurrentRenewals":           "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"RenewTimeout":                    "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"ContinueAfterColdRecreate":       "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"MaxColdRecreatesPerIdleEpoch":    "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"MaxProviderTokensPerIdleEpoch":   "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"HeuristicOverrides":              "keepwarm feature config field (moved to internal/plugins/features/keepwarm)",
	"HeuristicOverride":               "keepwarm feature config type (moved to internal/plugins/features/keepwarm)",
	"DefaultMaxRefreshesPerIdleEpoch": "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
	"DefaultMaxIdleDuration":          "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
	"DefaultMaxActiveTargets":         "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
	"DefaultMaxConcurrentRenewals":    "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
	"DefaultRenewTimeout":             "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
	"DefaultMaxPolicyEntries":         "keepwarm feature default constant (moved to internal/plugins/features/keepwarm)",
}

// scanFileCoreConfigSymbols checks an AST for forbidden symbols and large prompt literals.
func scanFileCoreConfigSymbols(rel string, f *ast.File) []string {
	var violations []string

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if reason, forbidden := forbiddenCoreConfigSymbols[x.Name]; forbidden {
				violations = append(violations, fmt.Sprintf("%s: forbidden symbol %q (%s)", rel, x.Name, reason))
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				val := x.Value
				// Strip quotes
				if len(val) >= 2 && (val[0] == '`' || val[0] == '"') {
					val = val[1 : len(val)-1]
				}
				// Check for prompt markers
				if strings.Contains(val, "You now become a thinker") ||
					strings.Contains(val, "Session Steering Memo") {
					violations = append(violations, fmt.Sprintf("%s: forbidden prompt literal detected (%q)", rel, val[:min(len(val), 32)]))
				}
				// Check for excessively large prompt literal in config package (> 256 bytes)
				if len(val) > 256 {
					violations = append(violations, fmt.Sprintf("%s: large string literal (%d bytes) forbidden in core config", rel, len(val)))
				}
			}
		}
		return true
	})

	return violations
}

// TestProductionCoreConfig_ForbidsFeatureSymbolsAndPrompts verifies that no
// production file in internal/core/config contains known optional feature symbols
// or large prompt literals (Requirement 10.4, Task 9.4).
func TestProductionCoreConfig_ForbidsFeatureSymbolsAndPrompts(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	configPkgRel := filepath.Join("internal", "core", "config")
	var violations []string

	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if pkg != configPkgRel {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		v := scanFileCoreConfigSymbols(rel, f)
		violations = append(violations, v...)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("internal/core/config contains forbidden feature symbols/prompts (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestProductionCoreConfig_SyntheticViolationsRejected proves that the scanner
// detects and rejects forbidden symbols and prompt literals when introduced.
func TestProductionCoreConfig_SyntheticViolationsRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		src        string
		wantSubstr string
	}{
		{
			name:       "stream to client field",
			src:        "package config\ntype Foo struct { StreamToClient string }\n",
			wantSubstr: "StreamToClient",
		},
		{
			name:       "default thinker prompt",
			src:        "package config\nconst DefaultInstructions = \"You are a thinker\"\n",
			wantSubstr: "DefaultInstructions",
		},
		{
			name:       "keepwarm max refreshes",
			src:        "package config\nconst MaxRefreshesPerIdleEpoch = 6\n",
			wantSubstr: "MaxRefreshesPerIdleEpoch",
		},
		{
			name:       "thinker prompt literal",
			src:        "package config\nvar prompt = \"You now become a thinker.\"\n",
			wantSubstr: "forbidden prompt literal",
		},
		{
			name:       "oversized prompt literal",
			src:        fmt.Sprintf("package config\nvar p = %q\n", strings.Repeat("A", 300)),
			wantSubstr: "large string literal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, f, err := ParseGoSource("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("ParseGoSource: %v", err)
			}
			violations := scanFileCoreConfigSymbols("synthetic.go", f)
			if len(violations) == 0 {
				t.Fatalf("expected violation for %s, got none", tc.name)
			}
			found := false
			for _, v := range violations {
				if strings.Contains(v, tc.wantSubstr) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected violation containing %q, got: %v", tc.wantSubstr, violations)
			}
		})
	}
}

// TestStructural_NewStandardFeatureConfigWithoutCoreEdits proves that a new
// standard feature config can be added through feature registration without
// editing core config production files (Requirement 10.4, Task 9.4).
func TestStructural_NewStandardFeatureConfigWithoutCoreEdits(t *testing.T) {
	t.Parallel()

	// 1. Define an arbitrary new feature configuration outside core
	type SyntheticFeatureConfig struct {
		Enabled            bool   `yaml:"enabled"`
		Strategy           string `yaml:"strategy"`
		PromptBudgetTokens int    `yaml:"prompt_budget_tokens"`
		RetryCeiling       int    `yaml:"retry_ceiling"`
	}

	decodeSyntheticFeature := func(n yaml.Node) (SyntheticFeatureConfig, error) {
		var cfg SyntheticFeatureConfig
		if err := n.Decode(&cfg); err != nil {
			return SyntheticFeatureConfig{}, fmt.Errorf("decode synthetic feature: %w", err)
		}
		if cfg.Strategy == "" {
			return SyntheticFeatureConfig{}, fmt.Errorf("strategy required")
		}
		return cfg, nil
	}

	// 2. Supply YAML configuring this new feature via canonical plugins.features
	rawYAML := `
server:
  address: "127.0.0.1:0"
logging:
  level: "info"
plugins:
  features:
    - id: "synthetic-ux-feature"
      enabled: true
      config:
        enabled: true
        strategy: "adaptive-speculative"
        prompt_budget_tokens: 4096
        retry_ceiling: 3
`

	// 3. StrictDecode decodes into core Config without knowing the feature's schema
	cfg, cat, err := config.StrictDecode([]byte(rawYAML))
	if err != nil {
		t.Fatalf("StrictDecode failed: %v", err)
	}
	if cat != config.CategoryOK {
		t.Fatalf("StrictDecode category = %q, want ok", cat)
	}

	// 4. Feature configuration was preserved as an opaque yaml.Node in Plugins.Features
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("expected 1 feature in Plugins.Features, got %d", len(cfg.Plugins.Features))
	}
	feat := cfg.Plugins.Features[0]
	if feat.ID != "synthetic-ux-feature" {
		t.Errorf("feature ID = %q, want synthetic-ux-feature", feat.ID)
	}
	if !feat.Enabled {
		t.Error("expected feature to be enabled")
	}

	// 5. The feature's own decoder successfully decodes its private configuration
	decoded, err := decodeSyntheticFeature(feat.Config)
	if err != nil {
		t.Fatalf("decodeSyntheticFeature: %v", err)
	}
	expected := SyntheticFeatureConfig{
		Enabled:            true,
		Strategy:           "adaptive-speculative",
		PromptBudgetTokens: 4096,
		RetryCeiling:       3,
	}
	if !reflect.DeepEqual(decoded, expected) {
		t.Fatalf("decoded feature config mismatch:\n got: %+v\n want: %+v", decoded, expected)
	}

	// 6. Test full LoadEffective pipeline with the new feature
	eff, err := config.LoadEffective(context.Background(), []byte(rawYAML), config.LoadEffectiveOptions{})
	if err != nil {
		t.Fatalf("LoadEffective failed: %v", err)
	}
	if len(eff.Config.Plugins.Features) != 1 {
		t.Fatalf("LoadEffective Plugins.Features len = %d, want 1", len(eff.Config.Plugins.Features))
	}
	effDecoded, err := decodeSyntheticFeature(eff.Config.Plugins.Features[0].Config)
	if err != nil {
		t.Fatalf("decode on LoadEffective output: %v", err)
	}
	if !reflect.DeepEqual(effDecoded, expected) {
		t.Fatalf("LoadEffective decoded config mismatch:\n got: %+v\n want: %+v", effDecoded, expected)
	}
}
