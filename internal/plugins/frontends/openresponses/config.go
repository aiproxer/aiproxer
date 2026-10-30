package openresponses

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"gopkg.in/yaml.v3"
)

const (
	ID                           = "openresponses"
	DefaultProfile               = "2026-04-24"
	DefaultBasePath              = "/openresponses/v1"
	DefaultPersistentStore       = "standard"
	DefaultTTL                   = "24h"
	DefaultMaxChainDepth         = 64
	DefaultMaxMaterializedBytes  = 67108864 // 64 MiB
	DefaultMaxConnectionAge      = "60m"
	DefaultIdleTimeout           = "5m"
	DefaultMaxQueuedTurns        = 1
	DefaultMaxQueuedBytes        = 8 * 1024 * 1024 // 8 MiB: one full-size turn envelope
	MaxAllowedWSConnectionAgeDur = 60 * time.Minute
	MaxAllowedChainDepth         = 1024
	MaxAllowedMaterializedBytes  = 256 << 20
	MaxAllowedQueuedTurns        = 1024
	MaxAllowedQueuedBytes        = 256 << 20 // 256 MiB ceiling for the per-session queued-byte bound
)

// Config represents the strict configuration for the OpenResponses frontend plugin.
type Config struct {
	Profile                  string             `yaml:"profile"`
	BasePath                 string             `yaml:"base_path"`
	Continuation             ContinuationConfig `yaml:"continuation"`
	WebSocket                WebSocketConfig    `yaml:"websocket"`
	ExposeAipUsageExtensions bool               `yaml:"expose_aip_usage_extensions"`
	continuationDepthSet     bool
	continuationBytesSet     bool
	queuedTurnsSet           bool
	queuedBytesSet           bool
}

// ContinuationConfig configures proxy-owned continuation behavior.
type ContinuationConfig struct {
	PersistentStore      string `yaml:"persistent_store"`
	TTL                  string `yaml:"ttl"`
	MaxChainDepth        int    `yaml:"max_chain_depth"`
	MaxMaterializedBytes int64  `yaml:"max_materialized_bytes"`
}

// WebSocketConfig configures client-facing WebSocket session behavior.
//
// Origin policy is strict by default: browser origins are accepted only when
// explicitly allowlisted. Relaxing it is development-only and requires both an
// explicit development_mode and an explicit allow_any_origin; the validator
// rejects allow_any_origin without development_mode, and the runtime policy
// never relaxes unless both are set, so a config can never be accidentally
// origin-open.
type WebSocketConfig struct {
	Enabled          bool     `yaml:"enabled"`
	MaxConnectionAge string   `yaml:"max_connection_age"`
	IdleTimeout      string   `yaml:"idle_timeout"`
	MaxQueuedTurns   int      `yaml:"max_queued_turns"`
	MaxQueuedBytes   int64    `yaml:"max_queued_bytes"`
	AllowedOrigins   []string `yaml:"allowed_origins"`
	DevelopmentMode  bool     `yaml:"development_mode"`
	AllowAnyOrigin   bool     `yaml:"allow_any_origin"`
}

// WSEnabled returns true if WebSocket transport is enabled (defaults to true).
func (w WebSocketConfig) IsEnabled() bool {
	return w.Enabled
}

