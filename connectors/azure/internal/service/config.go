package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout   = 60 * time.Second
	CredentialModeAPIKey = "api_key"
	CredentialModeEntra  = "entra"
)

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// Azure OpenAI key-mode credentials: AZURE_OPENAI_API_KEY[_N]. Secrets never
// appear in connector YAML; the host delivers ordered slots via Configure
// secrets. Entra/OAuth identities are provider-managed and never pooled
// here.
const EnvAPIKeyRoot = "AZURE_OPENAI_API_KEY"

// QuotaDomain is the explicit nonsecret quota scope for Azure OpenAI key
// selection. Deployment-scoped 429s advance keys within this domain only;
// they never alias an unrelated provider or deployment cooldown.
const QuotaDomain = "azure-openai"

type Config struct {
	ResourceName   string `yaml:"resource_name"`
	Endpoint       string `yaml:"endpoint"`
	APIVersion     string `yaml:"api_version"`
	CredentialMode string `yaml:"credential_mode"`
	HTTPTimeout    string `yaml:"http_timeout"`

	// Deployments maps Azure deployment name (the wire `model` value) to the
	// underlying model ID (informational: inventory filtering and display).
	// Azure OpenAI inference routes by deployment name, which need not equal
	// the underlying model ID, so at least one entry is required and
	// inventory advertises deployment names only.
	Deployments map[string]string `yaml:"deployments"`

	// Entra configuration fields (non-secret):
	TenantID string `yaml:"tenant_id"`
	ClientID string `yaml:"client_id"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("azure-openai", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("azure-openai: config: %w", err)
		}
	}

	cfg.ResourceName = strings.TrimSpace(cfg.ResourceName)
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.APIVersion = strings.TrimSpace(cfg.APIVersion)
	cfg.CredentialMode = strings.TrimSpace(cfg.CredentialMode)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)
	cfg.TenantID = strings.TrimSpace(cfg.TenantID)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)

	normalized := make(map[string]string, len(cfg.Deployments))
	for name, model := range cfg.Deployments {
		name = strings.TrimSpace(name)
		model = strings.TrimSpace(model)
		if name == "" {
			return Config{}, fmt.Errorf("azure-openai: deployments: empty deployment name is forbidden")
		}
		if model == "" {
			return Config{}, fmt.Errorf("azure-openai: deployments[%q]: underlying model is required", name)
		}
		if _, dup := normalized[name]; dup {
			return Config{}, fmt.Errorf("azure-openai: deployments[%q]: duplicate deployment name", name)
		}
		normalized[name] = model
	}
	cfg.Deployments = normalized

	if strings.Contains(strings.ToLower(cfg.Endpoint), "/compat") {
		return Config{}, fmt.Errorf("azure-openai: /compat endpoint is forbidden for ordinary calls")
	}

	return cfg, nil
}

// errMissingDeployments fails closed when no deployment mapping is configured.
// Azure inference routes by deployment name, so bare model IDs are not routable.
func errMissingDeployments() error {
	return fmt.Errorf("azure-openai: deployments is required (at least one <deployment-name>: <model> entry); Azure inference routes by deployment name, not model ID")
}

func (c Config) BaseURL() string {
	if c.Endpoint != "" {
		endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
		if !strings.HasSuffix(endpoint, "/openai/v1") {
			endpoint += "/openai/v1"
		}
		return endpoint
	}
	if c.ResourceName != "" {
		return fmt.Sprintf("https://%s.openai.azure.com/openai/v1", strings.TrimSpace(c.ResourceName))
	}
	return ""
}

type azureTransport struct {
	base          http.RoundTripper
	apiKey        string
	tokenProvider TokenProvider
	apiVersion    string
	mode          string
}

func (t *azureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}

	if t.mode == CredentialModeEntra {
		cloned.Header.Del("api-key")
		if t.tokenProvider != nil {
			tok, err := t.tokenProvider.GetToken(req.Context())
			if err != nil {
				return nil, fmt.Errorf("azure-openai: token provider: %w", err)
			}
			cloned.Header.Set("Authorization", "Bearer "+tok)
		}
	} else {
		// api_key mode
		cloned.Header.Del("Authorization")
		if t.apiKey != "" {
			cloned.Header.Set("api-key", t.apiKey)
		}
	}

	if t.apiVersion != "" {
		q := cloned.URL.Query()
		if q.Get("api-version") == "" {
			q.Set("api-version", t.apiVersion)
			cloned.URL.RawQuery = q.Encode()
		}
	}

	rt := t.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(cloned)
}

func (c Config) HTTPClient() (*http.Client, error) {
	return c.HTTPClientWithTokenProvider("", nil)
}

func (c Config) HTTPClientWithTokenProvider(apiKey string, tp TokenProvider) (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("azure-openai: http_timeout: %w", err)
		}
		d = parsed
	}

	mode := c.CredentialMode
	if mode == "" {
		if tp != nil {
			mode = CredentialModeEntra
		} else {
			mode = CredentialModeAPIKey
		}
	}

	transport := &azureTransport{
		base:          http.DefaultTransport,
		apiKey:        apiKey,
		tokenProvider: tp,
		apiVersion:    c.APIVersion,
		mode:          mode,
	}

	return &http.Client{
		Timeout:   d,
		Transport: transport,
	}, nil
}
