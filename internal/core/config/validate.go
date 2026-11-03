package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Validate checks plugin identity rules and continuity/store consistency after decoding.
// It does not validate model_aliases; call routing.ValidateModelAliasesConfig after LoadFile,
// or rely on runtimebundle.Build.
//
// Validate declares no capabilities, so Enterprise-only accounting keys
// (accounting.pricing, accounting.billing, accounting.authority.rules) fail
// closed. Runtime compositions declare their capabilities through
// [ValidateWithCapabilities] (driven by LoadEffectiveOptions.Capabilities).
func Validate(cfg *Config) error {
	return ValidateWithCapabilities(cfg, nil)
}

// ValidateWithCapabilities runs [Validate] with the process composition root's
// capability set. A nil or empty set fails closed for Enterprise-only
// accounting keys; see [CapabilitySet] and
// validateEnterpriseOnlyAccountingKeys for the exact presence predicate.
func ValidateWithCapabilities(cfg *Config, capabilities CapabilitySet) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	if cfg.ModelAliases == nil {
		cfg.ModelAliases = []ModelAliasConfig{}
	}
	if err := validatePluginSlice("plugins.frontends", cfg.Plugins.Frontends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.backends", cfg.Plugins.Backends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.features", cfg.Plugins.Features); err != nil {
		return err
	}
	if err := ValidateBackendDiscovery(cfg); err != nil {
		return err
	}
	if err := validateDatabaseConfig(cfg); err != nil {
		return err
	}
	if err := validateContinuityStores(cfg); err != nil {
		return err
	}
	if err := validateLogging(cfg); err != nil {
		return err
	}
	if err := validateDiagnosticsPaths(cfg); err != nil {
		return err
	}
	if err := validateObservability(cfg); err != nil {
		return err
	}
	if err := validateControlPlane(cfg); err != nil {
		return err
	}
	if err := validateDiagnosticsSecret(cfg); err != nil {
		return err
	}
	if err := ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		return err
	}
	if err := validateHTTPClient(cfg); err != nil {
		return err
	}
	if err := validateServer(cfg); err != nil {
		return err
	}
	if err := validateHTTPHeaders(cfg); err != nil {
		return err
	}
	if err := validateAccessAuth(cfg); err != nil {
		return err
	}
	if _, err := CompileGeoIP(cfg.Access.GeoIP); err != nil {
		return err
	}
	if _, err := CompileSelfDefense(cfg.Access.SelfDefense); err != nil {
		return err
	}
	if err := validateSecureSession(cfg); err != nil {
		return err
	}
	if err := validateModelCatalog(cfg); err != nil {
		return err
	}
	if err := validateModelInventory(cfg); err != nil {
		return err
	}
	if _, err := EffectiveStreamRecoveryAutoResume(cfg, StreamRecoveryOverrides{}); err != nil {
		return err
	}
	if err := validateEmptyVisibleTurnRetry(cfg); err != nil {
		return err
	}
	if err := validateEnterpriseOnlyAccountingKeys(cfg, capabilities); err != nil {
		return err
	}
	if err := validateAccounting(cfg); err != nil {
		return err
	}
	if err := validateMetering(cfg); err != nil {
		return err
	}
	if err := validateUpstreamCredentials(cfg); err != nil {
		return err
	}
	if err := validateRoutingHealth(cfg); err != nil {
		return err
	}
	if err := validateInterleaved(cfg); err != nil {
		return err
	}
	if err := validateRoutingAffinity(cfg); err != nil {
		return err
	}
	if err := validateRoutingExecutionComposition(cfg); err != nil {
		return err
	}
	if err := validateRoutingAttemptPolicy(cfg); err != nil {
		return err
	}
	if err := validateRoutingOverrideAdmin(cfg); err != nil {
		return err
	}
	if err := identity.Validate(&cfg.Identity); err != nil {
		return err
	}
	// After store-specific errors so operators see DSN/path issues before pool bounds.
	return validatePostgresPoolBound(cfg)
}

