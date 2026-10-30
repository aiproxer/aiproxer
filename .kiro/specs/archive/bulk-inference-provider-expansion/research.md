# Research & Design Decisions

## Purpose

This document is the frozen research input for the implementation plan. Implementation agents are **not** expected to repeat the provider survey or decide architecture. If a live/current provider contract contradicts a frozen row, stop only that provider task and report the contradiction; do not invent a new protocol/auth scheme.

Research was completed on 2026-08-28 and brownfield-revalidated on 2026-08-29 against Go-LIP current `main` at `3cf952ca` (including the feature-extension consolidation through #541 and the SDK-contract documentation in #542). Provider evidence was drawn from:

- OpenCode provider docs/implementation;
- Cline provider catalog/overrides;
- Hermes Agent provider registry/OAuth implementations;
- LiteLLM's provider matrix for breadth only;
- official provider documentation/OpenAPI where native protocol, endpoint, auth, or model discovery mattered.

## Brownfield Findings

Go-LIP contains the bounded schema, catalog and family adapters required for most of this feature:

- `internal/providerprofiles` owns the bounded `lip.provider-profile/v1` schema, embedded catalog, compiler and certification.
- `internal/standardplugins/provider_profile_binding.go` resolves and compiles `kind: provider-profile` rows, then rewrites them to one of four existing compatible factory kinds.
- Supported profile families are:
  - `openai-chat-compatible` -> `custom-openai-legacy-compatible`
  - `openai-responses-compatible` -> `custom-openai-responses-compatible`
  - `anthropic-compatible` -> `custom-anthropic-compatible`
  - `openresponses-compatible` -> `custom-openresponses-compatible`
- Compatible family adapters already own execution, streaming, credential pools, inventory, tokenizer/admission hooks, diagnostics and canonical translation.
- `make profile-only-check` and family/profile TCKs already ratchet profile-only growth away from core/canonical/frontend/ABI edits.

The production profile-to-backend binding is not yet semantically complete. On current `main`, `ExpandProviderProfileRows` compiles a profile but replaces it with `ProfileConfigNode(profile)` plus a generic compatible factory kind. That projection carries the prefix, base URL, env-var root, tokenizer and static models, but drops compiled capability ceilings, bounded safe headers, alternate model paths/closed quirks and OpenResponses capability/dialect configuration. The profile-aware `BuildProviderProfileBackend` applies those semantics, but current runtime composition does not call it; its direct uses are tests and conformance support. The placeholder profile does not exercise the dropped fields, so existing tests stay green.

The first implementation prerequisite is therefore a **narrow provider-profile binding repair** that carries the complete compiled profile semantics through the real registry build path. After that repair, the primary feature work is catalog population, not new protocol implementation. At this specification baseline, `internal/providerprofiles/catalog.json` contains only `example-openai-responses`.

A second key finding is that profile v1 is intentionally narrow. A standard profile can encode a static base URL, one supported credential env-var reference, family-default or static inventory, bounded safe headers, capability reductions and a closed quirk set. It intentionally rejects arbitrary transforms, URL templates, multi-secret schemes, unsupported namespace rewrites and disabled discovery. Providers needing dynamic account/project/product addressing, cloud signing, OAuth lifecycle, control-plane discovery, asynchronous prediction or native non-compatible request formats therefore belong in external connectors/bridges.

### Current-main extension-plane revalidation

The #535-#541 consolidation changed feature-extension composition (`FeatureBundle`/`PlaneSet`, frozen projections and lifecycle ownership), not provider-profile or backend-plugin ownership. The diff from `40168ce1` through current `main` does not change `internal/providerprofiles`, `internal/standardplugins/provider_profile_binding.go`, or the external backend-plugin ABI. In `compileCandidate`, `PrepareProviderProfiles` still runs as pure configuration preparation before feature surfaces are merged and frozen.

Required integration boundary:

- provider profiles remain immutable backend-generation input and do not become feature-plane contributions;
- external provider connectors remain external backend plugins rather than `PlaneSet` features;
- this work does not require changes to `internal/featurebundle`, `pkg/lipsdk/feature`, the `FeatureBundle`/`PlaneSet` projections, or core routing;
- a discovered need to change those surfaces is a stop condition for separate design review, not a reason to widen this spec.

## Existing Go-LIP Support: Do Not Duplicate

At baseline, the following are already supported and are exclusions from duplicate implementation:

- built-in `openai-responses`, `openai-legacy`, `anthropic`, `gemini`, `bedrock`;
- built-in `alibaba-token-plan-intl` (Anthropic Messages execution plus OpenAI-compatible inventory);
- generic compatible kinds for OpenAI Chat, OpenAI Responses, Anthropic Messages and OpenResponses;
- external OpenRouter, NVIDIA, Hugging Face, OpenCode Go/Zen, OpenAI Codex/app-server, CommandCode OpenAI/Anthropic, Ollama/Ollama Cloud, llama.cpp, LM Studio, vLLM and local-stub;
- existing Cursor SDK and ACP-family connectors.

**ACP is outside this specification.** No new ACP provider or ACP process wrapper belongs in this work.

## Source Interpretation Rules

1. Official current provider docs/OpenAPI win for native protocol, endpoint, auth and enumeration.
2. Cline/OpenCode are trusted secondary sources for provider names, base URLs, environment-variable conventions and coding-oriented compatibility.
3. Hermes is a primary secondary source for coding-plan/OAuth products and its explicit provider adapters.
4. LiteLLM is a **breadth source only**. LiteLLM's check marks under `/messages` or `/responses` often describe LiteLLM translation, not the upstream provider's native wire API. Never promote a provider to Responses/Anthropic merely because LiteLLM can normalize it.

Concrete corrections applied by this rule:

- current xAI OpenAPI exposed `/v1/chat/completions` but not `/v1/responses`, so API-key xAI remains Chat;
- current Kilo Gateway docs expose an OpenAI-compatible `/chat/completions` surface at `https://api.kilo.ai/api/gateway`, so Kilo is Chat despite a Cline product override that classified its client as Responses.

## Mechanical Provider Classification Algorithm

Implementation must follow this exact decision tree:

1. Check current implementation branch for equivalent support. Reuse/verify if present.
2. If native OpenAI Responses is proven and covers the complete intended coding/text model population, use **one bare Responses profile ID**.
3. If Responses is native but covers only a strict model subset, use `<provider>-responses` plus a supplemental `<provider>-openai` or `<provider>-anthropic`; do not create a bare alias.
4. If no native Responses is established and the provider fits static-base bearer OpenAI Chat, use one bare Chat profile.
5. If Anthropic Messages is the provider's selected compatible coding path, use one bare Anthropic profile unless another protocol is required for a distinct model population.
6. If more than one protocol identity is required, suffix **all** protocol identities using `-responses`, `-openai`, `-anthropic`, or `-openresponses`.
7. Do not create redundant protocol aliases for the same model set. Responses wins where this matrix selects it.
8. Use family-default remote inventory only if it cannot over-advertise models unsupported by the chosen flavor. Otherwise use static flavor-specific inventory.
9. If the product cannot fit static URL + one supported secret + current executable inventory/capability semantics, use the connector matrix instead of widening profile v1.

## Profile Population Safety Rules

### Capability policy

`providerprofiles.Compile` begins with the family maximum. Real provider profiles therefore must **reduce** unproven capabilities rather than silently inherit them.

For the long-tail OpenAI Chat matrix below, the conservative default is:

- retain `streaming`;
- retain `tools` because these rows were selected from coding-agent-compatible provider catalogs;
- disable `vision`;
- disable `documents`;
- disable `reasoning` unless the locked high-value table explicitly retains it;
- disable `parallel_tool_calls`.

If a frozen row is known to be plain-text/no-tools, disable `tools` as well. `morph` is explicitly treated this way unless an already-frozen fixture proves tool support.

For Anthropic-compatible profiles, retain `streaming` and `tools` only where the product is coding-agent compatible; disable unproven `vision`, `documents`, `parallel_tool_calls`, and especially `reasoning_replay`. A provider emitting thinking text does not prove exact historical reasoning replay semantics.

For Responses profiles, retain only capabilities explicitly supported by the frozen provider evidence. Responses support does not imply vision/documents/parallel tool calls.

### Tokenizer policy

Do **not** mass-assign `cl100k_base` or any other tokenizer merely because generic examples use it. Omit tokenizer fields for new provider profiles unless an existing Go-LIP rule or frozen provider evidence deliberately selects a local tokenizer. This avoids systematically wrong admission/accounting estimates for non-OpenAI tokenizers.

### Inventory policy

- OpenAI-family default discovery: `<base>/models`, response shape `{data:[{id}]}`.
- Anthropic-family default discovery: `<base>/v1/models`, Anthropic-compatible model response.
- Use static inventory if the provider's global catalog is broader than the selected API flavor.
- Do not add provider-specific parsers merely to filter flavor coverage.
- Every static inventory fixture asserts the complete `providerprofiles.Model` identity (`canonical_id`, `native_id`, and `display_name`), not IDs alone.

The explicitly frozen static identities are:

| Profile | Canonical ID | Native ID | Display name |
| --- | --- | --- | --- |
| `deepseek-responses` | `deepseek-v4-flash` | `deepseek-v4-flash` | `DeepSeek V4 Flash` |
| `scaleway-responses` | `openai/gpt-oss-120b:fp4` | `openai/gpt-oss-120b:fp4` | `GPT-OSS 120B FP4` |
| `scaleway-responses` | `openai/gpt-oss-20b:fp4` | `openai/gpt-oss-20b:fp4` | `GPT-OSS 20B FP4` |
| `kimi-coding` | `k3` | `k3` | `Kimi K3` |
| `kimi-coding` | `k3-256k` | `k3-256k` | `Kimi K3 256K` |
| `kimi-coding` | `kimi-for-coding` | `kimi-for-coding` | `Kimi for Coding` |
| `kimi-coding` | `kimi-for-coding-highspeed` | `kimi-for-coding-highspeed` | `Kimi for Coding High-Speed` |
| `thinking-machines` | `thinkingmachines/Inkling` | `thinkingmachines/Inkling` | `Thinking Machines Inkling` |

## Locked Responses / Multi-Flavor Profiles

| Go-LIP profile | Family | Preferred? | Base URL | Credential env | Inventory decision | Locked reason |
| --- | --- | --- | --- | --- | --- | --- |
| `deepseek-responses` | Responses | yes | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | static `deepseek-v4-flash` | Official Responses API supports Flash but not V4 Pro at research date. |
| `deepseek-openai` | Chat | supplemental | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | `/models` if fixture conforms, else static Flash+Pro | Full Chat model coverage. No third Anthropic alias needed. |
| `fireworks` | Responses | yes | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `/models` if fixture conforms, else static text set | Fireworks natively exposes Responses, Chat and Anthropic; Responses is sufficient for intended model population. |
| `groq` | Responses | yes | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | family default | Groq documents current models under Responses. |
| `digitalocean` | Responses | yes | `https://inference.do-ai.run/v1` | `DIGITALOCEAN_ACCESS_TOKEN` | family default | Serverless `/v1/responses` + `/v1/models`; OAuth Inference Routers are a separate control-plane concern. |
| `scaleway-responses` | Responses | yes | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | static Responses-supported models; seed with `openai/gpt-oss-120b:fp4`, `openai/gpt-oss-20b:fp4` when present in frozen catalog | Responses support is model-specific. |
| `scaleway-openai` | Chat | supplemental | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | family default | Broader Chat catalog. |
| `vercel-ai-gateway` | Responses | yes | `https://ai-gateway.vercel.sh/v1` | `AI_GATEWAY_API_KEY` | family default | Gateway natively exposes Responses, Chat, Anthropic and OpenResponses; use Responses as one default identity. |
| `requesty` | Responses | yes | `https://router.requesty.ai/v1` | `REQUESTY_API_KEY` | family default | Requesty exposes `/v1/responses` and `/v1/models`; no duplicate Chat alias needed. |
| `meta` | Responses | yes | `https://api.meta.ai/v1` | `META_MODEL_API_KEY` | family default after official list-models fixture conforms | Meta officially documents the OpenAI-compatible Responses API, `client.responses.create`, and `GET /models`; keep the profile only with an offline fixture of those public contracts. |
| `xai` | Chat | yes | `https://api.x.ai/v1` | `XAI_API_KEY` | family default | Current official xAI OpenAPI had Chat but no native Responses. OAuth is separate. |

Primary official evidence:

- DeepSeek Responses: <https://api-docs.deepseek.com/guides/responses_api/> and <https://api-docs.deepseek.com/quick_start/pricing/>
- Fireworks Responses/Anthropic compatibility: <https://docs.fireworks.ai/api-reference/post-responses>, <https://docs.fireworks.ai/tools-sdks/anthropic-compatibility>
- Groq Responses: <https://console.groq.com/docs/responses-api>
- DigitalOcean Responses: <https://docs.digitalocean.com/products/inference/how-to/use-responses-api/>
- Scaleway Generative API/models: <https://www.scaleway.com/en/developers/api/generative-apis>, <https://www.scaleway.com/en/docs/generative-apis/api-cli/using-models-api/>
- Vercel AI Gateway: <https://vercel.com/docs/ai-gateway/sdks-and-apis>, <https://vercel.com/docs/ai-gateway/models-and-providers>
- Requesty Responses/models: <https://docs.requesty.ai/api-reference/endpoint/responses-create>, <https://docs.requesty.ai/api-reference/endpoint/models-list>
- Meta Model API Responses/models: <https://ai.developer.meta.com/docs/features/responses/>, <https://ai.developer.meta.com/docs/api-reference/responses/create-response/>, <https://ai.developer.meta.com/docs/api-reference/models/list-models>
- xAI OpenAPI: <https://api.x.ai/api-docs/openapi.json>
- Kilo Gateway Chat contract: <https://kilo.ai/docs/gateway/quickstart>, <https://kilo.ai/docs/gateway/api-reference>

## Locked OpenAI Chat Profile Matrix

Every row below uses `family: openai-chat-compatible`, `auth.mode: bearer_env`, `models.discovery: family_default`, `models.namespace.mode: preserve` unless a task explicitly switches that row to static inventory after an offline fixture proves the provider model-list shape does not fit the existing contract. Apply the conservative capability policy above. Exact base URLs/env names are frozen here. Region/plan-suffixed env roots are deliberate Go-LIP configuration references that permit independent credentials; they are not claims that the vendor mandates those shell-variable names.

| Profile ID | Base URL | Env var | Note |
| --- | --- | --- | --- |
| `302ai` | `https://api.302.ai/v1` | `302AI_API_KEY` | Models.dev/Cline |
| `abacus` | `https://routellm.abacus.ai/v1` | `ABACUS_API_KEY` | Models.dev/Cline |
| `abliteration-ai` | `https://api.abliteration.ai/v1` | `ABLIT_KEY` | Models.dev/Cline |
| `ai-router` | `https://api.ai-router.dev/v1` | `AI_ROUTER_API_KEY` | broker |
| `aiand` | `https://api.aiand.com/v1` | `AIAND_API_KEY` | broker |
| `aihubmix` | `https://api.aihubmix.com/v1` | `AIHUBMIX_API_KEY` | Cline product override URL |
| `aki-io` | `https://aki.io/v1` | `AKI_IO_API_KEY` | hosted |
| `alibaba` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | international Model Studio |
| `alibaba-cn` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_CN_API_KEY` | China Model Studio; independent from international account |
| `alibaba-coding-plan` | `https://coding-intl.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_API_KEY` | international plan |
| `alibaba-coding-plan-cn` | `https://coding.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_CN_API_KEY` | China plan; independent from international plan |
| `alibaba-token-plan-cn` | `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `ALIBABA_TOKEN_PLAN_CN_API_KEY` | China plan; do not duplicate existing Intl backend or its credential root |
| `ambient` | `https://api.ambient.xyz/v1` | `AMBIENT_API_KEY` | hosted |
| `amd` | `https://developer.amd.com.cn/radeon/api/v1` | `AMD_API_KEY` | AMD token factory |
| `anyapi` | `https://api.anyapi.ai/v1` | `ANYAPI_API_KEY` | broker |
| `arcee` | `https://api.arcee.ai/api/v1` | `ARCEE_API_KEY` | hosted |
| `auriko` | `https://api.auriko.ai/v1` | `AURIKO_API_KEY` | broker |
| `baseten` | `https://inference.baseten.co/v1` | `BASETEN_API_KEY` | serverless only; private dedicated URLs remain custom-compatible |
| `berget` | `https://api.berget.ai/v1` | `BERGET_API_KEY` | hosted |
| `blueclaw` | `https://openai.blueclaw.network/v1` | `BLUECLAW_API_KEY` | hosted |
| `cerebras` | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | hosted |
| `chutes` | `https://llm.chutes.ai/v1` | `CHUTES_API_KEY` | hosted/TEE |
| `clarifai` | `https://api.clarifai.com/v2/ext/openai/v1` | `CLARIFAI_PAT` | compatibility layer |
| `claudinio` | `https://api.claudin.io/v1` | `CLAUDINIO_API_KEY` | broker |
| `cline-pass` | `https://api.cline.bot/api/v1` | `CLINE_API_KEY` | API/subscription product, not ACP |
| `cloudferro-sherlock` | `https://api-sherlock.cloudferro.com/openai/v1` | `CLOUDFERRO_SHERLOCK_API_KEY` | EU hosted |
| `coralbricks` | `https://inference.coralbricks.ai/v1` | `CORAL_API_KEY` | hosted |
| `cortecs` | `https://api.cortecs.ai/v1` | `CORTECS_API_KEY` | hosted |
| `crof` | `https://crof.ai/v1` | `CROF_API_KEY` | broker |
| `crossmodel` | `https://api.crossmodel.ai/v1` | `CROSSMODEL_API_KEY` | broker |
| `crusoe` | `https://api.inference.crusoecloud.com/v1` | `CRUSOE_API_KEY` | managed inference |
| `daoxe` | `https://daoxe.com/v1` | `DAOXE_API_KEY` | broker |
| `deepinfra` | `https://api.deepinfra.com/v1/openai` | `DEEPINFRA_API_KEY` | OpenAI compatibility surface |
| `dinference` | `https://api.dinference.com/v1` | `DINFERENCE_API_KEY` | hosted |
| `drun` | `https://chat.d.run/v1` | `DRUN_API_KEY` | China hosted |
| `ebcloud` | `https://maas-api.ebcloud.com/v1` | `EBCLOUD_API_KEY` | hosted |
| `echo` | `https://echo.tracerml.ai/v1` | `ECHO_API_KEY` | hosted |
| `edenai` | `https://api.edenai.run/v3` | `EDENAI_API_KEY` | broker; static inventory if `/models` fixture is nonconforming |
| `empiriolabs` | `https://api.empiriolabs.ai/v1` | `EMPIRIOLABS_API_KEY` | broker |
| `evroc` | `https://models.think.evroc.com/v1` | `EVROC_API_KEY` | EU hosted |
| `fastrouter` | `https://go.fastrouter.ai/api/v1` | `FASTROUTER_API_KEY` | broker |
| `friendli` | `https://api.friendli.ai/serverless/v1` | `FRIENDLI_TOKEN` | serverless |
| `frogbot` | `https://app.frogbot.ai/api/v1` | `FROGBOT_API_KEY` | broker |
| `gmicloud` | `https://api.gmi-serving.com/v1` | `GMICLOUD_API_KEY` | hosted |
| `greenpt` | `https://api.greenpt.ai/v1` | `GREENPT_API_KEY` | hosted |
| `helicone` | `https://ai-gateway.helicone.ai/v1` | `HELICONE_API_KEY` | gateway; optional Helicone headers are not required for base support |
| `hetzner` | `https://inference.hetzner.com/api/v1` | `HETZNER_API_KEY` | hosted |
| `hpc-ai` | `https://api.hpc-ai.com/inference/v1` | `HPC_AI_API_KEY` | hosted |
| `hyper` | `https://hyper.charm.land/v1` | `HYPER_API_KEY` | hosted |
| `iflowcn` | `https://apis.iflow.cn/v1` | `IFLOW_API_KEY` | China |
| `impossibl` | `https://api.impossibl.com/v1` | `IMPOSSIBL_API_KEY` | broker |
| `inception` | `https://api.inceptionlabs.ai/v1` | `INCEPTION_API_KEY` | hosted |
| `inceptron` | `https://api.inceptron.io/v1` | `INCEPTRON_API_KEY` | hosted |
| `inference-net` | `https://inference.net/v1` | `INFERENCE_API_KEY` | renamed from generic `inference` ID to avoid collision |
| `inferx` | `https://model.inferx.net/endpoints/v1` | `INFERX_API_KEY` | hosted |
| `io-net` | `https://api.intelligence.io.solutions/api/v1` | `IOINTELLIGENCE_API_KEY` | IO.NET Intelligence |
| `jalapeno` | `https://api.jalapeno-cloud.ai/v1` | `JALAPENO_API_KEY` | hosted |
| `jiekou` | `https://api.jiekou.ai/openai` | `JIEKOU_API_KEY` | compatibility root |
| `kenari` | `https://kenari.id/v1` | `KENARI_API_KEY` | hosted |
| `kilo` | `https://api.kilo.ai/api/gateway` | `KILO_API_KEY` | official Kilo Gateway contract is OpenAI Chat `/chat/completions` with tool calling; do not classify as Responses without a future official contract |
| `llmgateway` | `https://api.llmgateway.io/v1` | `LLMGATEWAY_API_KEY` | one identity for duplicate upstream aliases |
| `llmtech` | `https://api.llmtech.eu/v1` | `LLMTECH_API_KEY` | EU hosted |
| `llmtr` | `https://llmtr.com/v1` | `LLMTR_API_KEY` | hosted |
| `longcat` | `https://api.longcat.chat/openai` | `LONGCAT_API_KEY` | compatibility root |
| `lucidquery` | `https://api.lucidquery.com/v1` | `LUCIDQUERY_API_KEY` | hosted |
| `meganova` | `https://api.meganova.ai/v1` | `MEGANOVA_API_KEY` | broker |
| `mistral` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | profile only if existing compatible fixture certifies tool/reasoning shape without new translation |
| `mixlayer` | `https://models.mixlayer.ai/v1` | `MIXLAYER_API_KEY` | broker |
| `moark` | `https://moark.com/v1` | `MOARK_API_KEY` | hosted |
| `modal` | `https://inference.us-west.modal.direct/v1` | `MODAL_PROXY_TOKEN` | shared endpoint/proxy token path |
| `model-oracle-ai` | `https://api.modeloracle.com/api/v1` | `MODEL_ORACLE_API_KEY` | broker |
| `modelis` | `https://modelishub.com/v1` | `MODELIS_API_KEY` | broker |
| `modelscope` | `https://api-inference.modelscope.cn/v1` | `MODELSCOPE_API_KEY` | China model service |
| `moonshot` | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` | international pay-as-you-go |
| `moonshot-cn` | `https://api.moonshot.cn/v1` | `MOONSHOT_CN_API_KEY` | China pay-as-you-go; independent from international account |
| `morph` | `https://api.morphllm.com/v1` | `MORPH_API_KEY` | conservative text-only unless frozen fixture proves tools |
| `neuralwatt` | `https://api.neuralwatt.com/v1` | `NEURALWATT_API_KEY` | broker |
| `nova` | `https://api.nova.amazon.com/v1` | `NOVA_API_KEY` | Amazon Nova direct API; distinct from Bedrock |
| `novita-ai` | `https://api.novita.ai/openai` | `NOVITA_API_KEY` | compatibility root |
| `ofox` | `https://api.ofox.ai/v1` | `OFOX_API_KEY` | broker |
| `opper` | `https://api.opper.ai/v3/compat` | `OPPER_API_KEY` | compatibility endpoint |
| `orcarouter` | `https://api.orcarouter.ai/v1` | `ORCAROUTER_API_KEY` | broker |
| `ovhcloud` | `https://oai.endpoints.kepler.ai.cloud.ovh.net/v1` | `OVHCLOUD_API_KEY` | OVHcloud AI Endpoints |
| `pendra` | `https://api.pendra.ai/api/v1` | `PENDRA_API_KEY` | broker |
| `pioneer` | `https://api.pioneer.ai/v1` | `PIONEER_API_KEY` | hosted |
| `poe` | `https://api.poe.com/v1` | `POE_API_KEY` | Poe API |
| `poolside` | `https://inference.poolside.ai/v1` | `POOLSIDE_API_KEY` | hosted Poolside; private deployment remains custom-compatible |
| `qihang-ai` | `https://api.qhaigc.net/v1` | `QIHANG_API_KEY` | hosted |
| `qiniu-ai` | `https://api.qnaigc.com/v1` | `QINIU_API_KEY` | hosted |
| `regolo-ai` | `https://api.regolo.ai/v1` | `REGOLO_API_KEY` | EU hosted |
| `routing-run` | `https://api.routing.run/v1` | `ROUTING_RUN_API_KEY` | broker |
| `scnet-token-plan` | `https://api.scnet.cn/api/llm/v1` | `SCNET_API_KEY` | China token plan |
| `scx-ai` | `https://api.scx.ai/v1` | `SCX_API_KEY` | Australian sovereign provider |
| `siliconflow` | `https://api.siliconflow.com/v1` | `SILICONFLOW_API_KEY` | global |
| `siliconflow-cn` | `https://api.siliconflow.cn/v1` | `SILICONFLOW_CN_API_KEY` | China |
| `stackit` | `https://api.openai-compat.model-serving.eu01.onstackit.cloud/v1` | `STACKIT_API_KEY` | EU sovereign platform |
| `standardcompute` | `https://api.stdcmpt.com/v1` | `STANDARDCOMPUTE_API_KEY` | hosted |
| `stepfun` | `https://api.stepfun.ai/v1` | `STEPFUN_API_KEY` | global |
| `stepfun-cn` | `https://api.stepfun.com/v1` | `STEPFUN_CN_API_KEY` | China |
| `stepfun-step-plan` | `https://api.stepfun.ai/step_plan/v1` | `STEPFUN_STEP_PLAN_API_KEY` | global plan |
| `stepfun-step-plan-cn` | `https://api.stepfun.com/step_plan/v1` | `STEPFUN_STEP_PLAN_CN_API_KEY` | China plan |
| `submodel` | `https://llm.submodel.ai/v1` | `SUBMODEL_INSTAGEN_ACCESS_KEY` | hosted |
| `synthetic` | `https://api.synthetic.new/openai/v1` | `SYNTHETIC_API_KEY` | broker |
| `tencent-coding-plan` | `https://api.lkeap.cloud.tencent.com/coding/v3` | `TENCENT_CODING_PLAN_API_KEY` | coding plan |
| `tencent-token-plan` | `https://api.lkeap.cloud.tencent.com/plan/v3` | `TENCENT_TOKEN_PLAN_API_KEY` | token plan |
| `tencent-tokenhub` | `https://tokenhub.tencentmaas.com/v1` | `TENCENT_TOKENHUB_API_KEY` | TokenHub |
| `tensorx` | `https://api.tensorx.ai/v1` | `TENSORX_API_KEY` | hosted |
| `the-grid-ai` | `https://api.thegrid.ai/v1` | `THEGRID_API_KEY` | hosted |
| `tinfoil` | `https://inference.tinfoil.sh/v1` | `TINFOIL_API_KEY` | confidential inference |
| `together` | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | hosted |
| `trustedrouter` | `https://api.trustedrouter.com/v1` | `TRUSTEDROUTER_API_KEY` | broker |
| `vultr` | `https://api.vultrinference.com/v1` | `VULTR_API_KEY` | hosted |
| `wafer-ai` | `https://pass.wafer.ai/v1` | `WAFER_API_KEY` | Wafer Pass |
| `wandb` | `https://api.inference.wandb.ai/v1` | `WANDB_API_KEY` | W&B Inference |
| `xiaomi` | `https://api.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | MiMo API |
| `xiaomi-token-plan-eu` | `https://token-plan-ams.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_EU_API_KEY` | Europe |
| `xiaomi-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_CN_API_KEY` | China |
| `xiaomi-token-plan-sg` | `https://token-plan-sgp.xiaomimimo.com/v1` | `XIAOMI_TOKEN_PLAN_SG_API_KEY` | Singapore |
| `xpersona` | `https://www.xpersona.co/v1` | `XPERSONA_API_KEY` | broker |
| `zai` | `https://api.z.ai/api/paas/v4` | `ZHIPU_API_KEY` | global GLM |
| `zai-cn` | `https://open.bigmodel.cn/api/paas/v4` | `ZHIPU_CN_API_KEY` | China GLM |
| `zai-coding-plan` | `https://api.z.ai/api/coding/paas/v4` | `ZHIPU_CODING_PLAN_API_KEY` | global coding plan |
| `zai-coding-plan-cn` | `https://open.bigmodel.cn/api/coding/paas/v4` | `ZHIPU_CODING_PLAN_CN_API_KEY` | China coding plan |
| `zeldoc` | `https://api.zeldoc.ai/v1` | `ZELDOC_API_KEY` | hosted |
| `zenifra` | `https://ai.zenifra.com/v1` | `ZENIFRA_AI_KEY` | hosted |
| `zenmux` | `https://zenmux.ai/api/v1` | `ZENMUX_API_KEY` | broker |

## Locked Anthropic-Compatible Profiles

Go-LIP's Anthropic compatible adapter appends `/v1/messages`; store the Anthropic SDK base root, not the final operation URL.

| Profile ID | Base URL | Env var | Inventory | Required capability posture |
| --- | --- | --- | --- | --- |
| `kimi-coding` | `https://api.kimi.com/coding/` | `KIMI_API_KEY` | static `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed` | tools+streaming; disable unproven vision/documents/parallel/reasoning_replay |
| `minimax` | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` | family-default `/v1/models` | tools+streaming; disable unproven reasoning_replay/vision/documents/parallel |
| `minimax-cn` | `https://api.minimaxi.com/anthropic` | `MINIMAX_CN_API_KEY` | family-default if fixture matches, otherwise static | separate China credential env so global and China can coexist |
| `thinking-machines` | `https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api` | `TINKER_API_KEY` | static initial `thinkingmachines/Inkling` | disable reasoning_replay and unproven media/parallel; Tinker states `cache_control` is ignored |

Kimi official coding-agent reference: <https://www.kimi.com/code/docs/en/>. The same four models are exposed through OpenAI and Anthropic-compatible paths; this spec chooses one Anthropic profile to avoid redundant aliases. Preserve truthful client identity; do not tamper with client identifiers/User-Agent to circumvent plan policy.

MiniMax official references: <https://platform.minimax.io/docs/api-reference/text-chat-anthropic> and <https://platform.minimax.io/docs/api-reference/models/anthropic/list-models>.

Thinking Machines/Tinker reference: <https://tinker-docs.thinkingmachines.ai/tinker/compatible-apis/anthropic/>.

## Products Deliberately Not Represented by Profile v1

| Product | Why profile v1 is insufficient | Treatment |
| --- | --- | --- |
| Cloudflare AI Gateway REST API | account ID in base URL, optional gateway ID header, protocol/model-dependent support | external connector, Responses preferred; Workers AI-specific product support is not claimed by this row |
| Azure OpenAI/Foundry | resource/deployment, API key or Entra, cloud control plane | external connector |
| Vertex AI | project/location/ADC/service account, publisher model routing | external connector |
| AWS SageMaker | SigV4, endpoint-specific payload contracts/control plane | external connector |
| OCI Generative AI | OCI signing, region/compartment/endpoint | external connector |
| IBM watsonx.ai | IBM IAM, project/space, native API | external connector |
| SAP AI Core | service-key JSON, OAuth, resource group/deployment discovery | external connector |
| Snowflake Cortex | account in URL, PAT/JWT/OAuth, optional role | external connector |
| Databricks AI | workspace host + token/OAuth + serving endpoint | external connector |
| Infomaniak AI | product ID in URL plus API key | external connector; do not add generic URL templates |
| Cohere | native `/v2/chat` semantics | native connector |
| Replicate | asynchronous prediction resource lifecycle and arbitrary model schemas | native connector restricted to supported language models |
| GitHub Copilot direct | OAuth/service-token entitlement lifecycle | non-ACP subscription bridge if public/permitted |
| GitLab Duo/DAP | instance-aware OAuth/PAT and workflow service | non-ACP bridge |
| Claude subscription | distinct OAuth/entitlement/billing path | non-ACP bridge if provider permits |
| Nous Portal | OAuth/scoped JWT subscription gateway | non-ACP bridge |
| xAI subscription | OAuth entitlement distinct from API key | non-ACP bridge |
| Qwen Portal | browser OAuth and request adaptations | non-ACP bridge |
| MiniMax OAuth | browser PKCE/device-style flow and refresh state | non-ACP bridge |
| Privatemode AI | operator-specific endpoint env plus key; no stable standard hosted endpoint frozen | continue using custom-compatible mode |
| Bailing | surveyed record exposed a final operation URL rather than a safely verified family base | defer; do not guess a base root |

## Connector Matrix: Frozen Architecture Inputs

| Planned identity | Transport / endpoint | Auth/addressing | Model enumeration | Implementation instruction |
| --- | --- | --- | --- | --- |
| `cloudflare-ai-gateway` | current account-scoped Cloudflare AI Gateway REST API under `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1`; Responses preferred | account ID + API token; optional gateway ID sent as `cf-aig-gateway-id` | Cloudflare account/model catalog filtered to selected protocol | reuse compatible codec inside connector; do not claim a separate Workers AI integration or use the deprecated `/compat` endpoint for ordinary calls |
| `azure-openai` / `azure-foundry` | Azure OpenAI/Foundry v1, Responses preferred where deployment supports it | resource/endpoint + deployment + API key or Microsoft Entra | Azure deployed model/resource APIs | one connector artifact may expose distinct kinds if product semantics require |
| `vertex` | native Vertex/Google generative API | GCP project + location + ADC/service account/workload identity | Vertex publisher/model catalog | distinct from existing Gemini API-key backend |
| `sagemaker` | SageMaker Runtime `InvokeEndpoint`/streaming equivalent | region + endpoint + AWS SigV4/default chain | SageMaker endpoints/control plane | require configured deterministic inference contract per endpoint; arbitrary containers are not automatically OpenAI-compatible |
| `oci-generative-ai` | OCI Generative AI inference | region + compartment + OCI signing/workload identity | OCI models/endpoints | connector-local OCI SDK/auth |
| `watsonx` | native watsonx chat/text | regional service + project/space + IBM IAM | watsonx foundation/deployed model APIs | native connector |
| `sapaicore` | AI Core/Generative AI Hub deployment URL | service-key ref -> clientid/clientsecret/auth URL/`AI_API_URL`; OAuth; resource group | AI Core deployments | service key never exposed in diagnostics; reuse compatible transport after deployment resolution only when applicable |
| `snowflake-cortex` | `https://{account}.snowflakecomputing.com/api/v2/cortex/v1` | account + role + PAT/JWT; optional documented browser OAuth | Cortex model catalog | dynamic-address connector |
| `databricks-ai` | `https://{host}/ai-gateway/mlflow/v1` | workspace host + token/OAuth | serving endpoints/models | dynamic-address connector |
| `infomaniak-ai` | `https://api.infomaniak.com/2/ai/{product_id}/openai/v1` | product ID + API key | product model list | simple dynamic-address compatible connector |
| `cohere` | `https://api.cohere.com`, native `POST /v2/chat` | Cohere token | Cohere model list | native canonical mapping, no LiteLLM translation |
| `replicate` | `https://api.replicate.com/v1`, prediction resources | bearer `REPLICATE_API_TOKEN` | models + versions/deployments | own create/stream-or-poll/cancel lifecycle; expose only configured language-model contracts |

Primary official connector references are already linked here; executor should follow them as API contracts rather than conduct a provider survey:

- Azure Responses/v1: <https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses>
- Vertex OpenAI compatibility/background: <https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/call-vertex-using-openai-library>
- SageMaker InvokeEndpoint: <https://docs.aws.amazon.com/sagemaker/latest/APIReference/API_runtime_InvokeEndpoint.html>
- OCI API index: <https://docs.oracle.com/en-us/iaas/api/>
- SAP service key: <https://help.sap.com/docs/sap-ai-core/sap-ai-core-service-guide/use-service-key>
- Cohere chat: <https://docs.cohere.com/v2/reference/chat>
- Replicate HTTP API: <https://replicate.com/docs/reference/http/>
- Cloudflare AI Gateway REST: <https://developers.cloudflare.com/ai-gateway/usage/rest-api/>

## OAuth / Subscription Bridge Reference Inputs

### Common behavior

Use Go-LIP's existing Codex secure-account patterns where applicable: connector-owned auth state, restrictive credential files, atomic writes, redacted diagnostics, refresh-before-expiry, terminal refresh-token quarantine, explicit re-login. Do not move provider OAuth state into canonical/core APIs.

### MiniMax OAuth: fully frozen flow

Pinned behavioral reference: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`, `website/docs/guides/minimax-oauth.md`.

- provider ID: `minimax-oauth`;
- inference base: `https://api.minimax.io/anthropic`;
- transport: Anthropic Messages;
- models: `MiniMax-M2.7`, `MiniMax-M2.7-highspeed` initially;
- no `MINIMAX_API_KEY` for this product;
- generate PKCE verifier/challenge and random state;
- POST `{base_url}/oauth/code` and obtain `user_code` + `verification_uri`;
- open/display verification URI and code;
- poll `{base_url}/oauth/token` until approved/expired;
- persist access token, refresh token and expiry;
- refresh when within 60 seconds of expiry using refresh-token grant;
- terminal HTTP 4xx, `invalid_grant`, revoked/`refresh_token_reused` -> quarantine refresh token, surface one re-auth-required state, no repeated doomed refresh loop;
- successful re-login clears quarantine.

### Qwen Portal OAuth

Pinned reference: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`, especially `plugins/model-providers/qwen-oauth/__init__.py`, `hermes_cli/auth_commands.py`, `agent/credential_sources.py`.

- provider ID: `qwen-oauth`;
- inference base: `https://portal.qwen.ai/v1`;
- auth type: external browser/PKCE OAuth, not an ordinary DashScope API key;
- keep product distinct from `alibaba`/DashScope profiles;
- connector-local request adaptations frozen from Hermes: normalize string message content into typed text parts, preserve image URL objects, system-last-part ephemeral cache marker only if current Portal accepts it, `vl_high_resolution_images: true`, and Qwen session metadata at the correct top-level request location;
- any adaptation requiring a new canonical field blocks this provider task rather than widening canonical APIs silently.

### xAI OAuth

Pinned behavioral sources: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`, xAI OAuth provider tests/docs and credential sources.

- provider ID: `xai-oauth`;
- distinct from API-key `xai` Chat profile;
- use current documented xAI subscription/device/browser authorization only;
- successful OAuth entitlement does not change the wire-family decision automatically;
- entitlement HTTP 403 after login is an entitlement error, not a refresh loop;
- terminal refresh errors use common quarantine behavior.

### GitHub Copilot direct

- **No Copilot ACP.**
- service identity observed in current coding clients: `https://api.githubcopilot.com`;
- use GitHub device/OAuth and Copilot service-token/model entitlement flow only when current provider policy/documentation permits third-party direct access;
- pinned survey references: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` provider/auth sources plus Cline's current `github-copilot` provider declaration;
- if the required token exchange is private/unsupported for third-party products, mark `github-copilot` unsupported-by-policy and **do not** reverse engineer it or substitute ACP.

### GitLab Duo / DAP

Pinned survey source: OpenCode commit `c77100a40c16a1c7c39115023ccd6f284b476c77` provider documentation/implementation.

- GitLab.com or configured self-managed `GITLAB_INSTANCE_URL`;
- OAuth recommended, PAT supported;
- self-managed OAuth client ID is explicit configuration when required;
- optional configured AI Gateway/workflow service;
- discover DAP workflow models (`duo-workflow-*`) dynamically from the instance/namespace;
- repository issue/MR/pipeline tools are outside this inference connector.

### Claude subscription

Pinned survey source: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` provider docs/auth code.

- distinct from Go-LIP's existing `anthropic` API-key backend;
- implement only a current documented/permitted third-party OAuth/setup-token route;
- do not spoof Claude Code client identity or claim included-plan billing that the route does not actually consume;
- if provider policy prohibits third-party use, record unsupported and stop this bridge.

### Nous Portal

Pinned survey source: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`, `website/docs/integrations/providers.md`, `hermes_cli/auth_commands.py`, `agent/credential_sources.py` and Portal runtime paths.

- distinct subscription gateway;
- OAuth-managed credentials;
- prefer scoped `inference:invoke` JWT; legacy opaque session-key fallback only if current public contract still requires it;
- automatic rotation/refresh and terminal refresh-token quarantine;
- send truthful Go-LIP/AIProxer client identity, never impersonate Hermes;
- enumerate Portal catalog dynamically.

## Exact Profile Runtime Shape

A normal Chat profile is mechanically:

```json
{
  "api_version": "lip.provider-profile/v1",
  "id": "together",
  "family": "openai-chat-compatible",
  "endpoint": {"base_url": "https://api.together.xyz/v1", "path_policy": "family_default"},
  "auth": {"mode": "bearer_env", "env_var": "TOGETHER_API_KEY"},
  "models": {"discovery": "family_default", "namespace": {"mode": "preserve"}},
  "capabilities": {"disable": ["vision", "documents", "reasoning", "parallel_tool_calls"]}
}
```

Do not add a tokenizer field unless frozen evidence requires one.

A normal Anthropic profile uses `api_key_env` and the Anthropic SDK base root:

```json
{
  "api_version": "lip.provider-profile/v1",
  "id": "minimax",
  "family": "anthropic-compatible",
  "endpoint": {"base_url": "https://api.minimax.io/anthropic", "path_policy": "family_default"},
  "auth": {"mode": "api_key_env", "env_var": "MINIMAX_API_KEY"},
  "models": {"discovery": "family_default", "namespace": {"mode": "preserve"}},
  "capabilities": {"disable": ["vision", "documents", "parallel_tool_calls", "reasoning_replay"]}
}
```

Operator configuration stays deliberately short:

```yaml
plugins:
  backends:
    - id: together
      kind: provider-profile
      enabled: true
      config:
        profile: together
```

The repaired runtime binding must derive the backend prefix (`profile.ID`), base URL, env-var root, inventory, bounded headers, closed quirks/dialects and capability ceiling from the single compiled profile authority. Current `ProfileConfigNode` projection is not sufficient evidence because it omits part of that compiled contract.

## Brownfield Gap Analysis and Repairs Applied

1. **Lossy runtime profile projection** -> add a prerequisite RED test through the production registry build path and repair the existing binding so all compiled profile semantics reach the family adapter; direct helper tests cannot certify this seam.
2. **Placeholder-only catalog** -> requirements/tasks explicitly populate real profiles and remove `example-openai-responses`.
3. **Schema types that are not executable** -> this spec does not enable namespace prefix/strip, disabled discovery, arbitrary transforms or an endpoint-template DSL.
4. **Family maximum capability overclaim** -> every real profile receives a conservative capability reduction; richer capabilities require frozen evidence.
5. **Tokenizer overgeneralization risk** -> mass tokenizer assignment was removed; tokenizer is omitted unless deliberate.
6. **Flavor-overbroad model discovery** -> narrow Responses identities use static flavor inventories with complete canonical/native/display identities when provider-wide `/models` is broader; DeepSeek/Scaleway are explicit cases.
7. **Translated/secondary protocol ambiguity** -> native flavor decisions require upstream/official evidence; Meta now has official Responses/models evidence, while xAI and Kilo remain Chat.
8. **Dynamic endpoint/multi-auth products** -> reclassified to connectors rather than widening profile v1.
9. **Ambiguous multi-protocol naming** -> if multiple identities are needed, all receive protocol suffixes; Responses is documented preferred.
10. **Regional credential collision** -> Alibaba, Moonshot, StepFun, Xiaomi, Z.AI, and MiniMax region/plan products use distinct env-var roots when accounts/entitlements may coexist; only deliberate same-credential protocol splits share a root.
11. **Post-#541 extension-plane drift risk** -> provider profiles remain pre-feature-plane immutable config input and provider connectors remain backend plugins; no `PlaneSet`/feature-bundle work was added.

## Risk Controls

| Risk | Required control |
| --- | --- |
| huge provider dump hides bad metadata | deterministic batches + exact expected-profile tests |
| compiler output is lost before backend construction | production-path RED tests + one profile-aware runtime authority |
| provider-wide `/models` advertises unsupported flavor | static flavor inventory |
| profile count creates runtime resources | preserve offline embedded compile and 1,000-profile scale proof |
| provider SDK leaks into root | external connector module only |
| OAuth refresh loop on revoked token | terminal-error quarantine + explicit re-auth |
| unofficial subscription tunneling | documented/permitted flow only; unsupported instead of circumvention |
| smaller executor invents abstraction | exact matrices + stop conditions + no broad research task |
| stale source claims | current-branch duplicate check; contradiction blocks only affected provider |

## Repository Sources of Truth

- `.kiro/steering/product.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/testing.md`
- `.kiro/steering/api-standards.md`
- `docs/extension-authoring.md`
- `internal/providerprofiles/{schema.go,compiler.go,embedded.go,certification.go,catalog.json}`
- `internal/standardplugins/{provider_profiles.go,provider_profile_binding.go,provider_profile_binding_test.go}`
- `internal/infra/runtimebundle/candidate_compile.go`
- `internal/featurebundle/merge_surface.go` and `pkg/lipsdk/feature`
- `internal/plugins/backends/modeldiscover/http_providers.go`
- archived `.kiro/specs/archive/extension-scalability-and-architecture-simplification/research.md`

Survey snapshots used to freeze catalog data:

- Cline generated provider catalog and product overrides (current 2026-08-28 survey snapshot)
- OpenCode provider documentation/registry at `c77100a40c16a1c7c39115023ccd6f284b476c77`
- Hermes Agent provider/OAuth implementation at `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`
- LiteLLM provider matrix as breadth evidence only
