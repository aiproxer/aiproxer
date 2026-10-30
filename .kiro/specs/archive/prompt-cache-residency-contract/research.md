# Research & Design Decisions

## Research Scope

This research supports the foundation contract for issue #342. It combines:

1. current Go-LIP architecture and plugin/connector contracts;
2. current provider documentation for prompt-cache lifetime/refresh semantics;
3. current OpenAI Codex implementation details relevant to ChatGPT-subscription reuse;
4. mature OSS agent/proxy patterns that expose failure modes not obvious from provider documentation.

Provider documentation was re-checked on 2026-08-16. The design intentionally distinguishes **documented contract**, **best-effort behavior**, and **empirical implementation detail**. Only the first category may justify deterministic generic scheduling without an explicit operator override.

## Current Go-LIP Findings

### Canonical request state already has a cache hint, but not residency authority

`pkg/lipapi.Call.PromptCacheKey` is explicitly documented as a protocol-neutral request hint and not canonical trajectory control. `SemanticExtensions` can carry the same prompt-cache hint through negotiated backend-plugin paths. This is useful foreground request semantics but insufficient as a cache-residency identity because a backend may derive, clamp, hash, replace, or supplement the value with provider-specific session/affinity state.

### Session identity already has several meanings

`lipapi.SessionRef` separates client session hints, continuity key, A-leg ID, and proxy-owned authoritative session ID. The cache contract must not introduce another implicit equivalence. A provider cache may follow an effective cache key, a concrete provider cache resource, a routed shard/account, or a stable prefix lineage that is not identical to any A-leg field.

### `execbackend.Backend` is the correct core-side capability envelope

`internal/core/execbackend.Backend` already exposes:

- static and model/candidate-aware capability resolvers;
- normal canonical `Open` execution;
- optional billing finalization;
- provider/local token counting;
- generation-local lifecycle and non-billable preflight seams.

This is a better integration point than adding a new canonical operation or giving a feature plugin direct access to concrete backends.

### Executable connectors already distinguish Execute from optional control operations

`pkg/lipsdk/backendplugin.ConfiguredInstance` owns normal `Execute`, while optional interfaces such as `TokenCounter` and `BillingFinalizer` expose dedicated RPC-style operations. The protocol already supports feature/minor negotiation and host-only accounting evidence outside canonical events. Prompt-cache residency/control fits those patterns directly.

### Immutable generation lifetime is already an ownership boundary

Configured backend instances are generation-owned. A cache-control handle should therefore be generation/instance scoped and become stale on close/reload rather than pinning a retired generation for an optimization.

## Provider Semantics: Why a Universal TTL/Heartbeat Contract Is Wrong

### Anthropic Claude API

Official prompt caching currently documents:

- default `ephemeral` cache lifetime of 5 minutes;
- optional 1-hour cache duration with different write pricing;
- cache hits refresh the short-lived cache;
- cacheable-prefix minimums vary by model;
- documented pre-warming with `max_tokens: 0`, which produces no model output; incompatible request features must be removed for that prewarm shape.

This is a strong example of a backend that can advertise **known sliding expiry + active renewal**.

Source: https://platform.claude.com/docs/en/build-with-claude/prompt-caching

### Amazon Bedrock

Bedrock prompt caching is model-specific, not one backend-wide TTL. Current documentation includes different lifetimes/capabilities by model family, including 5-minute and 1-hour modes for selected Claude paths and different values for other models. Region/cross-region routing can also affect cache behavior.

This validates model/candidate-aware profile resolution and route affinity.

Source: https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html

### OpenAI direct API

Current OpenAI Responses schema for GPT-5.6+ exposes `prompt_cache_options.ttl`; current generated SDK documentation describes `30m` as the supported/default value and as a **minimum lifetime**, not a deterministic eviction deadline. The older `prompt_cache_retention` field is deprecated and describes a different retention policy.

There is no basis for translating this into “renew at 29 minutes” in generic core. A minimum residency guarantee is a distinct lifecycle category. Active renewal should remain unsupported unless the concrete backend has a documented safe operation or separately validated implementation evidence.

Source: https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_create_params.py

### Gemini

Google exposes materially different cache models:

- implicit/best-effort caching, where clients do not own a cache resource;
- explicit `CachedContent` resources with configurable TTL; when omitted, current documentation defaults to one hour;
- explicit cache resources can have their TTL/expiration updated directly.

For explicit resources, the correct maintenance action is a resource TTL update, not dummy inference. The generic control contract must therefore support non-inference renewal operations.

Sources:
- https://ai.google.dev/gemini-api/docs/generate-content/caching
- https://ai.google.dev/api/caching