func validateSecureSession(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	ss := &cfg.SecureSession
	if ss.Enabled != nil && !*ss.Enabled {
		return fmt.Errorf(
			"secure_session.enabled: false is no longer supported; remove the field " +
				"(secure sessions default on) or set secure_session.enabled: true",
		)
	}
	if err := normalizeEnum(&ss.Store, "secure_session.store", "memory", "memory", "sqlite", "postgres"); err != nil {
		return err
	}
	store := ss.Store
	key := strings.TrimSpace(ss.TokenFingerprintKey)
	if store == "sqlite" || store == "postgres" {
		if len(key) < 32 {
			return fmt.Errorf("secure_session.token_fingerprint_key: must be at least 32 characters when store is %s", store)
		}
	} else if key != "" && len(key) < 32 {
		return fmt.Errorf(
			"secure_session.token_fingerprint_key: when set, must be at least 32 characters " +
				"(memory store may omit the key for a process-local ephemeral fingerprint)",
		)
	}
	rw := strings.TrimSpace(ss.ResumeWindow)
	if err := parsePositiveDurationField("secure_session.resume_window", rw); err != nil {
		return err
	}
	if err := normalizeEnum(&ss.AuditDurability, "secure_session.audit_durability", "best_effort", "best_effort", "durable"); err != nil {
		return err
	}
	audit := ss.AuditDurability
	if audit == "durable" {
		if store != "sqlite" && store != "postgres" {
			return fmt.Errorf(
				"secure_session.audit_durability: durable requires a durable secure_session.store "+
					"(sqlite or postgres), not %q",
				store,
			)
		}
	}
	if store == "sqlite" {
		path := strings.TrimSpace(ss.SQLitePath)
		if path == "" {
			return fmt.Errorf("secure_session.sqlite_path: required when store is \"sqlite\"")
		}
		if strings.ContainsAny(path, "\x00?#&") {
			return fmt.Errorf("secure_session.sqlite_path: must not contain NUL, ?, #, or & (ambiguous with SQLite URI query)")
		}
	}
	if store == "postgres" {
		dsn := strings.TrimSpace(ss.PostgresDSN)
		if dsn == "" {
			return fmt.Errorf("secure_session.postgres_dsn: required when store is \"postgres\"")
		}
		if strings.Contains(dsn, "\x00") {
			return fmt.Errorf("secure_session.postgres_dsn: must not contain NUL")
		}
	} else if d := strings.TrimSpace(ss.PostgresDSN); d != "" {
		return fmt.Errorf("secure_session.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}

	if err := normalizeEnum(&ss.NonDurableWarning, "secure_session.non_durable_warning", "log", "silent", "log", "strict"); err != nil {
		return err
	}

	if err := normalizeEnum(&ss.RedactionDefault, "secure_session.redaction_default", "standard", "standard", "strict"); err != nil {
		return err
	}

	if ss.DiagnosticsExposeSummaries {
		p := strings.TrimSpace(ss.DiagnosticsPathPrefix)
		if p == "" {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: required when diagnostics_expose_summaries is true")
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: must start with /")
		}
		// Shared secret for these routes on non-loopback binds is enforced by
		// [ValidateProtectedDiagnosticsPosture] (surface name secure_session_summaries).
	}
	if !ss.DiagnosticsExposeSummaries {
		if p := strings.TrimSpace(ss.DiagnosticsPathPrefix); p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: must start with /")
		}
	}
	if err := normalizeEnum(&ss.WorkspaceResolveOnError, "secure_session.workspace_resolve_on_error", "fail_open", "fail_open", "fail_closed"); err != nil {
		return err
	}
	if err := parsePositiveDurationField("secure_session.sql_query_cache_ttl", ss.SQLQueryCacheTTL); err != nil {
		return err
	}
	if ss.SQLQueryCacheMaxEntries < 0 {
		return fmt.Errorf("secure_session.sql_query_cache_max_entries: must be >= 0")
	}
	return nil
}

func validateServer(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	applyDefaultServerListenAddress(cfg)
	applyDefaultServerLimits(cfg)
	s := cfg.Server
	// Listener posture for no_auth vs broad binds is enforced in validateAccessAuth via
	// accessmode.ValidatePosture (combines server.auth_mode, access.mode, and listeners).
	switch cfg.EffectiveServerAuthMode() {
	case AuthModeNoAuth, AuthModeExternal:
	default:
		return fmt.Errorf("server.auth_mode: want no_auth or external, got %q", s.AuthMode)
	}
	if s.MaxConcurrentDecodes < 0 {
		return fmt.Errorf("server.max_concurrent_decodes: must be >= 0")
	}
	if s.MaxInflightDecodeBytes < 0 {
		return fmt.Errorf("server.max_inflight_decode_bytes: must be >= 0")
	}
	if s.MaxPendingWireEvents < 0 {
		return fmt.Errorf("server.max_pending_wire_events: must be >= 0")
	}
	if s.MaxInflightDecodeBytes < s.EffectiveMaxRequestBodyBytesForBudget() {
		return fmt.Errorf("server.max_inflight_decode_bytes: must be >= max single request body (%d bytes)", s.EffectiveMaxRequestBodyBytesForBudget())
	}
	if err := validateLargePayloadFastPath(s); err != nil {
		return err
	}
	for _, chk := range []struct {
		name string
		val  string
	}{
		{"read_header_timeout", s.ReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout},
		{"write_timeout", s.WriteTimeout},
		{"idle_timeout", s.IdleTimeout},
		{"shutdown_timeout", s.ShutdownTimeout},
		{"pre_request_keepalive.interval", s.PreRequestKeepalive.Interval},
	} {
		if err := parsePositiveDurationOptional("server."+chk.name, chk.val); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPClient(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	hc := cfg.HTTPClient
	if hc.MaxIdleConns != nil && *hc.MaxIdleConns < 1 {
		return fmt.Errorf("http_client.max_idle_conns: must be >= 1 when set")
	}
	if hc.MaxIdleConnsPerHost != nil && *hc.MaxIdleConnsPerHost < 1 {
		return fmt.Errorf("http_client.max_idle_conns_per_host: must be >= 1 when set")
	}
	parseDur := parsePositiveDurationOptional
	for _, chk := range []struct {
		name string
		val  string
	}{
		{"idle_conn_timeout", hc.IdleConnTimeout},
		{"response_header_timeout", hc.ResponseHeaderTimeout},
		{"dial_timeout", hc.DialTimeout},
		{"keep_alive", hc.KeepAlive},
		{"tls_handshake_timeout", hc.TLSHandshakeTimeout},
		{"expect_continue_timeout", hc.ExpectContinueTimeout},
		{"client_timeout", hc.ClientTimeout},
	} {
		if err := parseDur("http_client."+chk.name, chk.val); err != nil {
			return err
		}
	}
	return nil
}

func validateDiagnosticsSecret(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	s := strings.TrimSpace(cfg.Diagnostics.SharedSecret)
	if s != "" && len(s) < 12 {
		return fmt.Errorf("diagnostics.shared_secret: must be at least 12 characters when set")
	}
	return nil
}

// rejectHTTPPathDotDot rejects configured URL paths that contain a ".." segment. Such values are
// unnecessary for mux mounts, confuse overlap validation, and are a foot-gun at HTTP boundaries.
func rejectHTTPPathDotDot(fieldName, p string) error {
	if slices.Contains(strings.Split(p, "/"), "..") {
		return fmt.Errorf("%s: must not contain .. path segments", fieldName)
	}
	return nil
}

// operatorMountPathMeta mirrors httpcontract.operatorRoutePathMeta, the policy for an
// operator-supplied route path. The two live in different packages on purpose:
// internal/core/config must not depend on the stdhttp contract package, and the
// route contract must not depend on core config.
//
// The duplication is not left to discipline. TestOperatorDataPlanePathsAreLiteralAnd
// NormalizeUnchanged in internal/archtest runs one case table through BOTH validators
// and through the ingress self-defense route normalizer, and requires them to agree:
// every operator path they accept must be a literal path that the self-defense
// normalizer leaves unchanged. Adding a character to one list without the other fails
// there.
const operatorMountPathMeta = "?#*\\{}%"

// validateConfiguredMountPath is the single chokepoint for an operator-configured
// data-plane mount path: it trims, requires an absolute path, rejects a parent
// segment, rejects any ServeMux pattern or query-like syntax, and returns the
// normalized path without a trailing separator. An empty value means "not mounted"
// and returns an empty path with no error, because every configurable mount is
// optional.
//
// Every configurable mount path in the config model goes through here, so the rules
// cannot diverge between the diagnostics block, the metrics path, the secure-session
// and model diagnostic prefixes, and the protected operator mounts.
func validateConfiguredMountPath(field, p string, allowEmpty bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%s: must start with /", field)
	}
	if err := rejectHTTPPathDotDot(field, p); err != nil {
		return "", err
	}
	if strings.ContainsAny(p, operatorMountPathMeta) {
		return "", fmt.Errorf("%s: must be a literal path and must not contain any of %q "+
			"(ServeMux pattern syntax, a query or fragment separator, a wildcard, a backslash or a percent escape)",
			field, operatorMountPathMeta)
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("%s: must not contain an empty path segment", field)
	}
	for segment := range strings.SplitSeq(p, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("%s: must not contain a %q path segment", field, segment)
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return "", fmt.Errorf("%s: must not contain control or whitespace characters", field)
		}
	}
	return strings.TrimSuffix(p, "/"), nil
}

func validateDiagnosticsPaths(cfg *Config) error {
	paths, err := collectDataPlanePaths(cfg)
	if err != nil {
		return err
	}
	return rejectOverlappingDataPlanePaths(paths)
}

// rejectOverlappingDataPlanePaths rejects two configured data-plane paths that
// duplicate or nest, because the router could not serve both unambiguously.
func rejectOverlappingDataPlanePaths(paths []string) error {
	for i, existing := range paths {
		for _, s := range paths[i+1:] {
			if s == existing || strings.HasPrefix(s, existing+"/") || strings.HasPrefix(existing, s+"/") {
				return fmt.Errorf("diagnostics: paths %q and %q overlap or duplicate (normalize trailing slashes)", existing, s)
			}
		}
	}
	return nil
}

// collectDataPlanePaths returns every operator-configured data-plane path in cfg,
// normalized to an absolute path without a trailing separator, in a stable order.
//
// This is the single chokepoint for the configurable data-plane path surface:
// every diagnostics, metrics, model-diagnostics, secure-session and protected
// operator mount path is collected here, and the same set is what rejects
// duplicate and nested paths. Anything that needs to know which paths a
// configuration publishes must read them from here, so a newly configurable mount
// path cannot join the surface without also becoming visible to that consumer.
//
// Collection reports the same per-field errors validation always has. A
// configuration that reaches a consumer has already been validated, so the error
// is unreachable there; [ConfiguredDataPlanePaths] wraps this for those callers.
func collectDataPlanePaths(cfg *Config) ([]string, error) {
	if cfg == nil {
		return nil, nil
	}
	norm := func(p string) string {
		return strings.TrimSuffix(strings.TrimSpace(p), "/")
	}
	check := func(name, p string) (string, error) {
		return validateConfiguredMountPath("diagnostics."+name, p, true)
	}
	paths := make([]string, 0, 16)
	add := func(s string) {
		if s == "" {
			return
		}
		paths = append(paths, s)
	}
	hp, err := check("health_path", cfg.Diagnostics.HealthPath)
	if err != nil {
		return nil, err
	}
	add(hp)
	ap, err := check("attempts_path", cfg.Diagnostics.AttemptsPath)
	if err != nil {
		return nil, err
	}
	add(ap)
	ip, err := check("inventory_path", cfg.Diagnostics.InventoryPath)
	if err != nil {
		return nil, err
	}
	add(ip)
	rt, err := check("route_trace_path", cfg.Diagnostics.RouteTracePath)
	if err != nil {
		return nil, err
	}
	add(rt)
	pp, err := check("pprof_path", cfg.Diagnostics.PprofPath)
	if err != nil {
		return nil, err
	}
	add(pp)
	if cfg.SecureSessionEffectivelyEnabled() {
		ssp, err := validateConfiguredMountPath("secure_session.diagnostics_path_prefix", cfg.SecureSession.DiagnosticsPathPrefix, true)
		if err != nil {
			return nil, err
		}
		add(ssp)
	}
	mp, err := checkObservabilityMetricsPath(cfg)
	if err != nil {
		return nil, err
	}
	add(mp)
	mcd, err := validateConfiguredMountPath("model_catalog.diagnostics_path", cfg.ModelCatalog.DiagnosticsPath, true)
	if err != nil {
		return nil, err
	}
	add(mcd)
	mid, err := validateConfiguredMountPath("model_inventory.diagnostics_path", cfg.ModelInventory.DiagnosticsPath, true)
	if err != nil {
		return nil, err
	}
	add(mid)
	// accounting.admin.path, authority query, control_plane query, routing
	// override admin, and billing reports are protected operator mounts and
	// must satisfy the same dot-segment and percent-escape rules.
	for _, mount := range protectedMountPaths {
		resolved, err := validateProtectedMountPath(cfg, mount, norm)
		if err != nil {
			return nil, err
		}
		add(resolved)
	}
	return paths, nil
}

// ConfiguredDataPlanePaths returns every operator-configured data-plane path in
// cfg, normalized, from the same chokepoint that validates them.
//
// It exists so a default-on request-graph layer can know which paths the operator
// actually published without keeping its own copy of the configurable path
// surface. Those values are any normalized absolute non-root path, which includes
// paths inside the frozen ingress impossible-path families, so a consumer that
// refuses requests before the router must treat a published path as authoritative.
//
// The error is deliberately dropped: it is produced only by the same per-field
// checks [Validate] already runs, so a configuration that reached a caller has
// none. A caller that needs the error uses validation instead.
func ConfiguredDataPlanePaths(cfg *Config) []string {
	paths, err := collectDataPlanePaths(cfg)
	if err != nil {
		return nil
	}
	return paths
}

func checkObservabilityMetricsPath(cfg *Config) (string, error) {
	if cfg == nil || !cfg.Observability.Metrics.Enabled {
		return "", nil
	}
	return validateConfiguredMountPath("observability.metrics.path", cfg.Observability.Metrics.Path, true)
}

func validateObservability(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if cfg.Observability.Metrics.Enabled {
		p := strings.TrimSpace(cfg.Observability.Metrics.Path)
		if p == "" {
			return fmt.Errorf("observability.metrics.path: required when observability.metrics.enabled is true")
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("observability.metrics.path: must start with /")
		}
		if err := rejectHTTPPathDotDot("observability.metrics.path", p); err != nil {
			return err
		}
	}
	if cfg.Observability.Tracing.Enabled {
		if sr := cfg.Observability.Tracing.SampleRatio; sr != nil {
			r := *sr
			if r <= 0 || r > 1 {
				return fmt.Errorf("observability.tracing.sample_ratio: must be > 0 and <= 1 when set (got %v)", r)
			}
		}
	}
	// Collapse thresholds are validated even when metrics are disabled so a
	// latent typo cannot surprise a later enablement; with metrics disabled no
	// detector runs regardless of these values.
	if _, err := CompilePromptCacheCollapse(cfg.Observability.Metrics.PromptCacheCollapse); err != nil {
		return err
	}
	return nil
}

func normalizeLogging(cfg *Config) {
	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "info"
	}
	if strings.TrimSpace(cfg.Logging.Format) == "" {
		cfg.Logging.Format = "json"
	}
}

