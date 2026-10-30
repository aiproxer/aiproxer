package config

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// Config contains only core-owned runtime settings and opaque plugin config payloads.
//
// A decoded Config is not self-validating: [Validate] checks core fields (plugins, continuity, logging, etc.) but does
// not validate model_aliases. After [LoadFile], call routing.ValidateModelAliasesConfig(cfg) from package
// internal/core/routing before wiring; composition (for example internal/infra/runtimebundle.Build) compiles
// model_aliases via routing.NewAliasResolver. Default route selector resolution is [EffectiveDefaultRouteSelector]
// in this package (see effective_default_route.go).
type Config struct {
	Server         ServerConfig         `yaml:"server"`
	HTTPHeaders    HTTPHeadersConfig    `yaml:"http_headers"`
	Access         AccessConfig         `yaml:"access"`
	Auth           AuthConfig           `yaml:"auth"`
	Logging        LoggingConfig        `yaml:"logging"`
	Diagnostics    DiagnosticsConfig    `yaml:"diagnostics"`
	Observability  ObservabilityConfig  `yaml:"observability"`
	HTTPClient     HTTPClientConfig     `yaml:"http_client"`
	Database       DatabaseConfig       `yaml:"database"`
	Routing        RoutingConfig        `yaml:"routing"`
	Continuity     ContinuityConfig     `yaml:"continuity"`
	SecureSession  SecureSessionConfig  `yaml:"secure_session"`
	StreamRecovery StreamRecoveryConfig `yaml:"stream_recovery"`
	// EmptyVisibleTurnRetry is the opt-in gate for the empty-visible-turn
	// corrective retry; disabled by default.
	EmptyVisibleTurnRetry EmptyVisibleTurnRetryConfig `yaml:"empty_visible_turn_retry"`
	Hooks                 HooksConfig                 `yaml:"hooks"`
	Accounting            AccountingConfig            `yaml:"accounting"`
	Interleaved           InterleavedConfig           `yaml:"interleaved"`
	Plugins               PluginsConfig               `yaml:"plugins"`
	ModelAliases          []ModelAliasConfig          `yaml:"model_aliases"`
	ModelCatalog          ModelCatalogConfig          `yaml:"model_catalog"`
	ModelInventory        ModelInventoryConfig        `yaml:"model_inventory"`
	// ControlPlane is the optional control-plane persistence/query/event-ledger
	// capability. Disabled by default; enabled requires explicit startup
	// validation (see validateControlPlane).
	ControlPlane ControlPlaneConfig `yaml:"control_plane"`
	// Metering is the optional durable metering journal (Phase 5). Disabled by
	// default so Executor.MeteringRecorder stays nil until explicitly enabled.
	Metering MeteringConfig `yaml:"metering"`
	// Identity controls proxy-wide upstream and downstream identity presentation.
	// Defaults identify as AIProxer (not client passthrough). Backend connector wiring
	// and A-leg Server middleware are applied in later integration waves.
	Identity identity.Config `yaml:"identity"`
	// UpstreamCredentials is the nonsecret proxy-wide static-credential
	// discovery policy (numbered environment scan bound and quota-switch
	// threshold). See UpstreamCredentialsConfig.
	UpstreamCredentials UpstreamCredentialsConfig `yaml:"upstream_credentials"`
	// ConfigDir is the directory containing the loaded config file. Set by [LoadFile];
	// empty when Config is constructed without loading from disk.
	ConfigDir string `yaml:"-"`
}

type AccountingConfig struct {
	Enabled bool   `yaml:"enabled"`
	Mode    string `yaml:"mode"`
	// CountTimeout bounds provider/local count calls; empty selects the composition-root default.
	CountTimeout  string                        `yaml:"count_timeout"`
	Tokenizer     AccountingTokenizerConfig     `yaml:"tokenizer"`
	Preflight     AccountingPreflightConfig     `yaml:"preflight"`
	Ledger        AccountingLedgerConfig        `yaml:"ledger"`
	Admin         AccountingAdminConfig         `yaml:"admin"`
	Observability AccountingObservabilityConfig `yaml:"observability"`
	// StrictAuthoritative rejects backend wiring unless every configured backend can provide authoritative usage.
	StrictAuthoritative bool                       `yaml:"strict_authoritative"`
	Pricing             AccountingPricingConfig    `yaml:"pricing"`
	Authority           AccountingAuthorityConfig  `yaml:"authority"`
	Concurrency         ConcurrencyAuthorityConfig `yaml:"concurrency"`
	// Billing contains report-path configuration for an injected composition.
	// Billing is enabled only by complete host composition.
	Billing AccountingBillingConfig `yaml:"billing"`
}