### DeepSeek

DeepSeek documents automatic disk context caching as best-effort and states that unused entries are normally cleared after a period on the order of hours to days, without a contractual exact TTL. Cache reuse depends on matching persisted prefix units.

This is an observation/affinity candidate, but not a safe deterministic timer profile by default.

Source: https://api-docs.deepseek.com/news/news0802/

### OpenRouter

OpenRouter explicitly documents provider-sticky routing after cached requests to improve cache hit probability. Since the downstream provider can vary, an OpenRouter route cannot safely be modeled as one universal TTL or cache target unless the concrete downstream residency identity can be preserved.

Source: https://openrouter.ai/docs/guides/best-practices/prompt-caching

### xAI and Mistral

Both expose cache-affinity guidance/keys, but cache affinity does not imply a deterministic cache lifetime. xAI explicitly describes cache eviction as possible; Mistral's prompt-cache key is a routing optimization rather than a correctness boundary.

Sources:
- https://docs.x.ai/developers/advanced-api-usage/prompt-caching
- https://docs.mistral.ai/studio-api/conversations/advanced/prompt-caching

## Codex / ChatGPT-Subscription Findings

Codex subscription traffic is a distinct backend product from direct `api.openai.com` usage and must not inherit direct-OpenAI cache lifecycle assumptions.

Current `openai/codex` implementation establishes several independent identities:

- session/thread identity;
- `prompt_cache_key` cache/routing affinity;
- per-turn `x-codex-turn-state`, which current source documents as valid only within the Codex turn and unsafe to replay across turns;
- WebSocket continuation via `previous_response_id`;
- a WebSocket v2 connection setup call using `response.create` with `generate=false`.

The `generate=false` call is useful evidence that Codex has a non-normal-generation connection-prewarm operation, but current source does **not** document it as resetting prompt-cache residency TTL. It must not be promoted to a cache-renewal primitive until controlled testing proves both cache effect and continuation/quota safety.

Source: https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs

Design implication: Codex can support cache-key/usage observation and affinity without advertising active renewal. Per-turn routing state must not leak into a reusable cross-turn handle.

## OSS Implementation Findings

### Aider

Aider validates the product value of bounded keepalive pings but also has historical failure evidence where cache warming re-entered the agent flow and caused repeated model calls. The durable lesson is not its exact timer; it is that maintenance must be semantically isolated from foreground agent execution.

References:
- https://github.com/Aider-AI/aider/blob/main/aider/website/docs/usage/caching.md
- https://github.com/Aider-AI/aider/blob/main/aider/coders/base_coder.py

### cortexkit/anthropic-auth

This implementation is a useful Anthropic-specific precedent:

- per-session cache target tracking;
- bounded retained request bytes/session count;
- pre-expiry scheduling;
- Anthropic `max_tokens: 0` prewarm;
- incompatible-field stripping;
- request timeout and non-fatal failures;
- cache usage evidence.

It demonstrates why provider-ready renewal state belongs near the provider adapter, but Go-LIP should avoid copying full provider request clones into generic core.

Reference: https://github.com/cortexkit/anthropic-auth/blob/main/packages/core/src/cachekeep.ts

### Permafrost

Permafrost is a proxy-level DeepSeek cache stabilization implementation. Its live testing found that an anchor-only placeholder did not reliably refresh the desired persisted prefix, so it retained/replayed exact provider request context and affinity. This is direct evidence against a generic dummy message invented by core.

Reference: https://github.com/jianzhichun/permafrost

### Hermes Agent

Current Hermes contains high-value cache-identity fixes:

- physical session rotation caused by compaction can preserve a separate logical cache scope;
- `/new`, branches, delegate subagents, and tool children remain isolated;
- content-addressed `prompt_cache_key` derivation includes logical scope plus stable system/tool prefix;
- Codex physical session identity is kept distinct while `x-client-request-id` mirrors the effective final cache key;
- cache key length is bounded;
- tests pin rotation continuity and sibling/subagent isolation.

References:
- https://github.com/NousResearch/hermes-agent/blob/main/agent/prompt_cache_scope.py
- https://github.com/NousResearch/hermes-agent/blob/main/agent/transports/codex.py
- https://github.com/NousResearch/hermes-agent/blob/main/tests/agent/test_prompt_cache_scope.py

Go-LIP should borrow the **identity separation**, not Hermes's ownership of key derivation. A proxy often receives a harness-generated key and then a backend may transform it again. Therefore the cache target must be observed after backend preparation rather than recomputed by the proxy from session lineage.

## Selected Design Decisions

### D1 — Residency is provider-observed state, not a central TTL fact

