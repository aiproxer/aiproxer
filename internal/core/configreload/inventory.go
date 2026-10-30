package configreload

import "slices"

// FieldDisposition is the maintained inventory disposition for one path.
// Every inventoried path must set exactly one; there is no implicit default.
type FieldDisposition string

const (
	DispositionReloadable  FieldDisposition = "reloadable"
	DispositionStartupOnly FieldDisposition = "startup_only"
	DispositionConditional FieldDisposition = "conditional"
	DispositionMixed       FieldDisposition = "mixed"
)

// Valid reports whether d is an explicit disposition.
func (d FieldDisposition) Valid() bool {
	switch d {
	case DispositionReloadable, DispositionStartupOnly, DispositionConditional, DispositionMixed:
		return true
	default:
		return false
	}
}

// FieldClass is one inventoried top-level section or startup override.
// SecretBearing is orthogonal to Disposition; errors never include values.
type FieldClass struct {
	Path          string
	Disposition   FieldDisposition
	SecretBearing bool
	Notes         string
}

// RequiredTopLevelPaths lists every current YAML top-level Config section.
func RequiredTopLevelPaths() []string {
	return []string{
		"server", "access", "auth", "logging", "diagnostics", "observability",
		"http_client", "http_headers", "database", "routing", "continuity", "secure_session",
		"stream_recovery", "empty_visible_turn_retry", "hooks", "accounting", "interleaved", "plugins",
		"model_aliases", "model_catalog", "model_inventory",
		"control_plane", "metering", "identity", "upstream_credentials",
	}
}

// RequiredStartupOverridePaths lists fixed CLI/env overrides (req 7.7).
func RequiredStartupOverridePaths() []string {
	return []string{
		"override.cli.config",
		"override.cli.multi_user",
		"override.cli.auto_resume",
		"override.cli.auto_resume_idle_timeout",
		"override.cli.auto_resume_grace_period",
		"override.env.AIP_AUTO_RESUME",
		"override.env.AIP_AUTO_RESUME_IDLE_TIMEOUT",
		"override.env.AIP_AUTO_RESUME_GRACE_PERIOD",
		"override.env.AIP_AUTO_RESUME_POST_OUTPUT_POLICY",
	}
}

// Inventory returns the authoritative reloadability classification table.
func Inventory() []FieldClass { return slices.Clone(inventoryTable) }

// TypedComparatorSections lists every inventoried path covered by a maintained
// typed comparator in Classify (requirement 7.2 / task 2.2 FieldCoverage).
func TypedComparatorSections() []string {
	out := make([]string, 0, len(inventoryTable))
	for _, e := range inventoryTable {
		out = append(out, e.Path)
	}
	return out
}

// MissingClassifications returns declared paths absent from inventoried paths.
func MissingClassifications(declared, inventoried []string) []string {
	have := make(map[string]struct{}, len(inventoried))
	for _, p := range inventoried {
		have[p] = struct{}{}
	}
	var missing []string
	for _, p := range declared {
		if _, ok := have[p]; !ok {
			missing = append(missing, p)
		}
	}
	slices.Sort(missing)
	return missing
}

var inventoryTable = []FieldClass{
	{Path: "server", Disposition: DispositionMixed, Notes: "listener/timeouts/decode budgets/inflight spool limit startup-only; request limits reloadable"},
	{Path: "access", Disposition: DispositionStartupOnly, Notes: "access mode startup-only (req 7.3)"},
	{Path: "auth", Disposition: DispositionMixed, SecretBearing: true, Notes: "handler class startup-only; local_api_keys reloadable"},
	{Path: "logging", Disposition: DispositionStartupOnly, Notes: "logger sink/format process-owned"},
	{Path: "diagnostics", Disposition: DispositionStartupOnly, SecretBearing: true, Notes: "paths + shared_secret process-owned"},
	{Path: "observability", Disposition: DispositionStartupOnly, Notes: "metrics/tracing/collapse-threshold topology startup-only"},
	{Path: "http_client", Disposition: DispositionReloadable, Notes: "generation-owned HTTP tuning"},
	{Path: "http_headers", Disposition: DispositionReloadable, Notes: "inbound header aliases for auth/route/session/trace"},
	{Path: "database", Disposition: DispositionStartupOnly, Notes: "pool topology startup-only"},
	{Path: "routing", Disposition: DispositionMixed, Notes: "affinity store startup-only; selectors/health reloadable"},
	{Path: "continuity", Disposition: DispositionStartupOnly, SecretBearing: true, Notes: "store type/path/DSN process-owned"},
	{Path: "secure_session", Disposition: DispositionMixed, SecretBearing: true, Notes: "store/DSN/fingerprint startup-only; policy reloadable"},
	{Path: "stream_recovery", Disposition: DispositionReloadable, Notes: "request/stream policy; CLI/env overrides fixed"},
	{Path: "empty_visible_turn_retry", Disposition: DispositionReloadable, Notes: "generation-owned corrective retry gate; pinned per in-flight turn"},
	{Path: "hooks", Disposition: DispositionReloadable, Notes: "request-plane hook policy"},
	{Path: "accounting", Disposition: DispositionMixed, SecretBearing: true, Notes: "store topology startup-only; pricing/preflight reloadable"},
	{Path: "interleaved", Disposition: DispositionReloadable, Notes: "request-plane thinking policy"},
	{Path: "plugins", Disposition: DispositionConditional, SecretBearing: true, Notes: "rows reloadable when factory catalog fixed"},
	{Path: "model_aliases", Disposition: DispositionReloadable, Notes: "routing alias rows"},
	{Path: "model_catalog", Disposition: DispositionConditional, Notes: "cache/worker restart; overrides reloadable"},
	{Path: "model_inventory", Disposition: DispositionConditional, Notes: "cache/refresh worker restart; diagnostics reloadable"},
	{Path: "control_plane", Disposition: DispositionMixed, SecretBearing: true, Notes: "store topology startup-only; query policy reloadable"},
	{Path: "metering", Disposition: DispositionMixed, SecretBearing: true, Notes: "journal topology startup-only; enablement reloadable"},
	{Path: "identity", Disposition: DispositionReloadable, Notes: "generation identity projection"},
	{Path: "upstream_credentials", Disposition: DispositionReloadable, Notes: "numbered-env scan bound and quota-switch threshold; re-resolved per candidate"},

	{Path: "override.cli.config", Disposition: DispositionStartupOnly, Notes: "fixed source path"},
	{Path: "override.cli.multi_user", Disposition: DispositionStartupOnly, Notes: "multi-user CLI gate"},
	{Path: "override.cli.auto_resume", Disposition: DispositionStartupOnly, Notes: "stream-recovery CLI override"},
	{Path: "override.cli.auto_resume_idle_timeout", Disposition: DispositionStartupOnly, Notes: "stream-recovery CLI override"},
	{Path: "override.cli.auto_resume_grace_period", Disposition: DispositionStartupOnly, Notes: "stream-recovery CLI override"},
	{Path: "override.env.AIP_AUTO_RESUME", Disposition: DispositionStartupOnly, Notes: "stream-recovery env override"},
	{Path: "override.env.AIP_AUTO_RESUME_IDLE_TIMEOUT", Disposition: DispositionStartupOnly, Notes: "stream-recovery env override"},
	{Path: "override.env.AIP_AUTO_RESUME_GRACE_PERIOD", Disposition: DispositionStartupOnly, Notes: "stream-recovery env override"},
	{Path: "override.env.AIP_AUTO_RESUME_POST_OUTPUT_POLICY", Disposition: DispositionStartupOnly, Notes: "stream-recovery env override"},
}
