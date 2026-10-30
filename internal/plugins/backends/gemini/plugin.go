package gemini

import (
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/geminigenerate"
)

// Config configures the Gemini generateContent backend connector (official genai SDK).
// BaseURL is the API origin (e.g. https://generativelanguage.googleapis.com) without a path suffix;
// the SDK appends /v1beta/models/... for Google AI backend.
//
// google.golang.org/genai does not apply the same automatic multi-attempt retries on
// generateContent/streamGenerateContent as openai-go and anthropic-sdk-go; credential tests
// can count httptest invocations without an SDKMaxRetries-style knob.
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// QuotaSwitchThreshold is the Retry-After/RetryInfo duration strictly above
	// which a 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
}

const DefaultBaseURL = "https://generativelanguage.googleapis.com"

// New returns a runtime backend that invokes Gemini generateContent streaming via google.golang.org/genai.
func New(cfg Config) execbackend.Backend {
	// Structured credentials take precedence over legacy keys in both
	// serving (via NewPoolFromCredentials) and inventory: when set, legacy
	// material is never attempted, so a stale programmatic key cannot stop
	// discovery while serving stays healthy.
	structured := credentialSecrets(cfg.Credentials)
	invAPIKey, invAPIKeys := cfg.APIKey, cfg.APIKeys
	if len(structured) > 0 {
		invAPIKey, invAPIKeys = "", nil
	}
	return geminigenerate.NewBackend(geminigenerate.Config{
		BackendID:            ID,
		BaseURL:              cfg.BaseURL,
		APIKey:               cfg.APIKey,
		APIKeys:              cfg.APIKeys,
		Credentials:          cfg.Credentials,
		HTTPClient:           cfg.HTTPClient,
		QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
		ModelInventory: modeldiscover.GeminiModelsProvider{
			BaseURL:              cfg.BaseURL,
			APIKey:               invAPIKey,
			APIKeys:              invAPIKeys,
			Credentials:          structured,
			HTTPClient:           cfg.HTTPClient,
			QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
		},
	})
}

// credentialSecrets projects structured credentials to ordered secret strings
// for inventory selection. IDs stay serving-side; inventory uses a per-call
// local pool so background discovery never cools serving credentials.
func credentialSecrets(creds []credpool.Credential) []string {
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		if c.Secret == "" {
			continue
		}
		out = append(out, c.Secret)
	}
	return out
}
