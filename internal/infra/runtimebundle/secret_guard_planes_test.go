package runtimebundle

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// TestSecretGuardFromPlanes_ReadIsolation pins read-boundary frozen-value
// isolation: mutating a plane/inventory returned by secretGuardFromPlanes
// must not alter subsequent reads from the same frozen set. The reader builds
// fresh structs and clones the categories slice on every call, so consumers
// never alias frozen generation state.
func TestSecretGuardFromPlanes_ReadIsolation(t *testing.T) {
	t.Parallel()

	cfg := &secretguard.ExecutionConfig{
		AccessMode:         "single_user",
		ConfigVersion:      "v1",
		SourceCategories:   []string{"env", "catalog"},
		BetterLeaksEnabled: true, BetterLeaksRuleCount: 3, BetterLeaksDecodeDepth: 4,
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.ContributeSource(cs, aipfeature.PlaneSecretGuardExecution, aipfeature.SourceGenerationBinder, "secret-guard-execution", cfg); err != nil {
		t.Fatalf("ContributeSource: %v", err)
	}
	frozen := cs.Freeze()

	plane, inv := secretGuardFromPlanes(frozen)
	if plane.AccessMode != "single_user" {
		t.Fatalf("plane AccessMode=%q want single_user", plane.AccessMode)
	}
	if inv == nil {
		t.Fatal("expected non-nil inventory")
	}

	// Mutate everything mutable in the returned values.
	plane.AccessMode = "mutated"
	inv.AccessMode = "mutated"
	inv.CatalogEntryCount = -1
	*inv.BetterLeaksRuleCount = -1
	*inv.BetterLeaksDecodeDepth = -1
	if len(inv.SourceCategories) > 0 {
		inv.SourceCategories[0] = "mutated"
	}

	againPlane, againInv := secretGuardFromPlanes(frozen)
	if againPlane.AccessMode != "single_user" {
		t.Fatalf("reread plane AccessMode=%q want single_user", againPlane.AccessMode)
	}
	if againInv == nil {
		t.Fatal("expected non-nil inventory on reread")
	}
	if againInv.AccessMode != "single_user" {
		t.Fatalf("reread inventory access mode=%q want single_user", againInv.AccessMode)
	}
	if againInv.CatalogEntryCount != 0 {
		t.Fatalf("reread catalog count=%d want 0", againInv.CatalogEntryCount)
	}
	if *againInv.BetterLeaksRuleCount != 3 || *againInv.BetterLeaksDecodeDepth != 4 {
		t.Fatalf("reread BetterLeaks bounds=(%d,%d) want (3,4)", *againInv.BetterLeaksRuleCount, *againInv.BetterLeaksDecodeDepth)
	}
	if len(againInv.SourceCategories) != 2 || againInv.SourceCategories[0] != "env" {
		t.Fatalf("reread categories=%v want [env catalog]", againInv.SourceCategories)
	}
}

func TestSecretGuardFromPlanes_DiscoveryCapabilityAndInventory(t *testing.T) {
	t.Parallel()
	capability := &struct{ generation string }{generation: "fixture"}
	cfg := &secretguard.ExecutionConfig{
		AccessMode: "single_user", Capability: capability,
		CatalogEntryCount: 4, CatalogAction: "block",
		SourceCategories:          []string{"proxy_env"},
		LocalAutoDiscoveryEnabled: true, BetterLeaksEnabled: true,
		BetterLeaksVersion: "fixture-version", BetterLeaksConfigHash: "fixture-config",
		BetterLeaksRuleCount: 0, BetterLeaksConfidence: "high",
		BetterLeaksDecodeDepth: 0, BetterLeaksWorkers: 2, DiscoveryDetectorCount: 1,
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.ContributeSource(cs, aipfeature.PlaneSecretGuardExecution, aipfeature.SourceGenerationBinder, "secret-guard-execution", cfg); err != nil {
		t.Fatal(err)
	}
	plane, inventory := secretGuardFromPlanes(cs.Freeze())
	if plane.Capability != capability {
		t.Fatal("frozen discovery capability lost at runtime composition boundary")
	}
	want := &diag.InventorySecretGuard{
		CatalogEntryCount: 4, SourceCategories: []string{"proxy_env"},
		AccessMode: "single_user", Action: "block", LocalAutoDiscoveryEnabled: true,
		BetterLeaksEnabled: true, BetterLeaksVersion: "fixture-version",
		BetterLeaksConfigHash: "fixture-config", BetterLeaksRuleCount: new(int),
		BetterLeaksConfidence: "high", BetterLeaksDecodeDepth: new(int),
		BetterLeaksWorkers: 2, DiscoveryDetectorCount: 1,
	}
	if !reflect.DeepEqual(inventory, want) {
		t.Fatal("frozen discovery posture lost in diagnostics, including explicit zero settings")
	}

	featureCfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{
		ID: "sg-main", Kind: "secrets-guard", Enabled: true,
	}}}}
	metadata := featurehost.InventoryMetadataForConfig(featureCfg, nil, inventory)
	if len(metadata) != 1 || metadata[0].InstanceID != "sg-main" || metadata[0].SecretGuard == nil {
		t.Fatalf("secret-guard metadata was not associated with its configured instance: %+v", metadata)
	}
	projection := metadata[0].SecretGuard
	if projection.InstanceID != "sg-main" || projection.CatalogEntryCount != 4 || projection.Action != "block" || projection.AccessMode != "single_user" {
		t.Fatalf("secret-guard inventory projection=%+v", projection)
	}
	if projection.BetterLeaksRuleCount == nil || *projection.BetterLeaksRuleCount != 0 ||
		projection.BetterLeaksDecodeDepth == nil || *projection.BetterLeaksDecodeDepth != 0 {
		t.Fatalf("explicit zero BetterLeaks settings lost presence: %+v", projection)
	}
	if len(projection.SourceCategories) != 1 || projection.SourceCategories[0] != "proxy_env" {
		t.Fatalf("secret-guard source categories=%v, want [proxy_env]", projection.SourceCategories)
	}
	projection.SourceCategories[0] = "mutated"
	if inventory.SourceCategories[0] != "proxy_env" {
		t.Fatal("inventory projection aliases generation-composed source categories")
	}
}
