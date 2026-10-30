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

// EnvAPITokenRoot is the canonical nonsecret host environment namespace for
// Cloudflare credentials: CLOUDFLARE_API_TOKEN[_N]. Secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
const EnvAPITokenRoot = "CLOUDFLARE_API_TOKEN"

// QuotaDomain is the explicit nonsecret quota scope for Cloudflare
// credential selection: rotation decisions never alias an unrelated
// provider or project cooldown to this domain.
const QuotaDomain = "cloudflare"

type Config struct {
	AccountID   string `yaml:"account_id"`
	GatewayID   string `yaml:"gateway_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("cloudflare", EnvAPITokenRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("cloudflare: config: %w", err)
		}
	}

	cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	cfg.GatewayID = strings.TrimSpace(cfg.GatewayID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("cloudflare: /compat endpoint is forbidden for ordinary calls")
	}

	return cfg, nil
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		origin = DefaultOrigin
	}
	return fmt.Sprintf("%s/client/v4/accounts/%s/ai/v1", origin, strings.TrimSpace(c.AccountID))
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
