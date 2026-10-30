package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// TestDiscoveredModelResolver_usesTheDesignatedDefaultModel proves the
// resolver answers with the model the provider designated as the instance's
// default, even when the enumeration order lists another model first.
func TestDiscoveredModelResolver_usesTheDesignatedDefaultModel(t *testing.T) {
	t.Parallel()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "claude-acp",
			Kind:            "claudeagentacp",
			BackendPrefixes: []string{"claude-acp"},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models: []modelinventory.Model{
					// An embedding model listed first is not the default: the
					// provider owns that choice.
					{CanonicalID: "vendor/text-embedding-3-small", NativeID: "text-embedding-3-small"},
					{CanonicalID: "vendor/claude-sonnet-5.5", NativeID: "claude-sonnet-5-5", IsDefault: true},
				},
			},
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resolver := discoveredModelResolver(rt)
	if resolver == nil {
		t.Fatal("expected a resolver for a published inventory")
	}
	if got := resolver("claude-acp"); got != "vendor/claude-sonnet-5.5" {
		t.Fatalf("model = %q want vendor/claude-sonnet-5.5", got)
	}
	if got := resolver("other-instance"); got != "" {
		t.Fatalf("model = %q want none", got)
	}
}

// TestDiscoveredModelResolver_unmarkedModelsHaveNoDefault proves an instance
// that published several models without designating one has no default model.
// The enumeration order is not a preference order, so no model is adopted.
func TestDiscoveredModelResolver_unmarkedModelsHaveNoDefault(t *testing.T) {
	t.Parallel()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "wire-backend",
			Kind:            "custom-openai-legacy-compatible",
			BackendPrefixes: []string{"wire-backend"},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models: []modelinventory.Model{
					{CanonicalID: "wire-backend/text-embedding-3-small", NativeID: "text-embedding-3-small"},
					{CanonicalID: "wire-backend/gpt-4o-mini", NativeID: "gpt-4o-mini"},
				},
			},
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resolver := discoveredModelResolver(rt)
	if resolver == nil {
		t.Fatal("expected a resolver for a published inventory")
	}
	if got := resolver("wire-backend"); got != "" {
		t.Fatalf("model = %q want none", got)
	}
}

// TestDiscoveredModelResolver_singlePublishedModelIsTheDefault proves one
// published model is unambiguous: it is the instance's default even when the
// provider marked nothing.
func TestDiscoveredModelResolver_singlePublishedModelIsTheDefault(t *testing.T) {
	t.Parallel()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "wire-backend",
			Kind:            "custom-openai-legacy-compatible",
			BackendPrefixes: []string{"wire-backend"},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models: []modelinventory.Model{
					{CanonicalID: "wire-backend/model-a", NativeID: "model-a"},
				},
			},
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resolver := discoveredModelResolver(rt)
	if resolver == nil {
		t.Fatal("expected a resolver for a published inventory")
	}
	if got := resolver("wire-backend"); got != "wire-backend/model-a" {
		t.Fatalf("model = %q want wire-backend/model-a", got)
	}
}

// TestDiscoveredModelResolver_noInventoryHasNoResolver proves an empty
// inventory leaves the default route resolution without a model: the route
// stays absent instead of naming a model no backend serves.
func TestDiscoveredModelResolver_noInventoryHasNoResolver(t *testing.T) {
	t.Parallel()
	if resolver := discoveredModelResolver(nil); resolver != nil {
		t.Fatal("expected no resolver without a registry runtime")
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resolver := discoveredModelResolver(rt)
	if resolver == nil {
		t.Fatal("expected a resolver for a published inventory")
	}
	if got := resolver("claude-acp"); got != "" {
		t.Fatalf("model = %q want none", got)
	}
}

// TestFrozenDefaultRouteBindings_coversEveryLeaf proves the frozen bindings
// cover every leaf of the effective default route.
//
// A default route an operator expanded into a composite route can run more
// than one model, and a branch that names only a model inherits the default
// backend. Each of those leaves keeps its binding, so the route still
// addresses the model the backend published after an inventory refresh
// withdraws it.
func TestFrozenDefaultRouteBindings_coversEveryLeaf(t *testing.T) {
	t.Parallel()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "wire-backend",
			Kind:            "custom-openai-legacy-compatible",
			BackendPrefixes: []string{"wire-backend"},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models: []modelinventory.Model{
					{CanonicalID: "wire-backend/model-a", NativeID: "model-a"},
					{CanonicalID: "wire-backend/model-b", NativeID: "model-b"},
				},
			},
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	reg := publishedRegistry(rt)
	if reg == nil {
		t.Fatal("expected a published registry")
	}
	bindings := frozenDefaultRouteBindings(reg, []routing.DefaultRouteLeaf{
		{Backend: "wire-backend", Model: "wire-backend/model-a"},
		{Backend: "wire-backend", Model: "wire-backend/model-b"},
		{Backend: "wire-backend", Model: "wire-backend/model-unknown"},
	})
	if len(bindings) != 2 {
		t.Fatalf("bindings = %+v want the two known leaves", bindings)
	}
	native, ok := bindings.Find("wire-backend", "wire-backend/model-b")
	if !ok || native != "model-b" {
		t.Fatalf("find = %q ok=%v want model-b", native, ok)
	}
	if _, ok := bindings.Find("wire-backend", "wire-backend/model-unknown"); ok {
		t.Fatal("a leaf the registry does not know must not keep a binding")
	}
}
