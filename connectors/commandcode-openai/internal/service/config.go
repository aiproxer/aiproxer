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

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// this export: COMMANDCODE_OPENAI_API_KEY[_N]. The sibling
// commandcode-anthropic export uses COMMANDCODE_ANTHROPIC_API_KEY; the two
// exports never share a root. Secrets never appear in connector YAML; the
// host delivers ordered slots via Configure secrets.
const EnvAPIKeyRoot = "COMMANDCODE_OPENAI_API_KEY"

type Config struct {
	BaseURL    string        `yaml:"base_url"`
	Timeout    time.Duration `yaml:"timeout"`
	HTTPClient func() (*http.Client, error)
}

func ParseConfigYAML(b []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("commandcode-openai", EnvAPIKeyRoot, b); err != nil {
		return Config{}, err
	}
	var raw struct {
		BaseURL string `yaml:"base_url"`
		Timeout string `yaml:"timeout"`
	}
	if len(bytes.TrimSpace(b)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&raw); err != nil {
			return Config{}, fmt.Errorf("commandcode-openai config: %w", err)
		}
	}
	base := strings.TrimSpace(raw.BaseURL)
	if base == "" {
		base = DefaultURL
	}
	var to time.Duration
	if s := strings.TrimSpace(raw.Timeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Config{}, fmt.Errorf("commandcode-openai timeout %q: %w", s, err)
		}
		to = d
	}
	cfg := Config{
		BaseURL: strings.TrimRight(base, "/"),
		Timeout: to,
	}
	cfg.HTTPClient = func() (*http.Client, error) {
		return &http.Client{Timeout: cfg.Timeout}, nil
	}
	return cfg, nil
}