// AccountingBillingConfig enables journal-backed monetary settlement and reports.
type AccountingBillingConfig struct {
	// ReportsPath selects the protected billing report surface for an injected
	// billing composition. Empty selects /admin/billing.
	ReportsPath string `yaml:"reports_path"`
}

type AccountingTokenizerConfig struct {
	DefaultEncoding string            `yaml:"default_encoding"`
	ModelMappings   map[string]string `yaml:"model_mappings"`
}

type AccountingPreflightConfig struct {
	Mode                 string `yaml:"mode"`
	MaxInputTokens       int64  `yaml:"max_input_tokens"`
	MaxOutputTokens      int64  `yaml:"max_output_tokens"`
	MaxContextTokens     int64  `yaml:"max_context_tokens"`
	ClampMaxOutputTokens bool   `yaml:"clamp_max_output_tokens"`
	// UnknownOutputPolicy selects how to bound exposure when the client omits
	// max_output_tokens: require_client_limit | configured_default |
	// model_backend_maximum | clamp | deny. Empty applies the documented default.
	UnknownOutputPolicy string `yaml:"unknown_output_policy"`
}

type AccountingLedgerConfig struct {
	// Store/SQLitePath/PostgresDSN/WritePolicy remain accepted for
	// backward-compatible YAML. Phase 8 no longer opens or writes this ledger;
	// Bun billingstore is the monetary journal.
	Store       string `yaml:"store"`
	SQLitePath  string `yaml:"sqlite_path"`
	PostgresDSN string `yaml:"postgres_dsn"`
	WritePolicy string `yaml:"write_policy"`
}

// MeteringConfig enables the optional durable metering journal (requirements 13.1–13.5).
// When Enabled is false, runtime leaves Executor.MeteringRecorder nil (requirement 17.1).
type MeteringConfig struct {
	Enabled bool                  `yaml:"enabled"`
	Journal MeteringJournalConfig `yaml:"journal"`
}

// MeteringJournalConfig selects the journal store backend.
type MeteringJournalConfig struct {
	Store       string `yaml:"store"` // memory | sqlite | postgres; empty with enabled defaults to memory
	SQLitePath  string `yaml:"sqlite_path"`
	PostgresDSN string `yaml:"postgres_dsn"`
}

type AccountingAdminConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Path         string `yaml:"path"`
	MaxBodyBytes int64  `yaml:"max_body_bytes"`
}

type AccountingObservabilityConfig struct {
	Enabled bool `yaml:"enabled"`
}

type AccountingPricingConfig struct {
	Currency       string                       `yaml:"currency"`
	CatalogVersion string                       `yaml:"catalog_version"`
	Models         []AccountingModelPriceConfig `yaml:"models"`
}

type AccountingModelPriceConfig struct {
	Backend              string `yaml:"backend"`
	Model                string `yaml:"model"`
	InputPer1M           string `yaml:"input_per_1m"`
	CachedInputPer1M     string `yaml:"cached_input_per_1m"`
	CacheWriteInputPer1M string `yaml:"cache_write_input_per_1m"`
	OutputPer1M          string `yaml:"output_per_1m"`
	ReasoningOutputPer1M string `yaml:"reasoning_output_per_1m"`
}

