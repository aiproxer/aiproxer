# Research & Design Decisions

## Research Scope

This document converts the provider/OSS research behind issue #342 into orchestration decisions for the second, chronologically dependent Kiro spec. It assumes the `prompt-cache-residency-contract` foundation has landed and therefore does not redefine effective cache identity, provider lifecycle DTOs, opaque handles, renew/release semantics, or connector ABI.

External provider facts were re-checked on 2026-08-16. The orchestration deliberately distinguishes:

1. **deterministic provider fact** — usable by default scheduling;
2. **provider minimum/best-effort fact** — useful for observation but not a default eviction timer;
3. **operator heuristic** — explicit host policy, never represented as provider truth;
4. **unproven protocol behavior** — no autonomous renewal.

## Go-LIP Runtime Findings

### OS-command tool classification is already canonical

`pkg/lipapi.ToolEvent` carries a canonical `ToolCategory` derived by the existing lifecycle correlation/classifier. `ToolCategoryOSCommand` covers bash/exec/terminal/process families. The keep-warm feature should consume this output instead of creating a second tool-name catalog or inspecting shell text.

The category proves only that the model handed the client an OS-command-shaped tool call. Go-LIP cannot see the harness's local subprocess execution. Therefore the observable pause is:

```text
committed successful B-leg emits completed OS-command tool call
        -> no subsequent real A-leg turn yet
```

This is sufficient because a short command returns before the first cache-expiry deadline and cancels the epoch without provider cost.

### A-leg is the correct pause owner, but not the cache identity

A-leg/session authority is stable across B-legs and is the correct owner for an idle epoch and admin disable policy. Cache target identity still comes only from the backend observation contract. One A-leg can have multiple provider cache targets and each new real turn supersedes the previous idle epoch.

### B2BUA commitment matters

Fallback/race attempts can produce provider responses/usage, but only the B-leg whose output is committed to the client is part of the continuing session trajectory. Maintenance must therefore arm from the committed successful B-leg only. A losing race cannot create background provider traffic just because it happened to populate a cache.

### Runtime generations are the controller lifetime

The foundation makes renewal handles/controller functions configured-backend-instance scoped. The existing runtime already owns backend instances through immutable config generations and quiesce/close ordering.

A process-global scheduler retaining backend callbacks would either:

- keep old generations reachable after reload; or
- need a second indirection/weak registry to rediscover equivalent backends, which would recreate routing/account-affinity logic.

Selected decision: **one scheduler manager per runtime generation**. It owns no per-target long-lived goroutine. On quiesce it cancels/release targets and joins bounded workers before backend close. Maintenance does not migrate across reload; the next foreground turn establishes fresh targets.

Process-owned per-session admin disable state remains safe because it stores only A-leg policy, never provider handles or backend references.

### Route override gives the right admin-control precedent

`internal/core/routeoverride` already demonstrates an A-leg-keyed command service separated from immutable routing execution. Keep-warm disable/clear should use the same conceptual split:

- authenticated admin command;
- bounded process-owned state keyed by validated A-leg authority;
- no untrusted client request field;
- mutation invalidates live derived state immediately.

## Provider Evidence Matrix

| Backend/product | Current cache fact | Safe default scheduling | Initial active renewal posture |
|---|---|---|---|
| Anthropic direct Messages | caching opt-in; ephemeral default 5m, optional 1h; cache usage fields; documented `max_tokens: 0` prewarm | yes when backend observation supplies deterministic expiry for an actually cached target | first active candidate, but foreground cache enrollment is a separate explicit backend option |
| Anthropic via Bedrock/Vertex/other products | lifecycle/API behavior product-specific; automatic caching support differs by product | only from product adapter observation | not inherited from direct Anthropic implementation |
| OpenAI direct GPT-5.6+ | implicit/explicit breakpoints; current `ttl=30m` is documented as a **minimum** lifetime, backend may retain longer | no default eviction timer from the minimum | observation-only until safe renewal primitive proven |
| Codex ChatGPT subscription | prompt cache key/session affinity exists; current upstream has turn-scoped routing state and WS `generate=false` setup | no contractual cache expiry | observation/affinity only; active renewal requires controlled live/protocol proof |
| Gemini implicit | automatic/best-effort | no deterministic timer | observation-only |
| Gemini explicit `CachedContent` | cache resource expiration/TTL is mutable | yes if Go-LIP owns/observes such a resource | future: current adapter does not create/manage `CachedContent`; orchestrator must not start doing so implicitly |
| DeepSeek | automatic disk cache; best-effort, unused entries commonly hours-days | no default timer | observation-only; explicit operator heuristic only after a safe adapter renewal implementation exists |
| OpenRouter | downstream provider specific; sticky routing can preserve cache affinity | only if downstream residency is preserved/observed | no generic aggregator renewal |
| xAI/Mistral/Z.AI and similar | affinity/automatic cache semantics exist, deterministic public TTL often absent | no default timer | observation-only until adapter evidence |

