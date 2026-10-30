package service

import "time"

const (
	PluginID                = "com.aiproxer.backend.minimexoauth"
	FactoryKind             = "minimax-oauth"
	DisplayName             = "MiniMax OAuth"
	Description             = "MiniMax subscription OAuth connector via PKCE and Anthropic Messages"
	DefaultPortalBaseURL    = "https://api.minimax.io"
	DefaultInferenceBaseURL = "https://api.minimax.io/anthropic"
	CNPortalBaseURL         = "https://api.minimaxi.com"
	CNInferenceBaseURL      = "https://api.minimaxi.com/anthropic"
	DefaultScope            = "group_id profile model.completion"
	DefaultGrantType        = "urn:ietf:params:oauth:grant-type:user_code"
	DefaultUserAgent        = "aiproxer/0.1.0 (minimax-oauth)"
	ModelM27                = "MiniMax-M2.7"
	ModelM27Highspeed       = "MiniMax-M2.7-highspeed"
	DefaultHTTPTimeout      = 60 * time.Second
	TokenRefreshSkew        = 60 * time.Second
	ErrorBodyLimit          = 16 * 1024
)