**Decision:** foreground B-leg execution may produce a backend-owned `ResidencyObservation`; core consumes it but does not derive it from provider name/model/session.

**Rationale:** only the concrete adapter knows final request rewrites, selected account/region/downstream, cache breakpoints and provider response evidence.

### D2 — No canonical maintenance operation

**Decision:** cache residency/control remains a host-only plugin/core control plane. `pkg/lipapi` receives no maintenance operation/event.

**Rationale:** maintenance is not a client trajectory action and must not invoke route/failover/continuation/tool semantics.

### D3 — Lifecycle taxonomy expresses semantics, not vendor names

**Decision:** profile lifecycle categories distinguish known sliding expiry, fixed resource expiry, minimum residency, best-effort/evictable, and unknown.

**Rationale:** these categories cover the provider behaviors found without encoding one provider table into core.

### D4 — Adapter-local retained state behind a bounded handle

**Decision:** the host stores only a small opaque instance-scoped handle; provider-ready prompt/request state stays in bounded volatile adapter/connector memory.

**Rationale:** prevents generic core from retaining prompts/provider payloads and maps naturally to executable connector process isolation.

### D5 — Explicit renew/release control seam

**Decision:** active renewal is a dedicated optional backend operation with an idempotent release/forget lifecycle.

**Rationale:** mirrors existing optional control operations and avoids synthetic inference re-entry.

### D6 — Generation retirement invalidates targets

**Decision:** cache optimization never keeps a retired backend generation alive.

**Rationale:** immutable generation lifetime is a stronger architectural invariant than cache continuity. A future foreground turn can establish a fresh target.

### D7 — Effective route/account affinity is enforced by the issuing backend

**Decision:** control is invoked on the exact issuing backend instance; the adapter validates account/region/product/downstream affinity and fails stale rather than substituting.

**Rationale:** core must not duplicate provider routing identity or rerun the normal route planner.

### D8 — Observation and accounting are non-canonical sidebands

**Decision:** use a sideband pattern analogous to existing accounting evidence. Canonical output ordering remains unchanged.

**Rationale:** provider optimization facts are useful to host policy but must never become client-visible model events.

## Rejected Alternatives

### Central `provider/model -> TTL` catalog in core

Rejected because route products, provider modes and semantics differ; unknown/best-effort/minimum-residency cannot be represented safely as one expiry number.

### Generic `RefreshStrategy` receiving a full canonical call

Rejected because it encourages core to synthesize/replay inference and puts too much provider behavior behind a weak generic request abstraction.

### Store provider-ready request in a host `WarmTarget`

Rejected because it makes prompt retention a core concern and creates persistence/logging/security risk. Adapter-local state plus an opaque handle is a cleaner capability boundary.

### Use physical A-leg/session ID as cache identity

Rejected because Codex/Hermes/subagent/compaction cases demonstrate distinct session, cache-scope and turn identities.

### Treat transport keepalive as prompt-cache keepalive

Rejected. HTTP/WebSocket connection retention and model prompt-cache residency are independent resources with different lifecycles.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| ABI surface grows too broadly | connector maintenance burden | additive optional feature/minor, small DTOs, reusable converter/TCK |
| Provider-local retained prompt state grows unbounded | memory/privacy risk | count+byte caps, TTL/stale pruning, explicit release, never persist |
| Control advances provider conversation state | foreground corruption | capability must report unsupported unless semantic isolation is proven |
| Core starts interpreting provider cache keys | architecture erosion | opaque IDs/handles + archtest preventing provider-specific core logic |
| Old generation retained for warm cache | reload/resource leak | generation close invalidates handles; no generation pin for maintenance |
| HTTP success misreported as cache renewal | false confidence/cost | explicit renewal result/evidence taxonomy with unknown/cold states |
| Aggregator warms wrong downstream | wasted cost | backend must preserve downstream affinity or advertise no deterministic renewal |
| High-cardinality telemetry leaks identifiers | observability/privacy issue | coarse labels only; target/handle/key never metrics labels |

## Implementation Validation Still Required

- Determine exact numeric bounds for target ID, generation ID, opaque handle, and provider-local retained bytes using existing backendplugin bound conventions.
- Add the smallest additive backendplugin protocol minor/feature and RPC shape consistent with current generated protobuf workflow.
- Choose the exact stream-sideband bridge implementation so essential and connector backends expose the same observation-source contract without changing `lipapi.ManagedEventStream`.
- Reuse existing accounting authority/plane semantics where correct; if maintenance needs a distinct accounting plane, add it additively rather than overloading client-turn accounting.
- Provider implementations should begin with test doubles/one proven backend; broad active-renewal rollout belongs to the follow-on orchestration spec.
