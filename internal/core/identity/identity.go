package identity

// Mode selects how an identity field is produced or suppressed.
type Mode string

const (
	// ModeProxy emits the configured AIProxer product identity (or field default).
	ModeProxy Mode = "proxy"
	// ModePassthrough forwards the client-supplied value when available.
	ModePassthrough Mode = "passthrough"
	// ModeCustom emits FieldPolicy.Value.
	ModeCustom Mode = "custom"
	// ModeDrop omits the header/field entirely.
	ModeDrop Mode = "drop"
)

// FieldPolicy is one configurable identity carrier (mode + optional custom value).
type FieldPolicy struct {
	Mode  Mode   `yaml:"mode"`
	Value string `yaml:"value"`
}

// OpenRouterPolicy configures OpenRouter HTTP-Referer / X-OpenRouter-Title app attribution.
// Vendor wire details stay adapter-local; these are proxy-wide policy knobs only.
type OpenRouterPolicy struct {
	AppURL   FieldPolicy `yaml:"app_url"`
	AppTitle FieldPolicy `yaml:"app_title"`
}

// UpstreamPolicy configures B-leg (proxy -> backend) identity presentation.
type UpstreamPolicy struct {
	UserAgent  FieldPolicy      `yaml:"user_agent"`
	OpenRouter OpenRouterPolicy `yaml:"openrouter"`
}

// DownstreamPolicy configures A-leg (proxy -> client) identity presentation.
// Server allows proxy|custom|drop only (no passthrough).
type DownstreamPolicy struct {
	Server FieldPolicy `yaml:"server"`
}

// Config is the root YAML identity subtree under identity:.
type Config struct {
	Upstream   UpstreamPolicy   `yaml:"upstream"`
	Downstream DownstreamPolicy `yaml:"downstream"`
}

// OpenRouterOverride is a partial OpenRouter attribution override for one backend.
// Nil field pointers mean inherit from the global policy; a non-nil pointer
// (including ModeDrop) replaces that field.
type OpenRouterOverride struct {
	AppURL   *FieldPolicy `yaml:"app_url,omitempty"`
	AppTitle *FieldPolicy `yaml:"app_title,omitempty"`
}

// BackendOverride is a partial backend identity override suitable for later
// standardplugin factory integration. Nil pointers inherit; non-nil applies
// explicitly (ModeDrop is distinct from omission).
type BackendOverride struct {
	UserAgent  *FieldPolicy        `yaml:"user_agent,omitempty"`
	OpenRouter *OpenRouterOverride `yaml:"openrouter,omitempty"`
}

// EffectiveUpstream is the resolved upstream policy after global defaults and
// optional backend override merge.
type EffectiveUpstream struct {
	UserAgent FieldPolicy
	AppURL    FieldPolicy
	AppTitle  FieldPolicy
}

// EffectiveDownstream is the resolved downstream policy after defaults.
type EffectiveDownstream struct {
	Server FieldPolicy
}
