// Package alibabatokenplanintl implements Alibaba Cloud's international Token Plan backend.
package alibabatokenplanintl

import (
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
)

const (
	ID                   = "alibaba-token-plan-intl"
	DefaultBaseURL       = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic"
	DefaultModelsBaseURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
)

type Config struct {
	BaseURL       string
	ModelsBaseURL string
	APIKey        string
	APIKeys       []string
	Credentials   []credpool.Credential
	HTTPClient    *http.Client
	SDKMaxRetries *int
	// QuotaSwitchThreshold is the Retry-After duration strictly above which a
	// 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
}

// New returns an Anthropic Messages-compatible Token Plan backend.
func New(cfg Config) execbackend.Backend {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	modelsBaseURL := strings.TrimRight(strings.TrimSpace(cfg.ModelsBaseURL), "/")
	if modelsBaseURL == "" {
		modelsBaseURL = DefaultModelsBaseURL
	}
	return anthropicmessages.NewBackend(anthropicmessages.Config{
		BackendID:            ID,
		BaseURL:              baseURL,
		APIKey:               cfg.APIKey,
		APIKeys:              cfg.APIKeys,
		Credentials:          cfg.Credentials,
		HTTPClient:           cfg.HTTPClient,
		SDKMaxRetries:        cfg.SDKMaxRetries,
		QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
		NormalizeRoles:       true,
		NormalizeModel:       normalizeModel,
		ThinkingFromEffort:   true,
		OmitToolChoice:       true,
		ModelInventory: modeldiscover.OpenAICompatibleModelsProvider{
			BaseURL:         modelsBaseURL,
			APIKey:          cfg.APIKey,
			APIKeys:         cfg.APIKeys,
			HTTPClient:      cfg.HTTPClient,
			CanonicalPrefix: ID,
		},
	})
}

// normalizeModel strips the provider namespace that some clients prepend to
// Model Studio model IDs. Alibaba's catalog uses bare IDs such as
// "qwen3.8-max-preview", so a qualified "alibaba/qwen3.8-max-preview" selector
// is reduced to the bare ID before it is sent upstream. Other models are
// returned unchanged.
func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if normalized, ok := strings.CutPrefix(model, "alibaba/"); ok {
		return normalized
	}
	return model
}
