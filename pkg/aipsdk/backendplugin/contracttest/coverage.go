package contracttest

// ConnectorFamilyCoverage identifies an executable connector module and the
// implementation family represented by its host-path contract test.
type ConnectorFamilyCoverage struct {
	ModulePath string `json:"module_path"`
	Family     string `json:"family"`
	Subject    string `json:"subject"`
}

// CurrentConnectorFamilyCoverage is the checked-in coverage manifest for the
// executable connector modules. It is intentionally data-only so external
// connector authors can audit coverage without importing connector internals.
var CurrentConnectorFamilyCoverage = []ConnectorFamilyCoverage{
	{ModulePath: "connectors/acp", Family: "acp", Subject: "acp"},
	{ModulePath: "connectors/agycliacp", Family: "acp-derivative", Subject: "agycliacp"},
	{ModulePath: "connectors/claudeagentacp", Family: "acp-derivative", Subject: "claudeagentacp"},
	{ModulePath: "connectors/cursorcliacp", Family: "acp-derivative", Subject: "cursorcliacp"},
	{ModulePath: "connectors/geminicliacp", Family: "acp-derivative", Subject: "geminicliacp"},
	{ModulePath: "connectors/codex", Family: "codex", Subject: "codex"},
	{ModulePath: "connectors/opencode", Family: "opencode", Subject: "opencode"},
	{ModulePath: "connectors/ollama", Family: "ollama", Subject: "ollama"},
	{ModulePath: "connectors/vllm", Family: "vllm", Subject: "vllm"},
	{ModulePath: "connectors/huggingface", Family: "huggingface", Subject: "huggingface"},
	{ModulePath: "connectors/cloudflare", Family: "openai-compatible", Subject: "cloudflare"},
	{ModulePath: "connectors/azure", Family: "openai-compatible", Subject: "azure-openai"},
	{ModulePath: "connectors/nvidia", Family: "openai-compatible", Subject: "nvidia"},
	{ModulePath: "connectors/openrouter", Family: "openai-compatible", Subject: "openrouter"},
	{ModulePath: "connectors/snowflake", Family: "openai-compatible", Subject: "snowflake-cortex"},
	{ModulePath: "connectors/databricks", Family: "openai-compatible", Subject: "databricks-ai"},
	{ModulePath: "connectors/infomaniak", Family: "openai-compatible", Subject: "infomaniak-ai"},
	{ModulePath: "connectors/llamacpp", Family: "openai-compatible", Subject: "llamacpp"},
	{ModulePath: "connectors/lmstudio", Family: "openai-compatible", Subject: "lmstudio"},
	{ModulePath: "connectors/vertex", Family: "vertex", Subject: "vertex"},
	{ModulePath: "connectors/sagemaker", Family: "sagemaker", Subject: "sagemaker"},
	{ModulePath: "connectors/oci", Family: "oci", Subject: "oci-generative-ai"},
	{ModulePath: "connectors/watsonx", Family: "watsonx", Subject: "watsonx"},
	{ModulePath: "connectors/sapaicore", Family: "sapaicore", Subject: "sapaicore"},
	{ModulePath: "connectors/cohere", Family: "cohere", Subject: "cohere"},
	{ModulePath: "connectors/replicate", Family: "replicate", Subject: "replicate"},
	{ModulePath: "connectors/gitlabduo", Family: "gitlabduo", Subject: "gitlabduo"},
	{ModulePath: "connectors/nousportal", Family: "nousportal", Subject: "nousportal"},
	{ModulePath: "connectors/xaioauth", Family: "xaioauth", Subject: "xaioauth"},
	{ModulePath: "connectors/qwenoauth", Family: "qwenoauth", Subject: "qwenoauth"},
	{ModulePath: "connectors/minimexoauth", Family: "minimax-oauth", Subject: "minimax-oauth"},
	{ModulePath: "connectors/localstub", Family: "test-emulator", Subject: "localstub"},
}