// DatabaseConfig is optional connection pool tuning for managed PostgreSQL handles
// opened by the proxy (see internal/infra/db). Omitted or zero values preserve driver
// defaults when no store is postgres; max_open_conns must be > 0 when any store is postgres.
// ConnectionMode and SchemaMode apply only to the dual-plane authority, concurrency,
// and metering PostgreSQL runtime paths. Other PostgreSQL stores retain their own
// owning lifecycle and compatibility migration behavior.
type DatabaseConfig struct {
	ConnectionMode  DatabaseConnectionMode `yaml:"connection_mode"`
	SchemaMode      DatabaseSchemaMode     `yaml:"schema_mode"`
	MaxOpenConns    int                    `yaml:"max_open_conns"`
	MaxIdleConns    int                    `yaml:"max_idle_conns"`
	ConnMaxLifetime string                 `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime string                 `yaml:"conn_max_idle_time"`
}

type DatabaseConnectionMode string

const (
	DatabaseConnectionModeDirect          DatabaseConnectionMode = "direct"
	DatabaseConnectionModeTransactionPool DatabaseConnectionMode = "transaction_pool"
)

type DatabaseSchemaMode string

const (
	DatabaseSchemaModeAutoMigrate DatabaseSchemaMode = "auto_migrate"
	DatabaseSchemaModeVerifyOnly  DatabaseSchemaMode = "verify_only"
)

func (c DatabaseConfig) EffectiveConnectionMode() DatabaseConnectionMode {
	if c.ConnectionMode == "" {
		return DatabaseConnectionModeDirect
	}
	return c.ConnectionMode
}

func (c DatabaseConfig) EffectiveSchemaMode() DatabaseSchemaMode {
	if c.SchemaMode == "" {
		return DatabaseSchemaModeAutoMigrate
	}
	return c.SchemaMode
}

// ModelAliasConfig is one regexp-based rewrite of an incoming route selector (see internal/core/routing/aliases.go).
type ModelAliasConfig struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// ObservabilityConfig toggles Prometheus metrics and OpenTelemetry tracing.
type ObservabilityConfig struct {
	Metrics MetricsConfig `yaml:"metrics"`
	Tracing TracingConfig `yaml:"tracing"`
}

// MetricsConfig controls the Prometheus /metrics endpoint.
type MetricsConfig struct {
	// Enabled exposes aip_http_* metrics and process/go collectors when true.
	// When false (zero value), no /metrics handler is registered (legacy behavior).
	Enabled bool `yaml:"enabled"`
	// Path is the HTTP path for Prometheus scraping (e.g. "/metrics"). Empty defaults to /metrics in LoadFile.
	Path string `yaml:"path"`
	// ExemplarsEnabled attaches trace_id exemplars to selected histograms and enables OpenMetrics on /metrics.
	ExemplarsEnabled bool `yaml:"exemplars_enabled"`
	// PromptCacheCollapse carries optional prompt-cache collapse diagnostic
	// tuning. Omitted fields select documented defaults; the block is
	// startup-fixed and is validated even when metrics are disabled.
	PromptCacheCollapse PromptCacheCollapseConfig `yaml:"prompt_cache_collapse"`
}

// TracingConfig enables OpenTelemetry traces (incoming otelhttp + OTLP export via standard OTEL_* env vars).
type TracingConfig struct {
	// Enabled turns on SDK wiring, W3C propagation, and outbound HTTP tracing on the shared upstream client.
	Enabled bool `yaml:"enabled"`
	// ServiceName sets otel resource service.name when non-empty; otherwise OTEL_SERVICE_NAME or "aipstd".
	ServiceName string `yaml:"service_name"`
	// SampleRatio when set and strictly between 0 and 1 applies ParentBased(TraceIDRatioBased) for root spans.
	// When nil or 1, the SDK default sampler applies (typically full sampling for new roots).
	SampleRatio *float64 `yaml:"sample_ratio"`
}

// HTTPClientConfig tunes the shared outbound HTTP client used for upstream LLM calls.
type HTTPClientConfig struct {
	// TrustEnvironmentProxy when true (default) uses http.ProxyFromEnvironment for outbound requests.
	// When false, the transport ignores HTTP_PROXY/HTTPS_PROXY/NO_PROXY (reduces deputy risk if env is untrusted).
	// Omitted or null in YAML defaults to true in [LoadFile] / [EffectiveTrustEnvironmentProxy].
	TrustEnvironmentProxy *bool `yaml:"trust_environment_proxy"`
	// MaxIdleConns is the Transport MaxIdleConns pool cap. Omit to use the bundled default (~100).
	MaxIdleConns *int `yaml:"max_idle_conns,omitempty"`
	// MaxIdleConnsPerHost defaults to 64 when omitted (Go's default of 2 is usually too low for LLM APIs).
	MaxIdleConnsPerHost *int `yaml:"max_idle_conns_per_host,omitempty"`
	// IdleConnTimeout is a Go duration string (e.g. "90s"). Empty uses the httpclient default.
	IdleConnTimeout string `yaml:"idle_conn_timeout"`
	// ResponseHeaderTimeout bounds waiting for response headers (e.g. "60s"). Empty uses default.
	ResponseHeaderTimeout string `yaml:"response_header_timeout"`
	// DialTimeout is the net.Dialer Timeout for establishing connections (e.g. "30s").
	DialTimeout string `yaml:"dial_timeout"`
	// KeepAlive is the net.Dialer KeepAlive interval (e.g. "30s").
	KeepAlive string `yaml:"keep_alive"`
	// TLSHandshakeTimeout caps TLS handshakes (e.g. "10s").
	TLSHandshakeTimeout string `yaml:"tls_handshake_timeout"`
	// ExpectContinueTimeout is the Transport expect-continue timeout (e.g. "1s").
	ExpectContinueTimeout string `yaml:"expect_continue_timeout"`
	// ClientTimeout is [http.Client.Timeout] for the full request including body (e.g. "120s").
	ClientTimeout string `yaml:"client_timeout"`
}

// EffectiveTrustEnvironmentProxy returns whether outbound calls should honor process proxy environment variables.
func (c *Config) EffectiveTrustEnvironmentProxy() bool {
	if c == nil || c.HTTPClient.TrustEnvironmentProxy == nil {
		return true
	}
	return *c.HTTPClient.TrustEnvironmentProxy
}

// LoggingConfig controls process-wide slog output and optional HTTP access logs.
type LoggingConfig struct {
	// Level is one of: debug, info, warn, error (case-insensitive). Empty defaults to info in LoadFile/Validate.
	Level string `yaml:"level"`
	// Format is json or text (case-insensitive). Empty defaults to json in LoadFile/Validate.
	Format string `yaml:"format"`
	// AddSource adds source file/line to each record when true.
	AddSource bool `yaml:"add_source"`
	// AccessLog emits one structured line per HTTP request when true.
	AccessLog bool `yaml:"access_log"`
	// AccessLogSkipPaths are URL path prefixes (must start with /) for which access logs are suppressed.
	AccessLogSkipPaths []string `yaml:"access_log_skip_paths"`
	// AccessLogIncludeRawPath when true adds the full URL path to access logs (higher cardinality).
	// Default false: only route_group.
	AccessLogIncludeRawPath bool `yaml:"access_log_include_raw_path"`
}

// HooksConfig carries core hook-bus tuning (not plugin opaque payloads).
type HooksConfig struct {
	// ToolReactorErrorPolicy is one of: fail_open (default), fail_closed, swallow_event.
	ToolReactorErrorPolicy string `yaml:"tool_reactor_error_policy"`
}

type AuthMode string

const (
	// AuthModeNoAuth permits unauthenticated local single-user traffic only on explicit loopback binds.
	AuthModeNoAuth AuthMode = "no_auth"
	// AuthModeExternal requires an injected or configured auth layer and may bind non-loopback interfaces.
	AuthModeExternal AuthMode = "external"
)

type ServerConfig struct {
	Address  string   `yaml:"address"`
	AuthMode AuthMode `yaml:"auth_mode"`
	// MaxRequestBodyBytes caps HTTP request bodies for bundled frontends. Zero selects
	// each handler's default limit (see internal/plugins/frontends/reqbody).
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes"`
	// ReadHeaderTimeout is a Go duration string (e.g. "10s") for [http.Server.ReadHeaderTimeout].
	// Empty defaults to 10s (historical stdhttp behavior).
	ReadHeaderTimeout string `yaml:"read_header_timeout"`
	// ReadTimeout is [http.Server.ReadTimeout] (full request body read + per-connection read deadlines).
	// Empty defaults to 30s.
	ReadTimeout string `yaml:"read_timeout"`
	// WriteTimeout is [http.Server.WriteTimeout]. Empty defaults to 120s.
	WriteTimeout string `yaml:"write_timeout"`
	// IdleTimeout is [http.Server.IdleTimeout]. Empty defaults to 120s.
	IdleTimeout string `yaml:"idle_timeout"`
	// ShutdownTimeout bounds HTTP drain and host close on process shutdown.
	// Empty defaults to 15s.
	ShutdownTimeout string `yaml:"shutdown_timeout"`
	// MaxConcurrentDecodes caps concurrent frontend protocol decode/materialization work.
	// Zero selects the documented default (32) during validation. Body ReadAll and JSON
	// preflight run before admission.
	MaxConcurrentDecodes int `yaml:"max_concurrent_decodes"`
	// MaxInflightDecodeBytes caps weighted in-flight decode bytes across concurrent decodes
	// while decode is active. Zero selects the documented default (64 MiB) during validation.
	MaxInflightDecodeBytes int64 `yaml:"max_inflight_decode_bytes"`
	// MaxPendingWireEvents caps backend adapter-internal pending-event queues per stream (0 = unlimited).
	MaxPendingWireEvents int `yaml:"max_pending_wire_events"`
	// LargePayloadFastPath controls the optional large-payload streaming fast
	// path (design section 3). Default off; see LargePayloadFastPathConfig for
	// plaintext-spool and optimization-budget semantics.
	LargePayloadFastPath LargePayloadFastPathConfig `yaml:"large_payload_fast_path"`
	// PreRequestKeepalive optionally emits SSE comment keepalives while streaming frontends wait for
	// pre-request admission handlers to finish inside executor setup.
	PreRequestKeepalive PreRequestKeepaliveConfig `yaml:"pre_request_keepalive"`
}

type PreRequestKeepaliveConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"`
}

type EffectivePreRequestKeepaliveConfig struct {
	Enabled  bool
	Interval time.Duration
}

const (
	defaultServerReadHeaderTimeout = 10 * time.Second
	defaultServerReadTimeout       = 30 * time.Second
	defaultServerWriteTimeout      = 120 * time.Second
	defaultServerIdleTimeout       = 120 * time.Second
	defaultPreRequestKAInterval    = 15 * time.Second
	DefaultShutdownTimeout         = 15 * time.Second

	defaultMaxRequestBodyBytes    int64 = 8 << 20
	DefaultMaxConcurrentDecodes         = 32
	DefaultMaxInflightDecodeBytes       = 64 << 20
)

func parseServerDurationOrDefault(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// EffectiveReadHeaderTimeout returns ReadHeaderTimeout or the default (10s).
func (s ServerConfig) EffectiveReadHeaderTimeout() time.Duration {
	return parseServerDurationOrDefault(s.ReadHeaderTimeout, defaultServerReadHeaderTimeout)
}

// EffectiveReadTimeout returns ReadTimeout or the default (30s).
func (s ServerConfig) EffectiveReadTimeout() time.Duration {
	return parseServerDurationOrDefault(s.ReadTimeout, defaultServerReadTimeout)
}

// EffectiveWriteTimeout returns WriteTimeout or the default (120s).
func (s ServerConfig) EffectiveWriteTimeout() time.Duration {
	return parseServerDurationOrDefault(s.WriteTimeout, defaultServerWriteTimeout)
}

// EffectiveIdleTimeout returns IdleTimeout or the default (120s).
func (s ServerConfig) EffectiveIdleTimeout() time.Duration {
	return parseServerDurationOrDefault(s.IdleTimeout, defaultServerIdleTimeout)
}

// EffectiveShutdownTimeout returns ShutdownTimeout or the default (15s).
func (s ServerConfig) EffectiveShutdownTimeout() time.Duration {
	return parseServerDurationOrDefault(s.ShutdownTimeout, DefaultShutdownTimeout)
}

// EffectiveMaxRequestBodyBytes returns MaxRequestBodyBytes when positive, otherwise zero
// (callers treat zero as "use handler default").
func (s ServerConfig) EffectiveMaxRequestBodyBytes() int64 {
	if s.MaxRequestBodyBytes > 0 {
		return s.MaxRequestBodyBytes
	}
	return 0
}

// EffectiveMaxConcurrentDecodes returns MaxConcurrentDecodes when positive, otherwise the default (32).
func (s ServerConfig) EffectiveMaxConcurrentDecodes() int {
	if s.MaxConcurrentDecodes > 0 {
		return s.MaxConcurrentDecodes
	}
	return DefaultMaxConcurrentDecodes
}

// EffectiveMaxInflightDecodeBytes returns MaxInflightDecodeBytes when positive, otherwise the default (64 MiB).
func (s ServerConfig) EffectiveMaxInflightDecodeBytes() int64 {
	if s.MaxInflightDecodeBytes > 0 {
		return s.MaxInflightDecodeBytes
	}
	return DefaultMaxInflightDecodeBytes
}

// EffectiveMaxPendingWireEvents returns MaxPendingWireEvents as configured (0 = unlimited).
func (s ServerConfig) EffectiveMaxPendingWireEvents() int {
	return s.MaxPendingWireEvents
}

// EffectiveMaxRequestBodyBytesForBudget returns the largest single request body the server admits
// for decode-budget checks: MaxRequestBodyBytes when positive, otherwise the documented 8 MiB default.
func (s ServerConfig) EffectiveMaxRequestBodyBytesForBudget() int64 {
	if s.MaxRequestBodyBytes > 0 {
		return s.MaxRequestBodyBytes
	}
	return defaultMaxRequestBodyBytes
}

func (s ServerConfig) EffectivePreRequestKeepalive() EffectivePreRequestKeepaliveConfig {
	out := EffectivePreRequestKeepaliveConfig{
		Enabled:  s.PreRequestKeepalive.Enabled,
		Interval: defaultPreRequestKAInterval,
	}
	if d, err := time.ParseDuration(strings.TrimSpace(s.PreRequestKeepalive.Interval)); err == nil && d > 0 {
		out.Interval = d
	}
	return out
}

type DiagnosticsConfig struct {
	Enabled      bool   `yaml:"enabled"`
	HealthPath   string `yaml:"health_path"`
	AttemptsPath string `yaml:"attempts_path"`
	// InventoryPath registers a JSON plugin inventory endpoint when non-empty (e.g. "/debug/inventory").
	InventoryPath string `yaml:"inventory_path"`
	// RouteTracePath registers a JSON ring buffer of recent routing decisions when non-empty.
	RouteTracePath string `yaml:"route_trace_path"`
	// PprofPath registers net/http/pprof handlers under this prefix when diagnostics.enabled is true
	// (e.g. "/debug/pprof"). Leave empty to disable. Do not expose publicly without access controls.
	PprofPath string `yaml:"pprof_path"`
	// SharedSecret when non-empty requires header X-AIP-Diagnostics-Secret on attempts, inventory,
	// route trace, and pprof routes (not on health). Use a long random value in production.
	SharedSecret string `yaml:"shared_secret"`
}

const DefaultMaxAttempts = 3

type RoutingConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	// DefaultRoute is the selector used when the client omits X-AIP-Route (e.g. "openai-responses:gpt-4o-mini").
	DefaultRoute string                 `yaml:"default_route"`
	Health       RoutingHealthConfig    `yaml:"health"`
	Affinity     RoutingAffinityConfig  `yaml:"affinity"`
	Transport    RoutingTransportConfig `yaml:"transport"`
	// ExecutionCompositionPolicy controls whether multi-leaf routing compositions (weighted, parallel, thinker, failover)
	// can include whole-agent/orchestration runtimes. Defaults to "safe".
	ExecutionCompositionPolicy ExecutionCompositionPolicy `yaml:"execution_composition_policy"`
	// OverrideAdmin is the opt-in protected HTTP surface for A-leg routing overrides.
	// Disabled by default. Disabling the endpoint does not clear persisted override state.
	OverrideAdmin RoutingOverrideAdminConfig `yaml:"override_admin"`
	// AttemptPolicy decides what an attempt candidate does when it
	// cannot replay the dialect of client-sent reasoning items: log_skip
	// (strip those parts for that attempt; the standard-distribution
	// default, explicitly reversing archived
	// openai-responses-reasoning-preservation line 34 for client-sent
	// reasoning) or reject (today's hard refusal for strict operators).
	// It reuses the reject | log_skip vocabulary and is distinct from the
	// reasoning-output-preservation feature's on_unrepresentable, which
	// keeps its existing scope (proxy-restored reasoning only).
	AttemptPolicy string `yaml:"attempt_policy"`
}

