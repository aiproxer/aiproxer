// Package service implements the openai-chatgpt-plan backend plugin service.
//
// The connector owns the official OpenAI ChatGPT-plan provider boundary: the
// Sign in with ChatGPT (SIWC) registration and profile state, the public
// Responses transport, and the account-scoped model inventory. It reaches the
// host only through the versioned backend-plugin ABI and never imports host
// internals or the legacy private-Codex connector.
package service

const (
	// FactoryKind is the backend kind of the direct-inference export. It is
	// distinct from the legacy private-Codex kinds and from the app-server
	// kind that the agent-runtime phase reserves.
	FactoryKind = "openai-chatgpt-plan"

	// PluginID identifies this connector executable in packaged manifests and
	// release metadata.
	PluginID = "com.aiproxer.backend.openai-chatgpt-plan"

	// DisplayName is the operator-facing name of the direct export.
	DisplayName = "OpenAI ChatGPT Plan"

	// Description states the provider boundary of the direct export.
	Description = "OpenAI ChatGPT plan inference through official Sign in with ChatGPT (SIWC)"

	// Version and BuildID identify the packaged connector build.
	Version = "0.1.0"
	BuildID = "localdev"
)
