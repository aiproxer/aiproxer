# Requirements Document

## Introduction

Go-LIP must enter the Open Core release with broad, explicit support for hosted inference providers, inference brokers, and coding-oriented subscription gateways without turning each OpenAI/Anthropic-compatible vendor into a new Go backend implementation.

This specification expands provider coverage through the already-implemented `lip.provider-profile/v1` mechanism whenever an upstream can be represented by one of Go-LIP's certified compatible protocol families. A dedicated connector or authentication bridge is required only when the upstream cannot be represented safely by the existing declarative profile contract.

The provider inventory and protocol facts frozen by this specification are recorded in `research.md`. Implementation agents MUST treat that researched matrix as input data; implementation is not expected to rediscover provider endpoints, credentials, API flavors, or model-discovery behavior.

## Scope and Boundary Context

In scope:

- text/code/agentic inference providers, brokers, gateways, and direct subscription-backed inference paths;
- OpenAI Responses, OpenAI Chat Completions (legacy), Anthropic Messages, and OpenResponses-compatible upstream surfaces already representable by Go-LIP;
- first-class embedded provider profiles with stable backend prefixes, endpoints, credential environment-variable references, inventory policy, capability ceilings, and any already-supported closed quirks;
- external connector/bridge implementations for included providers that require cloud signing, OAuth/token lifecycle, dynamic account/project/deployment addressing, provider-native request/response semantics, or control-plane model/deployment discovery;
- deterministic operator configuration, diagnostics, inventory, conformance, documentation, and release packaging for the added provider identities.

Out of scope:

- **all ACP/Agent Client Protocol providers and ACP wrappers**, including Copilot ACP, Gemini CLI ACP, Cursor CLI ACP, AGY CLI ACP, or any new ACP connector;
- adding new frontend protocols;
- audio-only, speech-only, image-only, embedding-only, reranker-only, OCR-only, or other non-language-model integrations merely because LiteLLM exposes them;
- an automatic runtime dependency on Models.dev, Cline, OpenCode, Hermes, or LiteLLM;
- runtime downloading or executing provider definitions;
- arbitrary provider rewrite DSLs, regex transforms, scripts, templates, or secret values in provider profiles;
- Open Core vs Enterprise licensing/product separation;
- reimplementing providers already supported by the current standard distribution or an existing connector unless the researched matrix identifies a distinct missing API/credential product.

## Frozen Coverage Classes

The implementation MUST cover the following classes from the researched matrix:

1. **Responses-first / multi-flavor high-value providers:** DeepSeek, Fireworks AI, Groq, DigitalOcean Serverless Inference, Scaleway Generative APIs, Vercel AI Gateway, Requesty, Meta Model API, plus any other provider explicitly marked `Responses` in the frozen matrix.
2. **Conventional OpenAI-compatible hosted providers/brokers:** the static-base-URL rows in the frozen Chat matrix, including Kilo Gateway, Alibaba/DashScope plan variants, Moonshot, xAI API-key access, Together, Cerebras, SambaNova where frozen, Mistral subject to compatibility certification, Baseten, DeepInfra, Novita, Venice where frozen, Featherless/Friendli where frozen, Arcee, GMI Cloud, IO.NET, Helicone, LLM Gateway, Z.AI/Zhipu plan variants, Xiaomi plan variants, Tencent plan variants, StepFun plan variants, ModelScope, Amazon Nova direct API, OVHcloud, STACKIT, SCX.ai, SiliconFlow, W&B Inference, Poolside hosted API, Poe, Tinfoil, ZenMux, and the researched long-tail rows that fit `lip.provider-profile/v1` without schema widening.
3. **Anthropic-compatible hosted products:** Kimi for Coding, MiniMax global/China, Thinking Machines/Tinker, and any other frozen Anthropic-profile row that fits the current v1 profile contract.
4. **Managed-cloud / dynamic-address integrations:** Azure OpenAI/Foundry, Google Vertex AI, AWS SageMaker, OCI Generative AI, IBM watsonx.ai, SAP AI Core, Cloudflare AI Gateway, Snowflake Cortex, Databricks AI, Infomaniak AI, and dynamic-account products explicitly classified as connector work by the frozen matrix.
5. **Provider-native integrations:** Cohere and Replicate, plus any frozen non-ACP provider whose required language-model semantics cannot be represented by an existing compatible family.
6. **Non-ACP subscription/OAuth bridges:** direct GitHub Copilot HTTP subscription access where public/permitted, GitLab Duo/DAP, Claude subscription/OAuth access where public/permitted, Nous Portal, xAI subscription OAuth, Qwen OAuth, and MiniMax OAuth.

