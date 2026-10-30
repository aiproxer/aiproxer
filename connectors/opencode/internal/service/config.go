package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog"
	"gopkg.in/yaml.v3"
)

const DefaultHTTPTimeout = 60 * time.Second

// EnvAPIKeyRootGo and EnvAPIKeyRootZen are the canonical nonsecret host
// environment namespaces for the two OpenCode exports. The scopes stay
// distinct: opencode-go admits only OPENCODE_GO_API_KEY[_N], opencode-zen
// only OPENCODE_ZEN_API_KEY[_N]. Secrets never appear in connector YAML,
// auth.json or connector-side environment; the host delivers ordered slots
// via Configure secrets.
const (
	EnvAPIKeyRootGo  = "OPENCODE_GO_API_KEY"
	EnvAPIKeyRootZen = "OPENCODE_ZEN_API_KEY"
)

type Config struct {
	BaseURL     string               `yaml:"base_url"`
	HTTPTimeout string               `yaml:"http_timeout"`
	Models      []catalog.ModelEntry `yaml:"-"`
}

type configYAML struct {
	BaseURL     string `yaml:"base_url"`
	HTTPTimeout string `yaml:"http_timeout"`
	Models      []struct {
		ID           string `yaml:"id"`
		DisplayName  string `yaml:"display_name"`
		Endpoint     string `yaml:"endpoint"`
		AISDKPackage string `yaml:"ai_sdk_package"`
	} `yaml:"models"`
}

// envRootForKind returns the approved host environment namespace for kind.
func envRootForKind(kind string) string {
	if kind == FactoryKindZen {
		return EnvAPIKeyRootZen
	}
	return EnvAPIKeyRootGo
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("opencode", envRootForKind(kind), raw); err != nil {
		return Config{}, err
	}
	var y configYAML
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &y); err != nil {
			return Config{}, fmt.Errorf("opencode: config: %w", err)
		}
	}
	cfg := Config{
		BaseURL:     strings.TrimSpace(y.BaseURL),
		HTTPTimeout: strings.TrimSpace(y.HTTPTimeout),
	}
	if cfg.BaseURL == "" {
		switch kind {
		case FactoryKindGo:
			cfg.BaseURL = DefaultGoURL
		case FactoryKindZen:
			cfg.BaseURL = DefaultZenURL
		default:
			return Config{}, fmt.Errorf("opencode: unknown factory kind %q", kind)
		}
	}
	for _, row := range y.Models {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		cfg.Models = append(cfg.Models, catalog.ModelEntry{
			RawID:        id,
			DisplayName:  strings.TrimSpace(row.DisplayName),
			Endpoint:     strings.TrimSpace(row.Endpoint),
			AISDKPackage: strings.TrimSpace(row.AISDKPackage),
		})
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("opencode: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
