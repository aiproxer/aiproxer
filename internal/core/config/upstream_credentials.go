package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Upstream credential policy defaults and bounds (requirements 1.1, 1.5).
//
// max_env_index is the inclusive scan bound N: the bare root fills slot 1 and
// suffixed names root_2..root_N are scanned sparsely without stopping at
// gaps. quota_switch_retry_after is the proxy-wide threshold: only a
// trustworthy Retry-After strictly greater than this duration marks a
// credential quota-cooling (consumed by the credential coordinator; this
// package only validates and exposes the nonsecret policy value).
const (
	// DefaultUpstreamMaxEnvIndex is the proxy-wide default numbered scan bound.
	DefaultUpstreamMaxEnvIndex = 20
	// MinUpstreamMaxEnvIndex and MaxUpstreamMaxEnvIndex bound max_env_index.
	MinUpstreamMaxEnvIndex = 1
	MaxUpstreamMaxEnvIndex = 256
	// DefaultUpstreamQuotaSwitchRetryAfter is the default quota-switch threshold.
	DefaultUpstreamQuotaSwitchRetryAfter = "1h"
)

// UpstreamCredentialsConfig is the nonsecret proxy-wide static-credential
// discovery policy. It carries bounds and durations only, never secret values.
type UpstreamCredentialsConfig struct {
	// MaxEnvIndex is the inclusive numbered scan bound N (bare slot 1 plus
	// suffixes _2.._N). Zero selects the default; otherwise 1..256.
	MaxEnvIndex int `yaml:"max_env_index"`
	// QuotaSwitchRetryAfter is a Go duration string (e.g. "1h"). Empty selects
	// the default; otherwise it must be whole milliseconds (at least 1ms),
	// matching the host-to-connector policy wire precision.
	QuotaSwitchRetryAfter string `yaml:"quota_switch_retry_after"`
}

// EffectiveMaxEnvIndex returns the validated scan bound: the default for an
// omitted (zero) value, otherwise the configured value within 1..256. Errors
// name the field only, never credential material.
func (c UpstreamCredentialsConfig) EffectiveMaxEnvIndex() (int, error) {
	if c.MaxEnvIndex == 0 {
		return DefaultUpstreamMaxEnvIndex, nil
	}
	if c.MaxEnvIndex < MinUpstreamMaxEnvIndex || c.MaxEnvIndex > MaxUpstreamMaxEnvIndex {
		return 0, fmt.Errorf("upstream_credentials.max_env_index: must be between %d and %d, got %d",
			MinUpstreamMaxEnvIndex, MaxUpstreamMaxEnvIndex, c.MaxEnvIndex)
	}
	return c.MaxEnvIndex, nil
}

// EffectiveQuotaSwitchRetryAfter returns the validated quota-switch
// threshold: the default for an omitted (empty) value, otherwise the parsed
// duration. The threshold must be a whole number of milliseconds (at least
// 1ms): the host-to-connector policy wire carries whole milliseconds, so a
// fractional value would silently truncate and diverge from the built-in
// threshold. Errors name the field only, never credential material.
func (c UpstreamCredentialsConfig) EffectiveQuotaSwitchRetryAfter() (time.Duration, error) {
	raw := strings.TrimSpace(c.QuotaSwitchRetryAfter)
	if raw == "" {
		raw = DefaultUpstreamQuotaSwitchRetryAfter
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("upstream_credentials.quota_switch_retry_after: invalid duration %q", c.QuotaSwitchRetryAfter)
	}
	if d < time.Millisecond || d%time.Millisecond != 0 {
		return 0, fmt.Errorf("upstream_credentials.quota_switch_retry_after: must use whole milliseconds (at least 1ms)")
	}
	return d, nil
}

var (
	credentialEnvRootName   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	credentialEnvRootSuffix = regexp.MustCompile(`_[0-9]+$`)
)

// ValidateCredentialEnvRoot reports whether root is a well-formed bare
// credential namespace: uppercase, no numbered suffix (slot suffixes are
// derived by the resolver, never configured). Errors name the rejected root
// only; roots never carry secret values by construction.
func ValidateCredentialEnvRoot(root string) error {
	if !credentialEnvRootName.MatchString(root) || credentialEnvRootSuffix.MatchString(root) {
		return fmt.Errorf("invalid credential environment root %q: want an uppercase bare name such as OPENAI_API_KEY (no _N suffix)", root)
	}
	return nil
}

// validateUpstreamCredentials enforces the nonsecret discovery policy before
// any environment lookup, process launch or publication (requirement 1.5).
func validateUpstreamCredentials(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if _, err := cfg.UpstreamCredentials.EffectiveMaxEnvIndex(); err != nil {
		return err
	}
	_, err := cfg.UpstreamCredentials.EffectiveQuotaSwitchRetryAfter()
	return err
}