// ExecutionCompositionPolicy controls whether composite routing expressions can include agent runtimes.
type ExecutionCompositionPolicy string

const (
	// ExecutionCompositionSafe restricts selector composition to pure inference backends; agent runtimes are allowed only as direct routes.
	ExecutionCompositionSafe ExecutionCompositionPolicy = "safe"
	// ExecutionCompositionUnrestricted allows arbitrary composition of agent runtimes and inference backends.
	ExecutionCompositionUnrestricted ExecutionCompositionPolicy = "unrestricted"
)

// EffectiveExecutionCompositionPolicy returns the normalized policy, defaulting empty to safe.
func (c RoutingConfig) EffectiveExecutionCompositionPolicy() ExecutionCompositionPolicy {
	normalized := ExecutionCompositionPolicy(strings.TrimSpace(string(c.ExecutionCompositionPolicy)))
	switch normalized {
	case ExecutionCompositionUnrestricted:
		return ExecutionCompositionUnrestricted
	default:
		return ExecutionCompositionSafe
	}
}

// EffectiveAttemptPolicy returns the normalized client-sent reasoning
// policy, defaulting empty to log_skip. Unknown values fail closed to reject;
// Validate rejects them at load time.
func (c RoutingConfig) EffectiveAttemptPolicy() aipapi.AttemptPolicy {
	switch aipapi.AttemptPolicy(strings.TrimSpace(strings.ToLower(c.AttemptPolicy))) {
	case aipapi.AttemptLogSkip, "":
		return aipapi.AttemptLogSkip
	case aipapi.AttemptReject:
		return aipapi.AttemptReject
	default:
		return aipapi.AttemptReject
	}
}

