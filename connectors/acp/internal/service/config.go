package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	FactoryKind        = "acp"
	DefaultHTTPTimeout = 60 * time.Second
)

type Config struct {
	BaseURL string `yaml:"base_url"`
	// HTTPTimeout is optional; when set, Configure builds a caller-owned client.
	HTTPTimeout string `yaml:"http_timeout"`
	// UsageInputExcludesCache declares that the upstream ACP agent reports its
	// input counter outside the cached and the rewritten prompt subsets (the
	// Anthropic-family exclusive shape).
	//
	// The relation belongs to the agent, not to ACP: the ACP usage object
	// carries the counters but does not define whether the input counter
	// contains the cached subsets, and shipping agents disagree
	// (agent-client-protocol#1860). The default is therefore the inclusive
	// interpretation, which is what an agent that reports an input counter
	// containing its cached tokens needs. An operator whose agent reports the
	// exclusive shape opts in here.
	UsageInputExcludesCache bool `yaml:"usage_input_excludes_cache"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) == 0 {
		return Config{}, fmt.Errorf("acp: base_url is required")
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("acp: config yaml: %w", err)
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("acp: base_url is required")
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	to := DefaultHTTPTimeout
	if s := strings.TrimSpace(c.HTTPTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("acp: http_timeout: %w", err)
		}
		to = d
	}
	return &http.Client{Timeout: to}, nil
}
