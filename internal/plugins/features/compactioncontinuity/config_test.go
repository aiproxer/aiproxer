package compactioncontinuity

import (
	"strings"
	"testing"
	"time"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func testYAML(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return node
}

func TestDecodeConfig_DefaultsDisabledAndBounded(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, "{}"))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Extractor.Enabled {
		t.Fatal("semantic extraction must be disabled by default")
	}
	if !cfg.Preserve.Plan || !cfg.Preserve.UserDecisions || !cfg.Preserve.Constraints || !cfg.Preserve.Rationale || !cfg.Preserve.RejectedAlternatives {
		t.Fatalf("preserve defaults: %+v", cfg.Preserve)
	}
	if cfg.Worker.MaxConcurrency <= 0 || cfg.Worker.QueueCapacity <= 0 || cfg.Capsule.MaxTokens <= 0 || cfg.Source.TTL <= 0 || cfg.Result.TTL <= 0 || cfg.BranchTTL <= 0 {
		t.Fatalf("defaults must be finite positive bounds: %+v", cfg)
	}
	if cfg.Failure.Mode != FailureModeFailOpen {
		t.Fatalf("failure mode: got %q want %q", cfg.Failure.Mode, FailureModeFailOpen)
	}
}

func TestDecodeConfig_D18ShapeAndAliases(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, `
preserve:
  plan: true
  user_decisions: true
  constraints: true
  rationale: true
  rejected_alternatives: true
extractor:
  route: "openai-responses:small-model"
  timeout: 8s
  max_input_tokens: 12000
  max_output_tokens: 2000
  max_concurrency: 2
  queue_capacity: 16
barrier_timeout: 2s
pending_result_ttl: 2h
max_capsule_tokens: 2500
source_ttl: 2h
failure_mode: fail_open
`))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if !cfg.Extractor.Enabled || cfg.Extractor.Route != "openai-responses:small-model" || cfg.Extractor.Inherit {
		t.Fatalf("extractor: %+v", cfg.Extractor)
	}
	if cfg.Extractor.Timeout != 8*time.Second || cfg.Extractor.MaxInputTokens != 12000 || cfg.Extractor.MaxOutputTokens != 2000 {
		t.Fatalf("extractor limits: %+v", cfg.Extractor)
	}
	if cfg.Worker.MaxConcurrency != 2 || cfg.Worker.QueueCapacity != 16 {
		t.Fatalf("worker limits: %+v", cfg.Worker)
	}
	if cfg.Barrier.Timeout != 2*time.Second || cfg.PendingResultTTL != 2*time.Hour || cfg.Result.TTL != 2*time.Hour || cfg.MaxCapsuleTokens != 2500 || cfg.Capsule.MaxTokens != 2500 || cfg.SourceTTL != 2*time.Hour || cfg.Source.TTL != 2*time.Hour {
		t.Fatalf("retention aliases: %+v", cfg)
	}
}

func TestDecodeConfig_PreserveFalseValuesRemainFalse(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, "preserve:\n  plan: false\n  user_decisions: false\n  constraints: false\n  rationale: false\n  rejected_alternatives: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Preserve != (PreserveConfig{}) {
		t.Fatalf("preserve false values were defaulted: %+v", cfg.Preserve)
	}
}

func TestDecodeConfig_UnknownNestedKeyRejected(t *testing.T) {
	t.Parallel()
	_, err := DecodeConfig(testYAML(t, "worker:\n  max_concurrency: 2\n  typo: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "worker.typo") {
		t.Fatalf("error = %v, want worker.typo rejection", err)
	}
}

func TestDecodeConfig_ExtractorRequiresRouteOrInherit(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"extractor:\n  enabled: true\n",
		"extractor:\n  timeout: 8s\n",
	} {
		_, err := DecodeConfig(testYAML(t, raw))
		if err == nil || !strings.Contains(err.Error(), "route") {
			t.Errorf("raw %q: error = %v, want explicit route/inherit rejection", raw, err)
		}
	}
}

func TestDecodeConfig_ExtractorExplicitInherit(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, "extractor:\n  enabled: true\n  inherit: true\n"))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if !cfg.Extractor.Enabled || !cfg.Extractor.Inherit || cfg.Extractor.Route != "" {
		t.Fatalf("extractor inherit: %+v", cfg.Extractor)
	}
}

func TestDecodeConfig_PositiveBoundsAndRetention(t *testing.T) {
	t.Parallel()
	cases := []string{
		"extractor:\n  enabled: true\n  route: inherit\n  max_input_tokens: 0\n",
		"worker:\n  max_concurrency: 0\n",
		"worker:\n  queue_capacity: -1\n",
		"barrier_timeout: 0s\n",
		"max_capsule_tokens: -1\n",
		"source_ttl: 0s\n",
		"pending_result_ttl: -1s\n",
	}
	for _, raw := range cases {
		if _, err := DecodeConfig(testYAML(t, raw)); err == nil {
			t.Errorf("raw %q: expected positive-bound rejection", raw)
		}
	}

	_, err := DecodeConfig(testYAML(t, "pending_result_ttl: 3h\nsource_ttl: 2h\n"))
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("retention error = %v, want pending/source consistency rejection", err)
	}
}

func TestValidatePrerequisites_EnabledRequiresAllCapabilities(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, "extractor:\n  enabled: true\n  route: inherit\n"))
	if err != nil {
		t.Fatal(err)
	}
	base := Prerequisites{DetectorPreview: true, DetectorCommit: true, BranchCoordinator: true, BackgroundAux: true}
	if err := ValidatePrerequisites(cfg, base); err != nil {
		t.Fatalf("complete prerequisites: %v", err)
	}
	for name, mutate := range map[string]func(*Prerequisites){
		"detector preview":   func(p *Prerequisites) { p.DetectorPreview = false },
		"detector commit":    func(p *Prerequisites) { p.DetectorCommit = false },
		"branch coordinator": func(p *Prerequisites) { p.BranchCoordinator = false },
		"background aux":     func(p *Prerequisites) { p.BackgroundAux = false },
	} {
		p := base
		mutate(&p)
		if err := ValidatePrerequisites(cfg, p); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ReplaceAll(name, " ", " ")) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestConfigSnapshot_IsolatedFromLaterReload(t *testing.T) {
	t.Parallel()
	old, err := DecodeConfig(testYAML(t, "extractor:\n  enabled: true\n  route: old:model\n  timeout: 8s\n"))
	if err != nil {
		t.Fatal(err)
	}
	submitted := old.Snapshot()
	next, err := DecodeConfig(testYAML(t, "extractor:\n  enabled: true\n  route: new:model\n  timeout: 3s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Extractor.Route != "old:model" || submitted.Extractor.Timeout != 8*time.Second {
		t.Fatalf("submission snapshot changed: %+v", submitted.Extractor)
	}
	if next.Extractor.Route != "new:model" || next.Extractor.Timeout != 3*time.Second {
		t.Fatalf("new generation did not use new config: %+v", next.Extractor)
	}
}

func TestFeatureBundle_IsSchemaValidNoopUntilSemanticsLand(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(testYAML(t, "{}"))
	if err != nil {
		t.Fatal(err)
	}
	b := FeatureBundle(cfg)
	if err := b.Validate(); err != nil {
		t.Fatalf("FeatureBundle.Validate: %v", err)
	}
	if preservers := aipfeature.Get(b.PlaneSet, aipfeature.PlaneCompactionPreservers); len(preservers) != 0 {
		t.Fatalf("no semantic preserver should be composed yet: %d", len(preservers))
	}
}
