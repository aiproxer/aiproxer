package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultHTTPTimeout = 60 * time.Second

// dummyBearer is the constant nonsecret bearer the no-auth local transport
// presents. Local servers that require a non-empty Authorization header keep
// working; no static key is ever admitted.
const dummyBearer = "lmstudio"

type Config struct {
	BaseURL string `yaml:"base_url"`
	// APIKey is runtime-only (always the constant dummy). It is never
	// decoded from YAML: the yaml tag is intentionally absent so literal
	// api_key fields fail closed.
	APIKey      string `yaml:"-"`
	HTTPTimeout string `yaml:"http_timeout"`
}

// forbiddenSecretFields are top-level YAML configuration keys that must
// never appear in a no-auth local export. Comparison is case-insensitive
// with -/_ unified; values are never echoed. Static exports use the shared
// openaicompat.RejectSecretFields policy; this local variant carries the
// accurate no-auth guidance (there is no approved environment root here).
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "client_secret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"authorization": {},
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := rejectSecretFields(raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: config: %w", "lmstudio", err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	cfg.APIKey = dummyBearer
	return cfg, nil
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field. The error names only the field path, never
// the rejected value.
func rejectSecretFields(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%s: config: invalid YAML", "lmstudio")
	}
	for name := range fields {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("%s: config field %q is forbidden; this export is no-auth local_only, remove the key field", "lmstudio", name)
		}
	}
	return nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("%s: http_timeout: %w", "lmstudio", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
