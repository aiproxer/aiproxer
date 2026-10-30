package service

import "time"

const (
	PluginID             = "com.aiproxer.backend.nousportal"
	FactoryKind          = "nous-portal"
	DisplayName          = "Nous Portal"
	Description          = "Nous Portal inference connector via scoped OAuth JWT"
	DefaultPortalURL     = "https://portal.nousresearch.com"
	DefaultInferenceURL  = "https://inference-api.nousresearch.com/v1"
	DefaultUserAgent     = "aiproxer/0.1.0 (nous-portal)"
	ScopeInferenceInvoke = "inference:invoke"
	DefaultModel         = "nousresearch/hermes-3-llama-3.1-405b"
	DefaultHTTPTimeout   = 60 * time.Second
	TokenRefreshSkew     = 120 * time.Second
)
