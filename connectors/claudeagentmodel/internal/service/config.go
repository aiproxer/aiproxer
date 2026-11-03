package service

import (
	"bytes"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FactoryKind is the closed optional backend factory for this connector.
const FactoryKind = "claudeagentmodel"

// ModelNamespace keeps provider identity independent of the configured instance.
const ModelNamespace = "anthropic/"

const (
	// DefaultMaxRuntimes bounds live sidecars for one configured instance.
	DefaultMaxRuntimes = 8
	// MaxRuntimesLimit is the largest accepted live-sidecar bound.
	MaxRuntimesLimit = 64
	// DefaultTurnWaitSeconds bounds waiting for a conversation to release.
	DefaultTurnWaitSeconds = 30
	// DefaultPendingTTLSeconds bounds a parked tool result wait (S2).
	DefaultPendingTTLSeconds = 1800
	// DefaultIdleTimeoutSeconds bounds an idle completed conversation (S2).
	DefaultIdleTimeoutSeconds = 300
)

// Config is the strict, non-secret operator configuration. The sidecar script,
// SDK options, Node flags and MCP endpoints are connector-owned and never
// configurable, so a client or operator cannot turn this into a command runner.
type Config struct {
	Executable         string  `yaml:"executable"`
	CredentialSource   string  `yaml:"credential_source"`
	AdminPolicy        string  `yaml:"admin_policy"`
	DefaultWorkspace   string  `yaml:"default_workspace"`
	MaxRuntimes        int     `yaml:"max_runtimes"`
	TurnWaitSeconds    float64 `yaml:"turn_wait_seconds"`
	PendingTTLSeconds  float64 `yaml:"pending_ttl_seconds"`
	IdleTimeoutSeconds float64 `yaml:"idle_timeout_seconds"`
	StrictSemantics    bool    `yaml:"strict_semantics"`
}

// ParseConfigYAML parses the operator configuration with defaults. Values are
// bounded before any process, filesystem or network side effect. The enablement
// gate (admin_policy) is evaluated by the engine so diagnostics can distinguish
// a malformed configuration from an intentionally inert default.
func ParseConfigYAML(raw []byte) (Config, error) {
	cfg := Config{
		CredentialSource:   "host_login",
		AdminPolicy:        "refuse",
		MaxRuntimes:        DefaultMaxRuntimes,
		TurnWaitSeconds:    DefaultTurnWaitSeconds,
		PendingTTLSeconds:  DefaultPendingTTLSeconds,
		IdleTimeoutSeconds: DefaultIdleTimeoutSeconds,
	}
	invalid := errors.New("claudeagentmodel: invalid configuration; use typed non-secret fields")
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
	if cfg.CredentialSource == "" {
		cfg.CredentialSource = "host_login"
	}
	if cfg.AdminPolicy == "" {
		cfg.AdminPolicy = "refuse"
	}
	if cfg.MaxRuntimes == 0 {
		cfg.MaxRuntimes = DefaultMaxRuntimes
	}
	if err := boundedSeconds("turn_wait_seconds", &cfg.TurnWaitSeconds, DefaultTurnWaitSeconds, 3600); err != nil {
		return Config{}, err
	}
	if err := boundedSeconds("pending_ttl_seconds", &cfg.PendingTTLSeconds, DefaultPendingTTLSeconds, 86400); err != nil {
		return Config{}, err
	}
	if err := boundedSeconds("idle_timeout_seconds", &cfg.IdleTimeoutSeconds, DefaultIdleTimeoutSeconds, 86400); err != nil {
		return Config{}, err
	}
	if cfg.Executable == "" || !filepath.IsAbs(cfg.Executable) || strings.ContainsRune(cfg.Executable, 0) {
		return Config{}, errors.New("claudeagentmodel: executable must be an absolute path to the Claude Code CLI")
	}
	if cfg.DefaultWorkspace != "" && (!filepath.IsAbs(cfg.DefaultWorkspace) || strings.ContainsRune(cfg.DefaultWorkspace, 0)) {
		return Config{}, errors.New("claudeagentmodel: default_workspace must be absolute")
	}
	if cfg.CredentialSource != "host_login" && cfg.CredentialSource != "api_key" {
		return Config{}, errors.New("claudeagentmodel: credential_source must be host_login or api_key")
	}
	if cfg.AdminPolicy != "refuse" && cfg.AdminPolicy != "honor" {
		return Config{}, errors.New("claudeagentmodel: admin_policy must be refuse or honor")
	}
	if cfg.MaxRuntimes < 1 || cfg.MaxRuntimes > MaxRuntimesLimit {
		return Config{}, errors.New("claudeagentmodel: max_runtimes must be between 1 and 64")
	}
	return cfg, nil
}

// durations resolves the bounded conversation timers. The pending TTL and the
// post-completion idle timeout are distinct: one bounds a committed tool call,
// the other bounds a released continuation that never terminates.
func (c Config) durations() (turnWait, pendingTTL, idleTimeout time.Duration) {
	return time.Duration(c.TurnWaitSeconds * float64(time.Second)),
		time.Duration(c.PendingTTLSeconds * float64(time.Second)),
		time.Duration(c.IdleTimeoutSeconds * float64(time.Second))
}

func boundedSeconds(name string, value *float64, fallback, max float64) error {
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > max {
		return errors.New("claudeagentmodel: " + name + " is out of range")
	}
	if *value == 0 {
		*value = fallback
	}
	return nil
}
