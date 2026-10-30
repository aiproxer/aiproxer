package standardplugins_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestReasoningOutputPreservation_NotMandatoryRequirement(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindFeature && r.ID == standardplugins.ReasoningOutputPreservationFeatureID {
			t.Fatal("reasoning-output-preservation must not be a mandatory StandardDistributionRequirements entry; explicit disabled is the opt-out")
		}
	}
}

func TestEnsureReasoningOutputPreservationInConfig_AbsentInjectsEnabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("got %+v", cfg.Plugins.Features)
	}
	row := cfg.Plugins.Features[0]
	if !row.Enabled || row.ID != standardplugins.ReasoningOutputPreservationFeatureID {
		t.Fatalf("got %+v", row)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_DisabledSuppressesInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	cfg.Plugins.Features = []config.PluginConfig{{
		ID:      standardplugins.ReasoningOutputPreservationFeatureID,
		Enabled: false,
	}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 || cfg.Plugins.Features[0].Enabled {
		t.Fatalf("disabled row must be preserved without re-enable: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_OmittedEnabledIsOptOut(t *testing.T) {
	t.Parallel()
	// Plain-bool PluginConfig.Enabled defaults to false when the key is omitted.
	// Presence of a matching row (even without enabled: true) is an explicit opt-out
	// and must suppress standard injection.
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	cfg.Plugins.Features = []config.PluginConfig{{
		ID: standardplugins.ReasoningOutputPreservationFeatureID,
	}}
	if cfg.Plugins.Features[0].Enabled {
		t.Fatal("precondition: omitted enabled must decode/zero as false")
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("must not inject another row: %+v", cfg.Plugins.Features)
	}
	if cfg.Plugins.Features[0].Enabled {
		t.Fatal("omitted enabled must remain disabled opt-out")
	}
}

func TestEnsureReasoningOutputPreservationInConfig_FactoryKindMatchSuppressesInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	cfg.Plugins.Features = []config.PluginConfig{{
		Kind:    standardplugins.ReasoningOutputPreservationFeatureID,
		ID:      "custom-rp-instance",
		Enabled: false,
	}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("factory-kind match must suppress injection: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_CustomBundleNoInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: false}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 0 {
		t.Fatalf("custom/minimal bundles must not inject: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_EnabledCustomUnchanged(t *testing.T) {
	t.Parallel()
	custom, err := yamlMappingFromString(`
action: observe
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: log_skip
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	cfg.Plugins.Features = []config.PluginConfig{{
		ID:      standardplugins.ReasoningOutputPreservationFeatureID,
		Enabled: true,
		Config:  custom,
	}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("must not inject duplicate: %+v", cfg.Plugins.Features)
	}
	got, err := reasoningpreservation.DecodeConfig(cfg.Plugins.Features[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != reasoningpreservation.ActionObserve || got.UseBuiltinCatalog {
		t.Fatalf("custom enabled config must remain untouched: %+v", got)
	}
	if got.State.TTL != time.Hour || got.State.MaxTurnsPerSession != 4 {
		t.Fatalf("custom state must remain untouched: %+v", got.State)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_Idempotent(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	opts := standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, opts); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, opts); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("idempotent inject must not duplicate: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_ExactDecodedDefaults(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("want one injected row, got %+v", cfg.Plugins.Features)
	}
	got, err := reasoningpreservation.DecodeConfig(cfg.Plugins.Features[0].Config)
	if err != nil {
		t.Fatalf("DecodeConfig defaults: %v", err)
	}
	if got.Action != reasoningpreservation.ActionRestore {
		t.Fatalf("action=%q want %q", got.Action, reasoningpreservation.ActionRestore)
	}
	if !got.UseBuiltinCatalog {
		t.Fatal("use_builtin_catalog must be true")
	}
	if len(got.Rules) != 1 || got.Rules[0].Backend != "codex-primary" {
		t.Fatalf("rules must contain the exact Codex companion, got %+v", got.Rules)
	}
	if got.OnAmbiguous != reasoningpreservation.PolicyLogSkip {
		t.Fatalf("on_ambiguous=%q", got.OnAmbiguous)
	}
	if got.OnUnrepresentable != reasoningpreservation.PolicyReject {
		t.Fatalf("on_unrepresentable=%q", got.OnUnrepresentable)
	}
	if got.OnStateError != reasoningpreservation.PolicyReject {
		t.Fatalf("on_state_error=%q", got.OnStateError)
	}
	if got.State.TTL != 24*time.Hour {
		t.Fatalf("ttl=%v want 24h", got.State.TTL)
	}
	if got.State.MaxTurnsPerSession != 16 {
		t.Fatalf("max_turns=%d", got.State.MaxTurnsPerSession)
	}
	if got.State.MaxReasoningBytesPerTurn != 65536 {
		t.Fatalf("max_reasoning_bytes=%d", got.State.MaxReasoningBytesPerTurn)
	}
	if got.State.MaxSessionBytes != 262144 {
		t.Fatalf("max_session_bytes=%d", got.State.MaxSessionBytes)
	}
}

func yamlMappingFromString(raw string) (yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return yaml.Node{}, err
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0], nil
	}
	return doc, nil
}