// DecodeConfig decodes a YAML node into Config with strict unknown field checks and validation.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return defaultConfig(), nil
		}
		root = *root.Content[0]
	}

	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return defaultConfig(), nil
	}

	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}

	// Prepare container with defaults before decoding
	cfg := defaultConfig()

	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(&root); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	cfg.continuationDepthSet = mappingHasField(root, "continuation", "max_chain_depth")
	cfg.continuationBytesSet = mappingHasField(root, "continuation", "max_materialized_bytes")
	cfg.queuedTurnsSet = mappingHasField(root, "websocket", "max_queued_turns")
	cfg.queuedBytesSet = mappingHasField(root, "websocket", "max_queued_bytes")

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}

	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Profile:  DefaultProfile,
		BasePath: DefaultBasePath,
		Continuation: ContinuationConfig{
			PersistentStore:      DefaultPersistentStore,
			TTL:                  DefaultTTL,
			MaxChainDepth:        DefaultMaxChainDepth,
			MaxMaterializedBytes: DefaultMaxMaterializedBytes,
		},
		WebSocket: WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: DefaultMaxConnectionAge,
			IdleTimeout:      DefaultIdleTimeout,
			MaxQueuedTurns:   DefaultMaxQueuedTurns,
			MaxQueuedBytes:   DefaultMaxQueuedBytes,
			AllowedOrigins:   []string{},
		},
	}
}