### Anthropic current contract

Official current documentation states:

- prompt caching is opt-in; without cache control, no prompt cache is used;
- `ephemeral` default lifetime is 5 minutes;
- 1-hour TTL is available;
- short cache hits refresh residency;
- shorter-than-minimum prompts silently remain uncached and report zero cache creation/read usage;
- `max_tokens: 0` performs prefill/cache write/read without output generation;
- zero-output prewarm requires a cache breakpoint on the prefix shared with the subsequent request; placing the marker on a warmup placeholder is wrong;
- zero-output prewarm rejects streaming, enabled thinking, structured output formatting, and forced/`any` tool choice.

References:
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://github.com/anthropics/skills/blob/main/skills/claude-api/shared/prompt-caching.md

Current Go-LIP Anthropic request construction does not contain `cache_control` handling. Therefore:

**Decision:** `keepwarm.enabled=true` MUST NOT cause Go-LIP to start caching Anthropic prompts. Add a direct-Anthropic backend setting for foreground cache enrollment, defaulting to current behavior (`disabled/preserve`). Only when that setting creates an observed renewable cache target may the scheduler act.

Suggested provider-specific shape (exact config naming may follow existing backend schema conventions):

```yaml
anthropic:
  prompt_cache:
    enrollment: disabled   # disabled | automatic
    ttl: 5m                # 5m | 1h, meaningful only for automatic
```

For a renewable observed target, the adapter retains only the bounded provider-local state allowed by spec1 and constructs a documented non-streaming zero-output prewarm that preserves the exact observed cacheable prefix/breakpoint. The implementation must not guess that a newly synthesized placeholder shares the target. A live integration test must demonstrate cache-read/write evidence across: initial write -> wait/renew -> subsequent real request.

### OpenAI direct current contract

Current generated OpenAI SDK/schema documentation for GPT-5.6+ says:

- OpenAI chooses one implicit cache breakpoint by default and supports explicit breakpoints;
- `prompt_cache_options.ttl` defaults to `30m` and is currently the only supported TTL value;
- the TTL is a **minimum lifetime** and the backend may keep cache entries longer;
- deprecated `prompt_cache_retention` expresses a separate maximum-retention policy.

Reference: https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_create_params.py

**Decision:** minimum residency can improve confidence that a cache remains usable for at least 30 minutes, but it does not create an expiration deadline. Do not schedule a heartbeat at 29 minutes merely from that field.

### Codex subscription current contract

Current upstream Codex implementation separates:

- session/thread identity;
- prompt-cache affinity key;
- per-turn routing state (`x-codex-turn-state`) whose source comments forbid reuse across turns;
- WebSocket continuation/`previous_response_id`;
- a `response.create(generate=false)` connection setup/prewarm call.

The connection setup call is **not documented as a prompt-cache TTL refresh operation**. Using it autonomously could also interact with continuation/quota state.

Reference: https://github.com/openai/codex

**Decision:** Codex is high-value for observation, cache-hit metrics, and future testing, but popularity is not evidence for autonomous refresh. Active control remains disabled until a dedicated integration protocol test proves:

1. cache-hit lifetime changes versus a control group;
2. subscription quota/rate impact is acceptable and measured;
3. no client-visible continuation state is consumed/advanced;
4. turn-scoped routing state is not replayed outside its legal turn.

### Gemini current contract

Google's explicit cache API exposes `CachedContent` resources and permits updating only their expiration (`ttl` or `expire_time`). This is a clean non-inference renewal primitive.

Reference: https://ai.google.dev/api/caching

Current Go-LIP Gemini adapter only constructs normal `generateContent` calls and does not manage `CachedContent` resources.

**Decision:** do not let the generic scheduler create explicit cache resources. If a future/provider adapter owns a `CachedContent` resource and emits a renewable fixed-expiry observation, the same scheduler will work without core changes.

## Existing OSS Failure/Success Patterns

### Aider

Aider validates an explicit bounded keepalive count, but historical bugs show how dangerous it is when cache warming re-enters the normal model/agent loop. Go-LIP's separate cache-control seam eliminates that path by construction.

### cortexkit/anthropic-auth

Useful scheduler/provider patterns:

