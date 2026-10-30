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

type Config struct {
	Model       string `yaml:"model"`
	ModelID     string `yaml:"model_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("cohere", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("cohere: config: %w", err)
		}
	}

	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) DefaultModel() string {
	if c.Model != "" {
		return c.Model
	}
	return c.ModelID
}

func (c Config) Origin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return DefaultOrigin
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("cohere: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
