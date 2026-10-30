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

const (
	DefaultHTTPTimeout = 60 * time.Second
)

// EnvAPITokenRoot is the canonical nonsecret host environment namespace for
// Databricks credentials: DATABRICKS_API_TOKEN[_N]. Secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
const EnvAPITokenRoot = "DATABRICKS_API_TOKEN"

// QuotaDomain is the explicit nonsecret quota scope for Databricks
// credential selection: rotation decisions never alias an unrelated
// provider or project cooldown to this domain.
const QuotaDomain = "databricks-ai"

type Config struct {
	Host            string `yaml:"host"`
	ServingEndpoint string `yaml:"serving_endpoint"`
	APIOrigin       string `yaml:"api_origin"`
	CredentialMode  string `yaml:"credential_mode"`
	HTTPTimeout     string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("databricks-ai", EnvAPITokenRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("databricks-ai: config: %w", err)
		}
	}

	cfg.Host = normalizeHost(cfg.Host)
	cfg.ServingEndpoint = strings.TrimSpace(cfg.ServingEndpoint)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.CredentialMode = strings.TrimSpace(cfg.CredentialMode)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.Host), "/compat") ||
		strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("databricks-ai: /compat endpoint is forbidden")
	}

	return cfg, nil
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimRight(h, "/")
	return h
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		if c.Host != "" {
			origin = "https://" + c.Host
		}
	}
	if origin == "" {
		return ""
	}
	return origin + "/ai-gateway/mlflow/v1"
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("databricks-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
