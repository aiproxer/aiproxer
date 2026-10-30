package reasoningpreservation

import (
	"fmt"
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type CompressionMode string

const (
	CompressionShadow CompressionMode = "shadow"
	CompressionActive CompressionMode = "active"
)

const (
	HardCompressionTimeout                = 30 * time.Second
	HardCompressionMaxInputTokens         = 200000
	HardCompressionMaxInputBytes          = 4 * 1024 * 1024
	HardCompressionMaxOutputTokens        = 32000
	HardCompressionMaxOutputBytes         = HardRawOutputCeiling
	HardCompressionMaxSurrogateBytes      = 256 * 1024
	HardCompressionMaxSurrogateOverhead   = 1024
	HardCompressionMaxPendingPerSession   = 64
	HardCompressionMaxSurrogatePerSession = 4 * 1024 * 1024
	HardCompressionMaxPendingTotal        = 4096
	HardCompressionMaxSurrogateTotal      = 64 * 1024 * 1024
	HardCompressionMinSourceBytesCeiling  = 4 * 1024 * 1024
	HardCompressionMinSavedBytesCeiling   = 4 * 1024 * 1024
	CompressionSurrogateOverhead          = 1024
)

type CompressionConfig struct {
	Enabled                     bool            `yaml:"enabled"`
	Mode                        CompressionMode `yaml:"mode"`
	Route                       string          `yaml:"route"`
	Timeout                     time.Duration   `yaml:"timeout"`
	MaxInputTokens              int             `yaml:"max_input_tokens"`
	MaxInputBytes               int             `yaml:"max_input_bytes"`
	MaxOutputTokens             int             `yaml:"max_output_tokens"`
	MaxOutputBytes              int             `yaml:"max_output_bytes"`
	MaxSurrogateBytes           int             `yaml:"max_surrogate_bytes"`
	MinSourceBytes              int             `yaml:"min_source_bytes"`
	MinSavedBytes               int             `yaml:"min_saved_bytes"`
	MinSavingsRatio             float64         `yaml:"min_savings_ratio"`
	MaxPendingPerSession        int             `yaml:"max_pending_per_session"`
	MaxSurrogateBytesPerSession int             `yaml:"max_surrogate_bytes_per_session"`
	MaxPendingTotal             int             `yaml:"max_pending_total"`
	MaxSurrogateBytesTotal      int             `yaml:"max_surrogate_bytes_total"`
	EgressPolicyRef             string          `yaml:"egress_policy_ref"`
}

func (c CompressionConfig) ToLimits() CompressionLimits {
	if !c.Enabled {
		return CompressionLimits{}
	}
	return CompressionLimits{
		MaxPendingPerSession:        c.MaxPendingPerSession,
		MaxPendingTotal:             c.MaxPendingTotal,
		MaxSurrogateBytesPerTurn:    c.MaxSurrogateBytes,
		MaxSurrogateBytesPerSession: c.MaxSurrogateBytesPerSession,
		MaxSurrogateBytesTotal:      c.MaxSurrogateBytesTotal,
	}
}

func (c CompressionConfig) Validate() error {
	_, err := validateCompressionConfig(c)
	return err
}

func validateCompressionConfig(in CompressionConfig) (CompressionConfig, error) {
	if !in.Enabled {
		return CompressionConfig{Enabled: false}, nil
	}
	c := in
	c.Route = strings.TrimSpace(c.Route)
	c.EgressPolicyRef = strings.TrimSpace(c.EgressPolicyRef)
	if c.Route == "" {
		return CompressionConfig{}, fmt.Errorf("%s: compression.route is required", ID)
	}
	if c.EgressPolicyRef == "" {
		return CompressionConfig{}, fmt.Errorf("%s: compression.egress_policy_ref is required", ID)
	}
	if c.Mode == "" {
		c.Mode = CompressionShadow
	}
	switch c.Mode {
	case CompressionShadow, CompressionActive:
	default:
		return CompressionConfig{}, fmt.Errorf("%s: compression.mode must be %q or %q", ID, CompressionShadow, CompressionActive)
	}
	if c.Timeout <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.timeout must be > 0", ID)
	}
	if c.Timeout > HardCompressionTimeout {
		return CompressionConfig{}, fmt.Errorf("%s: compression.timeout must be <= %s", ID, HardCompressionTimeout)
	}
	if c.MaxInputTokens <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_input_tokens must be > 0", ID)
	}
	if c.MaxInputTokens > HardCompressionMaxInputTokens {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_input_tokens must be <= %d", ID, HardCompressionMaxInputTokens)
	}
	if c.MaxInputBytes <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_input_bytes must be > 0", ID)
	}
	if c.MaxInputBytes > HardCompressionMaxInputBytes {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_input_bytes must be <= %d", ID, HardCompressionMaxInputBytes)
	}
	if c.MaxOutputTokens <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_output_tokens must be > 0", ID)
	}
	if c.MaxOutputTokens > HardCompressionMaxOutputTokens {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_output_tokens must be <= %d", ID, HardCompressionMaxOutputTokens)
	}
	if c.MaxOutputBytes <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_output_bytes must be > 0", ID)
	}
	if c.MaxOutputBytes > HardCompressionMaxOutputBytes {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_output_bytes must be <= %d", ID, HardCompressionMaxOutputBytes)
	}
	if c.MaxSurrogateBytes <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes must be > 0", ID)
	}
	if c.MaxSurrogateBytes > HardCompressionMaxSurrogateBytes {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes must be <= %d", ID, HardCompressionMaxSurrogateBytes)
	}
	if c.MinSourceBytes <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_source_bytes must be > 0", ID)
	}
	if c.MinSourceBytes > HardCompressionMinSourceBytesCeiling {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_source_bytes must be <= %d", ID, HardCompressionMinSourceBytesCeiling)
	}
	if c.MinSavedBytes <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_saved_bytes must be > 0", ID)
	}
	if c.MinSavedBytes > HardCompressionMinSavedBytesCeiling {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_saved_bytes must be <= %d", ID, HardCompressionMinSavedBytesCeiling)
	}
	if math.IsNaN(c.MinSavingsRatio) || math.IsInf(c.MinSavingsRatio, 0) {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_savings_ratio must be in (0,1)", ID)
	}
	if c.MinSavingsRatio <= 0 || c.MinSavingsRatio >= 1 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_savings_ratio must be in (0,1)", ID)
	}
	if c.MaxPendingPerSession <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_pending_per_session must be > 0", ID)
	}
	if c.MaxPendingPerSession > HardCompressionMaxPendingPerSession {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_pending_per_session must be <= %d", ID, HardCompressionMaxPendingPerSession)
	}
	if c.MaxSurrogateBytesPerSession <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_per_session must be > 0", ID)
	}
	if c.MaxSurrogateBytesPerSession > HardCompressionMaxSurrogatePerSession {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_per_session must be <= %d", ID, HardCompressionMaxSurrogatePerSession)
	}
	if c.MaxPendingTotal <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_pending_total must be > 0", ID)
	}
	if c.MaxPendingTotal > HardCompressionMaxPendingTotal {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_pending_total must be <= %d", ID, HardCompressionMaxPendingTotal)
	}
	if c.MaxSurrogateBytesTotal <= 0 {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_total must be > 0", ID)
	}
	if c.MaxSurrogateBytesTotal > HardCompressionMaxSurrogateTotal {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_total must be <= %d", ID, HardCompressionMaxSurrogateTotal)
	}
	if c.MaxOutputBytes < c.MaxSurrogateBytes+CompressionSurrogateOverhead {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_output_bytes must be >= max_surrogate_bytes+%d", ID, CompressionSurrogateOverhead)
	}
	if c.MaxPendingTotal < c.MaxPendingPerSession {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_pending_total must be >= max_pending_per_session", ID)
	}
	if c.MaxSurrogateBytesTotal < c.MaxSurrogateBytesPerSession {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_total must be >= max_surrogate_bytes_per_session", ID)
	}
	if c.MaxSurrogateBytesTotal < c.MaxSurrogateBytes {
		return CompressionConfig{}, fmt.Errorf("%s: compression.max_surrogate_bytes_total must be >= max_surrogate_bytes", ID)
	}
	if c.MinSavedBytes > c.MinSourceBytes {
		return CompressionConfig{}, fmt.Errorf("%s: compression.min_saved_bytes must be <= min_source_bytes", ID)
	}
	return c, nil
}

func rejectUnknownCompressionKeys(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "" || node.Value == "null") {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: compression must be a mapping", ID)
	}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "enabled", "mode", "route", "timeout", "max_input_tokens", "max_input_bytes", "max_output_tokens", "max_output_bytes", "max_surrogate_bytes", "min_source_bytes", "min_saved_bytes", "min_savings_ratio", "max_pending_per_session", "max_surrogate_bytes_per_session", "max_pending_total", "max_surrogate_bytes_total", "egress_policy_ref":
		default:
			return fmt.Errorf("%s: unknown compression key %q", ID, node.Content[i].Value)
		}
	}
	return nil
}
