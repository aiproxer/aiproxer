package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/product"
	"gopkg.in/yaml.v3"
)

const FactoryKind = "geminicliacp"

type Config struct {
	Executable       string   `yaml:"executable"`
	Model            string   `yaml:"model"`
	ExtraArgs        []string `yaml:"extra_args"`
	DefaultWorkspace string   `yaml:"default_workspace"`
	IdleTimeoutS     float64  `yaml:"idle_timeout_seconds"`
	StaleKillDelayS  float64  `yaml:"stale_kill_delay_seconds"`
	AutoAccept       bool     `yaml:"auto_accept"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("geminicliacp: config yaml: %w", err)
		}
	}
	return cfg, nil
}

func (c Config) toProduct() product.Config {
	pc := product.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable: strings.TrimSpace(c.Executable), Model: strings.TrimSpace(c.Model),
			ExtraArgs: c.ExtraArgs, DefaultWorkspace: strings.TrimSpace(c.DefaultWorkspace),
		},
		AutoAccept: c.AutoAccept,
	}
	if c.IdleTimeoutS > 0 {
		pc.IdleTimeout = time.Duration(c.IdleTimeoutS * float64(time.Second))
	}
	if c.StaleKillDelayS > 0 {
		pc.StaleKillDelay = time.Duration(c.StaleKillDelayS * float64(time.Second))
	}
	return pc
}
