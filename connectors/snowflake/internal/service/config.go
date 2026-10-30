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
// Snowflake Cortex credentials: SNOWFLAKE_CORTEX_PAT[_N]. Secrets never
// appear in connector YAML; the host delivers ordered slots via Configure
// secrets.
const EnvAPITokenRoot = "SNOWFLAKE_CORTEX_PAT"

// QuotaDomain is the explicit nonsecret quota scope for Snowflake Cortex
// credential selection: rotation decisions never alias an unrelated
// provider or project cooldown to this domain.
const QuotaDomain = "snowflake-cortex"

type Config struct {
	Account        string `yaml:"account"`
	Role           string `yaml:"role"`
	APIOrigin      string `yaml:"api_origin"`
	CredentialMode string `yaml:"credential_mode"`
	HTTPTimeout    string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("snowflake-cortex", EnvAPITokenRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("snowflake-cortex: config: %w", err)
		}
	}

	cfg.Account = normalizeAccount(cfg.Account)
	cfg.Role = strings.TrimSpace(cfg.Role)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.CredentialMode = strings.TrimSpace(cfg.CredentialMode)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.Account), "/compat") ||
		strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("snowflake-cortex: /compat endpoint is forbidden")
	}

	return cfg, nil
}

func normalizeAccount(account string) string {
	acc := strings.TrimSpace(account)
	acc = strings.TrimPrefix(acc, "https://")
	acc = strings.TrimPrefix(acc, "http://")
	acc = strings.TrimRight(acc, "/")
	acc = strings.TrimSuffix(acc, ".snowflakecomputing.com")
	return acc
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		if c.Account != "" {
			origin = fmt.Sprintf("https://%s.snowflakecomputing.com", c.Account)
		}
	}
	if origin == "" {
		return ""
	}
	return origin + "/api/v2/cortex/v1"
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("snowflake-cortex: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
