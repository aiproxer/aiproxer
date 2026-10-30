package config

// AccessConfig selects deployment access posture (single-user vs multi-user).
// Empty Mode is normalized to single_user during validation/load.
type AccessConfig struct {
	Mode        string            `yaml:"mode"`
	GeoIP       GeoIPConfig       `yaml:"geoip"`
	SelfDefense SelfDefenseConfig `yaml:"self_defense"`
}

// SelfDefenseConfig is the typed ingress self-defense surface. Enabled and
// ImpossiblePaths are presence-aware so omitted means the documented default
// true while explicit false is preserved. No process state is represented here.
type SelfDefenseConfig struct {
	Enabled         *bool                     `yaml:"enabled"`
	ImpossiblePaths *bool                     `yaml:"impossible_paths"`
	Adaptive        SelfDefenseAdaptiveConfig `yaml:"adaptive"`
}

// SelfDefenseAdaptiveConfig carries the request-policy fields (reloadable) plus
// the process-state sizing fields (restart-required in v1).
type SelfDefenseAdaptiveConfig struct {
	AuthFailures      *int     `yaml:"auth_failures"`
	Window            string   `yaml:"window"`
	InitialQuarantine string   `yaml:"initial_quarantine"`
	MaxQuarantine     string   `yaml:"max_quarantine"`
	StateTTL          string   `yaml:"state_ttl"`
	MaxEntries        *int     `yaml:"max_entries"`
	ExemptCIDRs       []string `yaml:"exempt_cidrs"`
}

// GeoIPConfig describes the reloadable request-plane policy and the optional
// process-owned country database source.
type GeoIPConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Order    string            `yaml:"order"`
	Allow    GeoIPRuleConfig   `yaml:"allow"`
	Deny     GeoIPRuleConfig   `yaml:"deny"`
	ClientIP GeoIPClientConfig `yaml:"client_ip"`
	Database GeoIPDBConfig     `yaml:"database"`
}

type GeoIPRuleConfig struct {
	Countries []string `yaml:"countries"`
	CIDRs     []string `yaml:"cidrs"`
}

type ClientIPSource string

const (
	ClientIPSourceDirect        ClientIPSource = "direct"
	ClientIPSourceXForwardedFor ClientIPSource = "x_forwarded_for"
	ClientIPSourceForwarded     ClientIPSource = "forwarded"
)

type GeoIPClientConfig struct {
	Source         ClientIPSource `yaml:"source"`
	TrustedProxies []string       `yaml:"trusted_proxies"`
}

type GeoIPDatabaseSource string

const (
	GeoIPDatabaseSourceManaged GeoIPDatabaseSource = "managed"
	GeoIPDatabaseSourceLocal   GeoIPDatabaseSource = "local"
)

type GeoIPDBConfig struct {
	Source    GeoIPDatabaseSource `yaml:"source"`
	Edition   string              `yaml:"edition"`
	Directory string              `yaml:"directory"`
	LocalPath string              `yaml:"local_path"`
	Update    GeoIPUpdateConfig   `yaml:"update"`
}

type GeoIPUpdateConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"`
}

// AuthLocalAPIKeyRecord is one operator-configured API key (secret material belongs
// in config files only; validation and redaction are handled elsewhere).
// Key must be at least 16 Unicode code points after trimming (enforced with core auth validation).
type AuthLocalAPIKeyRecord struct {
	KeyID       string `yaml:"key_id"`
	PrincipalID string `yaml:"principal_id"`
	Key         string `yaml:"key"`
	// Attribution carries optional operator-controlled safe attribution for this key.
	// Missing optional fields remain unknown (no inference). Raw secrets and transport
	// headers must never be placed here.
	Attribution AuthLocalAttribution `yaml:"attribution"`
}

// AuthLocalAttribution mirrors [coreauth.LocalAttribution] for YAML decoding. Zero values
// mean "not configured".
type AuthLocalAttribution struct {
	DisplayName    string            `yaml:"display_name"`
	AuthMethod     string            `yaml:"auth_method"`
	TenantID       string            `yaml:"tenant_id"`
	OrganizationID string            `yaml:"organization_id"`
	WorkspaceID    string            `yaml:"workspace_id"`
	ProjectID      string            `yaml:"project_id"`
	DepartmentID   string            `yaml:"department_id"`
	CostCenterID   string            `yaml:"cost_center_id"`
	Roles          []string          `yaml:"roles"`
	SafeClaims     map[string]string `yaml:"safe_claims"`
	PolicyLabels   map[string]string `yaml:"policy_labels"`
}

// AuthRemoteConfig holds opaque placeholders for future remote auth wiring.
// No network clients are constructed from these fields in the OSS core.
type AuthRemoteConfig struct {
	Endpoint string `yaml:"endpoint"`
	Handler  string `yaml:"handler"`
}

// AuthConfig selects authentication handler, required level, event delivery policy,
// local key material, and remote delegation placeholders.
type AuthConfig struct {
	Handler            string `yaml:"handler"`
	RequiredLevel      string `yaml:"required_level"`
	EventFailurePolicy string `yaml:"event_failure_policy"`
	// EventDelivery selects how auth/session events are delivered: default (structured log sink),
	// disabled (no sink; explicit no delivery), or custom (requires BuildOptions.AuthEventSink at wiring).
	// Empty behaves like default.
	EventDelivery string                  `yaml:"event_delivery"`
	LocalAPIKeys  []AuthLocalAPIKeyRecord `yaml:"local_api_keys"`
	Remote        AuthRemoteConfig        `yaml:"remote"`
}
