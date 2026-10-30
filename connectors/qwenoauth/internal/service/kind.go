package service

import "time"

const (
	PluginID            = "com.aiproxer.backend.qwenoauth"
	FactoryKind         = "qwen-oauth"
	DisplayName         = "Qwen OAuth"
	Description         = "Qwen Portal subscription OAuth connector via PKCE and OpenAI Chat"
	DefaultTokenURL     = "https://chat.qwen.ai/api/v1/oauth2/token"
	DefaultInferenceURL = "https://portal.qwen.ai/v1"
	DefaultUserAgent    = "aiproxer/0.1.0 (qwen-oauth)"
	DefaultModel        = "qwen-coder-plus"
	DefaultHTTPTimeout  = 60 * time.Second
	TokenRefreshSkew    = 120 * time.Second
)
