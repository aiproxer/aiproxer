package service

import (
	"bytes"
	"errors"
	"io"
	"math"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
)

// The documented runtime limits. A turn waits for the previous turn of the
// same conversation to release its runtime, and the pool keeps a bounded
// number of live agent processes. Both are operator-configurable.
const (
	// DefaultTurnWaitSeconds bounds how long a turn waits for the previous
	// turn of the same conversation to release its runtime.
	DefaultTurnWaitSeconds = 30
	// DefaultMaxRuntimes bounds the live agent processes of one instance.
	DefaultMaxRuntimes = 8
	// MaxRuntimesLimit is the largest accepted runtime bound.
	MaxRuntimesLimit = 64
)

// Config deliberately has no credential or arbitrary SDK/argv fields.
type Config struct {
	Executable         string                `yaml:"executable"`
	DefaultWorkspace   string                `yaml:"default_workspace"`
	CredentialSource   string                `yaml:"credential_source"`
	AdminPolicy        string                `yaml:"admin_policy"`
	PermissionPolicy   string                `yaml:"permission_policy"`
	EnvAllowlist       []string              `yaml:"env_allowlist"`
	IdleTimeoutSeconds float64               `yaml:"idle_timeout_seconds"`
	TurnWaitSeconds    float64               `yaml:"turn_wait_seconds"`
	MaxRuntimes        int                   `yaml:"max_runtimes"`
	Gateway            product.GatewayConfig `yaml:"gateway"`
}

// ParseConfigYAML parses the operator configuration. The defaults are the
// documented ones: a turn waits for the previous turn of the same
// conversation to release its runtime, and the pool keeps a bounded number of
// live agent processes.
func ParseConfigYAML(raw []byte) (Config, error) {
	cfg := Config{CredentialSource: "api_key", AdminPolicy: "refuse", PermissionPolicy: "bypass", TurnWaitSeconds: DefaultTurnWaitSeconds, MaxRuntimes: DefaultMaxRuntimes}
	invalid := errors.New("claudeagentacp: invalid configuration; use typed non-secret fields and opaque Configure secrets")
	if len(raw) > 64<<10 {
		return Config{}, invalid
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		d := yaml.NewDecoder(bytes.NewReader(raw))
		d.KnownFields(true)
		if err := d.Decode(&cfg); err != nil {
			return Config{}, invalid
		}
		var extra any
		if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
			return Config{}, invalid
		}
	}
	if math.IsNaN(cfg.IdleTimeoutSeconds) || math.IsInf(cfg.IdleTimeoutSeconds, 0) || cfg.IdleTimeoutSeconds < 0 || cfg.IdleTimeoutSeconds >= float64(math.MaxInt64)/float64(time.Second) {
		return Config{}, invalid
	}
	if math.IsNaN(cfg.TurnWaitSeconds) || math.IsInf(cfg.TurnWaitSeconds, 0) || cfg.TurnWaitSeconds < 0 || cfg.TurnWaitSeconds >= float64(math.MaxInt64)/float64(time.Second) {
		return Config{}, invalid
	}
	if cfg.MaxRuntimes < 0 || cfg.MaxRuntimes > MaxRuntimesLimit {
		return Config{}, invalid
	}
	if cfg.CredentialSource == "" {
		cfg.CredentialSource = "api_key"
	}
	if cfg.AdminPolicy == "" {
		cfg.AdminPolicy = "refuse"
	}
	if cfg.PermissionPolicy == "" {
		cfg.PermissionPolicy = "bypass"
	}
	if cfg.PermissionPolicy != "bypass" && cfg.PermissionPolicy != "deny" {
		return Config{}, errors.New("claudeagentacp: permission_policy must be bypass or deny")
	}
	if cfg.AdminPolicy != "refuse" && cfg.AdminPolicy != "honor" {
		return Config{}, errors.New("claudeagentacp: admin_policy must be refuse or honor")
	}
	if cfg.DefaultWorkspace != "" && !filepath.IsAbs(cfg.DefaultWorkspace) {
		return Config{}, errors.New("claudeagentacp: default_workspace must be absolute")
	}
	if cfg.CredentialSource == "gateway" {
		if err := product.ValidateGatewayConfig(cfg.Gateway); err != nil {
			return Config{}, err
		}
	} else if !cfg.Gateway.Empty() {
		return Config{}, errors.New("claudeagentacp: gateway configuration requires credential_source gateway")
	}
	return cfg, nil
}
