package modelregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestInventoryProvenance_enrichesPrefixAndCapabilitySource(t *testing.T) {
	t.Parallel()
	inv := modelregistry.BackendInventory{
		BackendID:       "compat-a",
		Kind:            "custom-openai-legacy-compatible",
		BackendPrefixes: []string{"compat-a"},
	}
	rows := []modelregistry.BackendModel{{
		CanonicalID: "compat-a/model-a",
		NativeID:    "model-a",
		BackendID:   "compat-a",
		Kind:        "custom-openai-legacy-compatible",
		Source:      modelinventory.SourceStaticInline,
		LoadedAt:    time.Unix(100, 0).UTC(),
	}}
	got := modelregistry.EnrichBackendModelsForTest(rows, inv)
	if len(got) != 1 {
		t.Fatalf("rows=%d", len(got))
	}
	if got[0].Prefix != "compat-a" {
		t.Fatalf("prefix=%q", got[0].Prefix)
	}
	if got[0].CapabilitySource != modelregistry.CapabilitySourceStaticConfig {
		t.Fatalf("capability=%q", got[0].CapabilitySource)
	}
}

func TestInventoryProvenance_remoteDiscoveryCapability(t *testing.T) {
	t.Parallel()
	inv := modelregistry.BackendInventory{
		BackendID:       "compat-b",
		Kind:            "custom-openai-legacy-compatible",
		BackendPrefixes: []string{"compat-b"},
	}
	rows := []modelregistry.BackendModel{{
		CanonicalID: "compat-b/gpt",
		NativeID:    "gpt",
		Source:      modelinventory.SourceRemote,
	}}
	got := modelregistry.EnrichBackendModelsForTest(rows, inv)
	if got[0].CapabilitySource != modelregistry.CapabilitySourceRemoteDiscovery {
		t.Fatalf("capability=%q", got[0].CapabilitySource)
	}
}

// TestInventoryProvenance_preservedThroughBuild verifies instance, factory,
// model, capability, and inventory provenance survive the full Build path for a
// generic OpenResponses-compatible backend inventory.
func TestInventoryProvenance_preservedThroughBuild(t *testing.T) {
	t.Parallel()
	inv := modelregistry.BackendInventory{
		BackendID:       "or-inst",
		Kind:            "custom-openresponses-compatible",
		BackendPrefixes: []string{"my-or"},
		Provider: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "my-or/model-a",
				NativeID:    "model-a",
				DisplayName: "Model A",
			}},
		},
	}
	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{inv}, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	all := built.Registry.All()
	if len(all) != 1 {
		t.Fatalf("models = %d, want 1", len(all))
	}
	row := all[0]
	if row.BackendID != "or-inst" {
		t.Fatalf("instance provenance = %q", row.BackendID)
	}
	if row.Kind != "custom-openresponses-compatible" {
		t.Fatalf("factory provenance = %q", row.Kind)
	}
	if row.CanonicalID != "my-or/model-a" || row.NativeID != "model-a" {
		t.Fatalf("model provenance = %q / %q", row.CanonicalID, row.NativeID)
	}
	if row.Source != modelinventory.SourceStaticInline {
		t.Fatalf("inventory provenance = %q", row.Source)
	}
	if row.Prefix != "my-or" {
		t.Fatalf("prefix provenance = %q", row.Prefix)
	}
	if row.CapabilitySource != modelregistry.CapabilitySourceStaticConfig {
		t.Fatalf("capability provenance = %q", row.CapabilitySource)
	}
	if len(built.Discoveries) != 1 {
		t.Fatalf("discoveries = %d, want 1", len(built.Discoveries))
	}
	d := built.Discoveries[0]
	if d.BackendID != "or-inst" || d.Kind != "custom-openresponses-compatible" {
		t.Fatalf("discovery provenance = %+v", d)
	}
	if d.Status != modelinventory.DiscoveryStatusOK || d.Source != modelinventory.SourceStaticInline {
		t.Fatalf("discovery inventory provenance = %+v", d)
	}
}
