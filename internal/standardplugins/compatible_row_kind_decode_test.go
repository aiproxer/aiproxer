package standardplugins

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

// TestCompatibleRowKinds_startupProjectionUsesEachKindSchema proves the startup
// credential and admission projections decode a custom-compatible row with the
// schema of its own factory kind. A generic OpenResponses row declares keys
// (capabilities) that the OpenAI-family compatible schema rejects, so decoding
// every kind with that one schema fails startup for a row check-config accepts.
func TestCompatibleRowKinds_startupProjectionUsesEachKindSchema(t *testing.T) {
	t.Parallel()

	t.Run("openresponses_root_resolves_without_inventing_a_limit", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			openResponsesRow(t, "ors-startup", `backend_prefix: ors-startup
base_url: https://api.example.test/v1
api_key_env_var_root: ORS_STARTUP_KEY
capabilities: [structured_outputs, ordered_items]
`),
		}}}
		snap, err := SnapshotCandidateCredentials(snapshotMapEnv{"ORS_STARTUP_KEY": "ors-secret"}, cfg)
		if err != nil {
			t.Fatalf("SnapshotCandidateCredentials: %v", err)
		}
		if got := snap.Keys("ORS_STARTUP_KEY"); len(got) != 1 || got[0] != "ors-secret" {
			t.Fatalf("ORS_STARTUP_KEY = %#v, want [ors-secret]", got)
		}
		limits, err := CollectCompatibleAdmissionLimits(cfg.Plugins.Backends)
		if err != nil {
			t.Fatalf("CollectCompatibleAdmissionLimits: %v", err)
		}
		if len(limits) != 0 {
			t.Fatalf("limits = %#v, want no entry for a kind whose schema has no max_concurrent_requests", limits)
		}
	})

	t.Run("openai_family_limit_is_preserved", func(t *testing.T) {
		t.Parallel()
		rows := []config.PluginConfig{
			compatibleRowWithLimit(t, "legacy-startup", 2),
			openResponsesRow(t, "ors-startup", `backend_prefix: ors-startup
base_url: https://api.example.test/v1
capabilities: [structured_outputs, ordered_items]
`),
		}
		limits, err := CollectCompatibleAdmissionLimits(rows)
		if err != nil {
			t.Fatalf("CollectCompatibleAdmissionLimits: %v", err)
		}
		if got := limits["legacy-startup"]; got != 2 {
			t.Fatalf("legacy-startup limit = %d, want 2", got)
		}
		if _, ok := limits["ors-startup"]; ok {
			t.Fatalf("openresponses row must not invent a limit: %#v", limits)
		}
	})

	t.Run("strict_per_kind_validation_is_preserved", func(t *testing.T) {
		t.Parallel()
		rows := []config.PluginConfig{openResponsesRow(t, "ors-bad", `backend_prefix: ors-bad
base_url: https://api.example.test/v1
capabilities: [mind_reading]
`)}
		cfg := &config.Config{Plugins: config.PluginsConfig{Backends: rows}}
		if _, err := CollectCompatibleAdmissionLimits(rows); err == nil || !strings.Contains(err.Error(), "mind_reading") {
			t.Fatalf("CollectCompatibleAdmissionLimits error = %v, want unknown capability rejection", err)
		}
		if _, err := SnapshotCandidateCredentials(snapshotMapEnv{}, cfg); err == nil || !strings.Contains(err.Error(), "mind_reading") {
			t.Fatalf("SnapshotCandidateCredentials error = %v, want unknown capability rejection", err)
		}
	})

	t.Run("static_secret_is_rejected_without_echo", func(t *testing.T) {
		t.Parallel()
		const secret = "sk-ors-startup-secret"
		cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			openResponsesRow(t, "ors-secret", `backend_prefix: ors-secret
base_url: https://api.example.test/v1
api_key: `+secret+`
`),
		}}}
		_, err := SnapshotCandidateCredentials(snapshotMapEnv{}, cfg)
		if err == nil {
			t.Fatal("expected literal credential rejection")
		}
		if !strings.Contains(err.Error(), "forbidden config key") {
			t.Fatalf("error = %v, want the static-credential guard to own the rejection", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoed literal secret: %q", err)
		}
	})
}

func compatibleRowWithLimit(t *testing.T, id string, limit int) config.PluginConfig {
	t.Helper()
	raw := fmt.Sprintf("backend_prefix: %s\nbase_url: http://127.0.0.1:9/v1\nmax_concurrent_requests: %d\n", id, limit)
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return config.PluginConfig{Kind: CustomOpenAILegacyCompatibleID, ID: id, Enabled: true, Config: n}
}
