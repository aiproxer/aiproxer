# Requirements Document

## Introduction

AIProxer already has proxy-owned secure sessions, protocol-neutral `PromptCacheKey` (PCK) carriage, prompt-cache residency/keep-warm, and provider profiles. Many clients never send a provider cache-routing key, so follow-up turns land on cold provider cache shards.

V1 slice: an **opt-in standard feature** that fills an empty PCK with a stable opaque value derived from the admitted authoritative session, plus the minimum serializer work so the value actually reaches providers that document `prompt_cache_key`. Everything else is in **Deferred**.

The hint is advisory routing metadata. It is never session, continuation, or cache-residency authority, and never evidence of a cache hit.

## Requirement 1 — Opt-in feature, no core growth

**User Story:** As an operator, I want to switch locality hinting on deliberately, without paying for it when it is off.

### Acceptance Criteria

1. The feature shall be an ordinary standard feature (`id: downstream-cache-affinity`) registered in `StandardBundle().Features`, implemented under `internal/plugins/features/downstreamcacheaffinity`.
2. When the feature is absent or disabled, the generation shall contain no cache-affinity `PlaneAttemptTransforms` occupant, so large-payload wire fast-path eligibility is unchanged.
3. When enabled, the feature shall contribute exactly one `request.AttemptTransform` to the existing `PlaneAttemptTransforms`; the existing canonical-required wire eligibility gate applies unchanged (enabled requests take the canonical path).
4. The change shall add no field, type, stage, plane or callback to `internal/core`, `pkg/lipsdk`, `execbackend.Backend`, `request.AttemptMeta`, `runtimebundle`, `featurehost`, or the backend-plugin protocol.
5. The feature config shall be empty; any non-empty config shall fail validation.

## Requirement 2 — Stable, opaque, backend-scoped value

**User Story:** As a user, I want follow-up turns to carry a stable locality key without exposing my session identity.

### Acceptance Criteria

1. The only conversation input shall be `AttemptMeta.Session.AuthoritativeSessionID`. When it is empty, no value shall be generated.
2. Generation shall never use `ClientSessionHint`, `ALegID`, principal/scope, workspace, trace/request ID, model, headers or SafeMetadata.
3. The value shall be `aipca1_` + unpadded base64url of `SHA-256("aiproxer/downstream-cache-affinity/v1\x00" || BackendID || "\x00" || AuthoritativeSessionID)`: exactly 50 ASCII characters, never truncated.
4. Same session + same `AttemptMeta.BackendID` shall yield the same value across retries, restarts and proxy nodes; a different backend or session shall yield a different value.
5. The value, raw session ID and PCK shall never appear in logs, metrics labels or SafeMetadata.

## Requirement 3 — Explicit intent wins (fill-only)

**User Story:** As a coding-agent author, I want my own cache key to win over a proxy-generated one.

### Acceptance Criteria

1. When `Call.PromptCacheKeyValue()` returns a non-empty value, the feature shall leave the call unchanged.
2. When `Call.PromptCacheKeyValue()` returns an alias-conflict error, the feature shall leave the call unchanged and continue; existing call validation and serializers remain the authority for that error.
3. The transform shall never exclude a candidate and shall run after existing standard attempt transforms.
4. A generated PCK shall reach backend `Open` for the selected candidate, while the existing final session scrub still clears every raw session field.

## Requirement 4 — Forward PCK only where the provider documents it

**User Story:** As an operator, I want the hint on the wire only where the upstream contract accepts it.

### Acceptance Criteria

1. Direct OpenAI Responses and direct OpenAI Chat serializers shall forward the effective PCK as the typed SDK `PromptCacheKey` field; empty means omitted.
2. OpenAI-compatible family backends (`openaicompat` Chat/Responses) shall forward PCK only when built from a provider profile that declares the `openai.prompt_cache_key` quirk; otherwise the field shall be cleared before the request.
3. The `openai.prompt_cache_key` quirk shall be accepted only for the `openai-chat-compatible` and `openai-responses-compatible` families.
4. A profile may declare the quirk only when its vendor documentation names `prompt_cache_key` on the request surface that profile's family writes, and recommends session-scoped key granularity. The verified V1 set is `mistral` and `deepinfra`; a profile meeting only one of those two conditions, or neither, is dropped from V1, not redesigned.
5. Within the new direct OpenAI and `openaicompat` forwarding scope, arbitrary custom-compatible rows and unquirked embedded profiles (including `groq`, `deepseek-*`, `alibaba*` and `fireworks`) shall omit PCK; direct Anthropic and Gemini shall remain unchanged and shall not forward it. Existing OpenResponses forwarding is outside this new gate and remains unchanged. Undocumented providers may reject the field; only an actual rejection before output shall follow normal pre-output error classification, with retry/failover determined by policy, not guaranteed.

## Requirement 5 — Authorities stay separate

**User Story:** As a maintainer, I want the hint to stay advisory.

### Acceptance Criteria

1. A generated PCK shall not authorize or resume a session, choose a route or backend, continue a provider response, or populate prompt-cache residency targets/handles or arm keep-warm.
2. Provider rejection or ignoring of the hint shall follow normal error, retry and failover semantics.
3. Each candidate attempt shall compute its value from its own `BackendID`; no mutable hint state is shared between attempts.

## Deferred

Each item becomes a one-paragraph follow-up issue under #573 when needed:

- **Cross-provider carriers:** xAI (`x-grok-conv-id` header / Responses), Fireworks (`x-session-affinity`), Alibaba (`x-dashscope-session-cache`), RunInfra, other new catalog rows, and any non-JSON-field transport. Dynamic per-request affinity needs a separate carrier design; the static profile-header whitelist does not itself authorize it, and blanket whitelist widening is not a V1 requirement.
- **OpenRouter connector** `session_id` projection, its precedence against generic extra-body `session_id`, and executable-connector opt-in.
- **Per-backend capability metadata** (generic backend feature IDs, backend-plugin feature negotiation) — only if a provider appears that accepts explicit PCK but must not receive generated ones.
- **Keyed derivation** (HMAC from the secure-session root) — only if session IDs ever stop being 256-bit random values.
- **Feature metrics** (`source` counter) and operator docs beyond a config example.
- **Large-payload wire-path synthesis** so enabled generations keep the fast path.
- Provider-specific length bounds and pre-output length validation (all V1 targets accept ≥64; generated value is 50).