func validateLogging(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	normalizeLogging(cfg)
	lvl := strings.ToLower(strings.TrimSpace(cfg.Logging.Level))
	switch lvl {
	case "debug", "info", "warn", "error":
		cfg.Logging.Level = lvl
	default:
		return fmt.Errorf("logging.level: unknown %q (want debug, info, warn, error)", cfg.Logging.Level)
	}
	f := strings.ToLower(strings.TrimSpace(cfg.Logging.Format))
	switch f {
	case "json", "text":
		cfg.Logging.Format = f
	default:
		return fmt.Errorf("logging.format: unknown %q (want json, text)", cfg.Logging.Format)
	}
	for i, p := range cfg.Logging.AccessLogSkipPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("logging.access_log_skip_paths[%d]: empty entry", i)
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("logging.access_log_skip_paths[%d]: must start with /", i)
		}
		if err := rejectHTTPPathDotDot(fmt.Sprintf("logging.access_log_skip_paths[%d]", i), p); err != nil {
			return err
		}
		cfg.Logging.AccessLogSkipPaths[i] = p
	}
	return nil
}

func validateRoutingHealth(cfg *Config) error {
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return nil
	}
	if cb.FailureThreshold < 1 {
		return fmt.Errorf("routing.health.circuit_breaker: failure_threshold must be >= 1 when enabled")
	}
	raw := strings.TrimSpace(cb.OpenFor)
	if err := parsePositiveDurationField("routing.health.circuit_breaker.open_for", raw); err != nil {
		return err
	}
	return nil
}

