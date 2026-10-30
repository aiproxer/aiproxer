# Research & Brownfield Gap Analysis

Brownfield baseline audited against `main` at `40d90d36` (2026-10-08); provider-documentation evidence rechecked 2026-10-11 against `f301224c`. The source-gap table below describes the pre-implementation baseline, not the current feature branch. Core ownership closure (#598/#599/#600, `.kiro/specs/archive/core-feature-ownership-full-closure/`) and the marker-aware provider-profile lifecycle (#619/#622) are complete and are reused, not re-certified.

## Verified evidence

| Fact | Evidence |
|---|---|
| Candidate-aware attempt transforms already receive `BackendID`, `BackendPrefixes` and the authoritative `SessionView` | `pkg/lipsdk/request/attempt_transform.go:20-42`; `internal/core/runtime/executor_attempt_transform.go:28-52`; run per candidate at `executor_open_attempt.go:470-473` |
| Existing standard attempt transforms use order 0 | `pathvirtualization/outbound/attempt.go:63`; `reasoningpreservation/transform.go:37` |
| `PlaneAttemptTransforms` is canonical-required, so any occupant declines the large-payload wire path | `pkg/lipsdk/feature/plane_manifest.go:538-540`; `internal/core/largebody/authority_gate.go`, `eligibility.go` |
| Pure standard features register a factory; absent rows contribute nothing | `internal/standardplugins/standard_table.go:141-160`; `features_install.go` (`featureAgentLoopGuard`, `featurePartsNoop`); `feature_yaml.go:11` |
| Session IDs are 32 random bytes | `internal/core/securesession/app/ids.go:16,33-43` |
| No production stage after attempt transforms writes canonical PCK in the root module | `rg '\.PromptCacheKey *='`: only frontend decode, backend-plugin carrier conversions, and the Codex connector's own payload |
| PCK alias conflicts are already rejected by `Call` validation | `pkg/lipapi/call.go:249`; `semantic_extension.go:44` |
| Direct OpenAI Responses does not forward PCK | `internal/plugins/backends/openairesponses/invoke.go:39` (`ParamsForCall` has no PCK) |
| Direct OpenAI Chat does not forward PCK | `internal/plugins/backends/openailegacy/invoke.go:44` |
| OpenAI-compatible family reuses those serializers | `internal/plugins/backends/openaicompat/invoke.go:24,43` |
| SDK has typed fields for both | openai-go v3.66.0 `ChatCompletionNewParams.PromptCacheKey`, `responses.ResponseNewParams.PromptCacheKey` |
| OpenResponses protocol already forwards PCK | `internal/plugins/protocols/openresponses/encode.go:59-63` |
| Profiles have a bounded, family-checked quirk list | `internal/providerprofiles/schema.go:81-87,246-263,490-493` |
| Profile builders already receive the compiled profile from the marker-aware wrapper | `internal/standardplugins/provider_profile_binding.go:236-262,304-327` |
| Catalog has 144 rows, 137 of them OpenAI-compatible (129 Chat, 8 Responses); `mistral` and `deepinfra` are Chat rows, `meta` and `groq` are Responses rows; `xai-responses`, `runinfra` absent | `internal/providerprofiles/catalog.json` |
| Codex connector derives its own PCK and ignores the canonical one | `connectors/codex/internal/codex/attempt.go:50`, `headers.go:84-121` |

## Simplification record

The previous SDD (Oct 8 rebaseline) had 11 requirement areas, ~70 acceptance criteria, an 820-line design and 41 tasks, against steering budgets of 5 / 25 / 300 / 12 (`.kiro/steering/delivery.md`). This revision cuts it to one slice. Concepts retired:

| Retired | Why it is not needed |
|---|---|
| Keyed HMAC, `DomainKeyDeriver` capability, secure-root closure in `runtimebundle`, featurehost key composition, NUL-free label rule, key domain | Session IDs are 256-bit random; unkeyed domain-separated SHA-256 is equally opaque. It also becomes stable across restarts and nodes, which the keyed design did not guarantee for memory stores. |
| `pkg/lipsdk/backendfeature`, `execbackend.Backend.Features`, `AttemptMeta.BackendFeatures` | A backend that forwards PCK already accepts explicit client PCK on that carrier; the generated value uses the same carrier. Gating lives in the serializer (direct OpenAI always, compatible family by profile quirk). |
| Backend-plugin feature `downstream_cache_affinity_v1`, feature-minor entry, dual host offer lists, adapter mapping | Connectors already receive PCK through the existing semantic carrier; each connector decides whether to forward. OpenRouter is deferred. |
| Typed `cache_affinity` schema (per-flavor struct, transport enum, wire name, max length, synthesis flag, 10 validation rules, `MinCacheAffinityValueLength` cross-test) | A profile has exactly one family, all V1 targets use the typed SDK `prompt_cache_key` field, and the 50-char value fits every documented bound. One quirk ID replaces the schema. |
| Feature observer, `Source`/`Outcome` enums, metrics adapter in featurehost | Delivery steering defers metrics beyond a few counters; V1 ships none. Provider cache-usage evidence already exists. |
| featurehost adapter | The feature has no process resources, so it is a plain `StandardBundle().Features` factory. |
| Re-certification of the provider-profile lifecycle, marker-forgery tests | Already shipped and tested by #619/#622 (`ExpandProviderProfileRowsWithCatalog` rejects forged markers at `provider_profile_binding.go:153-157`). |
| Post-transform PCK writer AST ratchet, 12 bespoke arch ratchets, TCK package, benchmarks, ownership census | `testing.md` Test Proportionality: feature tests prove feature behavior; new arch checks extend generic rules in their own PR. The PCK-writer premise is checked once by the end-to-end test. |
| xAI header/Responses rows, RunInfra row, OpenRouter body precedence | Separate carriers or connectors; deferred as ordinary follow-ups. |

Net effect: zero changes to core, SDK, runtimebundle, featurehost and backend-plugin protocol; one ~40-line feature package, three small serializer edits, one quirk, two catalog edits.

## Accepted trade-offs

- **Wire fast path.** When enabled, every request takes the canonical path because the plane is canonical-required. Operators opt in knowingly; wire-path synthesis is deferred.
- **No per-backend synthesis opt-out.** When enabled, every backend that forwards PCK receives the generated value. That set is small and explicit: direct OpenAI, quirked profiles, OpenResponses-compatible upstreams (whose protocol defines `prompt_cache_key`).
- **No length pre-check.** The 50-char value fits all targets. An over-long explicit client key is rejected by the provider before output, which is already an explicit failure.

## Provider evidence for the `openai.prompt_cache_key` quirk

Selective survey of primary vendor documentation, rechecked 2026-10-11; not an audit of all 137 eligible catalog rows. V1 requires both the actual `prompt_cache_key` request-field surface written by the catalog family and a session-scoped fit. Automatic prefix caching, response telemetry, isolation and affinity headers are distinct concepts; documenting one does not prove absence of another. Only the existing `mistral` and `deepinfra` Chat rows receive the quirk.

### Row-by-row result

| Row | Family | Result | Evidence |
|---|---|---|---|
| `mistral` | Chat | **quirk** | https://docs.mistral.ai/studio/conversations/advanced/prompt-caching — curl body carries `"prompt_cache_key": "support-session-42"`; "Use a stable application-level identifier, such as a conversation ID, session ID, or workflow ID"; "increases the chance of a cache hit, but it doesn't guarantee one" |
| `deepinfra` | Chat | **quirk** | https://docs.deepinfra.com/chat/prompt-caching/ — parameter table: "`prompt_cache_key` / `string` / An explicit key for cache lookup"; "We recommend using a session-scoped key" |
| `meta` | Responses | excluded | https://dev.meta.ai/docs/prompt-caching accepts the field but inverts the granularity guidance: "Don't over-partition: unique keys per user or per session lower hit rates"; "not a per-user or per-request value" |
| `xai` | Chat | deferred | https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits documents `prompt_cache_key` for Responses and `x-grok-conv-id` for Chat, while the catalog row is Chat-family; no V1 carrier redesign |
| `fireworks` | Responses | unconfirmed on catalog surface; excluded | https://docs.fireworks.ai/guides/prompt-caching demonstrates Chat `x-session-affinity` / `user` routing and `prompt_cache_isolation_key` for cache separation. It does not confirm `prompt_cache_key` on the Responses row. Isolation serves privacy/reset needs and still permits reuse within a key; it is not universally worse caching |
| `groq` | Responses | excluded | https://console.groq.com/docs/responses-api lists `prompt_cache_key` under "Unsupported Features" |
| `deepseek-openai`, `deepseek-responses` | Chat / Responses | unconfirmed; excluded | https://api-docs.deepseek.com/guides/kv_cache documents automatic prefix caching and response `usage.prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`. These are telemetry, not request carriers; this guide does not establish PCK support or rejection on either catalog surface |
| `alibaba`, `alibaba-cn`, `alibaba-coding-plan`, `alibaba-coding-plan-cn`, `alibaba-token-plan-cn` | Chat | unconfirmed; excluded | https://help.aliyun.com/en/model-studio/qwen-api-via-dashscope describes the native DashScope API, not confirmation of `prompt_cache_key` and session-scoped fit on every compatible catalog row |
| `cerebras`, `together`, `novita-ai`, `vercel-ai-gateway` | Chat / Responses | unconfirmed; excluded | No verified request-field + session-scoped fit evidence retained in this selective survey. Automatic/gateway-managed caching documentation alone would not prove lack of key support |

Unconfirmed providers default to off; exclusions are V1 eligibility decisions, not blanket unsupported-provider claims. New rows or carriers remain Deferred.

### Rejection risk and evidence limits

An undocumented provider **may** reject PCK; absence from caching documentation does not establish rejection. Groq's primary Responses documentation explicitly lists PCK as unsupported, but a native Groq PCK failure was not directly observed. Earlier DeepSeek-model rejection reports concern Azure AI Foundry and Volcano Engine gateways, not native DeepSeek; they are secondary reports, not primary API-contract evidence, and cannot establish native behavior. Errors about other Groq fields likewise do not prove PCK-specific rejection.

Only an actual rejection before output follows the existing normal pre-output error classification. Retry or failover is controlled by policy and error eligibility; neither is guaranteed for every rejection. A provider may instead ignore an advisory hint. Default-off forwarding remains the safe V1 choice under this uncertainty.

The verified V1 set is therefore the two rows whose docs both name the field **and** recommend session-scoped granularity. No Responses-family row qualifies, so `FamilyOpenAIResponses` is an accepted but unused validation surface in V1.

No new live-provider probes are claimed. The narrow field-and-granularity rule is unchanged; no new quirk semantics or provider expansion follows from these corrections.

### Deferred carriers worth their own issues

Fireworks `x-session-affinity`, OpenRouter `session_id`, Alibaba `x-dashscope-session-cache`, xAI Responses plus `x-grok-conv-id`. Dynamic per-request affinity requires a separate carrier design. `providerprofiles.allowedHeader` (`schema.go:502-508`) governs static configured headers, not dynamic session-derived authority; widening it is neither sufficient nor a blanket requirement. V1 adds no core surface.
