package openresponsescompat

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// Build constructs one generic OpenResponses backend instance from strict
// compatible-mode YAML. Credentials are resolved from the environment lazily at
// request time; static inventory is loaded from config/file only. upstream is
// the shared outbound HTTP client; when nil, [httpclient.Standard] is used.
func Build(instanceID string, n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	return BuildWithSnapshot(instanceID, n, upstream, pluginreg.HostCredentialSnapshot{})
}

// BuildWithSnapshot constructs one generic OpenResponses backend instance
// with admitted candidate-snapshot credentials (requirement 1.4): the
// configured api_key_env_var_root resolves through the authorized snapshot
// when the composition root captured one, so serving keeps admitted keys
// across process-environment rotation. Without a resolved snapshot the
// candidate-build process-environment lookup applies. Static inventory is
// loaded from config/file only.
func BuildWithSnapshot(instanceID string, n yaml.Node, upstream *http.Client, snap pluginreg.HostCredentialSnapshot) (execbackend.Backend, error) {
	cfg, err := DecodeConfig(instanceID, ID, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	if _, err := validateBaseURL(cfg.BaseURL); err != nil {
		return execbackend.Backend{}, err
	}
	keys := compatmode.ResolveCustomKeys(cfg.APIKeyEnvVarRoot, snap)
	var primary string
	if len(keys) > 0 {
		primary = keys[0]
	}
	credentials := make([]credpool.Credential, 0, len(keys))
	for _, secret := range keys {
		credentials = append(credentials, credpool.Credential{Secret: secret})
	}
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}

	var base execbackend.Backend
	base, err = compatmode.ApplyStaticModelInventory(base, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}

	codec, err := NewCodecOptions(cfg.Profile, ID, "")
	if err != nil {
		return execbackend.Backend{}, err
	}

	return NewBackend(BackendSpec{
		ID:                   cfg.BackendPrefix,
		APIKeyEnvVarRoot:     cfg.APIKeyEnvVarRoot,
		APIKey:               primary,
		APIKeys:              keys,
		Credentials:          credentials,
		QuotaSwitchThreshold: snap.QuotaSwitchRetryAfter,
		BaseURL:              cfg.BaseURL,
		HTTPClient:           client,
		RequestLimits:        cfg.RequestLimits,
		ResponseLimits:       cfg.ResponseLimits,
		Caps:                 aipapi.NewBackendCaps(cfg.Capabilities...),
		DialectSupport:       dialectSupportFromConfig(cfg),
		Inventory:            base.ModelInventory,
		Codec:                codec,
	}), nil
}

// LifecycleOpenResponsesCompatible is the standardplugins lifecycle entrypoint
// for the generic OpenResponses built-in-compatible backend kind.
func LifecycleOpenResponsesCompatible(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildWithSnapshot(instanceID, n, upstream, deps.Credentials)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

func dialectSupportFromConfig(cfg Config) aipapi.DialectSupport {
	out := aipapi.DialectSupport{}
	for _, d := range cfg.Dialects.Item {
		out.ItemDialects = append(out.ItemDialects, aipapi.DialectRequirement{
			Kind:        "item",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, d := range cfg.Dialects.Reasoning {
		out.ReasoningDialects = append(out.ReasoningDialects, aipapi.DialectRequirement{
			Kind:        "reasoning",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, d := range cfg.Dialects.Compaction {
		out.CompactionDialects = append(out.CompactionDialects, aipapi.DialectRequirement{
			Kind:        "compaction",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, e := range cfg.Dialects.Extensions {
		out.ExtensionTypes = append(out.ExtensionTypes, aipapi.ExtensionRequirement{
			Namespace:   e.Namespace,
			Type:        e.Type,
			Implementor: e.Implementor,
		})
	}
	return aipapi.NormalizeDialectSupport(out)
}
