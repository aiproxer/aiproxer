# Provider Profiles Operator Guide

AIProxer includes a first-class, data-driven provider profile system. Profiles enable zero-dependency, verified integration with over 140 public and regional LLM providers without compiling external plugins or writing boilerplate YAML.

## Architecture and Design Principles

1. **Embedded & Data-Driven:** Provider profiles are declared in AIProxer's embedded catalog (internal/providerprofiles/catalog.json) under the typed aip.provider-profile/v1 schema.
2. **Zero-Allocation at Rest:** Unconfigured catalog profiles consume zero background goroutines, HTTP clients, connection pools, or memory structures. A backend is constructed only when explicitly configured in the active configuration generation.
3. **Conservative Capability Posture:** Provider profiles default to proven capabilities (streaming and tool calls). Unproven or vendor-inconsistent capabilities (vision, documents, reasoning, parallel_tool_calls, reasoning_replay) are disabled at compile time to prevent runtime silent degradation or prompt corruption.
4. **Offline Determinism:** All profile compilation, validation, and route inspection occur deterministically offline without network calls or external registry lookups.

## Provider Profiles vs. Generic Custom Backends vs. External Connectors

| Need | Configuration Method | Distinction |
| --- | --- | --- |
| **Catalog Provider (e.g. Groq, Together, DeepSeek, Kimi)** | `kind: provider-profile`, `config.profile: <id>` | First-class standard profiles with managed endpoints, credential roots, discovery policies, and capability ceilings derived automatically from the embedded catalog. |
| **Private / Uncataloged Custom Endpoint** | `kind: custom-*-compatible` | Generic compatible modes (e.g. `custom-openai-responses-compatible`, `custom-anthropic-compatible`) requiring explicit manual `backend_prefix`, `base_url`, and `api_key_env_var_root`. See [Custom Compatible Backends](custom-compatible-backends.md). |
| **Vendor SDKs / Cloud Platforms / Direct Model APIs** | External Connectors under `connectors/` | Executable gRPC plugin processes for cloud platforms (`azure-openai`, `vertex`, `sagemaker`, `oci-generative-ai`, `watsonx`, `sapaicore`, `snowflake-cortex`, `databricks-ai`, `cloudflare`, `infomaniak-ai`) and direct model APIs (`cohere`, `replicate`). See [Backend Plugin Operator Guide](backend-plugins/operator.md). |
| **OAuth / Subscription Bridges** | External Connectors under `connectors/` | Dedicated bridges (`nous-portal`, `xai-oauth`, `qwen-oauth`, `minimax-oauth`, and token-exchange bridge `gitlab-duo`) handling browser PKCE, device authorization, or scoped token refresh. Standard API-key profiles **do not** consume subscription quotas or OAuth credentials. **Note:** A direct GitHub Copilot bridge (`github-copilot`) and a raw Claude OAuth bridge (`claude-subscription` / `anthropic-oauth`) are [unsupported-by-policy](backend-plugins/unsupported.md). Your own Claude subscription is supported for personal use through the official Claude Code runtime ([`claudeagentacp`](claude-agent-acp-backend.md)); it must never be shared or resold. |

### Distinct API-key Profiles vs. Dedicated Connectors

Operators must observe the explicit boundary between catalog API-key profiles and dedicated external connectors:
- **xAI:** API-key profile `xai` (`kind: provider-profile`) vs. `xai-oauth` connector (`kind: xai-oauth`, plugin `com.aiproxer.backend.xaioauth`).
- **MiniMax:** API-key profiles `minimax` / `minimax-cn` (`kind: provider-profile`) vs. `minimax-oauth` connector (`kind: minimax-oauth`, plugin `com.aiproxer.backend.minimexoauth`, Anthropic Messages wire transport).
- **Alibaba / Qwen:** API-key profiles `alibaba*` / DashScope (`kind: provider-profile`) vs. `qwen-oauth` connector (`kind: qwen-oauth`, plugin `com.aiproxer.backend.qwenoauth`) vs. `alibaba-token-plan-intl` (dedicated in-process backend).
- **Anthropic / Claude:** In-process `anthropic` API-key backend vs. your own subscription through the official Claude Code runtime ([`claudeagentacp`](claude-agent-acp-backend.md), personal single-user use). A raw `claude-subscription` / `anthropic-oauth` bridge is [unsupported-by-policy](backend-plugins/unsupported.md).
- **Google / Gemini:** In-process `gemini` API-key backend vs. `vertex` connector (`kind: vertex`, using Google Cloud IAM / OAuth2).