Existing Go-LIP support is a hard exclusion from duplicate implementation. At specification baseline this includes OpenAI Responses, OpenAI legacy Chat, Anthropic API-key access, Gemini API-key access, Bedrock, Alibaba Token Plan International's existing dedicated backend, OpenRouter, NVIDIA, Hugging Face, OpenCode Go/Zen, OpenAI Codex/app-server, CommandCode OpenAI/Anthropic, Ollama/Ollama Cloud, llama.cpp, LM Studio, vLLM, local-stub, Cursor SDK, and existing ACP connectors.

## Requirements

### Requirement 1: Prefer Declarative Compatible-Provider Support

**Objective:** As an operator, I want compatible vendors to be represented as data rather than bespoke backend code, so provider breadth does not multiply runtime architecture and maintenance cost.

#### Acceptance Criteria

1. When an included provider can be represented by an existing certified compatible family using a static base URL, one supported credential environment-variable reference, supported model inventory behavior, bounded safe headers, capability ceilings, and already-implemented quirks, the standard distribution shall expose that provider through an embedded provider profile rather than a new provider-specific Go backend or executable connector.
2. When a provider profile is selected in runtime configuration, the system shall expand it through the existing `provider-profile` binding to the matching compatible factory without requiring an additional backend registration.
3. When a provider-profile-only change is made, the change shall not require edits to canonical `pkg/lipapi`, core routing/runtime, frontend implementations, backend-plugin ABI, or shared contribution registries.
4. If a provider requires behavior that `lip.provider-profile/v1` intentionally rejects, the implementation shall graduate that provider to an existing-family extension or external connector instead of weakening the profile schema or embedding arbitrary transforms.
5. The embedded provider catalog shall contain real supported provider rows and shall not retain the placeholder `example-openai-responses` row as a production-visible provider after the first real profile program lands.

### Requirement 2: Deterministic Protocol Selection and Provider Naming

**Objective:** As an operator, I want provider identities to communicate their upstream protocol clearly and use the modern Responses API by default where it is genuinely supported.

#### Acceptance Criteria

1. Where an included provider natively supports OpenAI Responses for the complete intended coding/text model set with the semantics required by Go-LIP, the system shall make the Responses-backed profile the provider's default/unsuffixed identity.
2. Where OpenAI Responses supports only a strict subset of a provider's intended model set, the system shall expose explicit protocol identities such as `<provider>-responses` and `<provider>-openai`, shall make `<provider>-responses` the preferred documented selector, and shall prevent the Responses identity from advertising models that cannot execute through Responses.
3. Where a provider serves distinct intended model populations through different supported API flavors, the system shall expose separate protocol-specific identities using the suffixes `-responses`, `-openai`, `-anthropic`, or `-openresponses` as applicable rather than combining incompatible upstream behavior behind one profile.
4. The suffix `-openai` shall mean OpenAI Chat Completions/legacy compatibility; it shall never mean the OpenAI Responses API.
5. If multiple API flavors expose the same intended model set and no material semantic/capability coverage is gained by the additional flavor, the system shall expose only the preferred flavor rather than duplicating equivalent provider profiles.
6. If official/current upstream evidence does not establish native Responses compatibility, the system shall not select Responses merely because LiteLLM or another intermediary can translate that provider behind its own `/responses` surface.
7. Provider IDs, backend prefixes, route selectors, diagnostics profile IDs, and canonical inventory prefixes shall use the same stable profile identity.
8. Kilo Gateway shall be treated as OpenAI Chat compatible in this specification because its current official gateway documentation exposes `/chat/completions` as the supported OpenAI-compatible inference endpoint; a Cline-internal Responses classification shall not override the provider's public contract.

