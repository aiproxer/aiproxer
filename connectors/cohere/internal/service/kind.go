package service

import "time"

const (
	FactoryKind = "cohere"
	PluginID    = "com.aiproxer.backend.cohere"
	DisplayName = "Cohere"
	Description = "Cohere REST connector"

	DefaultOrigin      = "https://api.cohere.com"
	DefaultHTTPTimeout = 60 * time.Second
)
