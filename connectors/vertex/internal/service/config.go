package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
	DefaultPublisher   = "google"
)

type Config struct {
	Project     string `yaml:"project"`
	Location    string `yaml:"location"`
	Publisher   string `yaml:"publisher"`
	Model       string `yaml:"model"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`
	// No credential fields: Google credentials arrive via host Configure
	// secrets (service_account_json) or the provider-managed ADC chain.
	// Literal secret-bearing YAML fields fail closed in rejectSecretFields
	// below.
}

// forbiddenSecretFields are top-level YAML configuration keys that must
// never carry provider credential material in this connector. Comparison is
// case-insensitive with -/_ unified; values are never echoed. The set
// covers the shared static-key taxonomy plus the service-account JSON
// spelling.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization":        {},
	"service_account_json": {}, "service_account": {}, "serviceaccount": {},
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := rejectSecretFields(raw); err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("vertex: config: %w", err)
		}
	}

	cfg.Project = strings.TrimSpace(cfg.Project)
	cfg.Location = strings.TrimSpace(cfg.Location)
	cfg.Publisher = strings.TrimSpace(cfg.Publisher)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Project == "" {
		return Config{}, fmt.Errorf("vertex: project is required")
	}
	if strings.Contains(cfg.Project, "://") || strings.Contains(cfg.Project, "/") {
		return Config{}, fmt.Errorf("vertex: invalid project name %q", cfg.Project)
	}
	if cfg.Location == "" {
		return Config{}, fmt.Errorf("vertex: location is required")
	}
	if cfg.Publisher == "" {
		cfg.Publisher = DefaultPublisher
	}

	return cfg, nil
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field. The error keeps the established
// secret-free shape, never the rejected value.
func rejectSecretFields(raw []byte) error {
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("vertex: config: %w", err)
	}
	for k := range rawMap {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("vertex: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
		}
	}
	return nil
}

func (c Config) BaseOrigin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	}
	loc := strings.ToLower(strings.TrimSpace(c.Location))
	if loc == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", loc)
}

func (c Config) ModelEndpoint(model string, stream bool) string {
	pub := strings.TrimSpace(c.Publisher)
	if pub == "" {
		pub = DefaultPublisher
	}
	origin := c.BaseOrigin()
	action := ":generateContent"
	if stream {
		action = ":streamGenerateContent?alt=sse"
	}
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/%s/models/%s%s",
		origin, c.Project, c.Location, pub, model, action)
}

func (c Config) InventoryEndpoint() string {
	pub := strings.TrimSpace(c.Publisher)
	if pub == "" {
		pub = DefaultPublisher
	}
	origin := c.BaseOrigin()
	return fmt.Sprintf("%s/v1beta1/publishers/%s/models", origin, pub)
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("vertex: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
