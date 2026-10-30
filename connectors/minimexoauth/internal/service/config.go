package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// Config holds runtime configuration for the MiniMax OAuth connector.
type Config struct {
	PortalBaseURL    string        `yaml:"portal_base_url"`
	InferenceBaseURL string        `yaml:"inference_base_url"`
	BaseURL          string        `yaml:"base_url"`
	Region           string        `yaml:"region"`
	OAuthClientID    string        `yaml:"oauth_client_id"`
	OAuthTokenFile   string        `yaml:"oauth_token_file"`
	OAuthScope       string        `yaml:"oauth_scope"`
	HTTPTimeout      time.Duration `yaml:"http_timeout"`

	// DirectToken is populated exclusively from secrets (api_key or access_token).
	DirectToken string `yaml:"-"`
}

func ParseAndValidateConfig(cfgYAML []byte, secrets backendplugin.SecretBundle) (Config, error) {
	var raw map[string]any
	if len(cfgYAML) > 0 {
		if err := yaml.Unmarshal(cfgYAML, &raw); err != nil {
			return Config{}, fmt.Errorf("minimax-oauth: unmarshal config YAML: %w", err)
		}
	}

	// Hard negative: reject literal credentials embedded in config YAML
	for _, forbidden := range []string{"api_key", "access_token", "refresh_token", "token", "secret", "client_secret"} {
		if val, exists := raw[forbidden]; exists && val != nil && strings.TrimSpace(fmt.Sprint(val)) != "" {
			return Config{}, fmt.Errorf("minimax-oauth: literal secret %q cannot be supplied in configuration YAML; use secrets bundle", forbidden)
		}
	}

	var cfg Config
	if len(cfgYAML) > 0 {
		if err := yaml.Unmarshal(cfgYAML, &cfg); err != nil {
			return Config{}, fmt.Errorf("minimax-oauth: unmarshal config: %w", err)
		}
	}

	// Secret extraction:
	// Static access token can be provided via secrets "access_token" or "api_key".
	// Notice: MINIMAX_API_KEY is explicitly forbidden and must never be used.
	for k := range secrets.Values {
		lowerK := strings.ToLower(k)
		if lowerK == "minimax_api_key" {
			return Config{}, errors.New("minimax-oauth: MINIMAX_API_KEY is not supported for minimax-oauth; use an OAuth token file or standard secrets")
		}
	}

	for _, k := range []string{"access_token", "api_key", "token"} {
		if val, ok := secrets.Values[k]; ok && len(val) > 0 {
			cfg.DirectToken = strings.TrimSpace(string(val))
			break
		}
	}

	// Client ID can be passed in config or secret
	if cfg.OAuthClientID == "" {
		for _, k := range []string{"oauth_client_id", "client_id"} {
			if val, ok := secrets.Values[k]; ok && len(val) > 0 {
				cfg.OAuthClientID = strings.TrimSpace(string(val))
				break
			}
		}
	}

	// Resolve Region & Base URLs:
	cfg.Region = strings.ToLower(strings.TrimSpace(cfg.Region))
	if cfg.Region == "" {
		cfg.Region = "global"
	}

	isChina := cfg.Region == "cn" || cfg.Region == "china" || cfg.Region == "minimax-cn"
	if cfg.PortalBaseURL == "" {
		if isChina {
			cfg.PortalBaseURL = CNPortalBaseURL
		} else {
			cfg.PortalBaseURL = DefaultPortalBaseURL
		}
	}
	if cfg.InferenceBaseURL == "" {
		if cfg.BaseURL != "" {
			cfg.InferenceBaseURL = cfg.BaseURL
		} else if isChina {
			cfg.InferenceBaseURL = CNInferenceBaseURL
		} else {
			cfg.InferenceBaseURL = DefaultInferenceBaseURL
		}
	}

	cfg.PortalBaseURL = strings.TrimRight(cfg.PortalBaseURL, "/")
	cfg.InferenceBaseURL = strings.TrimRight(cfg.InferenceBaseURL, "/")

	if cfg.OAuthScope == "" {
		cfg.OAuthScope = DefaultScope
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = DefaultHTTPTimeout
	}

	// If no static token, OAuth token file is required
	if cfg.DirectToken == "" {
		if cfg.OAuthTokenFile == "" {
			return Config{}, errors.New("minimax-oauth: oauth_token_file or static token secret is required")
		}
		if cfg.OAuthClientID == "" {
			return Config{}, errors.New("minimax-oauth: oauth_client_id is required (Hermes client_id must not be hardcoded in production)")
		}
	}

	return cfg, nil
}