- one shared manager rather than naked per-session ticker behavior;
- retain exact provider-ready cache prefix locally;
- pre-expiry refresh;
- timeout and bounded retained memory;
- strip incompatible fields for Anthropic zero-output prewarm;
- cache usage determines whether prewarm actually hit/wrote cache;
- errors do not fail foreground interaction.

### Permafrost

A proxy-level DeepSeek implementation found that a tiny invented placeholder was not sufficient to refresh the desired persisted prefix and retained exact provider identity/request context instead. This reinforces the spec1 adapter-local opaque target design and argues against any core-generated dummy prompt.

### Hermes Agent

Hermes demonstrates that a cache scope can intentionally survive physical compaction/session rotation while branches/subagents must remain isolated. Go-LIP should not reproduce Hermes lineage heuristics in the scheduler; the adapter/harness-derived effective target observation already embodies the correct identity.

## Selected Orchestration Decisions

### D1 — Idle epoch starts from a committed OS-command turn, not elapsed tool runtime

A completed `ToolCategoryOSCommand` in the committed successful B-leg plus at least one eligible target arms the epoch. No provider call occurs until a target approaches its schedule deadline. The next real A-leg turn invalidates the epoch before B-leg planning.

### D2 — Foreground resume always wins

`BeginForegroundTurn(aLegID)` performs an in-memory synchronous revision change/removal and cancels in-flight maintenance contexts. It does not wait for release RPC/provider work. Worker results carry epoch/target revisions and are ignored if stale.

### D3 — Scheduler is generation-owned

One heap/timer loop and a bounded worker pool per runtime generation. This is the strongest lifetime alignment with controller handles. Reload intentionally sacrifices maintenance continuity rather than retaining/migrating provider state.

### D4 — Deterministic scheduling uses proportional lead plus early spread

For an observation with deterministic expiration:

```text
window = ExpiresAt - ObservedAt
lead   = clamp(window / 10, 15s, 5m)
spread = min(lead / 4, 30s)
due    = ExpiresAt - lead - deterministicEarlySpread(0..spread)
```

The spread is derived from a stable hash of local epoch/target sequence data and fires **earlier**, never later than the base safety deadline. This prevents a burst of identical 5-minute targets from all becoming due at exactly the same instant while remaining fake-clock deterministic.

If `ExpiresAt <= now`, the target is already missed and is retired rather than intentionally cold-recreated. If `due <= now < ExpiresAt`, it is immediately eligible for bounded dispatch.

For `minimum_residency`, best-effort, or unknown lifetime, no automatic due time exists. Explicit operator heuristics are host policy and use their configured interval.

### D5 — Default autonomous budget is intentionally conservative

Initial configuration defaults:

```yaml
prompt_cache:
  keepwarm:
    enabled: true
    max_refreshes_per_idle_epoch: 6
    max_idle_duration: 1h
    max_active_targets: 1024
    max_concurrent_renewals: 4
    renew_timeout: 15s
    continue_after_cold_recreate: false
    max_cold_recreates_per_idle_epoch: 0
    heuristic_overrides: []
```

Rationale:

- `6` bounds repeated 5-minute-cache touches to roughly one half-hour maintenance window before the final refreshed cache naturally survives a little longer, while 1-hour caches consume far fewer calls; operators with longer builds can raise it.
- `1h` is a second independent abandonment bound.
- `1024` targets keeps host state finite while each target is small; this is configurable and capacity pressure only loses an optimization.
- `4` concurrent renewals prevents background traffic from competing aggressively with foreground inference.
- `15s` is below the default 5-minute safety lead and prevents a stuck background call from monopolizing a worker.
- cold recreation stops by default because a full rewrite is precisely the failure mode keep-warm is intended to avoid; the already-incurred call is accounted but not repeated.

No default monetary/token budget is required because refresh count + duration are enforceable even when usage pricing is unavailable. Optional token/cost budget configuration is fail-closed when it cannot be evaluated.

### D6 — Capacity preserves the most urgent targets

When registering targets at capacity, the manager retains targets with the earliest next due time. A target with a later due time than every retained target is rejected; otherwise the currently latest-due target is evicted/released. The rare O(N) selection at a hard capacity boundary is acceptable and simpler than a second heap. Ties use stable insertion sequence.

This avoids discarding a cache that is about to expire in favor of one that has much more residency left.

### D7 — No generic retry loop

One dispatch consumes one refresh slot. `Stale`, `Unsupported`, control error, missed expiry, or default `ColdRecreated` stops the target. A successful `Renewed`/`StillResident` result continues only with a replacement observation that supplies enough timing, or with an explicit heuristic.

