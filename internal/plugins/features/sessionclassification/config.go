// Package sessionclassification owns the session-classification feature policy.
package sessionclassification

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const ID = "session-classification"

type Mode string

const (
	ModeHeuristic Mode = "heuristic"
	ModeJev       Mode = "jev"
	ModeHybrid    Mode = "hybrid"
)

const (
	// MaxIgnoredUserAgentPrefixes bounds the configured literal exclusion list.
	MaxIgnoredUserAgentPrefixes = 16
	// MaxIgnoredUserAgentPrefixBytes caps each normalized exclusion prefix.
	MaxIgnoredUserAgentPrefixBytes = 128
	// MaxWorkspaceMarkers bounds marker inspection for one evaluation.
	MaxWorkspaceMarkers = 32
	// MaxWorkspaceMarkerBytes caps each workspace marker inspected by policy.
	MaxWorkspaceMarkerBytes = 128
	// MaxRemoteAttemptsPerSession bounds configured remote attempts.
	MaxRemoteAttemptsPerSession = 5
	// MinRemoteTimeout is the smallest allowed remote timeout.
	MinRemoteTimeout = time.Millisecond
	// MaxRemoteTimeout caps a single remote decision timeout.
	MaxRemoteTimeout = 30 * time.Second
	// MaxRemoteLeaseTTL bounds the shared remote decision lease.
	MaxRemoteLeaseTTL = 2 * time.Minute
	// MaxRemoteRetryBackoff bounds the delay between remote attempts.
	MaxRemoteRetryBackoff = 30 * time.Second
	// RemoteLeaseSafetyMargin ensures lease expiry follows the hard timeout.
	RemoteLeaseSafetyMargin = 100 * time.Millisecond
)

type Config struct {
	Mode      Mode
	Heuristic HeuristicConfig
	Remote    *RemoteConfig
}

type HeuristicConfig struct {
	IgnoredUserAgentPrefixes []string
}

type RemoteConfig struct {
	Provider              string
	APIKeyEnv             string
	Timeout               time.Duration
	MaxAttemptsPerSession int
	LeaseTTL              time.Duration
	RetryBackoff          time.Duration
	PositiveThreshold     float64
}

