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
	Model             string `yaml:"model"`
	InferenceContract string `yaml:"inference_contract"`
	Version           string `yaml:"version"`
	APIOrigin         string `yaml:"api_origin"`
	HTTPTimeout       string `yaml:"http_timeout"`
	PollIntervalRaw   string `yaml:"poll_interval"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("replicate", EnvAPITokenRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("replicate: config: %w", err)
		}
	}

	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.InferenceContract = strings.TrimSpace(cfg.InferenceContract)
	cfg.Version = strings.TrimSpace(cfg.Version)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)
	cfg.PollIntervalRaw = strings.TrimSpace(cfg.PollIntervalRaw)

	if cfg.Model == "" {
		return Config{}, fmt.Errorf("replicate: model is required (owner/name format, e.g. meta/llama-3-70b-instruct)")
	}
	if strings.Contains(cfg.Model, "://") || strings.HasPrefix(cfg.Model, "/") || strings.HasSuffix(cfg.Model, "/") || strings.Count(cfg.Model, "/") != 1 {
		return Config{}, fmt.Errorf("replicate: invalid model %q: must be in owner/name format (e.g. meta/llama-3-70b-instruct); full prediction URLs are rejected", cfg.Model)
	}

	if cfg.InferenceContract == "" {
		return Config{}, fmt.Errorf("replicate: inference_contract is required (must be \"prompt-text\")")
	}
	if cfg.InferenceContract != "prompt-text" {
		return Config{}, fmt.Errorf("replicate: unsupported inference_contract %q; v1 requires \"prompt-text\"", cfg.InferenceContract)
	}

	return cfg, nil
}

func (c Config) ModelOwnerAndName() (string, string) {
	parts := strings.Split(c.Model, "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func (c Config) Origin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return DefaultOrigin
}

func (c Config) PollInterval() time.Duration {
	if c.PollIntervalRaw != "" {
		if d, err := time.ParseDuration(c.PollIntervalRaw); err == nil && d > 0 {
			return d
		}
	}
	return DefaultPollInterval
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("replicate: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
