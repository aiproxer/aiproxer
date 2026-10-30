package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"gopkg.in/yaml.v3"
)

func backendGemini(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	if err := config.RejectBackendStaticCredentials("gemini backend config", geminiCredentialRoot, n); err != nil {
		return execbackend.Backend{}, err
	}
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("gemini backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), gemini.DefaultBaseURL)
	ek := keys.Gemini
	primaryKey := firstResolvedAPIKey(ek)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "gemini backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := gemini.Config{
		BaseURL:              base,
		APIKey:               primaryKey,
		APIKeys:              ek,
		HTTPClient:           httpClient,
		QuotaSwitchThreshold: keys.QuotaSwitchThreshold,
	}
	return applyConfiguredModelInventory(gemini.New(cfg), y.Models)
}