// Validate checks all fields against strict profile and boundary rules.
func (c *Config) Validate() error {
	c.Profile = strings.TrimSpace(c.Profile)
	if c.Profile == "" {
		c.Profile = DefaultProfile
	}
	if c.Profile != DefaultProfile {
		return fmt.Errorf("invalid profile %q (expected %q)", c.Profile, DefaultProfile)
	}

	if c.BasePath == "" {
		c.BasePath = DefaultBasePath
	}
	normPath, err := httpcontract.NormalizePath(c.BasePath)
	if err != nil {
		return fmt.Errorf("invalid base_path: %w", err)
	}
	// base_path is operator input, so it must be a LITERAL route. httpcontract
	// normalizes a route path and therefore still accepts the ServeMux pattern
	// syntax the bundled frontends' own claims use, which an operator-supplied base
	// must not: a base carrying a wildcard or a query separator would publish a
	// pattern whose owned requests the ingress self-defense carve could not match,
	// shadowing the operator's own route.
	if err := httpcontract.ValidateOperatorRoutePath("base_path", c.BasePath); err != nil {
		return fmt.Errorf("invalid base_path: %w", err)
	}
	if normPath == "/" || strings.ContainsAny(normPath, "\r\n\t") {
		return fmt.Errorf("invalid base_path %q", c.BasePath)
	}
	c.BasePath = normPath

	// Continuation validation
	if c.Continuation.PersistentStore == "" {
		c.Continuation.PersistentStore = DefaultPersistentStore
	}
	if c.Continuation.PersistentStore != DefaultPersistentStore {
		return fmt.Errorf("unsupported continuation.persistent_store %q (supported: %q)", c.Continuation.PersistentStore, DefaultPersistentStore)
	}
	if c.Continuation.TTL == "" {
		c.Continuation.TTL = DefaultTTL
	}
	if c.Continuation.MaxChainDepth == 0 && !c.continuationDepthSet {
		c.Continuation.MaxChainDepth = DefaultMaxChainDepth
	}
	if c.Continuation.MaxMaterializedBytes == 0 && !c.continuationBytesSet {
		c.Continuation.MaxMaterializedBytes = DefaultMaxMaterializedBytes
	}
	ttlDur, err := time.ParseDuration(c.Continuation.TTL)
	if err != nil || ttlDur <= 0 {
		return fmt.Errorf("invalid continuation.ttl %q", c.Continuation.TTL)
	}
	if c.Continuation.MaxChainDepth <= 0 || c.Continuation.MaxChainDepth > MaxAllowedChainDepth {
		return fmt.Errorf("invalid continuation.max_chain_depth %d", c.Continuation.MaxChainDepth)
	}
	if c.Continuation.MaxMaterializedBytes <= 0 || c.Continuation.MaxMaterializedBytes > MaxAllowedMaterializedBytes {
		return fmt.Errorf("invalid continuation.max_materialized_bytes %d", c.Continuation.MaxMaterializedBytes)
	}

	// WebSocket validation
	if c.WebSocket.MaxConnectionAge == "" {
		c.WebSocket.MaxConnectionAge = DefaultMaxConnectionAge
	}
	maxAgeDur, err := time.ParseDuration(c.WebSocket.MaxConnectionAge)
	if err != nil || maxAgeDur <= 0 || maxAgeDur > MaxAllowedWSConnectionAgeDur {
		return fmt.Errorf("invalid websocket.max_connection_age %q (must be > 0 and <= 60m)", c.WebSocket.MaxConnectionAge)
	}

	if c.WebSocket.IdleTimeout == "" {
		c.WebSocket.IdleTimeout = DefaultIdleTimeout
	}
	if c.WebSocket.MaxQueuedTurns == 0 && !c.queuedTurnsSet {
		c.WebSocket.MaxQueuedTurns = DefaultMaxQueuedTurns
	}
	idleTimeoutDur, err := time.ParseDuration(c.WebSocket.IdleTimeout)
	if err != nil || idleTimeoutDur <= 0 {
		return fmt.Errorf("invalid websocket.idle_timeout %q", c.WebSocket.IdleTimeout)
	}

	if c.WebSocket.MaxQueuedTurns <= 0 || c.WebSocket.MaxQueuedTurns > MaxAllowedQueuedTurns {
		return fmt.Errorf("invalid websocket.max_queued_turns %d", c.WebSocket.MaxQueuedTurns)
	}

	// Per-session queued-byte bound, coupled to the message/queue limits: the
	// buffer must admit at least one full-size envelope (otherwise the read pump
	// could never place the message it already holds), and an oversized bound is
	// rejected so a session's worst-case buffered payload stays bounded. The
	// default keeps the safe single-turn behavior unchanged.
	if c.WebSocket.MaxQueuedBytes == 0 && !c.queuedBytesSet {
		c.WebSocket.MaxQueuedBytes = DefaultMaxQueuedBytes
	}
	if c.WebSocket.MaxQueuedBytes <= 0 {
		return fmt.Errorf("invalid websocket.max_queued_bytes %d (must be > 0)", c.WebSocket.MaxQueuedBytes)
	}
	if c.WebSocket.MaxQueuedBytes < wsDefaultMaxMessageBytes {
		return fmt.Errorf("invalid websocket.max_queued_bytes %d (must be >= the one-message bound %d)", c.WebSocket.MaxQueuedBytes, wsDefaultMaxMessageBytes)
	}
	if c.WebSocket.MaxQueuedBytes > MaxAllowedQueuedBytes {
		return fmt.Errorf("invalid websocket.max_queued_bytes %d (must be <= %d)", c.WebSocket.MaxQueuedBytes, MaxAllowedQueuedBytes)
	}

	for i, origin := range c.WebSocket.AllowedOrigins {
		normalized, err := normalizeOrigin(origin)
		if err != nil {
			return fmt.Errorf("invalid websocket.allowed_origins[%d]: %w", i, err)
		}
		c.WebSocket.AllowedOrigins[i] = normalized
	}

	// Development-only origin relaxation: never the default, never without an
	// explicit development mode. A relayed allow_any_origin is a config error.
	if c.WebSocket.AllowAnyOrigin && !c.WebSocket.DevelopmentMode {
		return fmt.Errorf("websocket.allow_any_origin requires websocket.development_mode (origin relaxation is development-only)")
	}

	return nil
}

func mappingHasField(root yaml.Node, section, field string) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != section || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		child := root.Content[i+1]
		for j := 0; j+1 < len(child.Content); j += 2 {
			if child.Content[j].Value == field {
				return true
			}
		}
	}
	return false
}

func normalizeOrigin(raw string) (string, error) {
	origin := strings.TrimSpace(raw)
	if origin == "" || strings.ContainsAny(origin, "\r\n\t") {
		return "", fmt.Errorf("origin must be a non-empty HTTP(S) origin")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("origin must include an HTTP(S) scheme and host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("origin scheme %q is not supported", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" {
		return "", fmt.Errorf("origin must not contain credentials, path, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("origin must not contain a path")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}
