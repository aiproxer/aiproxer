package standardplugins_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"gopkg.in/yaml.v3"
)

func TestEnsureReasoningOutputPreservationInConfig_CodexCompanionDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		backends []config.PluginConfig
		features []config.PluginConfig
		wantRule []string
	}{
		{
			name:     "one direct codex instance",
			backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}},
			wantRule: []string{"codex-primary"},
		},
		{
			name: "multiple direct instances are isolated",
			backends: []config.PluginConfig{
				{Kind: "openai-codex", ID: "codex/primary one", Enabled: true},
				{Kind: "openai-codex", ID: "codex-secondary", Enabled: true},
			},
			wantRule: []string{"codex/primary one", "codex-secondary"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Plugins: config.PluginsConfig{Backends: tc.backends, Features: tc.features}}
			if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
				t.Fatal(err)
			}
			row := reasoningFeatureRow(t, cfg)
			decoded, err := reasoningpreservation.DecodeConfig(row.Config)
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded.Rules) != len(tc.wantRule) {
				t.Fatalf("rules=%+v want %d", decoded.Rules, len(tc.wantRule))
			}
			seen := map[string]bool{}
			for _, rule := range decoded.Rules {
				if rule.Backend == "" || rule.Enabled == nil || !*rule.Enabled || len(rule.ModelKeywords) != 0 {
					t.Fatalf("companion rule must be enabled backend-only: %+v", rule)
				}
				if seen[rule.ID] || len(rule.ID) > 64 || !strings.HasPrefix(rule.ID, "codex-native-context-") {
					t.Fatalf("rule id is not bounded/deterministic: %q", rule.ID)
				}
				seen[rule.ID] = true
				found := false
				for _, id := range tc.wantRule {
					if rule.Backend == id {
						found = true
					}
				}
				if !found {
					t.Fatalf("unexpected companion target %q", rule.Backend)
				}
			}
		})
	}
}

