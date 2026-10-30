package compatmode

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/hostcredentials"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
)

// ResolveEnvAPIKeys reads numbered env-root keys into an independent slice for
// one runtime instance through the shared policy-driven host resolver with
// the default proxy-wide bound. Empty root yields no-auth. Resolution errors
// (misconfigured root_1, oversized values) resolve to no keys so a serving
// path fail-closed; factory builds must use [ResolveEnvAPIKeysWithPolicy] to
// surface the secret-free error at candidate admission instead.
func ResolveEnvAPIKeys(apiKeyEnvVarRoot string) []string {
	keys, _ := ResolveEnvAPIKeysWithPolicy(apiKeyEnvVarRoot, osenv.Process{}, config.DefaultUpstreamMaxEnvIndex)
	return keys
}

// ResolveEnvAPIKeysWithPolicy resolves one root through the shared host
// resolver: deterministic sparse scan through maxIndex, first-occurrence
// de-duplication, explicit root_1 rejection and value-size budgets. A nil env
// resolves no keys. Errors are secret-free and name only the root or slot.
func ResolveEnvAPIKeysWithPolicy(apiKeyEnvVarRoot string, env hostcredentials.Environment, maxIndex int) ([]string, error) {
	slots, err := hostcredentials.Resolve(apiKeyEnvVarRoot, env, maxIndex)
	if err != nil {
		return nil, err
	}
	return hostcredentials.Values(slots), nil
}

// FirstAPIKey returns the first resolved credential or empty for no-auth.
func FirstAPIKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
