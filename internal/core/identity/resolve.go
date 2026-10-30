package identity

// ResolvedValue returns the concrete identity string for emission when the mode
// produces a fixed value (proxy or custom). Passthrough and drop return "".
func (f FieldPolicy) ResolvedValue(proxyDefault string) string {
	switch f.Mode {
	case ModeProxy:
		return proxyDefault
	case ModeCustom:
		return f.Value
	default:
		return ""
	}
}

// UserAgentValue returns the proxy/custom User-Agent string for the field.
func (e EffectiveUpstream) UserAgentValue() string {
	return e.UserAgent.ResolvedValue(DefaultProductName)
}

// AppURLValue returns the proxy/custom OpenRouter app URL for the field.
func (e EffectiveUpstream) AppURLValue() string {
	return e.AppURL.ResolvedValue(DefaultProjectURL)
}

// AppTitleValue returns the proxy/custom OpenRouter app title for the field.
func (e EffectiveUpstream) AppTitleValue() string {
	return e.AppTitle.ResolvedValue(DefaultProductName)
}

// ServerValue returns the proxy/custom Server header value.
func (e EffectiveDownstream) ServerValue() string {
	return e.Server.ResolvedValue(DefaultProductName)
}
