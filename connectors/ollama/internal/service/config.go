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
// Ollama Cloud credentials: OLLAMA_CLOUD_API_KEY[_N]. The local ollama
// export is a no-auth local_only runtime and takes no credentials. Secrets
// never appear in connector YAML; the host delivers ordered cloud slots via
// Configure secrets.
const EnvAPIKeyRoot = "OLLAMA_CLOUD_API_KEY"

type Config struct {
	BaseURL string `yaml:"base_url"`
	// APIKey is runtime-only (host Configure secrets for cloud, the constant
	// dummy for local). It is never decoded from YAML: the yaml tag is
	// intentionally absent so literal api_key fields fail closed.
	APIKey      string `yaml:"-"`
	HTTPTimeout string `yaml:"http_timeout"`
	ModelsURL   string `yaml:"models_url"`
	NativeRoot  string `yaml:"native_root"`
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("ollama", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: config: %w", kind, err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.ModelsURL = strings.TrimSpace(cfg.ModelsURL)
	cfg.NativeRoot = strings.TrimSpace(cfg.NativeRoot)
	switch kind {
	case FactoryKindCloud:
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultCloudURL
		}
		if cfg.ModelsURL == "" {
			cfg.ModelsURL = DefaultCloudTags
		}
	default:
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultLocalURL
		}
		if cfg.APIKey == "" {
			cfg.APIKey = DummyLocalKey
		}
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("ollama: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}

func nativeRootFromBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/v1")
	return base
}
