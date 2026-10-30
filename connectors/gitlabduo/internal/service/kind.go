package service

import "time"

const (
	FactoryKind = "gitlab-duo"
	PluginID    = "com.aiproxer.backend.gitlabduo"
	DisplayName = "GitLab Duo"
	Description = "GitLab Duo / DAP HTTP bridge"

	DefaultInstanceURL  = "https://gitlab.com"
	DefaultAIGatewayURL = "https://cloud.gitlab.com"
	DefaultHTTPTimeout  = 60 * time.Second

	// DefaultUserAgent ensures truthful client identification (no OpenCode or VS Code spoofing).
	DefaultUserAgent = "aiproxer/1.0.0 (gitlab-duo connector)"

	// Documented agentic chat models from the pin.
	ModelHaiku45  = "duo-chat-haiku-4-5"
	ModelSonnet45 = "duo-chat-sonnet-4-5"
	ModelOpus45   = "duo-chat-opus-4-5"

	// Underlying Claude models mapped by the provider.
	BackendClaudeHaiku45  = "claude-haiku-4-5-20251001"
	BackendClaudeSonnet45 = "claude-sonnet-4-5-20250929"
	BackendClaudeOpus45   = "claude-opus-4-5-20251101"
)
