package config

// ModelCatalogConfig controls models.dev catalog usage, local cache, optional external refresh, and operator overrides.
// Concrete fetch/cache adapters live outside core config; this struct is the typed operator surface (see design: ModelCatalogConfig).
type ModelCatalogConfig struct {
	// Enabled when true uses the latest valid local catalog snapshot for request-time decisions (when present).
	Enabled bool `yaml:"enabled"`
	// ExternalUpdatesEnabled when true allows periodic background fetches of the catalog source (independent of Enabled).
	ExternalUpdatesEnabled bool `yaml:"external_updates_enabled"`
	// UpdateInterval is a Go duration string (e.g. "1h") for automatic refresh when ExternalUpdatesEnabled is true.
	UpdateInterval string `yaml:"update_interval"`
	// FetchTimeout is an optional Go duration string applied to catalog HTTP GET when the request context has no
	// deadline (0 or empty = rely on transport/client timeouts only).
	FetchTimeout string `yaml:"fetch_timeout"`
	// SourceURL is the HTTPS (or HTTP) URL for the catalog snapshot when ExternalUpdatesEnabled is true.
	SourceURL string `yaml:"source_url"`
	// CachePath is the local filesystem path for the persisted snapshot file used when Enabled or ExternalUpdatesEnabled.
	CachePath string `yaml:"cache_path"`
	// DiagnosticsPath when non-empty registers catalog status JSON under this absolute URL path (must not overlap other diagnostics paths).
	DiagnosticsPath string `yaml:"diagnostics_path"`
	// ModelOverrides are operator facts keyed by route/catalog model name (see spec requirement 5.1).
	ModelOverrides []ModelCatalogModelOverrideEntry `yaml:"model_overrides"`
	// BackendModelOverrides take precedence over ModelOverrides for matching backend/model pairs (requirement 5.2).
	BackendModelOverrides []ModelCatalogBackendModelOverrideEntry `yaml:"backend_model_overrides"`
}

// ModelCatalogModelOverrideEntry is one model-scoped override row from configuration.
// Optional capability and limit fields use YAML omission for "unknown"; for booleans, true means
// explicitly supported and false means explicitly unsupported (runtimebundle maps into modelcatalog).
type ModelCatalogModelOverrideEntry struct {
	Model string `yaml:"model"`

	Tools             *bool `yaml:"tools,omitempty"`
	StructuredOutputs *bool `yaml:"structured_outputs,omitempty"`
	Reasoning         *bool `yaml:"reasoning,omitempty"`
	Vision            *bool `yaml:"vision,omitempty"`
	Documents         *bool `yaml:"documents,omitempty"`

	ContextLimitTokens *int64 `yaml:"context_limit_tokens,omitempty"`
	InputLimitTokens   *int64 `yaml:"input_limit_tokens,omitempty"`
	OutputLimitTokens  *int64 `yaml:"output_limit_tokens,omitempty"`
}

// ModelCatalogBackendModelOverrideEntry is one backend+model pair override row from configuration.
type ModelCatalogBackendModelOverrideEntry struct {
	Backend string `yaml:"backend"`
	Model   string `yaml:"model"`

	Tools             *bool `yaml:"tools,omitempty"`
	StructuredOutputs *bool `yaml:"structured_outputs,omitempty"`
	Reasoning         *bool `yaml:"reasoning,omitempty"`
	Vision            *bool `yaml:"vision,omitempty"`
	Documents         *bool `yaml:"documents,omitempty"`

	ContextLimitTokens *int64 `yaml:"context_limit_tokens,omitempty"`
	InputLimitTokens   *int64 `yaml:"input_limit_tokens,omitempty"`
	OutputLimitTokens  *int64 `yaml:"output_limit_tokens,omitempty"`
}
