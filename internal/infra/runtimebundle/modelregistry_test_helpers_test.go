package runtimebundle_test

import "github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"

func testOpenAIBackendYAML() string {
	// Env-only credential contract (requirement 2.1): no literal secrets in
	// backend YAML. Keys arrive from the host snapshot bound at registration.
	return `models:
  source: inline
  items:
    - canonical_id: openai/test-model
      native_id: test-model
      display_name: Test Model
`
}

func testModelInventory() modelinventory.Provider {
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: []modelinventory.Model{{
			CanonicalID: "test/model",
			NativeID:    "model",
			DisplayName: "Test Model",
		}},
	}
}
