package openaicred

import (
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

// CredentialsFromConfig builds [credpool.Credential] entries for one backend instance.
// Ordering matches [credpool.BackendKeySecrets] (api_key first, then api_keys; deduplicated).
func CredentialsFromConfig(apiKey string, apiKeys []string) ([]credpool.Credential, error) {
	secrets, err := credpool.BackendKeySecrets(apiKey, apiKeys)
	if err != nil {
		return nil, err
	}
	out := make([]credpool.Credential, len(secrets))
	for i, s := range secrets {
		out[i] = credpool.Credential{Secret: s}
	}
	return out, nil
}

func NewPoolFromCredentials(apiKey string, apiKeys []string, credentials []credpool.Credential) (*credpool.Pool, error) {
	return credpool.NewPoolFromCredentials(apiKey, apiKeys, credentials)
}
