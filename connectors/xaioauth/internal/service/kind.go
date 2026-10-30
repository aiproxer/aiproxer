package service

import "time"

const (
	PluginID            = "com.aiproxer.backend.xaioauth"
	FactoryKind         = "xai-oauth"
	DisplayName         = "xAI OAuth"
	Description         = "xAI subscription OAuth connector via OIDC and OpenAI Chat"
	DefaultIssuerURL    = "https://auth.x.ai"
	DefaultInferenceURL = "https://api.x.ai/v1"
	DefaultUserAgent    = "aiproxer/0.1.0 (xai-oauth)"
	DefaultModel        = "grok-2"
	DefaultHTTPTimeout  = 60 * time.Second
	TokenRefreshSkew    = 60 * time.Second
)
