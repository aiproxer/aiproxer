package service

const (
	FactoryKind = "openrouter"
	PluginID    = "com.aiproxer.backend.openrouter"
	DefaultURL  = "https://openrouter.ai/api/v1"

	// DefaultAppURL / DefaultAppTitle are proxy-mode OpenRouter attribution defaults.
	DefaultAppURL   = "https://github.com/aiproxer/aiproxer"
	DefaultAppTitle = "AIProxer"

	extHTTPReferer     = "openrouter.http_referer"
	extTitle           = "openrouter.title"
	extCategories      = "openrouter.categories"
	extMetadataHeader  = "openrouter.metadata_header"
	extProvider        = "openrouter.provider"
	extModels          = "openrouter.models"
	extRoute           = "openrouter.route"
	extPlugins         = "openrouter.plugins"
	extPrediction      = "openrouter.prediction"
	extDebug           = "openrouter.debug"
	extServiceTier     = "openrouter.service_tier"
	extSessionID       = "openrouter.session_id"
	extStopServerTools = "openrouter.stop_server_tools_when"
	extTrace           = "openrouter.trace"
	extInclude         = "openrouter.include"
	extUser            = "openrouter.user"
	extResponseFormat  = "openrouter.response_format"
	extReasoning       = "openrouter.reasoning"
	extUpstreamFlavor  = "openrouter.upstream_flavor"
	extraBodyPrefix    = "openai.extra_body."
)
