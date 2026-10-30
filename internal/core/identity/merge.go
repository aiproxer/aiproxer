package identity

// MergeUpstream resolves effective upstream identity from a global config and an
// optional partial backend override. Zero-value global configs receive defaults
// on a copy (the caller's global is not mutated). Nil override pointers inherit;
// non-nil policies (including ModeDrop) replace the corresponding field.
func MergeUpstream(global Config, override *BackendOverride) EffectiveUpstream {
	g := global
	ApplyDefaults(&g)
	out := EffectiveUpstream{
		UserAgent: g.Upstream.UserAgent,
		AppURL:    g.Upstream.OpenRouter.AppURL,
		AppTitle:  g.Upstream.OpenRouter.AppTitle,
	}
	if override == nil {
		return out
	}
	if override.UserAgent != nil {
		out.UserAgent = *override.UserAgent
	}
	if override.OpenRouter != nil {
		if override.OpenRouter.AppURL != nil {
			out.AppURL = *override.OpenRouter.AppURL
		}
		if override.OpenRouter.AppTitle != nil {
			out.AppTitle = *override.OpenRouter.AppTitle
		}
	}
	return out
}

// EffectiveDownstreamOf returns the resolved downstream identity policy.
// Zero-value configs receive defaults on a copy (cfg is not mutated).
func EffectiveDownstreamOf(cfg Config) EffectiveDownstream {
	c := cfg
	ApplyDefaults(&c)
	return EffectiveDownstream{Server: c.Downstream.Server}
}
