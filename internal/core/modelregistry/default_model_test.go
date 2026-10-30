package modelregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// registryWithModels builds a registry from the given rows through the public
// build path, one inventory per backend instance.
func registryWithModels(t *testing.T, rows ...modelregistry.BackendModel) *modelregistry.Registry {
	t.Helper()
	byBackend := map[string][]modelinventory.Model{}
	order := []string{}
	for _, row := range rows {
		if _, seen := byBackend[row.BackendID]; !seen {
			order = append(order, row.BackendID)
		}
		byBackend[row.BackendID] = append(byBackend[row.BackendID], modelinventory.Model{
			CanonicalID: row.CanonicalID,
			NativeID:    row.NativeID,
			IsDefault:   row.IsDefault,
		})
	}
	inventories := make([]modelregistry.BackendInventory, 0, len(order))
	for _, backendID := range order {
		inventories = append(inventories, modelregistry.BackendInventory{
			BackendID:       backendID,
			Kind:            "test-kind",
			BackendPrefixes: []string{backendID},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models:   byBackend[backendID],
			},
		})
	}
	built, err := modelregistry.Build(context.Background(), inventories, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built.Registry
}

// TestRegistry_DefaultModelPrefersTheDesignatedModel proves the provider's
// designation wins over the enumeration order. A catalog that lists an
// embedding model first must not make it the instance's default.
func TestRegistry_DefaultModelPrefersTheDesignatedModel(t *testing.T) {
	t.Parallel()
	reg := registryWithModels(t,
		modelregistry.BackendModel{
			BackendID: "claude-acp", CanonicalID: "vendor/text-embedding-3-small", NativeID: "text-embedding-3-small",
		},
		modelregistry.BackendModel{
			BackendID: "claude-acp", CanonicalID: "vendor/claude-sonnet-5.5", NativeID: "claude-sonnet-5-5", IsDefault: true,
		},
	)
	model, ok := reg.DefaultModel("claude-acp")
	if !ok {
		t.Fatal("expected a designated default model")
	}
	if model.CanonicalID != "vendor/claude-sonnet-5.5" {
		t.Fatalf("canonical id = %q want vendor/claude-sonnet-5.5", model.CanonicalID)
	}
}

// TestRegistry_DefaultModelUsesTheOnlyPublishedModel proves one published
// model is unambiguous: it is the instance's default even when the provider
// marked nothing.
func TestRegistry_DefaultModelUsesTheOnlyPublishedModel(t *testing.T) {
	t.Parallel()
	reg := registryWithModels(t, modelregistry.BackendModel{
		BackendID: "wire-backend", CanonicalID: "wire-backend/model-a", NativeID: "model-a",
	})
	model, ok := reg.DefaultModel("wire-backend")
	if !ok {
		t.Fatal("expected the only published model to be the default")
	}
	if model.CanonicalID != "wire-backend/model-a" {
		t.Fatalf("canonical id = %q want wire-backend/model-a", model.CanonicalID)
	}
}

// TestRegistry_DefaultModelRefusesAnUnmarkedAmbiguity proves several
// unmarked models have no reliable preference order: the instance designates
// no default, and no arbitrary model is adopted.
func TestRegistry_DefaultModelRefusesAnUnmarkedAmbiguity(t *testing.T) {
	t.Parallel()
	reg := registryWithModels(t,
		modelregistry.BackendModel{
			BackendID: "wire-backend", CanonicalID: "wire-backend/text-embedding-3-small", NativeID: "text-embedding-3-small",
		},
		modelregistry.BackendModel{
			BackendID: "wire-backend", CanonicalID: "wire-backend/gpt-4o-mini", NativeID: "gpt-4o-mini",
		},
	)
	if _, ok := reg.DefaultModel("wire-backend"); ok {
		t.Fatal("expected no default model for an ambiguous inventory")
	}
}

// TestRegistry_DefaultModelRefusesAnAmbiguousDesignation proves a provider
// that marks more than one model designates no default: an ambiguous default
// must not become a route.
func TestRegistry_DefaultModelRefusesAnAmbiguousDesignation(t *testing.T) {
	t.Parallel()
	reg := registryWithModels(t,
		modelregistry.BackendModel{
			BackendID: "claude-acp", CanonicalID: "vendor/model-a", NativeID: "model-a", IsDefault: true,
		},
		modelregistry.BackendModel{
			BackendID: "claude-acp", CanonicalID: "vendor/model-b", NativeID: "model-b", IsDefault: true,
		},
	)
	if _, ok := reg.DefaultModel("claude-acp"); ok {
		t.Fatal("expected no default model for an ambiguous designation")
	}
}

// TestRegistry_DefaultModelIsPerInstance proves the designation never leaks
// across backend instances.
func TestRegistry_DefaultModelIsPerInstance(t *testing.T) {
	t.Parallel()
	reg := registryWithModels(t,
		modelregistry.BackendModel{
			BackendID: "claude-acp", CanonicalID: "vendor/model-a", NativeID: "model-a", IsDefault: true,
		},
		modelregistry.BackendModel{
			BackendID: "other-acp", CanonicalID: "vendor/model-b", NativeID: "model-b",
		},
		modelregistry.BackendModel{
			BackendID: "other-acp", CanonicalID: "vendor/model-c", NativeID: "model-c",
		},
	)
	if _, ok := reg.DefaultModel("other-acp"); ok {
		t.Fatal("expected no default model for an unmarked instance")
	}
	model, ok := reg.DefaultModel("claude-acp")
	if !ok || model.CanonicalID != "vendor/model-a" {
		t.Fatalf("default = %+v ok=%v", model, ok)
	}
}

// TestRegistry_DefaultModelSurvivesDeduplication proves the designation
// survives the deduplication of identical mappings, whatever the enumeration
// order is.
//
// A provider may publish the same mapping twice, and the duplicate that
// carries the designation can come second. The order must not decide which row
// keeps it: the designation is the provider's, and it survives.
func TestRegistry_DefaultModelSurvivesDeduplication(t *testing.T) {
	t.Parallel()
	rows := []modelregistry.BackendModel{
		{BackendID: "wire-backend", CanonicalID: "vendor/model-a", NativeID: "model-a"},
		{BackendID: "wire-backend", CanonicalID: "vendor/model-b", NativeID: "model-b"},
		{BackendID: "wire-backend", CanonicalID: "vendor/model-a", NativeID: "model-a", IsDefault: true},
	}
	reversed := []modelregistry.BackendModel{
		rows[2],
		rows[1],
		rows[0],
	}
	for name, models := range map[string][]modelregistry.BackendModel{
		"designation last":  rows,
		"designation first": reversed,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reg := registryWithModels(t, models...)
			model, ok := reg.DefaultModel("wire-backend")
			if !ok {
				t.Fatal("expected the designated default model to survive deduplication")
			}
			if model.CanonicalID != "vendor/model-a" {
				t.Fatalf("canonical id = %q want vendor/model-a", model.CanonicalID)
			}
		})
	}
}
