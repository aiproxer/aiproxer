package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/alibabatokenplanintl"
	"gopkg.in/yaml.v3"
)

// alibabaTokenPlanIntlYAML is the secret-free config-file schema for the
// backend. Credentials are supplied exclusively via the
// ALIBABA_TOKEN_PLAN_API_KEY host environment root; any literal credential
// field is rejected by the central static-credential guard before decode.
type alibabaTokenPlanIntlYAML struct {
	BaseURL       string             `yaml:"base_url"`
	Models        modelInventoryYAML `yaml:"models"`
	SDKMaxRetries *int               `yaml:"sdk_max_retries"`
}

// backendAlibabaTokenPlanIntl constructs the Alibaba Token Plan (international)
// backend. Credentials are accepted only from the resolved UpstreamAPIKeys
// (ALIBABA_TOKEN_PLAN_API_KEY); any API key material in YAML is a hard error.
func backendAlibabaTokenPlanIntl(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	if err := config.RejectBackendStaticCredentials(alibabatokenplanintl.ID+" backend config", alibabaTokenPlanCredentialRoot, n); err != nil {
		return execbackend.Backend{}, err
	}
	var y alibabaTokenPlanIntlYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: %w", alibabatokenplanintl.ID, err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), alibabatokenplanintl.DefaultBaseURL)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, alibabatokenplanintl.ID+" backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := alibabatokenplanintl.Config{
		BaseURL:              base,
		APIKey:               firstResolvedAPIKey(keys.AlibabaTokenPlan),
		APIKeys:              keys.AlibabaTokenPlan,
		HTTPClient:           httpClient,
		SDKMaxRetries:        sdkMaxRetriesOrDefault(y.SDKMaxRetries),
		QuotaSwitchThreshold: keys.QuotaSwitchThreshold,
	}
	return applyConfiguredModelInventory(alibabatokenplanintl.New(cfg), y.Models)
}
