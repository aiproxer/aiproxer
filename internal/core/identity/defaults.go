package identity

import "strings"

// Literal product defaults for proxy identity presentation.
const (
	DefaultProductName = "aiproxer"
	DefaultProjectURL  = "https://github.com/aiproxer/aiproxer"
)

// Field value bounds after trim.
const (
	MaxUserAgentBytes = 512
	MaxServerBytes    = 512
	MaxAppTitleBytes  = 256
	MaxAppURLBytes    = 2048
)

// ApplyDefaults fills empty/whitespace modes with ModeProxy. Proxy-mode values
// stay empty; resolved identity strings come from [FieldPolicy.ResolvedValue].
// [Validate] calls this; prefer Validate for load paths. Nil cfg is a no-op.
func ApplyDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(string(cfg.Upstream.UserAgent.Mode)) == "" {
		cfg.Upstream.UserAgent.Mode = ModeProxy
	}
	if strings.TrimSpace(string(cfg.Upstream.OpenRouter.AppURL.Mode)) == "" {
		cfg.Upstream.OpenRouter.AppURL.Mode = ModeProxy
	}
	if strings.TrimSpace(string(cfg.Upstream.OpenRouter.AppTitle.Mode)) == "" {
		cfg.Upstream.OpenRouter.AppTitle.Mode = ModeProxy
	}
	if strings.TrimSpace(string(cfg.Downstream.Server.Mode)) == "" {
		cfg.Downstream.Server.Mode = ModeProxy
	}
}
