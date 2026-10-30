package standardplugins

import "strings"

// DefaultWireModel returns the canonical default upstream model id for a bundled backend factory id,
// or an empty string when the factory kind has no canonical default model.
//
// This mapping is intentionally separate from [*Registry]: the registry holds constructors and mounts;
// DefaultWireModel encodes standard-distribution routing defaults used when configs omit explicit
// route selectors (see config.EffectiveDefaultRouteSelector). Alternate bundles may supply a
// different config.WireModelForBackend via runtimebundle.BuildOptions.WireModel without changing
// registry factories. It must not be duplicated per frontend handler.
//
// A kind without a canonical default is a dynamic-inventory backend: an executable connector whose
// models are discovered at runtime, or a migrated compatible kind whose inventory is configured per
// instance. Such a kind never receives a synthesized route. The configuration names the route
// explicitly instead, because no default model can be known before the inventory is discovered.
func DefaultWireModel(backendID string) string {
	switch strings.TrimSpace(backendID) {
	case "openai-responses", "openai-legacy":
		return "gpt-4o-mini"
	case "anthropic":
		return "claude-3-5-haiku-20241022"
	case "gemini":
		return "gemini-2.0-flash"
	case "bedrock":
		return "anthropic.claude-3-haiku-20240307-v1:0"
	default:
		return ""
	}
}
