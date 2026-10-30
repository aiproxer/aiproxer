package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	InferenceURL   string `yaml:"inference_url"`
	TokenURL       string `yaml:"token_url"`
	OAuthClientID  string `yaml:"oauth_client_id"`
	OAuthTokenFile string `yaml:"oauth_token_file"`
	Model          string `yaml:"model"`
	ModelID        string `yaml:"model_id"`
	DefaultModelID string `yaml:"default_model"`
	HTTPTimeout    string `yaml:"http_timeout"`

	// Forbidden literal secrets in YAML - rejected if present
	APIKey       string `yaml:"api_key"`
	APIKey2      string `yaml:"apikey"`
	Token        string `yaml:"token"`
	RefreshToken string `yaml:"refresh_token"`
	Secret       string `yaml:"secret"`
	ClientSecret string `yaml:"client_secret"`
	PAT          string `yaml:"pat"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var rawMap map[string]any
		if err := yaml.Unmarshal(raw, &rawMap); err != nil {
			return Config{}, fmt.Errorf("qwen-oauth: config: %w", err)
		}
		for k := range rawMap {
			lower := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
			if lower == "api_key" || lower == "apikey" || lower == "token" ||
				lower == "refresh_token" || lower == "secret" ||
				lower == "client_secret" || lower == "pat" {
				return Config{}, fmt.Errorf("qwen-oauth: literal secrets in configuration YAML are forbidden; supply credentials via ConfigureRequest.Secrets or oauth_token_file")
			}
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("qwen-oauth: config: %w", err)
		}
	}

	if strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIKey2) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.RefreshToken) != "" ||
		strings.TrimSpace(cfg.Secret) != "" ||
		strings.TrimSpace(cfg.ClientSecret) != "" ||
		strings.TrimSpace(cfg.PAT) != "" {
		return Config{}, fmt.Errorf("qwen-oauth: literal secrets in configuration YAML are forbidden; supply credentials via ConfigureRequest.Secrets or oauth_token_file")
	}

	cfg.InferenceURL = strings.TrimSpace(cfg.InferenceURL)
	cfg.TokenURL = strings.TrimSpace(cfg.TokenURL)
	cfg.OAuthClientID = strings.TrimSpace(cfg.OAuthClientID)
	cfg.OAuthTokenFile = strings.TrimSpace(cfg.OAuthTokenFile)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.DefaultModelID = strings.TrimSpace(cfg.DefaultModelID)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) GetTokenURL() string {
	if c.TokenURL != "" {
		return strings.TrimRight(c.TokenURL, "/")
	}
	return DefaultTokenURL
}

func (c Config) GetInferenceURL() string {
	if c.InferenceURL != "" {
		return strings.TrimRight(c.InferenceURL, "/")
	}
	return DefaultInferenceURL
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
	return DefaultModel
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("qwen-oauth: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