// DecodeConfig strictly decodes the feature-owned config subtree. Registration
// enablement belongs to the outer plugins.features entry and is not duplicated
// here.
func DecodeConfig(input yaml.Node) (Config, error) {
	cfg := Config{Mode: ModeHeuristic}
	root, empty, err := configRoot(&input)
	if err != nil {
		return Config{}, err
	}
	if empty {
		return cfg, nil
	}
	fields, err := mappingFields(root, "config", "mode", "heuristic", "remote")
	if err != nil {
		return Config{}, err
	}
	if node, ok := fields["mode"]; ok {
		value, valid := scalarString(node, 32)
		if !valid {
			return Config{}, configError("mode must be a bounded string")
		}
		cfg.Mode = Mode(strings.TrimSpace(value))
		if cfg.Mode == "" {
			cfg.Mode = ModeHeuristic
		}
	}

	if node, ok := fields["heuristic"]; ok {
		cfg.Heuristic, err = decodeHeuristicConfig(node)
		if err != nil {
			return Config{}, err
		}
	}
	if node, ok := fields["remote"]; ok {
		remote, decodeErr := decodeRemoteConfig(node)
		if decodeErr != nil {
			return Config{}, decodeErr
		}
		cfg.Remote = &remote
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that a decoded or programmatically built policy is servable.
// The omitted mode is the documented V1 default (deterministic heuristic), and
// remote settings exist only for the modes that require a remote decision.
func (c Config) Validate() error {
	switch c.Mode {
	case "", ModeHeuristic:
		if c.Remote != nil {
			return configError("remote settings require jev or hybrid mode")
		}
	case ModeJev, ModeHybrid:
		if c.Remote == nil {
			return configError("jev and hybrid modes require explicit remote settings")
		}
		if err := c.Remote.validate(); err != nil {
			return err
		}
	default:
		return configError("mode must be heuristic, jev, or hybrid")
	}
	return c.Heuristic.validate()
}

// validate bounds the operator-supplied literal exclusion list so a policy
// built outside DecodeConfig cannot widen evaluation work.
func (h HeuristicConfig) validate() error {
	if len(h.IgnoredUserAgentPrefixes) > MaxIgnoredUserAgentPrefixes {
		return configError("ignored_user_agent_prefixes exceeds its entry limit")
	}
	for _, prefix := range h.IgnoredUserAgentPrefixes {
		if len(prefix) > MaxIgnoredUserAgentPrefixBytes || hasControlOrInvalidUTF8(prefix) {
			return configError("ignored_user_agent_prefixes contains an invalid bounded string")
		}
	}
	return nil
}

// validate enforces the finite operational bounds a remote decision needs. The
// credential stays a referenced environment name; no credential value is ever
// accepted or echoed here.
func (r RemoteConfig) validate() error {
	if r.Provider != "jev" {
		return configError("remote provider must be jev")
	}
	if !validEnvironmentName(r.APIKeyEnv) {
		return configError("api_key_env must name an environment variable")
	}
	if r.Timeout < MinRemoteTimeout || r.Timeout > MaxRemoteTimeout {
		return configError("remote timeout is outside its finite bounds")
	}
	if r.MaxAttemptsPerSession < 1 || r.MaxAttemptsPerSession > MaxRemoteAttemptsPerSession {
		return configError("max_attempts_per_session is outside its finite bounds")
	}
	if r.LeaseTTL > MaxRemoteLeaseTTL || r.LeaseTTL <= r.Timeout+RemoteLeaseSafetyMargin {
		return configError("lease_ttl must exceed timeout plus the safety margin and remain finite")
	}
	if r.RetryBackoff < 0 || r.RetryBackoff > MaxRemoteRetryBackoff {
		return configError("retry_backoff is outside its finite bounds")
	}
	if !validRemoteThreshold(r.PositiveThreshold) {
		return configError("positive_threshold must be greater than zero and at most one")
	}
	return nil
}

func configRoot(node *yaml.Node) (*yaml.Node, bool, error) {
	if node == nil || node.Kind == 0 {
		return nil, true, nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, true, nil
		}
		node = node.Content[0]
	}
	if node.Kind == 0 || isNullNode(node) {
		return nil, true, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, false, configError("config must be a mapping or null")
	}
	return node, false, nil
}

func decodeHeuristicConfig(node *yaml.Node) (HeuristicConfig, error) {
	if isNullNode(node) {
		return HeuristicConfig{}, nil
	}
	fields, err := mappingFields(node, "heuristic", "ignored_user_agent_prefixes")
	if err != nil {
		return HeuristicConfig{}, err
	}
	list, ok := fields["ignored_user_agent_prefixes"]
	if !ok {
		return HeuristicConfig{}, nil
	}
	if list == nil || list.Kind != yaml.SequenceNode || list.Tag != "!!seq" {
		return HeuristicConfig{}, configError("ignored_user_agent_prefixes must be a bounded sequence")
	}
	if len(list.Content) > MaxIgnoredUserAgentPrefixes {
		return HeuristicConfig{}, configError("ignored_user_agent_prefixes exceeds its entry limit")
	}

	prefixes := make([]string, 0, len(list.Content))
	seen := make(map[string]struct{}, len(list.Content))
	for _, item := range list.Content {
		value, valid := scalarString(item, MaxIgnoredUserAgentPrefixBytes)
		if !valid {
			return HeuristicConfig{}, configError("ignored_user_agent_prefixes contains an invalid bounded string")
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > MaxIgnoredUserAgentPrefixBytes || hasControlOrInvalidUTF8(value) {
			return HeuristicConfig{}, configError("ignored_user_agent_prefixes contains an invalid bounded string")
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		prefixes = append(prefixes, value)
	}
	return HeuristicConfig{IgnoredUserAgentPrefixes: prefixes}, nil
}

func decodeRemoteConfig(node *yaml.Node) (RemoteConfig, error) {
	fields, err := mappingFields(node, "remote", "provider", "api_key_env", "timeout", "max_attempts_per_session", "lease_ttl", "retry_backoff", "positive_threshold")
	if err != nil {
		return RemoteConfig{}, err
	}
	for _, key := range [...]string{"provider", "api_key_env", "timeout", "max_attempts_per_session", "lease_ttl", "retry_backoff", "positive_threshold"} {
		if _, ok := fields[key]; !ok {
			return RemoteConfig{}, configError("remote settings require every operational field explicitly")
		}
	}

	provider, ok := scalarString(fields["provider"], 32)
	if !ok {
		return RemoteConfig{}, configError("remote provider must be a bounded string")
	}
	apiKeyEnv, ok := scalarString(fields["api_key_env"], 128)
	if !ok {
		return RemoteConfig{}, configError("api_key_env must be a bounded environment name")
	}
	timeout, ok := durationString(fields["timeout"])
	if !ok {
		return RemoteConfig{}, configError("remote timeout must be a bounded duration")
	}
	attempts, ok := integerScalar(fields["max_attempts_per_session"])
	if !ok {
		return RemoteConfig{}, configError("max_attempts_per_session must be a bounded integer")
	}
	leaseTTL, ok := durationString(fields["lease_ttl"])
	if !ok {
		return RemoteConfig{}, configError("lease_ttl must be a bounded duration")
	}
	retryBackoff, ok := durationString(fields["retry_backoff"])
	if !ok {
		return RemoteConfig{}, configError("retry_backoff must be a bounded duration")
	}
	threshold, ok := numberScalar(fields["positive_threshold"])
	if !ok {
		return RemoteConfig{}, configError("positive_threshold must be a bounded number")
	}
	remote := RemoteConfig{
		Provider:              provider,
		APIKeyEnv:             apiKeyEnv,
		Timeout:               timeout,
		MaxAttemptsPerSession: attempts,
		LeaseTTL:              leaseTTL,
		RetryBackoff:          retryBackoff,
		PositiveThreshold:     threshold,
	}
	if err := remote.validate(); err != nil {
		return RemoteConfig{}, err
	}
	return remote, nil
}

func mappingFields(node *yaml.Node, section string, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content)%2 != 0 {
		return nil, configError(section + " must be a mapping")
	}
	if len(node.Content)/2 > len(allowed) {
		return nil, configError(section + " contains unknown or duplicate keys")
	}
	fields := make(map[string]*yaml.Node, len(allowed))
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" || len(keyNode.Value) > 64 {
			return nil, configError(section + " contains an invalid key")
		}
		key := keyNode.Value
		if !isAllowedKey(key, allowed) {
			return nil, configError(section + " contains an unknown key")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, configError(section + " contains a duplicate key")
		}
		fields[key] = node.Content[i+1]
	}
	return fields, nil
}

func isAllowedKey(key string, allowed []string) bool {
	return slices.Contains(allowed, key)
}

func scalarString(node *yaml.Node, maxBytes int) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || len(node.Value) > maxBytes || !utf8.ValidString(node.Value) {
		return "", false
	}
	return node.Value, true
}

func durationString(node *yaml.Node) (time.Duration, bool) {
	value, ok := scalarString(node, 32)
	if !ok {
		return 0, false
	}
	duration, err := time.ParseDuration(value)
	return duration, err == nil
}

func integerScalar(node *yaml.Node) (int, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" || len(node.Value) > 10 {
		return 0, false
	}
	value, err := strconv.ParseInt(node.Value, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

func numberScalar(node *yaml.Node) (float64, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!int" && node.Tag != "!!float") || len(node.Value) > 32 {
		return 0, false
	}
	value, err := strconv.ParseFloat(node.Value, 64)
	return value, err == nil
}

func isNullNode(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func hasControlOrInvalidUTF8(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > 128 || !isEnvironmentNameStart(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !isEnvironmentNameStart(c) && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func isEnvironmentNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func configError(reason string) error {
	return fmt.Errorf("%s: %s", ID, reason)
}