func validateRoutingAffinity(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(cfg.Routing.Affinity.Store))
	if store == "" {
		store = "memory"
	}
	if err := normalizeEnum(&store, "routing.affinity.store", "memory", "memory"); err != nil {
		return err
	}
	if err := normalizeEnum(&cfg.Routing.Affinity.MissingIdentity, "routing.affinity.missing_identity", "fail_closed", "ignore", "fail_closed"); err != nil {
		return err
	}
	cfg.Routing.Affinity.Store = store
	return nil
}

func validatePluginSlice(section string, rows []PluginConfig) error {
	seen := make(map[string]struct{})
	for _, p := range rows {
		id := p.InstanceID()
		if id == "" {
			return fmt.Errorf("%s: plugin row requires non-empty id", section)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s: duplicate plugin instance id %q", section, id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(p.FactoryID()) == "" {
			return fmt.Errorf("%s: plugin %q missing factory kind (set kind or id)", section, id)
		}
	}
	return nil
}

func validateContinuityStores(cfg *Config) error {
	// Validate the declared name before the legacy switch can mask it.
	declared := strings.ToLower(strings.TrimSpace(cfg.Continuity.Store))
	switch declared {
	case "", ContinuityStoreMemory, ContinuityStoreSQLite, ContinuityStorePostgres:
	default:
		return fmt.Errorf("continuity.store: want memory, sqlite, or postgres, got %q", cfg.Continuity.Store)
	}
	// in_memory is the legacy spelling of "store: memory" and keeps forcing the
	// process-local backing. The store field decides the backing whenever
	// in_memory does not, and an omitted store defaults to the same backing, so
	// the whole continuity block may be omitted.
	//
	// The two spellings may not disagree. in_memory: true with a durable store
	// asked for two backends at once, and the durable one was silently
	// downgraded to the process-local store. The contradiction is refused here,
	// where check-config and the serving runtime both see it, and the remedy is
	// named.
	if err := validateContinuityStoreSpelling(cfg.Continuity); err != nil {
		return err
	}
	store := EffectiveContinuityStore(cfg.Continuity)
	if store == ContinuityStoreMemory {
		if err := validateIgnoredContinuitySQLitePath(cfg.Continuity); err != nil {
			return err
		}
		if cfg.Continuity.MaxLegs < 0 {
			return fmt.Errorf("continuity: max_legs must be >= 0 for memory store")
		}
		if d := strings.TrimSpace(cfg.Continuity.PostgresDSN); d != "" {
			return fmt.Errorf("continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"")
		}
		// The memory backing is the only one that reads ttl, so its value is
		// validated here rather than at store construction: check-config and
		// the serving runtime must accept and refuse the same configurations.
		if err := validateContinuityTTL(cfg.Continuity.TTL); err != nil {
			return err
		}
		return nil
	}
	if d := strings.TrimSpace(cfg.Continuity.PostgresDSN); d != "" && store != "postgres" {
		return fmt.Errorf("continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"")
	}
	if store == "sqlite" {
		path := strings.TrimSpace(cfg.Continuity.SQLitePath)
		if path == "" {
			return fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		if strings.ContainsAny(path, "\x00?#&") {
			return fmt.Errorf(
				"continuity.sqlite_path: must not contain NUL, ?, #, or & " +
					"(ambiguous with SQLite URI query)",
			)
		}
		if strings.TrimSpace(cfg.Continuity.TTL) != "" {
			return fmt.Errorf(
				"continuity: ttl is not supported for sqlite store (memory-only); remove ttl or use store: memory",
			)
		}
		if cfg.Continuity.MaxLegs != 0 {
			return fmt.Errorf(
				"continuity: max_legs is not supported for sqlite store (memory-only); " +
					"remove max_legs or use store: memory",
			)
		}
		return nil
	}
	// store == "postgres"
	if err := validateIgnoredContinuitySQLitePath(cfg.Continuity); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Continuity.TTL) != "" {
		return fmt.Errorf(
			"continuity: ttl is not supported for postgres store (memory-only); remove ttl or use store: memory",
		)
	}
	if cfg.Continuity.MaxLegs != 0 {
		return fmt.Errorf(
			"continuity: max_legs is not supported for postgres store (memory-only); " +
				"remove max_legs or use store: memory",
		)
	}
	dsn := strings.TrimSpace(cfg.Continuity.PostgresDSN)
	if dsn == "" {
		return fmt.Errorf("continuity.postgres_dsn: required when store is \"postgres\"")
	}
	if strings.Contains(dsn, "\x00") {
		return fmt.Errorf("continuity.postgres_dsn: must not contain NUL")
	}
	return nil
}

// validateContinuityStoreSpelling refuses the contradiction between the legacy
// in_memory flag and a durable backing name. in_memory: true is the legacy
// spelling of "store: memory", so it may only accompany an omitted store or an
// explicit "store: memory". Every other combination names two backings at
// once, and the process-local one would silently win.
func validateContinuityStoreSpelling(c ContinuityConfig) error {
	if !c.InMemory {
		return nil
	}
	declared := strings.ToLower(strings.TrimSpace(c.Store))
	if declared == "" || declared == ContinuityStoreMemory {
		return nil
	}
	return fmt.Errorf(
		"continuity.in_memory: true asks for the process-local backing, but continuity.store is %q; "+
			"remove in_memory or set store: memory",
		declared,
	)
}

// validateIgnoredContinuitySQLitePath refuses a sqlite_path on a backing that
// never reads it. Only the sqlite store opens the path, so accepting it next to
// the memory or postgres backing created a false expectation of persistence.
func validateIgnoredContinuitySQLitePath(c ContinuityConfig) error {
	if strings.TrimSpace(c.SQLitePath) == "" {
		return nil
	}
	return fmt.Errorf(
		"continuity.sqlite_path: may only be set when store is \"sqlite\"; " +
			"remove sqlite_path or set store: sqlite",
	)
}

// validateContinuityTTL checks the memory backing's eviction window. The runtime
// parses the same value when it constructs the store, so both refuse the same
// spellings and report the same reason.
func validateContinuityTTL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("continuity.ttl: %w", err)
	}
	if d < 0 {
		return fmt.Errorf("continuity.ttl must be non-negative")
	}
	return nil
}

func validateDatabaseConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	_, err := ParseDatabasePoolSettings(cfg.Database)
	return err
}

func validatePostgresPoolBound(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	if usesManagedPostgres(cfg) && cfg.Database.MaxOpenConns <= 0 {
		return fmt.Errorf("database.max_open_conns: required when any store is postgres")
	}
	return nil
}

func validateModelCatalog(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	mc := &cfg.ModelCatalog
	if mc.Enabled {
		if strings.TrimSpace(mc.CachePath) == "" {
			return fmt.Errorf("model_catalog.cache_path: required when model_catalog.enabled is true")
		}
	}
	if mc.ExternalUpdatesEnabled {
		if strings.TrimSpace(mc.CachePath) == "" {
			return fmt.Errorf("model_catalog.cache_path: required when model_catalog.external_updates_enabled is true")
		}
		su := strings.TrimSpace(mc.SourceURL)
		if su == "" {
			return fmt.Errorf("model_catalog.source_url: required when model_catalog.external_updates_enabled is true")
		}
		u, err := url.Parse(su)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("model_catalog.source_url: invalid URL")
		}
		if u.Scheme != "https" {
			return fmt.Errorf("model_catalog.source_url: want https URL when model_catalog.external_updates_enabled is true")
		}
		ui := strings.TrimSpace(mc.UpdateInterval)
		if err := parsePositiveDurationFieldRequired("model_catalog.update_interval", ui); err != nil {
			if ui == "" {
				return fmt.Errorf(
					"model_catalog.update_interval: must be positive when " +
						"model_catalog.external_updates_enabled is true",
				)
			}
			return err
		}
	}
	if su := strings.TrimSpace(mc.SourceURL); su != "" {
		u, err := url.Parse(su)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("model_catalog.source_url: invalid URL")
		}
	}
	ft := strings.TrimSpace(mc.FetchTimeout)
	if err := parsePositiveDurationField("model_catalog.fetch_timeout", ft); err != nil {
		if d, perr := time.ParseDuration(ft); perr == nil && d <= 0 {
			return fmt.Errorf("model_catalog.fetch_timeout: must be a positive duration when set")
		}
		return err
	}
	posLimit := func(field string, v *int64) error {
		if v == nil {
			return nil
		}
		if *v <= 0 {
			return fmt.Errorf("model_catalog: %s must be positive when set", field)
		}
		return nil
	}
	for i, row := range mc.ModelOverrides {
		if strings.TrimSpace(row.Model) == "" {
			return fmt.Errorf("model_catalog.model_overrides[%d].model: required", i)
		}
		if err := posLimit("context_limit_tokens", row.ContextLimitTokens); err != nil {
			return err
		}
		if err := posLimit("input_limit_tokens", row.InputLimitTokens); err != nil {
			return err
		}
		if err := posLimit("output_limit_tokens", row.OutputLimitTokens); err != nil {
			return err
		}
	}
	for i, row := range mc.BackendModelOverrides {
		if strings.TrimSpace(row.Backend) == "" {
			return fmt.Errorf("model_catalog.backend_model_overrides[%d].backend: required", i)
		}
		if strings.TrimSpace(row.Model) == "" {
			return fmt.Errorf("model_catalog.backend_model_overrides[%d].model: required", i)
		}
		if err := posLimit("context_limit_tokens", row.ContextLimitTokens); err != nil {
			return err
		}
		if err := posLimit("input_limit_tokens", row.InputLimitTokens); err != nil {
			return err
		}
		if err := posLimit("output_limit_tokens", row.OutputLimitTokens); err != nil {
			return err
		}
	}
	return nil
}

