package service

import "time"

const (
	FactoryKind = "watsonx"
	PluginID    = "com.aiproxer.backend.watsonx"
	DisplayName = "IBM watsonx.ai"
	Description = "IBM watsonx.ai external backend connector"

	DefaultAPIVersion  = "2024-05-31"
	DefaultIAMOrigin   = "https://iam.cloud.ibm.com"
	DefaultHTTPTimeout = 60 * time.Second
)
