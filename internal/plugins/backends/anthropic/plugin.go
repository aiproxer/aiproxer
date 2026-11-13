package anthropic

import (
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
)

// Config configures the Anthropic Messages API backend connector (official SDK).
// BaseURL is the API origin only (e.g. https://api.anthropic.com), without a /v1 suffix;
// the SDK posts to /v1/messages relative to it.
type Config struct {
	BaseURL string
	APIKey  string
	// BackendPrefix overrides the backend/model-inventory prefix. Empty uses the bundled Anthropic ID.
	BackendPrefix string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries *int
	// QuotaSwitchThreshold is the Retry-After duration strictly above which a
	// 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
	// CompatibleModeAuth enables optional credentials for custom-anthropic-compatible.
	CompatibleModeAuth bool
	// ModelsEndpoint, when set, is the absolute inventory URL from the shared
	// endpoint descriptor join table. Empty uses BaseURL+"/v1/models".
	ModelsEndpoint string
	// CacheEnrollment is independent from keep-warm orchestration. The default
	// disabled value preserves existing foreground request behavior.
	CacheEnrollment CacheEnrollmentMode
	// CacheTTL is valid only for automatic enrollment and must be 5m or 1h.
	CacheTTL string
}

const (
	DefaultBaseURL = "https://api.anthropic.com"
	APIVersion     = "2023-06-01"
)

// New returns a runtime backend that invokes the Anthropic Messages API using anthropic-sdk-go.
func New(cfg Config) execbackend.Backend {
	if err := ValidateCacheConfig(cfg.CacheEnrollment, cfg.CacheTTL); err != nil {
		return invalidConfigBackend(err)
	}
	id := strings.TrimSpace(cfg.BackendPrefix)
	if id == "" {
		id = ID
	}
	return anthropicmessages.NewBackend(anthropicmessages.Config{
		BackendID:            id,
		BaseURL:              cfg.BaseURL,
		APIKey:               cfg.APIKey,
		APIKeys:              cfg.APIKeys,
		Credentials:          cfg.Credentials,
		HTTPClient:           cfg.HTTPClient,
		SDKMaxRetries:        cfg.SDKMaxRetries,
		QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
		ProviderCounter:      NewTokenCounter(cfg), CompatibleModeAuth: cfg.CompatibleModeAuth,
		// Native Anthropic accepts the request-level `output_config.effort`.
		// Compatible upstreams are not guaranteed to, so they keep the field
		// off and let capability negotiation strip the canonical effort.
		ForwardEffort:   !cfg.CompatibleModeAuth,
		CacheEnrollment: string(cfg.CacheEnrollment),
		CacheTTL:        cfg.CacheTTL,
		ModelInventory: modeldiscover.AnthropicModelsProvider{
			BaseURL:              cfg.BaseURL,
			ModelsEndpoint:       cfg.ModelsEndpoint,
			APIKey:               cfg.APIKey,
			APIKeys:              cfg.APIKeys,
			Credentials:          credentialSecrets(cfg.Credentials),
			QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
			HTTPClient:           cfg.HTTPClient,
			CanonicalPrefix:      id,
			CompatibleModeAuth:   cfg.CompatibleModeAuth,
		},
	})
}

// credentialSecrets projects structured credentials onto ordered secret
// values for the remote inventory provider, which resolves its own ordered
// selection from plain secrets.
func credentialSecrets(credentials []credpool.Credential) []string {
	if len(credentials) == 0 {
		return nil
	}
	out := make([]string, 0, len(credentials))
	for _, cred := range credentials {
		out = append(out, cred.Secret)
	}
	return out
}