### Requirement 3: Correct Model Enumeration and Flavor-Specific Inventory

**Objective:** As an operator, I want each provider identity to enumerate only models that the selected upstream API flavor can actually serve.

#### Acceptance Criteria

1. When a profile's provider exposes an OpenAI-compatible model-list endpoint whose returned language-model set is valid for that profile's selected flavor, model inventory shall use the existing family-default remote discovery behavior.
2. When a provider's `/models` response contains models that are not valid for the selected profile flavor, the profile shall use a static curated inventory for that flavor instead of exposing an over-broad remote inventory.
3. When an Anthropic-compatible provider exposes its model list through the already-supported alternate-model-path quirk, the profile shall use that existing closed quirk and explicit model path; no new arbitrary path/rewrite mechanism shall be introduced.
4. Static model rows shall carry explicit canonical IDs, native IDs, and useful display names, and canonical IDs shall remain stable across reloads.
5. Inventory failure for one configured provider shall remain fail-soft according to the existing model-inventory contract and shall not suppress inventories from unrelated providers.
6. No profile shall claim model capabilities that exceed the certified family capability ceiling; provider-specific unsupported capabilities shall be disabled where required by researched evidence.

### Requirement 4: Secure and Predictable Credential Handling

**Objective:** As an operator, I want every added provider to follow Go-LIP's existing secrets and authentication boundaries.

#### Acceptance Criteria

1. Embedded provider profiles shall contain credential environment-variable names only and shall never contain API-key, OAuth-token, client-secret, cookie, or other credential values.
2. A bearer-token compatible profile shall use `bearer_env`; an Anthropic `x-api-key` compatible profile shall use `api_key_env`; unsupported auth combinations shall continue to fail closed.
3. If an included provider requires more than one independent credential/configuration value to construct requests or its endpoint, that requirement shall not be hidden in a profile's static headers; the provider shall use an appropriate connector/bridge or an explicitly shared, bounded architecture extension.
4. OAuth/subscription integrations shall store, refresh, rotate, revoke, and quarantine credentials using provider-specific secure lifecycle code and shall never expose refresh/access tokens in diagnostics or config serialization.
5. Cloud-managed providers shall use the provider's supported workload identity/signing chain where applicable rather than converting cloud credentials into a literal static bearer token in YAML.
6. Existing restrictions on remote HTTP endpoints, URL userinfo/query/fragment data, authorization-like profile headers, and literal YAML secrets shall remain in force.
7. When region/plan products may be configured simultaneously, the system shall use distinct credential environment-variable references where sharing one variable would prevent independent credentials. The frozen matrix applies this to the Alibaba, Moonshot, StepFun, Xiaomi, Z.AI, and MiniMax region/plan products; protocol-split profiles may share a reference only when they intentionally use the same credential product.

### Requirement 5: Non-Profile Provider Integrations

**Objective:** As an operator, I want major non-compatible providers and managed clouds supported without contaminating core logic or the declarative profile seam.

#### Acceptance Criteria

