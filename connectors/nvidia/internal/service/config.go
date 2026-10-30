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
// NVIDIA credentials: NVIDIA_API_KEY[_N]. Secrets never appear in connector
// YAML; the host delivers ordered slots via Configure secrets.
const EnvAPIKeyRoot = "NVIDIA_API_KEY"

type Config struct {
	BaseURL     string `yaml:"base_url"`
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("nvidia", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: config: %w", "nvidia", err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("%s: http_timeout: %w", "nvidia", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