> [!IMPORTANT]
> **ACP is outside this feature.**
> Agent Client Protocol (ACP) integrations, ACP connectors, and process wrappers are outside the scope of provider profiles. No ACP provider or wrapper is represented in the provider profiles catalog.

## Operator Configuration

Configuring a provider profile requires only the runtime backend `id`, `kind: provider-profile`, and `config.profile`:

```yaml
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-chat
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
  backends:
    # 1. Bare Responses-first provider
    - id: groq
      kind: provider-profile
      enabled: true
      config:
        profile: groq

    # 2. Flavor-split Responses provider (preferred for Responses traffic)
    - id: deepseek-responses
      kind: provider-profile
      enabled: true
      config:
        profile: deepseek-responses

    # 3. Flavor-split Chat provider (supplemental for Chat traffic)
    - id: deepseek-openai
      kind: provider-profile
      enabled: true
      config:
        profile: deepseek-openai

    # 4. Standard Chat provider
    - id: together
      kind: provider-profile
      enabled: true
      config:
        profile: together

    # 5. Anthropic Messages provider
    - id: kimi-coding
      kind: provider-profile
      enabled: true
      config:
        profile: kimi-coding
```

See [`config/examples/provider-profiles-bulk.example.yaml`](../config/examples/provider-profiles-bulk.example.yaml) for a complete runnable example. Validate configurations using:

```bash
go run ./cmd/aipstd check-config --config config/examples/provider-profiles-bulk.example.yaml
```

## Multi-Flavor Splits (Responses Preferred)

When an upstream vendor supports distinct API families with non-identical semantics or model sets, AIProxer provides explicit flavor splits:

- **Responses-Preferred Status:** For providers offering native OpenAI Responses APIs, the `*-responses` profile is **preferred**.
- **DeepSeek:**
  - `deepseek-responses` (**Preferred**): Native /responses endpoint, static inventory locked to `deepseek-v4-flash`, reasoning capability preserved. Strict quirk `openai.responses.tool_output_adjacency`: a `function_call_output` separated from its call by user/developer/system messages is restored by delaying those messages past the result (byte-identical on healthy input). Model-authored interleavings that survive negotiation (a later tool call, an assistant message) and ambiguous pairings are never reordered — such requests are refused with a classified error that fails over to the next candidate instead of being forwarded in an order the conversation never had. Histories carrying reasoning items are rejected earlier at capability negotiation (the backend cannot replay them), so exact reasoning artifacts never reach this quirk.
  - `deepseek-openai` (**Supplemental**): Chat Completions endpoint, family-default discovery exposing both Flash and Pro models for Chat clients. Strict quirk `openai.chat.reasoning_content_on_tool_calls`: every assistant message in a tools-carrying request gains `reasoning_content: ""` when the field is absent (never overwrites preserved reasoning; tool-less requests untouched).
- **Scaleway:**
  - `scaleway-responses` (**Preferred**): Native /responses endpoint, static inventory for serverless Responses models (`openai/gpt-oss-120b:fp4`, `openai/gpt-oss-20b:fp4`).
  - `scaleway-openai` (**Supplemental**): Chat Completions endpoint with family-default /models discovery.

## Anthropic Messages Compatible Family

Anthropic-compatible profiles use the Anthropic Messages wire protocol:
- **Base URL Root:** Profiles store the SDK root URL (e.g. `https://api.kimi.com/coding/`), not the operation path; AIProxer's Anthropic adapter appends `/v1/messages` and `/v1/models` deterministically.
- **Authentication Mode:** Uses `api_key_env` (the upstream receives `x-api-key: <KEY>`).
- **Client Identity:** AIProxer maintains truthful client identification and does not spoof client identifiers (e.g. Claude Code or OpenCode).
- **Supported Profiles:**
  - `kimi-coding`: Kimi coding agent endpoint (`KIMI_API_KEY`) with static `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed`.
  - `minimax`: Global MiniMax Anthropic endpoint (`MINIMAX_API_KEY`) with family-default discovery.
  - `minimax-cn`: China MiniMax Anthropic endpoint (`MINIMAX_CN_API_KEY`) with isolated credentials.
  - `thinking-machines`: Tinker Anthropic endpoint (`TINKER_API_KEY`) with static `thinkingmachines/Inkling`. Upstream ignores prompt cache control.

