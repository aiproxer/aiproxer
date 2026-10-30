package archtest

import (
	"testing"
)

var openaicompatForbiddenConcreteProviders = []forbiddenDep{
	{
		Substr: "/connectors/openrouter",
		ErrMsg: "internal/plugins/backends/openaicompat must not import external openrouter connector",
	},
	{
		Substr: "/connectors/nvidia",
		ErrMsg: "internal/plugins/backends/openaicompat must not import external nvidia connector",
	},
}

// TestInternalCoreDoesNotDependOnVendorSDKs keeps orchestration and core contracts free of
// official provider client modules (openai-go, anthropic-sdk-go, genai, bedrockruntime).
func TestInternalCoreDoesNotDependOnVendorSDKs(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, publicContractVendorSDKDeps)
}

// TestOpenaiCompatSharedAdapterDoesNotImportConcreteProviders ensures the shared
// openai-go adapter layer stays provider-agnostic after Phase 7 externalization.
func TestOpenaiCompatSharedAdapterDoesNotImportConcreteProviders(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/backends/openaicompat/..."}, openaicompatForbiddenConcreteProviders)
}
