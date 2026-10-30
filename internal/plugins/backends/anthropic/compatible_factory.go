package anthropic

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"gopkg.in/yaml.v3"
)

// BuildCompatible constructs a built-in Anthropic-compatible backend from strict
// compatible-mode YAML for one configured runtime instance.
func BuildCompatible(instanceID, factoryKind string, n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	return BuildCompatibleWithSnapshot(instanceID, factoryKind, n, upstream, "", pluginreg.HostCredentialSnapshot{})
}

// BuildCompatibleWithModelsPath constructs an Anthropic-compatible backend with
// an explicit model-list path. The path is a bounded family quirk seam; an
// empty path retains the family default (/v1/models).
func BuildCompatibleWithModelsPath(instanceID, factoryKind string, n yaml.Node, upstream *http.Client, modelsPath string) (execbackend.Backend, error) {
	return BuildCompatibleWithSnapshot(instanceID, factoryKind, n, upstream, modelsPath, pluginreg.HostCredentialSnapshot{})
}

// BuildCompatibleWithSnapshot constructs the backend with admitted
// candidate-snapshot credentials (requirement 1.4): the configured
// api_key_env_var_root resolves through the authorized snapshot when the
// composition root captured one, so serving and remote inventory keep
// admitted keys across process-environment rotation. Without a resolved
// snapshot the candidate-build process-environment lookup applies. The path
// is a bounded family quirk seam; an empty path retains the family default
// (/v1/models).
func BuildCompatibleWithSnapshot(instanceID, factoryKind string, n yaml.Node, upstream *http.Client, modelsPath string, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
	cfg, err := config.DecodeCompatibleModeConfig(instanceID, factoryKind, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	ep, err := compatmode.ParseEndpoint(cfg)
	if err != nil {
		return execbackend.Backend{}, err
	}
	base := ep.BaseURL()
	modelsEndpoint, err := compatmode.AnthropicModelsEndpoint(ep)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if modelsPath != "" {
		modelsEndpoint, err = compatmode.AnthropicModelsEndpointPath(ep, modelsPath)
		if err != nil {
			return execbackend.Backend{}, err
		}
	}
	ek := compatmode.ResolveCustomKeys(cfg.APIKeyEnvVarRoot, snap)
	var primaryKey string
	if len(ek) > 0 {
		primaryKey = ek[0]
	}
	credentials := make([]credpool.Credential, 0, len(ek))
	for _, secret := range ek {
		credentials = append(credentials, credpool.Credential{Secret: secret})
	}
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}
	// The shared quota-aware coordinator owns retry policy for this family;
	// SDK-level retries stay disabled so a Retry-After hint surfaces to the
	// coordinator immediately instead of sleeping inside one attempt.
	noRetries := 0
	be := New(Config{
		BaseURL:              base,
		BackendPrefix:        cfg.BackendPrefix,
		APIKey:               primaryKey,
		APIKeys:              ek,
		Credentials:          credentials,
		HTTPClient:           client,
		SDKMaxRetries:        &noRetries,
		QuotaSwitchThreshold: snap.QuotaSwitchRetryAfter,
		CompatibleModeAuth:   true,
		ModelsEndpoint:       modelsEndpoint,
	})
	be, err = compatmode.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatmode.ApplyRuntimePolicy(be, cfg)
}

// LifecycleAnthropicCompatible is the standardplugins lifecycle entrypoint.
func LifecycleAnthropicCompatible(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatibleWithSnapshot(instanceID, "custom-anthropic-compatible", n, upstream, "", deps.Credentials)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}
