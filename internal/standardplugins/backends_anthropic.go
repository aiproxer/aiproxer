package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"gopkg.in/yaml.v3"
)

// anthropicHostedYAML is the direct Anthropic backend config shape. Cache
// enrollment stays independent from keep-warm orchestration: the default
// disabled value preserves existing foreground request behavior.
type anthropicHostedYAML struct {
	openAIStyleYAML `yaml:",inline"`
	// CacheEnrollment is one of "disabled" or "automatic" (default disabled).
	CacheEnrollment anthropic.CacheEnrollmentMode `yaml:"cache_enrollment"`
	// CacheTTL is valid only for automatic enrollment and must be 5m or 1h.
	CacheTTL string `yaml:"cache_ttl"`
}

func backendAnthropic(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	if err := config.RejectBackendStaticCredentials("anthropic backend config", anthropicCredentialRoot, n); err != nil {
		return execbackend.Backend{}, err
	}
	var y anthropicHostedYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("anthropic backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), anthropic.DefaultBaseURL)
	ek := keys.Anthropic
	primaryKey := firstResolvedAPIKey(ek)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "anthropic backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := anthropic.Config{
		BaseURL:              base,
		APIKey:               primaryKey,
		APIKeys:              ek,
		HTTPClient:           httpClient,
		SDKMaxRetries:        sdkMaxRetriesOrDefault(y.SDKMaxRetries),
		QuotaSwitchThreshold: keys.QuotaSwitchThreshold,
		CacheEnrollment:      y.CacheEnrollment,
		CacheTTL:             y.CacheTTL,
	}
	return applyConfiguredModelInventory(anthropic.New(cfg), y.Models)
}
