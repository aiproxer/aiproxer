package compatibleparity

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// InstancePair describes two same-kind compatible instances that must remain
// isolated across endpoint, env credential root, tokenizer, inventory, and
// concurrency settings.
type InstancePair struct {
	Family    Family
	Factory   string
	InstanceA InstanceConfig
	InstanceB InstanceConfig
}

// InstanceConfig is the fixture shape for one CompatibleModeConfig row.
type InstanceConfig struct {
	InstanceID            string
	BackendPrefix         string
	BaseURL               string
	APIKeyEnvVarRoot      string
	EnvKeyValue           string
	TokenizerID           string
	MaxConcurrentRequests int
	ModelNativeID         string
	ModelCanonicalID      string
}

// IsolationPairs returns two-instance fixtures for every compatible family.
func IsolationPairs() []InstancePair {
	return []InstancePair{
		isolationPair(FamilyOpenAILegacy, "custom-openai-legacy-compatible"),
		isolationPair(FamilyOpenAIResponses, "custom-openai-responses-compatible"),
		isolationPair(FamilyAnthropic, "custom-anthropic-compatible"),
	}
}

func isolationPair(family Family, factory string) InstancePair {
	prefix := string(family)
	baseA := "http://127.0.0.1:18001"
	baseB := "http://127.0.0.1:18002"
	if family != FamilyAnthropic {
		baseA += "/v1"
		baseB += "/v1"
	}
	return InstancePair{
		Family:  family,
		Factory: factory,
		InstanceA: InstanceConfig{
			InstanceID:            prefix + "-a",
			BackendPrefix:         prefix + "-a",
			BaseURL:               baseA,
			APIKeyEnvVarRoot:      "COMPAT_PARITY_" + envSuffix(family) + "_A",
			EnvKeyValue:           "sk-test-a-" + prefix,
			TokenizerID:           "cl100k_base",
			MaxConcurrentRequests: 1,
			ModelNativeID:         "model-a",
			ModelCanonicalID:      prefix + "-a/model-a",
		},
		InstanceB: InstanceConfig{
			InstanceID:            prefix + "-b",
			BackendPrefix:         prefix + "-b",
			BaseURL:               baseB,
			APIKeyEnvVarRoot:      "COMPAT_PARITY_" + envSuffix(family) + "_B",
			EnvKeyValue:           "sk-test-b-" + prefix,
			TokenizerID:           "o200k_base",
			MaxConcurrentRequests: 2,
			ModelNativeID:         "model-b",
			ModelCanonicalID:      prefix + "-b/model-b",
		},
	}
}

func envSuffix(family Family) string {
	switch family {
	case FamilyOpenAILegacy:
		return "LEGACY"
	case FamilyOpenAIResponses:
		return "RESP"
	case FamilyAnthropic:
		return "ANTH"
	default:
		return "UNK"
	}
}

// CompatibleYAML renders a strict CompatibleModeConfig document for decoding
// and factory construction through the standardplugins registry.
func CompatibleYAML(inst InstanceConfig, baseURL string) string {
	return fmt.Sprintf(`backend_prefix: %s
base_url: %s
api_key_env_var_root: %s
tokenizer: %s
max_concurrent_requests: %d
models:
  source: inline
  items:
    - canonical_id: %s
      native_id: %s
`, inst.BackendPrefix, baseURL, inst.APIKeyEnvVarRoot, inst.TokenizerID, inst.MaxConcurrentRequests, inst.ModelCanonicalID, inst.ModelNativeID)
}

// MustDecodeInstance decodes inst as CompatibleModeConfig against baseURL.
func MustDecodeInstance(instanceID, factory, raw string) (config.CompatibleModeConfig, error) {
	return config.DecodeCompatibleModeConfig(instanceID, factory, mustYAML(raw))
}