## System One Decision Compatible Family

Decision models are a separate family from chat/Responses. The System One family compiles to exactly the `decisions` capability and nothing else: no chat, tools, vision, or streaming.

- **Base URL Root:** Profiles store the SDK root URL (e.g. `https://api.subq.ai/v1`), not the operation path; AIProxer's System One adapter appends `/systemone` deterministically, so SubQ resolves to `POST https://api.subq.ai/v1/systemone`.
- **Authentication Mode:** Uses `bearer_env` (the upstream receives `Authorization: Bearer <KEY>`, e.g. `Authorization: Bearer $SUBQ_API_KEY`).
- **Static Discovery:** Discovery is static for this family — no network model discovery and no extra process at rest. `GET /v1/models` at SubQ may list chat IDs too, which is why the static inventory is deliberate.
- **Client Usage:** An operator enables the `systemone` frontend (authenticated `POST /v1/systemone`) and routes `subq-snap:subq-snap-preview`:

```bash
curl -s http://127.0.0.1:18096/v1/systemone \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AIP_LOCAL_KEY" \
  -d '{
    "model": "subq-snap:subq-snap-preview",
    "state": {"summary": "Deploy proposal for review.", "diff": "..."},
    "questions": {
      "approve": {"type": "noul", "instructions": "Is this change safe to deploy?"}
    }
  }'
```

A typed-answer response mirrors the wire shape (`model`, per-question `answers`, token `usage`):

```json
{
  "model": "subq-snap-preview",
  "answers": {
    "approve": {"type": "noul", "noul": 0.82}
  },
  "usage": {"input_tokens": 1234, "output_tokens": 0}
}
```

- **The `subquadratic-systemone` Profile:** Public-preview model. The static inventory contains only `subq-snap-preview`.
- **Runnable Example:** See [`config/examples/subquadratic-systemone.yaml`](../config/examples/subquadratic-systemone.yaml) (SubQ-only frontend with `max_questions: 16`). The sibling [`config/examples/custom-systemone-compatible.yaml`](../config/examples/custom-systemone-compatible.yaml) shows a TypeSafe plus operator-defined custom pairing.

### SubQ Scope and Constraint Notes

- **Text-only Phase 1.** SubQ's Decisions API also accepts `images`; AIProxer does not send them. A client `images` field is rejected by the frontend before any upstream traffic (never silently dropped). Multimodal parity is a separate, not-yet-implemented capability.
- **Provider Limits Not Enforced by AIProxer:** These SubQ limits differ from AIProxer defaults and are deliberately not tightened into the shared canonical invariants. Configure `max_questions` deliberately:
  - Max 16 questions per call (vs. frontend default 256).
  - Choice questions need 2–255 options (vs. AIProxer minimum 1).
  - Question names up to 100 characters (vs. AIProxer 256 bytes).
  - Public API max ~393,216 tokens; up to 4 inline images at most 4 MiB each / 8 MiB total; 13 MiB max body (vs. `server.max_request_body_bytes` default 8 MiB).
- **Billing/pricing:** The published price is $0.05 per 1M input tokens with free output. `usage.input_tokens` is authoritative when returned and `usage.output_tokens` is reported as a real zero — presence of that zero is preserved. Adding a profile does not create an operator rate, customer tariff, or `usage.cost`; a host that wants those attaches the optional binding and usage-limit seams explicitly.
- **Error handling / sanitization:** SubQ returns `{"detail": {"error_type": ..., "message": ...}}` (an object). AIProxer's System One adapter only extracts fields from `detail[]`, so a 400/422 surfaces as a fixed sanitized rejection (`body`) that never echoes evidence or vendor text. Per existing status policy, 402/403/429/5xx are retryable pre-output failures (a credit or entitlement failure may fail over to another candidate rather than surfacing SubQ's exact status), while 400/413/422 are terminal input rejections.
- **Security / privacy:** SubQ documents indefinite storage as the organization default; zero-data-retention is an organization-level arrangement with SubQ and must be verified per account. The Decisions endpoint has no documented per-call `store:false`. Decide deliberately before sending sensitive repository state, prompts, or documents. AIProxer's secrets guard denies decision requests outright whenever a secret guard is active, so a guarded deployment cannot use this backend until that is addressed.
- **Evidence caution:** SubQ's throughput/latency/quality claims are vendor-reported; on the vendor's own When2Call and API-Bank results Snap trails Jev. Do not auto-promote it over internal Jev gates or production tool routing on those claims.

