package service

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
)

type Config struct {
	Region              string `yaml:"region"`
	CompartmentID       string `yaml:"compartment_id"`
	ModelID             string `yaml:"model_id"`
	DedicatedEndpointID string `yaml:"dedicated_endpoint_id"`
	APIOrigin           string `yaml:"api_origin"`
	TenancyOCID         string `yaml:"tenancy_ocid"`
	UserOCID            string `yaml:"user_ocid"`
	Fingerprint         string `yaml:"fingerprint"`
	HTTPTimeout         string `yaml:"http_timeout"`
	// No credential fields: request-signing material arrives via host
	// Configure secrets or the provider-managed chain. Literal
	// secret-bearing YAML fields fail closed in rejectSecretFields below.
	// Nonsecret OCIDs (tenancy/user/fingerprint) stay valid YAML.
}

// forbiddenSecretFields are top-level YAML configuration keys that must
// never carry provider credential material in this connector. Comparison is
// case-insensitive with -/_ unified; values are never echoed. The set
// covers the shared static-key taxonomy plus OCI key spellings.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {}, "secret_key": {}, "secretkey": {},
	"private_key": {}, "privatekey": {}, "private_key_pem": {},
	"private_key_passphrase": {}, "passphrase": {},
	"pat": {}, "password": {}, "key": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization": {},
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := rejectSecretFields(raw); err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("oci-generative-ai: config: %w", err)
		}
	}

	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.CompartmentID = strings.TrimSpace(cfg.CompartmentID)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.DedicatedEndpointID = strings.TrimSpace(cfg.DedicatedEndpointID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.TenancyOCID = strings.TrimSpace(cfg.TenancyOCID)
	cfg.UserOCID = strings.TrimSpace(cfg.UserOCID)
	cfg.Fingerprint = strings.TrimSpace(cfg.Fingerprint)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Region == "" {
		return Config{}, fmt.Errorf("oci-generative-ai: region is required")
	}
	if cfg.CompartmentID == "" {
		return Config{}, fmt.Errorf("oci-generative-ai: compartment_id is required")
	}

	return cfg, nil
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field. The error keeps the established
// secret-free shape, never the rejected value.
func rejectSecretFields(raw []byte) error {
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("oci-generative-ai: config: %w", err)
	}
	for k := range rawMap {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("oci-generative-ai: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
		}
	}
	return nil
}

func (c Config) ChatEndpoint() string {
	origin := strings.TrimRight(c.APIOrigin, "/")
	if origin == "" {
		origin = fmt.Sprintf("https://inference.generativeai.%s.oci.oraclecloud.com", c.Region)
	}
	return origin + "/20231130/actions/chat"
}

func (c Config) ManagementEndpoint() string {
	origin := strings.TrimRight(c.APIOrigin, "/")
	if origin == "" {
		origin = fmt.Sprintf("https://generativeai.%s.oci.oraclecloud.com", c.Region)
	}
	return fmt.Sprintf("%s/20231130/models?compartmentId=%s", origin, url.QueryEscape(c.CompartmentID))
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("oci-generative-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
