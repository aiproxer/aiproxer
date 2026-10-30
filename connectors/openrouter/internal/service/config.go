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

const DefaultHTTPTimeout = 60 * time.Second

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// OpenRouter credentials: OPENROUTER_API_KEY[_N]. secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
const EnvAPIKeyRoot = "OPENROUTER_API_KEY"

type Config struct {
	BaseURL        string `yaml:"base_url"`
	HTTPTimeout    string `yaml:"http_timeout"`
	AppURLMode     string `yaml:"app_url_mode"`
	AppURLValue    string `yaml:"app_url_value"`
	AppTitleMode   string `yaml:"app_title_mode"`
	AppTitleValue  string `yaml:"app_title_value"`
	StaticReferer  string `yaml:"static_referer"`
	StaticTitle    string `yaml:"static_title"`
	LegacyAppURL   bool   `yaml:"legacy_app_url"`
	LegacyAppTitle bool   `yaml:"legacy_app_title"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("openrouter", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("openrouter: config: %w", err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	cfg.StaticReferer = strings.TrimSpace(cfg.StaticReferer)
	cfg.StaticTitle = strings.TrimSpace(cfg.StaticTitle)
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("openrouter: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
