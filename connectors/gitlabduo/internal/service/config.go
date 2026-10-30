package service

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	InstanceURL     string `yaml:"instance_url"`
	AIGatewayURL    string `yaml:"ai_gateway_url"`
	OAuthClientID   string `yaml:"oauth_client_id"`
	OAuthTokenFile  string `yaml:"oauth_token_file"`
	RootNamespaceID string `yaml:"root_namespace_id"`
	ProjectPath     string `yaml:"project_path"`
	Model           string `yaml:"model"`
	ModelID         string `yaml:"model_id"`
	DefaultModelID  string `yaml:"default_model"`
	HTTPTimeout     string `yaml:"http_timeout"`

	// Forbidden secrets in YAML - rejected if present
	PAT          string `yaml:"pat"`
	Token        string `yaml:"token"`
	APIKey       string `yaml:"api_key"`
	APIKey2      string `yaml:"apikey"`
	Secret       string `yaml:"secret"`
	ClientSecret string `yaml:"client_secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var rawMap map[string]any
		if err := yaml.Unmarshal(raw, &rawMap); err != nil {
			return Config{}, fmt.Errorf("gitlab-duo: config: %w", err)
		}
		for k := range rawMap {
			lower := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
			if lower == "pat" || lower == "token" || lower == "api_key" || lower == "apikey" ||
				lower == "secret" || lower == "client_secret" {
				return Config{}, fmt.Errorf("gitlab-duo: literal secrets in configuration YAML are forbidden; supply credentials via ConfigureRequest.Secrets")
			}
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("gitlab-duo: config: %w", err)
		}
	}

	if strings.TrimSpace(cfg.PAT) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIKey2) != "" ||
		strings.TrimSpace(cfg.Secret) != "" ||
		strings.TrimSpace(cfg.ClientSecret) != "" {
		return Config{}, fmt.Errorf("gitlab-duo: literal secrets in configuration YAML are forbidden; supply credentials via ConfigureRequest.Secrets")
	}

	cfg.InstanceURL = strings.TrimSpace(cfg.InstanceURL)
	cfg.AIGatewayURL = strings.TrimSpace(cfg.AIGatewayURL)
	cfg.OAuthClientID = strings.TrimSpace(cfg.OAuthClientID)
	cfg.OAuthTokenFile = strings.TrimSpace(cfg.OAuthTokenFile)
	cfg.RootNamespaceID = strings.TrimSpace(cfg.RootNamespaceID)
	cfg.ProjectPath = strings.TrimSpace(cfg.ProjectPath)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.DefaultModelID = strings.TrimSpace(cfg.DefaultModelID)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) GetInstanceURL() string {
	if c.InstanceURL != "" {
		return strings.TrimRight(c.InstanceURL, "/")
	}
	return DefaultInstanceURL
}

func (c Config) GetAIGatewayURL() string {
	if c.AIGatewayURL != "" {
		return strings.TrimRight(c.AIGatewayURL, "/")
	}
	return DefaultAIGatewayURL
}

func (c Config) IsSelfManaged() bool {
	inst := strings.TrimRight(strings.ToLower(c.GetInstanceURL()), "/")
	u, err := url.Parse(inst)
	if err != nil {
		return inst != "https://gitlab.com"
	}
	host := strings.ToLower(u.Host)
	return host != "gitlab.com" && host != "www.gitlab.com"
}

func (c Config) DefaultModel() string {
	if c.DefaultModelID != "" {
		return c.DefaultModelID
	}
	if c.Model != "" {
		return c.Model
	}
	if c.ModelID != "" {
		return c.ModelID
	}
	return ModelSonnet45
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("gitlab-duo: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