func TestEnsureReasoningOutputPreservationInConfig_SanitizedInstanceIDsRemainUnique(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{Kind: "openai-codex", ID: "codex/a", Enabled: true},
		{Kind: "openai-codex", ID: "codex-a", Enabled: true},
	}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	rules, err := reasoningpreservation.DecodeConfig(reasoningFeatureRow(t, cfg).Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Rules) != 2 || rules.Rules[0].ID == rules.Rules[1].ID {
		t.Fatalf("sanitized instance IDs must remain unique: %+v", rules.Rules)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_CodexEligibilityAndPrecedence(t *testing.T) {
	t.Parallel()
	custom, err := yamlMappingFromString(`
action: observe
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: log_skip
on_state_error: log_skip
rules:
  - id: operator-policy
    backend: codex-a
    model_keywords: [gpt-5]
    enabled: true
  - id: operator-disable
    backend: codex-b
    enabled: false
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Backends: []config.PluginConfig{
			{Kind: "openai-codex", ID: "codex-a", Enabled: true},
			{Kind: "openai-codex", ID: "codex-b", Enabled: true, Config: mustYAMLNode(t, "native_context:\n  enabled: false\n")},
			{Kind: "openai-codex-app-server", ID: "codex-app", Enabled: true},
			{Kind: "openrouter", ID: "other", Enabled: true},
			{Kind: "openai-codex", ID: "codex-compaction-off", Enabled: true, Config: mustYAMLNode(t, "native_context:\n  compaction:\n    enabled: false\n")},
		},
		Features: []config.PluginConfig{{ID: standardplugins.ReasoningOutputPreservationFeatureID, Enabled: true, Config: custom}},
	}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	row := reasoningFeatureRow(t, cfg)
	got, err := reasoningpreservation.DecodeConfig(row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != reasoningpreservation.ActionObserve || got.UseBuiltinCatalog {
		t.Fatalf("custom feature policy changed: %+v", got)
	}
	if len(got.Rules) != 4 { // operator policies plus codex-a and compaction-off companions
		t.Fatalf("rules=%+v", got.Rules)
	}
	for _, rule := range got.Rules {
		if strings.HasPrefix(rule.ID, "codex-native-context-") && (rule.Backend == "codex-b" || rule.Backend == "codex-app" || rule.Backend == "other") {
			t.Fatalf("ineligible backend received companion rule: %+v", rule)
		}
	}
	if got.Rules[0].ID != "operator-policy" || got.Rules[1].ID != "operator-disable" {
		t.Fatalf("custom rule order/policy changed: %+v", got.Rules)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_ExplicitFeatureDisabledAndIdempotent(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-a", Enabled: true}},
		Features: []config.PluginConfig{{ID: standardplugins.ReasoningOutputPreservationFeatureID, Enabled: false}},
	}}
	opts := standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, opts); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, opts); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 || cfg.Plugins.Features[0].Enabled {
		t.Fatalf("explicit feature opt-out was overridden: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureReasoningOutputPreservationInConfig_InvalidNativeEnabledFails(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
		Kind: "openai-codex", ID: "codex-invalid", Enabled: true,
		Config: mustYAMLNode(t, "native_context:\n  enabled: not-a-bool\n"),
	}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err == nil {
		t.Fatal("invalid native_context.enabled must fail companion composition")
	}
	if len(cfg.Plugins.Features) != 0 {
		t.Fatal("invalid backend config must not inject the feature")
	}
}

func TestCodexCompanionStandardComposition_OrdinaryAttemptGetsTrustedMarker(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
		Kind: "openai-codex", ID: "codex-process-instance", Enabled: true,
	}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	feature := reasoningFeatureRow(t, cfg)
	_, err := reasoningpreservation.DecodeConfig(feature.Config)
	if err != nil {
		t.Fatal(err)
	}
	// Compose through the standard registry, as the ordinary runtime does. The
	// feature owns both the attempt transform and surfaced stream observer.
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	genMerged, err := featurebundle.MergeFeatureSurfaceGenerated(reg, config.RegistrationsFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var transform request.AttemptTransform
	for _, candidate := range aipfeature.Get(genMerged.Frozen, aipfeature.PlaneAttemptTransforms) {
		if candidate != nil && candidate.ID() == reasoningpreservation.ID+"-transform" {
			transform = candidate
		}
	}
	if transform == nil {
		t.Fatal("standard composition did not install reasoning attempt transform")
	}
	var observer bool
	for _, factory := range aipfeature.Get(genMerged.Frozen, aipfeature.PlaneStreamObserverFactories) {
		if factory != nil && factory.ID() == reasoningpreservation.ID+"-observer" {
			observer = true
		}
	}
	if !observer {
		t.Fatal("surfaced-winner observer must remain feature-owned")
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	_, err = transform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "codex-process-instance", Model: "arbitrary-future-codex-model",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}},
		Session:       session.SessionView{AuthoritativeSessionID: "authoritative-session"},
	}, request.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if string(call.Extensions[standardplugins.ContinuityMarkerKey]) != string(standardplugins.ContinuityMarkerValue) {
		t.Fatalf("standard injected rule did not produce trusted marker: %s", json.RawMessage(call.Extensions[standardplugins.ContinuityMarkerKey]))
	}
}

func reasoningFeatureRow(t *testing.T, cfg *config.Config) config.PluginConfig {
	t.Helper()
	for _, row := range cfg.Plugins.Features {
		if row.FactoryID() == standardplugins.ReasoningOutputPreservationFeatureID || row.InstanceID() == standardplugins.ReasoningOutputPreservationFeatureID {
			if !row.Enabled {
				t.Fatalf("reasoning feature disabled: %+v", row)
			}
			return row
		}
	}
	t.Fatal("reasoning feature row missing")
	return config.PluginConfig{}
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	n, err := yamlMappingFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
