package diag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestBuildInventoryExtensions_secretGuardMetadataNoSyntheticSecrets(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "sg-main", Kind: "secrets-guard", Enabled: true},
			},
		},
	}
	ext := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{{
			InstanceID: "sg-main",
			SecretGuard: &InventorySecretGuard{
				InstanceID: "sg-main", CatalogEntryCount: 3,
				SourceCategories: []string{"proxy_env", "popular_env"},
				AccessMode:       "single_user", Action: "block",
			},
		}},
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features %d", len(ext.Features))
	}
	sg := ext.Features[0].SecretGuard
	if sg == nil {
		t.Fatal("missing secret_guard inventory")
	}
	if sg.InstanceID != "sg-main" || sg.Action != "block" || sg.AccessMode != "single_user" {
		t.Fatalf("secret_guard=%#v", sg)
	}
	if sg.CatalogEntryCount != 3 {
		t.Fatalf("catalog_entry_count=%d", sg.CatalogEntryCount)
	}
	if len(sg.SourceCategories) != 2 {
		t.Fatalf("source_categories=%#v", sg.SourceCategories)
	}

	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		"sk-test-openai-secretguard-fixture-001",
		"sk-or-test-secretguard-fixture-002",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("inventory JSON must not contain %q", forbidden)
		}
	}
}

func TestBuildInventoryExtensions_secretGuardEnabledZeroFactsRetainPresence(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "sg-zero", Kind: "secrets-guard", Enabled: true}},
		},
	}
	ruleCount, decodeDepth := 0, 0
	ext := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{{
			InstanceID: "sg-zero",
			SecretGuard: &InventorySecretGuard{
				InstanceID: "sg-zero", AccessMode: "single_user", Action: "block",
				BetterLeaksEnabled: true, BetterLeaksVersion: "v2.0.0-rc.1",
				BetterLeaksConfigHash: "config-hash", BetterLeaksRuleCount: &ruleCount,
				BetterLeaksConfidence: "medium", BetterLeaksDecodeDepth: &decodeDepth,
				BetterLeaksWorkers: 1, DiscoveryDetectorCount: 1,
			},
		}},
	})
	if len(ext.Features) != 1 || ext.Features[0].SecretGuard == nil {
		t.Fatalf("secret_guard inventory missing: %#v", ext.Features)
	}
	sg := ext.Features[0].SecretGuard
	if sg.BetterLeaksRuleCount == nil || *sg.BetterLeaksRuleCount != 0 {
		t.Fatalf("enabled zero BetterLeaks rule count lost presence: %#v", sg.BetterLeaksRuleCount)
	}
	if sg.BetterLeaksDecodeDepth == nil || *sg.BetterLeaksDecodeDepth != 0 {
		t.Fatalf("enabled zero BetterLeaks decode depth lost presence: %#v", sg.BetterLeaksDecodeDepth)
	}

	raw, err := json.Marshal(sg)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"betterleaks_active_rule_count", "betterleaks_max_decode_depth"} {
		value, present := fields[key]
		if !present || value != float64(0) {
			t.Fatalf("enabled zero-valued %s must remain present as 0, present=%t value=%v", key, present, value)
		}
	}
}
