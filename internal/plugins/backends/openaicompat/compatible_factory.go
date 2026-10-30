package openaicompat

import (
	"net/http"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/openai/openai-go/v3/option"
	"gopkg.in/yaml.v3"
)

// BuildCompatible constructs a built-in OpenAI-family compatible backend from
// strict compatible-mode YAML for one configured runtime instance.
func BuildCompatible(
	instanceID, factoryKind string,
	n yaml.Node,
	upstream *http.Client,
	flavor Flavor,
	transportCaps aipapi.BackendTransportCaps,
) (execbackend.Backend, error) {
	return BuildCompatibleWithHeaders(instanceID, factoryKind, n, upstream, flavor, transportCaps, nil, CompatibleQuirks{})
}

// BuildCompatibleWithHeaders is the profile composition seam for the bounded
// static-header subset. Custom-compatible YAML remains on BuildCompatible and
// cannot inject arbitrary headers.
func BuildCompatibleWithHeaders(
	instanceID, factoryKind string,
	n yaml.Node,
	upstream *http.Client,
	flavor Flavor,
	transportCaps aipapi.BackendTransportCaps,
	headers map[string]string,
	quirks CompatibleQuirks,
) (execbackend.Backend, error) {
	return BuildCompatibleWithSnapshot(instanceID, factoryKind, n, upstream, flavor, transportCaps, headers, quirks, pluginreg.HostCredentialSnapshot{})
}

// BuildCompatibleWithSnapshot constructs the backend with admitted
// candidate-snapshot credentials (requirement 1.4): the configured
// api_key_env_var_root resolves through the authorized snapshot when the
// composition root captured one, so serving and remote inventory keep
// admitted keys across process-environment rotation. Without a resolved
// snapshot the candidate-build process-environment lookup applies.
func BuildCompatibleWithSnapshot(
	instanceID, factoryKind string,
	n yaml.Node,
	upstream *http.Client,
	flavor Flavor,
	transportCaps aipapi.BackendTransportCaps,
	headers map[string]string,
	quirks CompatibleQuirks,
	snap pluginreg.HostCredentialSnapshot,
) (execbackend.Backend, error) {
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
	modelsEndpoint, err := compatmode.OpenAIModelsEndpoint(ep)
	if err != nil {
		return execbackend.Backend{}, err
	}
	ek := compatmode.ResolveCustomKeys(cfg.APIKeyEnvVarRoot, snap)
	var apiKey string
	if len(ek) > 0 {
		apiKey = ek[0]
	}
	credentials := make([]credpool.Credential, 0, len(ek))
	for _, secret := range ek {
		credentials = append(credentials, credpool.Credential{Secret: secret})
	}
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}
	inventory := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:              base,
		ModelsEndpoint:       modelsEndpoint,
		APIKey:               apiKey,
		APIKeys:              ek,
		Credentials:          ek,
		QuotaSwitchThreshold: snap.QuotaSwitchRetryAfter,
		HTTPClient:           client,
		CanonicalPrefix:      cfg.BackendPrefix,
		CompatibleModeAuth:   true,
	}
	var invModel modelinventory.Provider = inventory
	if staticInv, err := compatmode.ApplyStaticModelInventory(execbackend.Backend{}, cfg.Models); err != nil {
		return execbackend.Backend{}, err
	} else if staticInv.ModelInventory != nil {
		invModel = staticInv.ModelInventory
	}
	be := NewBackend(BackendSpec{
		ID:                   cfg.BackendPrefix,
		Flavor:               flavor,
		BaseURL:              base,
		APIKey:               apiKey,
		APIKeys:              ek,
		Credentials:          credentials,
		QuotaSwitchThreshold: snap.QuotaSwitchRetryAfter,
		HTTPClient:           client,
		SDKMaxRetries:        sdkMaxRetriesOrDefault(nil),
		Inventory:            invModel,
		CompatibleModeAuth:   true,
		ResolveFlavor:        func(aipapi.Call) Flavor { return flavor },
		RequestOptions:       staticHeaderOptions(headers),
		Quirks:               quirks,
	})
	be.TransportCaps = transportCaps
	return compatmode.ApplyRuntimePolicy(be, cfg)
}

func staticHeaderOptions(headers map[string]string) func(aipapi.Call) []option.RequestOption {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return func(aipapi.Call) []option.RequestOption {
		out := make([]option.RequestOption, 0, len(keys))
		for _, key := range keys {
			out = append(out, option.WithHeader(key, headers[key]))
		}
		return out
	}
}

// CompatibleTransportCaps exposes the existing family transport contract to
// profile composition without exposing a factory or provider registration.
func CompatibleTransportCaps(flavor Flavor) aipapi.BackendTransportCaps {
	if flavor == FlavorResponses {
		return customOpenAIResponsesTransportCaps()
	}
	return customOpenAILegacyTransportCaps()
}

func sdkMaxRetriesOrDefault(v *int) *int {
	if v != nil {
		return v
	}
	zero := 0
	return &zero
}

// LifecycleOpenAILegacyCompatible is the standardplugins lifecycle entrypoint.
func LifecycleOpenAILegacyCompatible(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatibleWithSnapshot(instanceID, "custom-openai-legacy-compatible", n, upstream, FlavorChat, customOpenAILegacyTransportCaps(), nil, CompatibleQuirks{}, deps.Credentials)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

// LifecycleOpenAIResponsesCompatible is the standardplugins lifecycle entrypoint.
func LifecycleOpenAIResponsesCompatible(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatibleWithSnapshot(instanceID, "custom-openai-responses-compatible", n, upstream, FlavorResponses, customOpenAIResponsesTransportCaps(), nil, CompatibleQuirks{}, deps.Credentials)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

func customOpenAILegacyTransportCaps() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		},
	)
}

func customOpenAIResponsesTransportCaps() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIResponses,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		},
	)
}