## Regional and Plan Credential Isolation

To prevent credential leaks or unintended billing crossover between global and regional accounts or between standard and token/coding plans, independent products require distinct environment variable roots:

- **Alibaba:** `alibaba` (`DASHSCOPE_API_KEY`), `alibaba-cn` (`DASHSCOPE_CN_API_KEY`), `alibaba-coding-plan` (`ALIBABA_CODING_PLAN_API_KEY`), `alibaba-coding-plan-cn` (`ALIBABA_CODING_PLAN_CN_API_KEY`), `alibaba-token-plan-cn` (`ALIBABA_TOKEN_PLAN_CN_API_KEY`).
- **Moonshot:** `moonshot` (`MOONSHOT_API_KEY`), `moonshot-cn` (`MOONSHOT_CN_API_KEY`).
- **StepFun:** `stepfun` (`STEPFUN_API_KEY`), `stepfun-cn` (`STEPFUN_CN_API_KEY`), `stepfun-step-plan` (`STEPFUN_STEP_PLAN_API_KEY`), `stepfun-step-plan-cn` (`STEPFUN_STEP_PLAN_CN_API_KEY`).
- **Xiaomi:** `xiaomi` (`XIAOMI_API_KEY`), `xiaomi-token-plan-eu` (`XIAOMI_TOKEN_PLAN_EU_API_KEY`), `xiaomi-token-plan-cn` (`XIAOMI_TOKEN_PLAN_CN_API_KEY`), `xiaomi-token-plan-sg` (`XIAOMI_TOKEN_PLAN_SG_API_KEY`).
- **Z.AI (Zhipu):** `zai` (`ZHIPU_API_KEY`), `zai-cn` (`ZHIPU_CN_API_KEY`), `zai-coding-plan` (`ZHIPU_CODING_PLAN_API_KEY`), `zai-coding-plan-cn` (`ZHIPU_CODING_PLAN_CN_API_KEY`).
- **MiniMax:** `minimax` (`MINIMAX_API_KEY`), `minimax-cn` (`MINIMAX_CN_API_KEY`).

## Dedicated Products (Not Profile Collisions)

The provider profile catalog explicitly forbids profile collisions with dedicated built-in backends or external connectors:
- Dedicated built-in / connectors: `openrouter`, `nvidia`, `huggingface`, `opencode-go`, `opencode-zen`, `openai-codex`, `commandcode-*`, `ollama*`, `lmstudio`, `vllm`, `alibaba-token-plan-intl`.

## Supported First-Class Provider Profiles

The catalog contains **145** standard profiles:

