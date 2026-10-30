package aipsdk

// StandardDistributionRequirements lists plugin ids the reference cmd/aipstd distribution
// expects in configuration and registry factories. Single source for mandatory validation.
// Phase 7/8 migrated OpenAI-compatible hosted/local runtimes, OpenCode Go/Zen, and
// Codex (openai-codex / openai-codex-app-server) to external connectors; they are
// discovered via manifests and must not appear here.
func StandardDistributionRequirements() []Requirement {
	return []Requirement{
		{Kind: PluginKindFrontend, ID: "openai-responses"},
		{Kind: PluginKindFrontend, ID: "openai-legacy"},
		{Kind: PluginKindFrontend, ID: "anthropic"},
		{Kind: PluginKindFrontend, ID: "gemini"},
		{Kind: PluginKindBackend, ID: "openai-responses"},
		{Kind: PluginKindBackend, ID: "openai-legacy"},
		{Kind: PluginKindBackend, ID: "anthropic"},
		{Kind: PluginKindBackend, ID: "gemini"},
		{Kind: PluginKindBackend, ID: "bedrock"},
		{Kind: PluginKindFeature, ID: "submit-noop"},
		{Kind: PluginKindFeature, ID: "parts-noop"},
		{Kind: PluginKindFeature, ID: "tool-reactor-noop"},
	}
}
