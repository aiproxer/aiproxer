package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ResourceGroup     string `yaml:"resource_group"`
	InferenceContract string `yaml:"inference_contract"`
	DeploymentID      string `yaml:"deployment_id"`
	APIOrigin         string `yaml:"api_origin"`
	OAuthOrigin       string `yaml:"oauth_origin"`
	HTTPTimeout       string `yaml:"http_timeout"`
	// No credential fields: the service key arrives via host Configure
	// secrets only. Literal secret-bearing YAML fields fail closed in
	// rejectSecretFields below.
}

// forbiddenSecretFields are top-level YAML configuration keys that must
// never carry provider credential material in this connector. Comparison is
// case-insensitive with -/_ unified; values are never echoed. The set
// covers the shared static-key taxonomy plus SAP service-key spellings.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {}, "clientsecret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization": {},
	"service_key":   {}, "servicekey": {},
	"clientid": {}, "client_id": {},
	"serviceurls": {}, "ai_api_url": {},
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := rejectSecretFields(raw); err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("sapaicore: config: %w", err)
		}
	}

	cfg.ResourceGroup = strings.TrimSpace(cfg.ResourceGroup)
	if cfg.ResourceGroup == "" {
		return Config{}, fmt.Errorf("sapaicore: resource_group is required")
	}

	cfg.InferenceContract = strings.TrimSpace(strings.ToLower(cfg.InferenceContract))
	if cfg.InferenceContract == "" {
		return Config{}, fmt.Errorf("sapaicore: inference_contract is required")
	}
	if cfg.InferenceContract != InferenceContractOpenAIChat {
		return Config{}, fmt.Errorf("sapaicore: unsupported inference_contract %q (expected %q)", cfg.InferenceContract, InferenceContractOpenAIChat)
	}

	cfg.DeploymentID = strings.TrimSpace(cfg.DeploymentID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.OAuthOrigin = strings.TrimSpace(cfg.OAuthOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field. The error keeps the established
// secret-free shape naming only the remediation path, never the value.
func rejectSecretFields(raw []byte) error {
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("sapaicore: config: %w", err)
	}
	for k := range rawMap {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("sapaicore: literal secrets in configuration YAML are forbidden; supply service_key via ConfigureRequest.Secrets")
		}
	}
	return nil
}

func (c Config) AIAPIOrigin(serviceKeyAIURL string) string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return strings.TrimRight(serviceKeyAIURL, "/")
}

func (c Config) OAuthURL(serviceKeyAuthURL string) string {
	origin := serviceKeyAuthURL
	if c.OAuthOrigin != "" {
		origin = c.OAuthOrigin
	}
	origin = strings.TrimRight(origin, "/")
	if !strings.HasSuffix(origin, "/oauth/token") {
		origin += "/oauth/token"
	}
	return origin
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("sapaicore: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}

type ServiceKey struct {
	ClientID     string `json:"clientid"`
	ClientSecret string `json:"clientsecret"`
	URL          string `json:"url"`
	ServiceURLs  struct {
		AIAPIURL string `json:"AI_API_URL"`
	} `json:"serviceurls"`
}

func ParseServiceKey(raw []byte) (ServiceKey, error) {
	if len(raw) == 0 {
		return ServiceKey{}, fmt.Errorf("sapaicore: missing required service_key in secrets")
	}
	var sk ServiceKey
	if err := json.Unmarshal(raw, &sk); err != nil {
		return ServiceKey{}, fmt.Errorf("sapaicore: invalid service_key JSON")
	}
	sk.ClientID = strings.TrimSpace(sk.ClientID)
	sk.ClientSecret = strings.TrimSpace(sk.ClientSecret)
	sk.URL = strings.TrimSpace(sk.URL)
	sk.ServiceURLs.AIAPIURL = strings.TrimSpace(sk.ServiceURLs.AIAPIURL)

	if sk.ClientID == "" || sk.ClientSecret == "" || sk.URL == "" || sk.ServiceURLs.AIAPIURL == "" {
		return ServiceKey{}, fmt.Errorf("sapaicore: invalid service_key: missing required fields (clientid, clientsecret, url, serviceurls.AI_API_URL)")
	}
	return sk, nil
}