| Profile ID | Protocol Family | Status | Base Endpoint | Credential Env Var | Inventory | Region / Plan Notes | Capability Caveats |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `groq` | openai-responses-compatible | Responses-First | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `fireworks` | openai-responses-compatible | Responses-First | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `digitalocean` | openai-responses-compatible | Responses-First | `https://inference.do-ai.run/v1` | `DIGITALOCEAN_ACCESS_TOKEN` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `vercel-ai-gateway` | openai-responses-compatible | Responses-First | `https://ai-gateway.vercel.sh/v1` | `AI_GATEWAY_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `requesty` | openai-responses-compatible | Responses-First | `https://router.requesty.ai/v1` | `REQUESTY_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `meta` | openai-responses-compatible | Responses-First | `https://api.meta.ai/v1` | `META_MODEL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `deepseek-responses` | openai-responses-compatible | Preferred (Responses) | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | Static: deepseek-v4-flash | Standard hosted API | Tools + streaming; disabled: vision, documents, parallel_tool_calls; quirk: openai.responses.tool_output_adjacency |
| `deepseek-openai` | openai-chat-compatible | Supplemental (Chat) | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, parallel_tool_calls; quirk: openai.chat.reasoning_content_on_tool_calls |
| `scaleway-responses` | openai-responses-compatible | Preferred (Responses) | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | Static: openai/gpt-oss-120b:fp4, openai/gpt-oss-20b:fp4 | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `scaleway-openai` | openai-chat-compatible | Supplemental (Chat) | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `302ai` | openai-chat-compatible | Standard | `https://api.302.ai/v1` | `302AI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `abacus` | openai-chat-compatible | Standard | `https://routellm.abacus.ai/v1` | `ABACUS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `abliteration-ai` | openai-chat-compatible | Standard | `https://api.abliteration.ai/v1` | `ABLIT_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `ai-router` | openai-chat-compatible | Standard | `https://api.ai-router.dev/v1` | `AI_ROUTER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `aiand` | openai-chat-compatible | Standard | `https://api.aiand.com/v1` | `AIAND_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `aihubmix` | openai-chat-compatible | Standard | `https://api.aihubmix.com/v1` | `AIHUBMIX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `aki-io` | openai-chat-compatible | Standard | `https://aki.io/v1` | `AKI_IO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `alibaba` | openai-chat-compatible | Standard | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `alibaba-cn` | openai-chat-compatible | Standard | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `alibaba-coding-plan` | openai-chat-compatible | Standard | `https://coding-intl.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `alibaba-coding-plan-cn` | openai-chat-compatible | Standard | `https://coding.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `alibaba-token-plan-cn` | openai-chat-compatible | Standard | `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `ALIBABA_TOKEN_PLAN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `ambient` | openai-chat-compatible | Standard | `https://api.ambient.xyz/v1` | `AMBIENT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `amd` | openai-chat-compatible | Standard | `https://developer.amd.com.cn/radeon/api/v1` | `AMD_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `anyapi` | openai-chat-compatible | Standard | `https://api.anyapi.ai/v1` | `ANYAPI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `arcee` | openai-chat-compatible | Standard | `https://api.arcee.ai/api/v1` | `ARCEE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `auriko` | openai-chat-compatible | Standard | `https://api.auriko.ai/v1` | `AURIKO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `baseten` | openai-chat-compatible | Standard | `https://inference.baseten.co/v1` | `BASETEN_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `berget` | openai-chat-compatible | Standard | `https://api.berget.ai/v1` | `BERGET_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `blueclaw` | openai-chat-compatible | Standard | `https://openai.blueclaw.network/v1` | `BLUECLAW_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `cerebras` | openai-chat-compatible | Standard | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `chutes` | openai-chat-compatible | Standard | `https://llm.chutes.ai/v1` | `CHUTES_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `clarifai` | openai-chat-compatible | Standard | `https://api.clarifai.com/v2/ext/openai/v1` | `CLARIFAI_PAT` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `claudinio` | openai-chat-compatible | Standard | `https://api.claudin.io/v1` | `CLAUDINIO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `cline-pass` | openai-chat-compatible | Standard | `https://api.cline.bot/api/v1` | `CLINE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `cloudferro-sherlock` | openai-chat-compatible | Standard | `https://api-sherlock.cloudferro.com/openai/v1` | `CLOUDFERRO_SHERLOCK_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `coralbricks` | openai-chat-compatible | Standard | `https://inference.coralbricks.ai/v1` | `CORAL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `cortecs` | openai-chat-compatible | Standard | `https://api.cortecs.ai/v1` | `CORTECS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `crof` | openai-chat-compatible | Standard | `https://crof.ai/v1` | `CROF_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `crossmodel` | openai-chat-compatible | Standard | `https://api.crossmodel.ai/v1` | `CROSSMODEL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `crusoe` | openai-chat-compatible | Standard | `https://api.inference.crusoecloud.com/v1` | `CRUSOE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `daoxe` | openai-chat-compatible | Standard | `https://daoxe.com/v1` | `DAOXE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `deepinfra` | openai-chat-compatible | Standard | `https://api.deepinfra.com/v1/openai` | `DEEPINFRA_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `dinference` | openai-chat-compatible | Standard | `https://api.dinference.com/v1` | `DINFERENCE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `drun` | openai-chat-compatible | Standard | `https://chat.d.run/v1` | `DRUN_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `ebcloud` | openai-chat-compatible | Standard | `https://maas-api.ebcloud.com/v1` | `EBCLOUD_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `echo` | openai-chat-compatible | Standard | `https://echo.tracerml.ai/v1` | `ECHO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `edenai` | openai-chat-compatible | Standard | `https://api.edenai.run/v3` | `EDENAI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `empiriolabs` | openai-chat-compatible | Standard | `https://api.empiriolabs.ai/v1` | `EMPIRIOLABS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `evroc` | openai-chat-compatible | Standard | `https://models.think.evroc.com/v1` | `EVROC_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `fastrouter` | openai-chat-compatible | Standard | `https://go.fastrouter.ai/api/v1` | `FASTROUTER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `friendli` | openai-chat-compatible | Standard | `https://api.friendli.ai/serverless/v1` | `FRIENDLI_TOKEN` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `frogbot` | openai-chat-compatible | Standard | `https://app.frogbot.ai/api/v1` | `FROGBOT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `gmicloud` | openai-chat-compatible | Standard | `https://api.gmi-serving.com/v1` | `GMICLOUD_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `greenpt` | openai-chat-compatible | Standard | `https://api.greenpt.ai/v1` | `GREENPT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `helicone` | openai-chat-compatible | Standard | `https://ai-gateway.helicone.ai/v1` | `HELICONE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `hetzner` | openai-chat-compatible | Standard | `https://inference.hetzner.com/api/v1` | `HETZNER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `hpc-ai` | openai-chat-compatible | Standard | `https://api.hpc-ai.com/inference/v1` | `HPC_AI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `hyper` | openai-chat-compatible | Standard | `https://hyper.charm.land/v1` | `HYPER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `iflowcn` | openai-chat-compatible | Standard | `https://apis.iflow.cn/v1` | `IFLOW_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `impossibl` | openai-chat-compatible | Standard | `https://api.impossibl.com/v1` | `IMPOSSIBL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `inception` | openai-chat-compatible | Standard | `https://api.inceptionlabs.ai/v1` | `INCEPTION_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `inceptron` | openai-chat-compatible | Standard | `https://api.inceptron.io/v1` | `INCEPTRON_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `inference-net` | openai-chat-compatible | Standard | `https://inference.net/v1` | `INFERENCE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `inferx` | openai-chat-compatible | Standard | `https://model.inferx.net/endpoints/v1` | `INFERX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `io-net` | openai-chat-compatible | Standard | `https://api.intelligence.io.solutions/api/v1` | `IOINTELLIGENCE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `jalapeno` | openai-chat-compatible | Standard | `https://api.jalapeno-cloud.ai/v1` | `JALAPENO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `jiekou` | openai-chat-compatible | Standard | `https://api.jiekou.ai/openai` | `JIEKOU_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `kenari` | openai-chat-compatible | Standard | `https://kenari.id/v1` | `KENARI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `kilo` | openai-chat-compatible | Standard | `https://api.kilo.ai/api/gateway` | `KILO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `llmgateway` | openai-chat-compatible | Standard | `https://api.llmgateway.io/v1` | `LLMGATEWAY_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `llmtech` | openai-chat-compatible | Standard | `https://api.llmtech.eu/v1` | `LLMTECH_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `llmtr` | openai-chat-compatible | Standard | `https://llmtr.com/v1` | `LLMTR_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `longcat` | openai-chat-compatible | Standard | `https://api.longcat.chat/openai` | `LONGCAT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `lucidquery` | openai-chat-compatible | Standard | `https://api.lucidquery.com/v1` | `LUCIDQUERY_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `meganova` | openai-chat-compatible | Standard | `https://api.meganova.ai/v1` | `MEGANOVA_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `mistral` | openai-chat-compatible | Standard | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `mixlayer` | openai-chat-compatible | Standard | `https://models.mixlayer.ai/v1` | `MIXLAYER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `moark` | openai-chat-compatible | Standard | `https://moark.com/v1` | `MOARK_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `modal` | openai-chat-compatible | Standard | `https://inference.us-west.modal.direct/v1` | `MODAL_PROXY_TOKEN` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `model-oracle-ai` | openai-chat-compatible | Standard | `https://api.modeloracle.com/api/v1` | `MODEL_ORACLE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `modelis` | openai-chat-compatible | Standard | `https://modelishub.com/v1` | `MODELIS_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `modelscope` | openai-chat-compatible | Standard | `https://api-inference.modelscope.cn/v1` | `MODELSCOPE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `moonshot` | openai-chat-compatible | Standard | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `moonshot-cn` | openai-chat-compatible | Standard | `https://api.moonshot.cn/v1` | `MOONSHOT_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `morph` | openai-chat-compatible | Standard | `https://api.morphllm.com/v1` | `MORPH_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: tools, vision, documents, reasoning, parallel_tool_calls |
| `neuralwatt` | openai-chat-compatible | Standard | `https://api.neuralwatt.com/v1` | `NEURALWATT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `nova` | openai-chat-compatible | Standard | `https://api.nova.amazon.com/v1` | `NOVA_API_KEY` | Family default | Amazon Nova direct API; bearer key | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `novita-ai` | openai-chat-compatible | Standard | `https://api.novita.ai/openai` | `NOVITA_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `ofox` | openai-chat-compatible | Standard | `https://api.ofox.ai/v1` | `OFOX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `opper` | openai-chat-compatible | Standard | `https://api.opper.ai/v3/compat` | `OPPER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `orcarouter` | openai-chat-compatible | Standard | `https://api.orcarouter.ai/v1` | `ORCAROUTER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `ovhcloud` | openai-chat-compatible | Standard | `https://oai.endpoints.kepler.ai.cloud.ovh.net/v1` | `OVHCLOUD_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `pendra` | openai-chat-compatible | Standard | `https://api.pendra.ai/api/v1` | `PENDRA_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `pioneer` | openai-chat-compatible | Standard | `https://api.pioneer.ai/v1` | `PIONEER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `poe` | openai-chat-compatible | Standard | `https://api.poe.com/v1` | `POE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `poolside` | openai-chat-compatible | Standard | `https://inference.poolside.ai/v1` | `POOLSIDE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `qihang-ai` | openai-chat-compatible | Standard | `https://api.qhaigc.net/v1` | `QIHANG_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `qiniu-ai` | openai-chat-compatible | Standard | `https://api.qnaigc.com/v1` | `QINIU_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `regolo-ai` | openai-chat-compatible | Standard | `https://api.regolo.ai/v1` | `REGOLO_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `routing-run` | openai-chat-compatible | Standard | `https://api.routing.run/v1` | `ROUTING_RUN_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `scnet-token-plan` | openai-chat-compatible | Standard | `https://api.scnet.cn/api/llm/v1` | `SCNET_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `scx-ai` | openai-chat-compatible | Standard | `https://api.scx.ai/v1` | `SCX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `siliconflow` | openai-chat-compatible | Standard | `https://api.siliconflow.com/v1` | `SILICONFLOW_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `siliconflow-cn` | openai-chat-compatible | Standard | `https://api.siliconflow.cn/v1` | `SILICONFLOW_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `stackit` | openai-chat-compatible | Standard | `https://api.openai-compat.model-serving.eu01.onstackit.cloud/v1` | `STACKIT_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `standardcompute` | openai-chat-compatible | Standard | `https://api.stdcmpt.com/v1` | `STANDARDCOMPUTE_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `stepfun` | openai-chat-compatible | Standard | `https://api.stepfun.ai/v1` | `STEPFUN_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `stepfun-cn` | openai-chat-compatible | Standard | `https://api.stepfun.com/v1` | `STEPFUN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `stepfun-step-plan` | openai-chat-compatible | Standard | `https://api.stepfun.ai/step_plan/v1` | `STEPFUN_STEP_PLAN_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `stepfun-step-plan-cn` | openai-chat-compatible | Standard | `https://api.stepfun.com/step_plan/v1` | `STEPFUN_STEP_PLAN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `submodel` | openai-chat-compatible | Standard | `https://llm.submodel.ai/v1` | `SUBMODEL_INSTAGEN_ACCESS_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `synthetic` | openai-chat-compatible | Standard | `https://api.synthetic.new/openai/v1` | `SYNTHETIC_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `tencent-coding-plan` | openai-chat-compatible | Standard | `https://api.lkeap.cloud.tencent.com/coding/v3` | `TENCENT_CODING_PLAN_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `tencent-token-plan` | openai-chat-compatible | Standard | `https://api.lkeap.cloud.tencent.com/plan/v3` | `TENCENT_TOKEN_PLAN_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `tencent-tokenhub` | openai-chat-compatible | Standard | `https://tokenhub.tencentmaas.com/v1` | `TENCENT_TOKENHUB_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `tensorx` | openai-chat-compatible | Standard | `https://api.tensorx.ai/v1` | `TENSORX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `the-grid-ai` | openai-chat-compatible | Standard | `https://api.thegrid.ai/v1` | `THEGRID_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `tinfoil` | openai-chat-compatible | Standard | `https://inference.tinfoil.sh/v1` | `TINFOIL_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `together` | openai-chat-compatible | Standard | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `trustedrouter` | openai-chat-compatible | Standard | `https://api.trustedrouter.com/v1` | `TRUSTEDROUTER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `vultr` | openai-chat-compatible | Standard | `https://api.vultrinference.com/v1` | `VULTR_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `wafer-ai` | openai-chat-compatible | Standard | `https://pass.wafer.ai/v1` | `WAFER_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `wandb` | openai-chat-compatible | Standard | `https://api.inference.wandb.ai/v1` | `WANDB_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xai` | openai-chat-compatible | Standard | `https://api.x.ai/v1` | `XAI_API_KEY` | Family default | Chat completions API; distinct from xai-oauth bridge | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xiaomi` | openai-chat-compatible | Standard | `https://api.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xiaomi-token-plan-eu` | openai-chat-compatible | Standard | `https://token-plan-ams.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_EU_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xiaomi-token-plan-cn` | openai-chat-compatible | Standard | `https://token-plan-cn.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xiaomi-token-plan-sg` | openai-chat-compatible | Standard | `https://token-plan-sgp.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_SG_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `xpersona` | openai-chat-compatible | Standard | `https://www.xpersona.co/v1` | `XPERSONA_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zai` | openai-chat-compatible | Standard | `https://api.z.ai/api/paas/v4` | `ZHIPU_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zai-cn` | openai-chat-compatible | Standard | `https://open.bigmodel.cn/api/paas/v4` | `ZHIPU_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zai-coding-plan` | openai-chat-compatible | Standard | `https://api.z.ai/api/coding/paas/v4` | `ZHIPU_CODING_PLAN_API_KEY` | Family default | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zai-coding-plan-cn` | openai-chat-compatible | Standard | `https://open.bigmodel.cn/api/coding/paas/v4` | `ZHIPU_CODING_PLAN_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zeldoc` | openai-chat-compatible | Standard | `https://api.zeldoc.ai/v1` | `ZELDOC_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zenifra` | openai-chat-compatible | Standard | `https://ai.zenifra.com/v1` | `ZENIFRA_AI_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `zenmux` | openai-chat-compatible | Standard | `https://zenmux.ai/api/v1` | `ZENMUX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, reasoning, parallel_tool_calls |
| `kimi-coding` | anthropic-compatible | Standard | `https://api.kimi.com/coding/` | `KIMI_API_KEY` | Static: k3, k3-256k, kimi-for-coding, kimi-for-coding-highspeed | Specialized plan; isolated credentials | Tools + streaming; disabled: vision, documents, parallel_tool_calls, reasoning_replay |
| `minimax` | anthropic-compatible | Standard | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` | Family default | Standard hosted API | Tools + streaming; disabled: vision, documents, parallel_tool_calls, reasoning_replay |
| `minimax-cn` | anthropic-compatible | Standard | `https://api.minimaxi.com/anthropic` | `MINIMAX_CN_API_KEY` | Family default | China endpoint; dedicated credential isolation | Tools + streaming; disabled: vision, documents, parallel_tool_calls, reasoning_replay |
| `thinking-machines` | anthropic-compatible | Standard | `https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api` | `TINKER_API_KEY` | Static: thinkingmachines/Inkling | Tinker hosted; prompt cache control ignored upstream | Tools + streaming; disabled: vision, documents, parallel_tool_calls, reasoning_replay |
| `typesafe` | systemone-compatible | Standard | `https://api.typesafe.ai/v1` | `TYPESAFE_API_KEY` | Static: jev-latest, jev-preview, jev-1.13.0 | Standard hosted API | Decisions only; no chat, tools, vision, or streaming |
| `openrouter-systemone` | systemone-compatible | Standard | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | Static: typesafe/jev-1.13, ~typesafe/jev-latest | Standard hosted API | Decisions only; no chat, tools, vision, or streaming |
| `commandcode-systemone` | systemone-compatible | Standard | `https://api.commandcode.ai/provider/v1` | `CMD_API_KEY` | Static: typesafe/jev | Standard hosted API | Decisions only; no chat, tools, vision, or streaming |
| `subquadratic-systemone` | systemone-compatible | Standard | `https://api.subq.ai/v1` | `SUBQ_API_KEY` | Static: subq-snap-preview | Public preview model; text-only Phase 1 | Decisions only; no chat, tools, vision, or streaming; client `images` rejected |