The provider adapter may implement protocol-required safe retries inside its control operation only if they cannot duplicate billable effects and comply with the spec1 contract; the generic scheduler itself never retries the operation.

### D8 — Per-session disable is a bounded process-owned policy store

The admin state contains only A-leg disable/inherit state, no provider target. Proposed default maximum: `4096` disabled-session entries. On capacity, reject a new disable command rather than silently evict an existing administrative disable. Session end/expiry removes the entry; process restart returns to global policy. Global disable remains the master gate.

### D9 — Cache enrollment is an orthogonal provider setting

The global orchestrator is enabled by default because its no-eligible-target path is cheap and behavior-preserving. Backend caching policy defaults remain unchanged. This avoids a surprising "turn on keepwarm -> higher cache-write cost/change data-retention policy" coupling.

### D10 — Provider rollout requires an evidence state

Treat support as a matrix rather than boolean marketing labels:

```text
unsupported
observation_only
renewal_experimental   (explicit operator enable / integration evidence)
renewal_supported      (safe default scheduling)
```

Core does not branch on these strings; they are documentation/test rollout states derived from the backend profile/observation capability. Promotion requires integration evidence, not popularity.

## Administrative API Direction

Follow the existing route-override service pattern with a dedicated keep-warm policy service, conceptually:

```go
type SessionPolicyService interface {
    Disable(ctx context.Context, aLegID string) (State, error)
    Clear(ctx context.Context, aLegID string) (State, error)
    Get(ctx context.Context, aLegID string) (State, error)
}
```

The admin HTTP/control surface resolves/validates the A-leg using existing admin/session authority and invokes the service. `Disable` also signals every live generation manager to invalidate that A-leg; the process-owned store itself does not know providers/controllers.

## Metrics Direction

Suggested metric families (exact repository prefix/naming follows existing metrics conventions):

```text
prompt_cache_keepwarm_active_epochs
prompt_cache_keepwarm_active_targets
prompt_cache_keepwarm_armed_total
prompt_cache_keepwarm_skipped_total{reason}
prompt_cache_keepwarm_dispatch_total
prompt_cache_keepwarm_result_total{result}
prompt_cache_keepwarm_cancel_total{reason}
prompt_cache_keepwarm_deadline_missed_total
prompt_cache_keepwarm_capacity_drop_total
prompt_cache_keepwarm_provider_tokens_total{kind}
prompt_cache_keepwarm_duration_seconds
```

Allowed labels are finite enums such as reason/result/token kind and coarse backend family only if existing metrics conventions guarantee bounded cardinality. Raw model/cache/session/target/handle identifiers are prohibited.

## Risks and Mitigations

| Risk | Consequence | Mitigation |
|---|---|---|
| foreground turn races renewal | extra charge/stale state | synchronous epoch invalidation + cancel + result revision check |
| manager retains old generation | resource leak/wrong account | generation-owned scheduler, quiesce before backend close, no migration |
| many targets expire together | background burst/deadline misses | proportional lead + deterministic early spread + bounded workers |
| forgotten session runs forever | unbounded cost | refresh count + max idle duration + optional cost budget |
| cache already cold | repeated full rewrites | count at dispatch, `ColdRecreated` stops by default |
| provider control fails | retry storm | no generic retry; target retired for epoch |
| keepwarm switch changes cache billing semantics | surprising cost/data behavior | separate provider enrollment setting, pre-feature default preserved |
| Codex/Gemini assumptions are wrong | continuation/quota/resource corruption | observation-only until provider-specific evidence gates pass |
| admin disable gets evicted | unexpected background traffic | bounded store rejects new disable at capacity; never silently evicts existing disable |
| scheduler saturation delays renewal past expiry | cold request/waste | miss detection drops target; no intentional late renew/recreate |

## Design Validation Questions Carried Forward

- Confirm the runtime integration point can invalidate an A-leg before B-leg planning without waiting on cache-control release.
- Confirm generation quiesce ordering can stop/join the scheduler before backend instance close and reject late observations.
- Confirm the committed-B-leg runtime has access to both correlated finished tool events and drained cache observations without introducing a second stream traversal.
- Confirm the optional token/cost budget can reuse existing accounting projection without blocking renewal on provider usage that is only known after the call; initial hard guarantees remain refresh/time/concurrency bounds.
- Confirm the direct Anthropic SDK version used by Go-LIP exposes the required cache-control and `max_tokens: 0` request shapes; otherwise upgrade the SDK in the implementation PR with compatibility tests.
- Do not promote Anthropic renewal to `renewal_supported` until the live cache-effect test is green; the scheduler implementation itself can ship safely with reference backends and zero active real providers.
