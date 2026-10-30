package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

type openAIHostedYAML struct {
	openAIStyleYAML  `yaml:",inline"`
	DefaultVerbosity string `yaml:"default_verbosity"`
}

func backendOpenAIResponses(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	if err := config.RejectBackendStaticCredentials("openairesponses backend config", openAICredentialRoot, n); err != nil {
		return execbackend.Backend{}, err
	}
	var y openAIHostedYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), openairesponses.DefaultBaseURL)
	ek := keys.OpenAI
	primaryKey := firstResolvedAPIKey(ek)
	verbosity, err := aipapi.ParseVerbosityLevel(y.DefaultVerbosity)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: default_verbosity: %w", err)
	}
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "openairesponses backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := openairesponses.Config{
		BaseURL:              base,
		APIKey:               primaryKey,
		APIKeys:              ek,
		HTTPClient:           httpClient,
		SDKMaxRetries:        sdkMaxRetriesOrDefault(y.SDKMaxRetries),
		QuotaSwitchThreshold: keys.QuotaSwitchThreshold,
		DefaultVerbosity:     verbosity,
	}
	return applyConfiguredModelInventory(openairesponses.New(cfg), y.Models)
}

func backendOpenAILegacy(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	if err := config.RejectBackendStaticCredentials("openailegacy backend config", openAICredentialRoot, n); err != nil {
		return execbackend.Backend{}, err
	}
	var y openAIHostedYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openailegacy backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), openailegacy.DefaultBaseURL)
	ek := keys.OpenAI
	primaryKey := firstResolvedAPIKey(ek)
	verbosity, err := aipapi.ParseVerbosityLevel(y.DefaultVerbosity)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("openailegacy backend config: default_verbosity: %w", err)
	}
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "openailegacy backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := openailegacy.Config{
		BaseURL:              base,
		APIKey:               primaryKey,
		APIKeys:              ek,
		HTTPClient:           httpClient,
		SDKMaxRetries:        sdkMaxRetriesOrDefault(y.SDKMaxRetries),
		QuotaSwitchThreshold: keys.QuotaSwitchThreshold,
		DefaultVerbosity:     verbosity,
	}
	return applyConfiguredModelInventory(openailegacy.New(cfg), y.Models)
}
