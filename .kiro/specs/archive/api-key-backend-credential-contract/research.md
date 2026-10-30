# Brownfield Research, Gap Validation & Design Decisions

## Baseline and method

- **Repository:** https://github.com/matdev83/go-llm-interactive-proxy
- **Inspected main:** 8fd26640b54a00c855365dfe45310e63cbeac136 (2026-10-14, source snapshot).
- **Classification:** Brownfield; source code, connector manifests, existing tests, Kiro steering and archived specs constrain the design.
- **Prior specification:** [.kiro/specs/archive/base-api-connector-porting/](https://github.com/matdev83/go-llm-interactive-proxy/tree/main/.kiro/specs/archive/base-api-connector-porting) standardized four official backends, including environment numbered defaults and pre-output credential pools, but did not encompass later executable connectors.
- **Current guard budget:** .kiro/steering/delivery.md and internal/qa/kirospec/budget.go enforce <=5 requirement areas, <=25 criteria, <=300 design lines and <=12 leaf tasks. This umbrella is implemented in independently mergeable code slices, not a single oversized PR.
- **Evidence type:** Static source/manifest/contract inspection; no provider-credential live probe, local Go test execution or runtime certification was performed during spec authoring. Implementation tasks own executable validation.

## Research Log: current paths and gap analysis

| Gap | Current implementation / source | Consequence | Repaired criteria |
| --- | --- | --- | --- |
| G01 Hard-coded independent caps | internal/standardplugins/keys.go and internal/plugins/backends/compatmode/env.go each hard-code index 32 | User requires configurable default 20, one authority | 1.1, 1.5 |
| G02 Gap-stop discovery | Both loaders break at first absent numbered slot; resolve_upstream_api_keys_test.go asserts the behavior | Sparse credentials beyond a gap are lost | 1.2 |
| G03 YAML precedes environment | internal/pluginreg/keys.go EffectiveAPIKeys selects YAML over env; standardplugins/backends_install.go and credentials.go accept api_key/api_keys/credentials | Static provider key material can be stored in config; not environment-only | 2.1, 4.1 |
| G04 Mixed YAML posture | Built-in OpenAI/Anthropic/Gemini accept secret fields; Alibaba Token Plan rejects; custom-compatible rejects literal fields, leaving uneven rules | Connector adoption can quietly regress security | 2.1, 5.2 |
| G05 Windows persistent-env exception | standardplugins/keys.go collectNumberedEnvKeysWithPersistentFallback can override process snapshot for Alibaba | Non-uniform environment authority and ordering | 1.1, 2.5 |
| G06 External inference provisioning excluded | infra/runtimebundle/backend_provisioning.go captureBackendProvisioning requires local_only + agent_runtime; common external inference exports are access_scope:any + inference | Stock host cannot automatically translate provider environment roots into external Configure.Secrets | 2.3, 4.2 |
| G07 Process env deliberately stripped | infra/backendplugins/processhost/env.go buildLaunchEnv and docs/backend-plugins/authoring.md forbid provider keys in child env | Connector-local os.Getenv is not equivalent to host capture; do not weaken isolation | 2.2, 2.3 |
| G08 External single-key implementations | connectors/openrouter, nvidia, huggingface, ollama, opencode, commandcode-* and many bulk-provider connectors consume a single api_key/token secret or literal YAML | No default numbered pool or quota rotation | 4.1, 4.2 |
| G09 OpenCode static file fallback | connectors/opencode/internal/service/auth.go reads YAML and auth.json after Configure secret | Direct contradiction of environment-only static key rule | 2.1, 2.2, 4.5 |
| G10 Colliding CommandCode env name | Both CommandCode flavors use COMMANDCODE_API_KEY via os.Getenv; YAML interpolation also accepted | Cannot independently operate or enforce connector namespace identity | 1.3, 4.5 |
| G11 Single-key compatible shortcuts | openresponsescompat/backend.go, streaming.go, compact.go; systemonecompat/backend.go use compatmode.FirstAPIKey | Numbered credentials exist but never rotate | 4.3 |
| G12 Any 429 rotates | pkg/credpool/retry_after.go default fallback 60s; openaicred/runner.go and provider adapters mark 429 cooled; Pool.Acquire skips cooled primary | Even one-second 429 promotes key 2, contrary to quota threshold policy | 3.1, 3.2, 3.3 |
| G13 Fast-wire lane not equivalent | internal/plugins/backends/openaicompat/wire_open.go acquires once and returns recoverable error after 429; body may be consumed | Missing same-backend safe rotation; naive loop risks duplicate/non-replayable requests | 3.4, 4.3 |
| G14 Descriptor/manifest mismatch | connectors/ollama/manifest/template.backendplugin.json declares ollama-cloud credential_mode none; service Describe declares static | Manifest trust posture can differ from runtime credential requirement | 5.1 |
| G15 Static credential classes conflated | Connector manifests label static for some workload/signing, local subscription/token and simple bearer-key cases; OAuth has separate oauth_user mode | Blindly rotating all static secrets would break auth and user/tenant isolation | 2.4, 4.5 |
| G16 Incomplete architectural enforcement | Existing config decoders and many connector-specific service paths own ad hoc key fields; backend-plugin manifest has no verified provider env namespace/credential slot contract | Future connectors can repeat regressions even if docs are correct | 5.1, 5.2, 5.3 |
| G17 Quota identity insufficiently described | pkg/credpool/pool.go selects in registration order, with cooldown keyed by credential ID within an instance; credentials may share provider account quota | Sharing between independent deployments or treating shared quota as independent causes retries/throttle | 3.1, 4.4 |
| G18 Static token schemas beyond bearer APIs | connectors/codex/internal/service/config.go exposes access_token/refresh_token; connectors/ollama/local compatible configs expose api_key; service-key connectors have varied secret modes | “No connector keys from config” must include static token/PAT/private-key fields without disrupting managed OAuth/workload providers | 2.1, 2.4, 4.5 |
| G19 Test coverage fragmentation | resolve_upstream_api_keys_test.go, credpool tests, connector contract suites, archtest and backend manifest tests cover local paths but not one auto-onboarding contract | A new inference connector can pass its local tests while violating namespace, provisioning or rotation invariants | 5.2, 5.5 |

### Current provider/API connector census (snapshot)

**In-process:** OpenAI Responses + OpenAI Chat use OPENAI_API_KEY by design (one provider namespace); Anthropic uses ANTHROPIC_API_KEY; Gemini uses GEMINI_API_KEY; Alibaba Token Plan international uses ALIBABA_TOKEN_PLAN_API_KEY. Bedrock uses workload identity and is not an interchangeable bearer-key pool.

**Generic compatibility:** OpenAI Chat, OpenAI Responses and Anthropic compatible use an explicitly configured api_key_env_var_root and established pool paths; OpenResponses compatible and SystemOne compatible resolve a root but use only its first value. Embedded provider profiles compile to these family implementations via internal/standardplugins/provider_profile_binding.go and inherit the family's shortcomings.

**Executable API-key/token cohort to migrate:** openrouter, nvidia, huggingface, commandcode-openai, commandcode-anthropic, opencode-go, opencode-zen, azure-openai API-key mode, cloudflare, cohere, databricks-ai PAT/token, infomaniak-ai, replicate, snowflake-cortex PAT, watsonx API-key exchange, ollama-cloud, and Cursor SDK's API-key agent session. The baseline manifests are under connectors/<module>/manifest/template.backendplugin.json; individual parsers and Configure handlers are under connectors/<module>/internal/service. Treat Cursor SDK's maintained standalone artifact as a release dependency: its in-tree fallback is not proof that an external published plugin was migrated.

**Additional static-secret/removal-only scope:** openai-codex HTTP static access_token, local Ollama/llama.cpp/LM Studio/vLLM optional reverse-proxy API keys, SAP AI Core service_key, and any connector YAML with static bearer/PAT/private-key material. Their quota-switch behavior must follow an explicit auth classification; Codex ChatGPT-plan/subscription identity and SAP service-key token exchange are **not automatically assumed** to have interchangeable per-key quotas.

**Distinct auth modes, still subject to the no-literal-secret-in-AIProxer-YAML rule:** AWS Bedrock/SageMaker workload/SigV4, Vertex credential chain, OCI request signing, Azure Entra, SaaS OAuth-user (gitlab-duo, minimax-oauth, nous-portal, qwen-oauth, xai-oauth), ACP/CLI agent login and genuinely unauthenticated local servers. Provider-managed OAuth stores and standard cloud workload identity files are not proxy configuration-file API-key fallbacks.

### Proposed nonsecret default namespace catalogue (subject to provider-mode applicability)

| Provider/connector | Canonical env root | Notes |
| --- | --- | --- |
| OpenAI official Chat/Responses | OPENAI_API_KEY | Deliberately shared provider namespace |
| Anthropic official | ANTHROPIC_API_KEY | Separate from CommandCode Anthropic |
| Gemini official | GEMINI_API_KEY | Provider-specific RetryInfo parsing |
| Alibaba Token Plan international | ALIBABA_TOKEN_PLAN_API_KEY | Process-env only; remove persistent registry override |
| OpenRouter | OPENROUTER_API_KEY | |
| NVIDIA | NVIDIA_API_KEY | |
| Hugging Face | HUGGINGFACE_API_KEY | |
| OpenCode Go / Zen | OPENCODE_GO_API_KEY / OPENCODE_ZEN_API_KEY | No implicit OPENCODE_API_KEY or auth.json fallback |
| CommandCode OpenAI / Anthropic | COMMANDCODE_OPENAI_API_KEY / COMMANDCODE_ANTHROPIC_API_KEY | Split currently shared generic root |
| Azure OpenAI API-key | AZURE_OPENAI_API_KEY | Only credential_mode api_key, never Entra |
| Cloudflare | CLOUDFLARE_API_TOKEN | Provider token form |
| Cohere | COHERE_API_KEY | |
| Databricks AI | DATABRICKS_API_TOKEN | Provider PAT/token |
| Infomaniak AI | INFOMANIAK_API_KEY | |
| Replicate | REPLICATE_API_TOKEN | |
| Snowflake Cortex | SNOWFLAKE_CORTEX_PAT | |
| watsonx | WATSONX_API_KEY | IAM token derived from a selected key |
| Ollama Cloud | OLLAMA_CLOUD_API_KEY | Correct manifest posture to static |
| Cursor SDK static API key | CURSOR_API_KEY | Select on safe session admission; no replay after agent side effects |
| Local Ollama / llama.cpp / LM Studio / vLLM | OLLAMA_API_KEY / LLAMACPP_API_KEY / LMSTUDIO_API_KEY / VLLM_API_KEY | Optional auth; preserve no-auth/dummy behavior when safe |
| Codex HTTP static token | OPENAI_CODEX_ACCESS_TOKEN | Removal/source enforcement; explicitly classify before any quota rotation |
| SAP AI Core service credentials | SAP_AI_CORE_SERVICE_KEY | Removal/source enforcement; OAuth exchange not assumed per-key quota |

**Root names are proposal defaults**, not claims about existing exported environment behavior. Reconcile with connector IDs, provider policy, security profiles and operator migration in implementation before freezing. Operator-supplied alternate root names remain nonsecret configuration references.

## Requirements gap validation and repairs

The initial operator intent was cross-checked against brownfield constraints and the following ambiguities were resolved **upstream in requirements**, before design:

1. “All static” is not “all API keys”: 2.4 and 4.5 preserve OAuth refresh, cloud workload/signing and agent side-effect semantics, while 2.1 categorically prohibits literal static secrets in **any** AIProxer YAML. This avoids destroying legitimate credentials providers while honoring the no-config-key rule.
2. “Unique per connector” would split two OpenAI protocol adapters that intentionally use the same provider API key: 1.3 permits **declared same-provider sharing**, not accidental aliasing. CommandCode's two distinct exports require distinct roots.
3. A brief 429 must not cool the primary in a way that indirectly promotes key 2: 3.2 explicitly forbids advancement **even on later concurrent requests**, and 3.3 covers missing hints.
4. Pre-output is necessary but not sufficient for fast-wire replay: 3.4 now requires a physically replayable input and absence of side effects, and 3.5 requires fail-closed behavior otherwise.
5. A trusted manifest alone cannot prove an arbitrary executable never opens a credential file: 5.3 separates enforceable host gates from the stronger security property requiring OS confinement.
6. Candidate reload and connector resource reuse can leak stale credential snapshots: 2.5 explicitly assigns generation capture/publication isolation and in-flight preservation.
7. “Config file keys” means provider static credential **values or file-based static API-key fallbacks**, not nonsecret env root references, OAuth-owned encrypted/permissioned stores or provider workload identity chains (2.1, 2.2, 2.4).

**Requirements gate result:** Five numbered EARS areas with 25 independently testable criteria; scope includes all discovered gaps, with external distributed/vault/sandbox functionality the only deferred work.

## Architecture options and selected direction

| Option | Strength | Failure mode | Verdict |
| --- | --- | --- | --- |
| Direct os.Getenv in each connector | Minimal diff | Violates isolated child environment; invites YAML/file fallback and repeated ad hoc code | Reject |
| Inject keys into child env | Implementation ease | Breaks host bootstrap secret-isolation guarantee and broadens process attack surface | Reject |
| Relax existing agent-only secret_refs guard | Reuses one function | Accidentally widens local-agent posture into shared inference, potentially breaking multi-user security | Reject |
| Host-side declared namespace + existing secure SecretBundle | Matches current composition and authenticated Configure ABI; one testable policy | Requires explicit manifest/SDK/schema migration and connector adapters | **Choose** |
| Centralize all HTTP retry in core | One place for errors | Core lacks provider quota semantics and wire replay authority; violates routing/kernel separation | Reject |
| Trusted adapter-owned pool + host-owned discovery | Leverages existing credpool; permits provider-specific classification | Need distinct transient throttle vs quota cooldown and safe cross-path behavior | **Choose** |

## Design-validation checklist and mandatory repairs

- **DV01 Security posture:** A naive extension of captureBackendProvisioning would fail the local_only + agent_runtime guard. Solution: separate strictly validated inference static-key capture into the host, keep the agent lane unchanged, and require auth/posture approval before any env lookup.
- **DV02 Version/ABI:** Do not add plaintext config keys or modify generated RPC merely to send multiple secrets. Use the existing bounded SecretBundle with deterministic numbered slot names and SDK typed decoding. Nonsecret manifest contract declarations must be versioned and validated; legacy unclassified API-key export must fail closed.
- **DV03 Short 429:** Simple Pool.MarkRateLimited cannot satisfy 3.2 because Pool.Acquire walks past cooled keys. Model transient same-key throttle separately from quota-unusable and auth-invalid; do not advance on short or missing Retry-After.
- **DV04 Replay:** The fast wire path consumes an io.Reader; adapter cannot promise transparent replay unless it owns a verified replayable snapshot or reconstructible bounded source. Where impossible, fail without retry instead of risking duplicate provider effects or a corrupt body.
- **DV05 Quota identity:** Two protocol aliases may share an env root but not necessarily a rate-limit bucket. Bind availability by explicit provider/deployment/quota domain and opaque credential ID; don't hash/log raw secrets as IDs. Scope shared state intentionally and preserve lifecycle ownership.
- **DV06 Third-party limitation:** Manifest validation + launch-env isolation + no plaintext YAML can enforce **host behavior**, but cannot disprove file IO by arbitrary executable code. First-party AST/schema gates and optional OS sandbox are distinct assurance levels; 5.3 states limits transparently.
- **DV07 Provider-mode exceptions:** Multi-export Ollama Cloud posture mismatch must be fixed; Cursor side effects and Codex subscription token must not be treated as safe per-request interchangeable API-key pools; cloud workload SDK files are separate auth contracts.
- **DV08 Delivery budget:** A single PR touching every connector exceeds repository slice guidance. The 12 leaf implementation tasks split by seam and connector cohort, each requiring its own focused validation and independently mergeable change set.

**Brownfield design-validation conclusion:** GO for an implementation-ready, security-preserving design once the explicit repairs above are reflected in design.md/tasks.md; no hidden production-code changes are authorized by this spec-only PR.

## References

- [Source tree and current main](https://github.com/matdev83/go-llm-interactive-proxy/tree/8fd26640b54a00c855365dfe45310e63cbeac136)
- [Existing env loader](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/standardplugins/keys.go) and [compatible loader](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/plugins/backends/compatmode/env.go)
- [Credential pool](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/pkg/credpool/pool.go) and [Retry-After](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/pkg/credpool/retry_after.go)
- [Executable provisioner](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/infra/runtimebundle/backend_provisioning.go) and [secure process environment](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/infra/backendplugins/processhost/env.go)
- [Manifest validation](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/pkg/lipsdk/backendplugin/manifest/validate.go), [plugin authoring](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/docs/backend-plugins/authoring.md) and [delivery budget](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/.kiro/steering/delivery.md)