// RoutingOverrideAdminConfig controls the protected GET/PUT/DELETE routing-override
// admin resource. Enablement is false by default.
type RoutingOverrideAdminConfig struct {
	Enabled      bool   `yaml:"enabled"`
	PathPrefix   string `yaml:"path_prefix"`
	MaxBodyBytes int64  `yaml:"max_body_bytes"`
}

type RoutingAffinityConfig struct {
	Store           string `yaml:"store"`
	MissingIdentity string `yaml:"missing_identity"`
}

type RoutingHealthConfig struct {
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type CircuitBreakerConfig struct {
	Enabled          bool   `yaml:"enabled"`
	FailureThreshold int    `yaml:"failure_threshold"`
	OpenFor          string `yaml:"open_for"`
}

// SecureSessionConfig controls the core-owned secure session layer (resume proofs, durable evidence, diagnostics).
// When Enabled is omitted (nil), secure sessions default to on with store memory unless overridden.
// Explicit enabled: false is rejected by validation (legacy continuity-only executor path was removed).
type SecureSessionConfig struct {
	// Enabled turns on secure-session validation and runtime wiring (store, tokens, audit gates).
	// Omitted in YAML defaults to enabled; use a pointer so explicit false can be rejected at validation time.
	Enabled *bool `yaml:"enabled"`
	// Store is "memory" (non-durable), "sqlite" (local durable), or "postgres" (managed durable).
	// Empty is normalized to "memory" in [LoadFile] when Enabled.
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// PostgresDSN is the connection string when store is "postgres".
	PostgresDSN string `yaml:"postgres_dsn"`
	// ResumeWindow is a Go duration string for inactivity-based resume limits; empty means no fixed
	// window (policy default).
	ResumeWindow string `yaml:"resume_window"`
	// TokenFingerprintKey is deployment secret material used to HMAC resume-token fingerprints; required for sqlite store.
	TokenFingerprintKey string `yaml:"token_fingerprint_key"`
	// AuditDurability is "best_effort" or "durable"; durable requires a durable store (sqlite or postgres)
	// and a long token fingerprint key.
	AuditDurability string `yaml:"audit_durability"`
	// RedactionDefault is "standard" or "strict" for operator-visible session payloads (diagnostics);
	// invalid values rejected when enabled.
	RedactionDefault string `yaml:"redaction_default"`
	// DiagnosticsExposeSummaries registers operator session summary routes when true (requires DiagnosticsPathPrefix
	// and a non-empty diagnostics.shared_secret, same minimum length as other protected diagnostics routes).
	DiagnosticsExposeSummaries bool `yaml:"diagnostics_expose_summaries"`
	// DiagnosticsPathPrefix is the URL prefix for secure-session diagnostics (e.g. "/debug/sessions");
	// must start with "/".
	DiagnosticsPathPrefix string `yaml:"diagnostics_path_prefix"`
	// NonDurableWarning is "silent", "log", or "strict" when store is non-durable (memory): strict fails
	// validation when audit requires durability.
	NonDurableWarning string `yaml:"non_durable_warning"`
	// RequireWorkspaceID when true rejects secure-session turns when no workspace id was resolved
	// (maps to [WorkspaceMatchRequired] on BeginTurn; Req 11.1 / 11.6).
	RequireWorkspaceID bool `yaml:"require_workspace_id"`
	// WorkspaceResolveOnError is "fail_open" (default) or "fail_closed". When fail_closed, workspace
	// resolver errors reject the request instead of continuing with an empty workspace (Req 11.6).
	WorkspaceResolveOnError string `yaml:"workspace_resolve_on_error"`
	// ResumeTokenBindPrincipalOnly when true fingerprints resume tokens using only the authenticated
	// principal id (not agent digest or first-message digest), so benign client metadata drift
	// between turns does not invalidate bearer resumes.
	ResumeTokenBindPrincipalOnly bool `yaml:"resume_token_bind_principal_only"`
	// SQLQueryCacheTTL is a Go duration string enabling process-local TTL caching of session existence
	// and transcript_enabled reads in durable SQL secure-session stores. Empty disables caching.
	SQLQueryCacheTTL string `yaml:"sql_query_cache_ttl"`
	// SQLQueryCacheMaxEntries caps entries per logical cache when SQLQueryCacheTTL is set; zero uses a store default.
	SQLQueryCacheMaxEntries int `yaml:"sql_query_cache_max_entries"`
}

type ContinuityConfig struct {
	// InMemory is the legacy spelling of "store: memory". When true it forces the
	// process-local backing, and it may not be combined with a durable store name:
	// the validator refuses that contradiction instead of downgrading the durable
	// store. When false (or omitted) the Store field decides, and
	// an omitted Store defaults to the process-local backing as well, so the whole
	// continuity block may be omitted.
	InMemory bool `yaml:"in_memory"`
	// Store names the continuity backing. Empty is normalized to "memory" by the
	// loader. Use "sqlite" for local durable storage (requires sqlite_path) or "postgres" for managed durable
	// (requires postgres_dsn).
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// PostgresDSN is the connection string when store is "postgres".
	PostgresDSN string `yaml:"postgres_dsn"`
	// TTL is in-memory store only (A-leg eviction). Ignored by SQLite until pruning is implemented.
	TTL string `yaml:"ttl"`
	// MaxLegs is in-memory store only when TTL is empty. Must be >= 0. Ignored by SQLite until pruning exists.
	MaxLegs int `yaml:"max_legs"`
}

type PluginsConfig struct {
	BackendDiscovery BackendDiscoveryConfig `yaml:"backend_discovery"`
	Frontends        []PluginConfig         `yaml:"frontends"`
	Backends         []PluginConfig         `yaml:"backends"`
	Features         []PluginConfig         `yaml:"features"`
}

// BackendDiscoveryConfig is the generic (provider-agnostic) discovery/trust subtree.
type BackendDiscoveryConfig struct {
	Enabled         bool                                   `yaml:"enabled"`
	Paths           []string                               `yaml:"paths"`
	Strict          bool                                   `yaml:"strict"`
	DevelopmentMode bool                                   `yaml:"development_mode"`
	Instances       map[string]BackendInstanceProvisioning `yaml:"instances,omitempty"`
	// EconomicEvidencePolicy selects the host's reaction to connector
	// economic evidence it cannot admit. It is "degrade" (the default) or
	// "strict". An unknown value fails startup.
	EconomicEvidencePolicy string `yaml:"economic_evidence_policy,omitempty"`
}

// BackendInstanceProvisioning contains opaque references, never credentials.
type BackendInstanceProvisioning struct {
	Kind         string                      `yaml:"kind"`
	SecretRefs   map[string]BackendSecretRef `yaml:"secret_refs,omitempty"`
	ExecutionEnv []string                    `yaml:"execution_env,omitempty"`
}

type BackendSecretRef struct {
	Env string `yaml:"env"`
}

// PluginConfig keeps plugin-private config opaque to the core.
type PluginConfig struct {
	// Kind is the bundled factory id used for registry lookup (e.g. openai-responses).
	// When empty, ID is treated as both factory kind and instance id (legacy single-field configs).
	Kind string `yaml:"kind,omitempty"`
	// ID is the runtime instance id: routing keys, executor backend map keys, and duplicate detection.
	ID      string    `yaml:"id"`
	Enabled bool      `yaml:"enabled"`
	Config  yaml.Node `yaml:"config"`
}

// FactoryID returns the registry/factory identifier for this plugin row.
func (p PluginConfig) FactoryID() string {
	k := strings.TrimSpace(p.Kind)
	if k != "" {
		return k
	}
	return strings.TrimSpace(p.ID)
}

// InstanceID returns the configured runtime instance identifier (never empty for valid configs).
func (p PluginConfig) InstanceID() string {
	return strings.TrimSpace(p.ID)
}