1. If an included provider requires SigV4/OCI signing, Microsoft/Google/IBM/SAP token exchange, dynamic project/account/deployment addressing, provider-native request/response structures, asynchronous prediction lifecycle, or control-plane discovery, the system shall implement it outside the declarative profile catalog.
2. Where an included provider is not profile-compatible, the system shall use the repository's external backend-connector architecture unless an already-existing in-process family adapter is the documented architecture owner for the shared behavior; provider connectors shall not be modeled as `PlaneSet` feature contributions.
3. Each new external connector shall own its credential handling, upstream transport, model/deployment discovery, cancellation/error mapping, connector manifest, packaging metadata, and conformance evidence without adding provider-specific branches to core routing or canonical execution.
4. Managed-cloud connectors shall expose stable Go-LIP model identities even when upstream resources are region-, project-, account-, deployment-, or compartment-scoped.
5. Provider-native connectors shall fail before upstream work when a canonical semantic requirement cannot be represented by that provider, rather than silently dropping or approximating required semantics.

### Requirement 6: Non-ACP OAuth and Subscription Bridges

**Objective:** As a developer, I want supported consumer/coding subscriptions usable directly where lawful and technically supported, without conflating them with API-key products or ACP execution.

#### Acceptance Criteria

1. OAuth/subscription access shall be represented as a distinct backend/credential product from the same vendor's ordinary API-key profile where billing, model entitlement, endpoint, or token lifecycle differs.
2. Device-code, browser OAuth, PKCE, and refresh-token flows shall follow each provider's documented/permitted flow and shall surface actionable re-authentication errors after terminal refresh failures.
3. A bridge shall not spoof another client, falsify User-Agent/client identity, scrape private credentials, bypass plan restrictions, or intentionally circumvent provider terms or entitlement checks.
4. **No ACP transport or ACP process-spawning path shall be added or modified as part of this specification.**
5. If a researched subscription integration ceases to provide a documented/permitted third-party path before implementation, that bridge shall be omitted with a documented unsupported result rather than implemented through an unofficial circumvention.

### Requirement 7: First-Class Operator Configuration and Diagnostics

**Objective:** As an operator, I want newly supported providers to be easy to configure and inspect consistently.

#### Acceptance Criteria

1. Every embedded provider profile shall be selectable through a concise `kind: provider-profile` runtime row referencing its stable profile ID.
2. When a profile row is expanded and its backend is constructed, the system shall preserve the compiled profile's backend prefix, upstream base URL, credential environment-variable root, tokenizer setting when deliberately present, inventory configuration, bounded safe headers, capability ceiling, closed family quirks, and OpenResponses dialect declarations rather than requiring operators to repeat those fields or projecting only a lossy subset into generic compatible configuration.
3. `check-config` shall reject unknown profile IDs and invalid embedded profiles before serving traffic and without contacting provider networks.
4. `inspect`, inventory diagnostics, and route diagnostics shall identify the profile, effective compatible family, origin, and sanitized endpoint identity without revealing credential values or sensitive URL data.
5. Every new external connector/bridge shall have a documented configuration example, credential inputs, model inventory behavior, and `doctor`/inspection expectations consistent with the connector framework.

### Requirement 8: Compatibility, Conformance, and Negative Semantics

**Objective:** As a maintainer, I want broad provider additions to preserve Go-LIP's canonical semantic guarantees rather than merely return text in happy-path smoke tests.

#### Acceptance Criteria

1. Each profile shall compile and certify against the existing provider-profile and compatible-family contracts before it can be considered supported.
2. When a compatible-family profile batch is implemented, the system shall exercise the existing backend TCK/profile certification and at least one representative executable mapping test through the production `kind: provider-profile` configuration-to-registry-build path for each family behavior newly relied upon by the batch; direct tests of a profile-aware helper alone are insufficient.
3. Adding a provider profile shall not create a new frontend-by-provider Cartesian conformance cell or a new end-to-end sentinel pair.
4. New connectors shall pass the connector contract suite, protocol/family-specific parity tests, cancellation/close tests, inventory tests, and hard-negative semantic rejection tests required by repository testing steering.
5. When a provider does not support a required tool/reasoning/document/vision/ordered-item semantic, the backend shall reject or become ineligible according to existing capability/dialect admission rules; it shall not silently downgrade the request.
6. LiteLLM compatibility tables may be used as breadth evidence only; a translated LiteLLM endpoint shall not be accepted as proof of a provider's native upstream API contract.
7. Real provider profiles shall explicitly reduce unproven family-maximum capabilities rather than inherit optimistic support silently.