func validateModelInventory(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	mi := &cfg.ModelInventory
	if strings.TrimSpace(mi.RefreshInterval) == "" {
		mi.RefreshInterval = DefaultModelInventoryRefreshInterval.String()
	}
	if strings.TrimSpace(mi.FetchTimeout) == "" {
		mi.FetchTimeout = DefaultModelInventoryFetchTimeout.String()
	}
	d, err := time.ParseDuration(strings.TrimSpace(mi.RefreshInterval))
	if err != nil {
		return fmt.Errorf("model_inventory.refresh_interval: %w", err)
	}
	if d < DefaultModelInventoryRefreshInterval {
		return fmt.Errorf("model_inventory.refresh_interval: must be at least %s", DefaultModelInventoryRefreshInterval)
	}
	ft, err := time.ParseDuration(strings.TrimSpace(mi.FetchTimeout))
	if err != nil {
		return fmt.Errorf("model_inventory.fetch_timeout: %w", err)
	}
	if ft <= 0 {
		return fmt.Errorf("model_inventory.fetch_timeout: must be a positive duration")
	}
	return nil
}

func validateRoutingAttemptPolicy(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	raw := strings.TrimSpace(strings.ToLower(cfg.Routing.AttemptPolicy))
	if raw == "" {
		return nil
	}
	switch raw {
	case string(aipapi.AttemptLogSkip), string(aipapi.AttemptReject):
		return nil
	default:
		return fmt.Errorf("invalid routing.attempt_policy %q: must be %q or %q",
			cfg.Routing.AttemptPolicy, aipapi.AttemptLogSkip, aipapi.AttemptReject)
	}
}

func validateRoutingExecutionComposition(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	raw := strings.TrimSpace(string(cfg.Routing.ExecutionCompositionPolicy))
	if raw == "" {
		return nil
	}
	switch ExecutionCompositionPolicy(raw) {
	case ExecutionCompositionSafe, ExecutionCompositionUnrestricted:
		return nil
	default:
		return fmt.Errorf("invalid routing.execution_composition_policy %q: must be %q or %q",
			cfg.Routing.ExecutionCompositionPolicy, ExecutionCompositionSafe, ExecutionCompositionUnrestricted)
	}
}