### Requirement 9: Incremental, Reviewable Bulk Delivery

**Objective:** As a maintainer, I want a large provider expansion to land in bounded batches so regressions and bad catalog data are attributable.

#### Acceptance Criteria

1. Profile additions shall be grouped into deterministic batches defined by this specification rather than one unreviewable catalog dump.
2. Every profile batch shall pass provider-profile validation, targeted profile/binding tests, the profile-only change-surface gate, and relevant parity checks before the next batch begins.
3. Connector/bridge work shall be independently buildable and testable per connector and shall not depend on unfinished unrelated connectors.
4. A failed or deferred provider shall not block completion of already-independent provider batches; its status shall be recorded explicitly rather than replaced with guessed configuration.
5. The final implementation shall leave a machine-checkable expected provider-ID inventory so accidental provider deletion, duplicate ID creation, or protocol-name drift is detected by tests.

### Requirement 10: Scalability and Offline Determinism

**Objective:** As an operator, I want broad provider coverage without startup network dependencies or provider-count-driven runtime architecture growth.

#### Acceptance Criteria

1. The standard provider-profile catalog shall remain embedded, bounded, deterministic, and loadable without network or process work.
2. Increasing the compatible provider catalog shall not create one factory, goroutine, HTTP client, process, or shared registry entry per unconfigured catalog profile at startup.
3. Catalog validation/compilation shall remain compatible with the existing 1,000-profile bounded-scale contract and shall not construct a frontend-by-provider compatibility product.
4. Models.dev/OpenCode/Cline/Hermes/LiteLLM data used during specification research or maintenance shall be checked into reviewed project data before release; the production binary shall not require those external registries at runtime.
5. When provider instances are composed, the system shall create runtime backend/inventory resources only for configured instances according to the existing generation lifecycle; profile compilation shall remain pure immutable generation input and shall not register feature-plane contributions.

### Requirement 11: Documentation and Support Contract

**Objective:** As a user, I want the supported-provider list to state what is actually implemented, how each provider is reached, and which protocol identity is preferred.

#### Acceptance Criteria

1. Project documentation shall list each newly supported first-class provider identity, its protocol family, base endpoint or endpoint construction rule, required environment variable(s), model enumeration method, and any plan/region-specific caveat.
2. Where multiple protocol identities exist for one provider, documentation shall mark the Responses identity as preferred when this specification selected it and shall explain which models require the supplemental identity.
3. Documentation shall distinguish API-key access from subscription/OAuth access and shall not imply that one consumes the other's billing/quota.
4. Documentation shall distinguish dedicated first-class support from the existing arbitrary custom-compatible backend modes.
5. Documentation shall explicitly state that ACP integrations are outside this provider-expansion specification.

### Requirement 12: No Duplicate or Stale Provider Claims

**Objective:** As a maintainer, I want the final provider catalog to reflect the actual repository and upstream contracts, not stale survey assumptions.

#### Acceptance Criteria

1. Before adding any frozen provider identity, implementation shall check the current branch for an existing built-in, connector, or profile with equivalent upstream product semantics and shall reuse or update it rather than create a duplicate.
2. If the implementation branch has gained equivalent support after this specification baseline, the corresponding task shall become a verification/documentation task instead of duplicating code.
3. Provider API flavor selection shall use the frozen researched evidence in `research.md`; if an upstream contract has materially changed, the executor shall stop only that provider task, record the exact contradiction, and continue independent tasks rather than invent a new architecture.
4. The final expected-provider manifest/test shall contain no duplicate semantic products under inconsistent aliases unless the distinct names intentionally represent region, commercial plan, protocol flavor, or credential product.
