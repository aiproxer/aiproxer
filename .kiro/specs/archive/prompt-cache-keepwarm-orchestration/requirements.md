# Requirements Document

## Introduction

Go-LIP shall add a proxy-controlled prompt-cache keep-warm orchestrator for issue #342. The orchestrator consumes only the provider-neutral residency observations and control seam defined by the chronologically prior `prompt-cache-residency-contract` spec. It shall detect when a coding-agent session is likely to remain idle while an external OS command runs, maintain only verified renewable provider cache targets before their usable residency expires, and stop immediately when a real client turn resumes or a hard budget/lifecycle boundary is reached.

The feature is **globally enabled by default**, but default-on means "orchestrate verified eligible cache targets" rather than "silently enable provider caching." Foreground provider cache enrollment/retention settings remain backend-owned semantics. If a backend produces no renewable residency observation, the orchestrator performs no provider call.

## Boundary Context

- **Dependency**: `prompt-cache-residency-contract` must land first. This spec consumes its lifecycle/profile/observation/renew/release/accounting contracts and shall not recreate provider cache semantics in core.
- **In scope**: committed-B-leg observation intake; OS-command arming; A-leg idle epochs; lifecycle-aware scheduling; generation-owned scheduler; bounded worker concurrency; foreground cancellation; refresh/duration/target budgets; cold-recreation handling; admin per-session disable/clear controls; global configuration; reload/shutdown behavior; maintenance accounting/metrics; provider rollout gates; Anthropic-direct opt-in enrollment/renewal as the first evidence-backed active provider integration if its live contract test passes.
- **Out of scope**: changing provider cache policy merely because keep-warm is enabled; a client-facing cache-maintenance API; arbitrary provider TTL guesses; browser/tool-duration inspection; polling local processes; persistent keep-warm targets; keeping retired backend generations alive; autonomous Codex renewal without evidence; automatic Gemini `CachedContent` resource creation; generic cache deletion.
- **Ownership**: core owns arming/scheduling/budgets/admin policy; backend adapters own provider cache enrollment, effective target identity, affinity, and renewal implementation.

## Requirement 1: Consume the Residency Contract Without Reintroducing Provider Semantics

**Objective:** As a maintainer, I want keep-warm policy to depend only on the stable residency contract, so that adding providers does not create another central cache-behavior matrix.

### Acceptance Criteria

1.1. The orchestrator shall consume only successful committed-B-leg residency observations and backend control operations defined by `prompt-cache-residency-contract`.

1.2. The orchestrator shall not parse provider prompt-cache keys, cache object IDs, provider headers, provider request bodies, auth state, or provider SDK types.

1.3. The orchestrator shall not contain built-in provider-name/model-name branches that decide TTL, cache-key lineage, request replay shape, or renewal semantics.

1.4. The orchestrator shall not construct a synthetic `lipapi.Call`, invoke normal backend `Open`/`Execute`, parse a model selector, or re-run routing/failover/racing/account selection for maintenance.

1.5. The orchestrator shall treat absence of a renewable observation or control capability as a normal no-op state and shall never fail foreground inference because cache maintenance is unavailable.

1.6. The orchestrator shall keep physical A-leg/session identity separate from backend target identity and cache-content generation; equality of those values shall never be inferred by core.

1.7. The implementation shall not modify `pkg/lipapi` to expose a client cache-maintenance operation/event or provider cache lifecycle field.

## Requirement 2: Arm Maintenance Only for an Observable Long-Idle Opportunity

**Objective:** As a proxy operator, I want maintenance to start only when a coding-agent turn is likely to pause for external execution, so that ordinary conversational idle time does not generate background provider traffic.

### Acceptance Criteria

2.1. When the **committed successful B-leg** contains at least one completed tool call whose canonical category is `ToolCategoryOSCommand`, and that B-leg yields at least one eligible renewable cache target, the orchestrator shall arm an idle epoch for the owning A-leg.

2.2. The orchestrator shall use the existing canonical tool lifecycle/category result and shall not parse shell command text, tool arguments, tool descriptions, or provider-specific tool names to estimate duration.

2.3. The orchestrator shall not claim to observe the actual client-side subprocess lifetime; the observable condition is that an OS-command tool call was handed to the client and no subsequent real A-leg turn has arrived yet.

2.4. A file read/search/edit/remove tool, web-access tool, unknown tool, plain assistant response, or OS-command tool call from a losing/uncommitted B-leg shall not arm the default keep-warm policy.

2.5. If several tool calls complete in one committed B-leg, one eligible OS-command category shall be sufficient to arm the idle epoch, and the epoch shall be created at most once for that completed foreground turn.

2.6. If the committed B-leg contains an OS-command tool call but produces no eligible renewable observation, the orchestrator shall record a bounded skip reason and shall schedule no provider operation.

2.7. The default trigger set shall remain `os_command`; broadening automatic triggers to other tool categories shall require explicit configuration or a later spec rather than heuristics in core.

## Requirement 3: Model Each Pause as a Revisioned A-Leg Idle Epoch

**Objective:** As a concurrency maintainer, I want every maintenance window to have an explicit lifetime, so that stale timers cannot affect a resumed or changed conversation.

### Acceptance Criteria

3.1. Each armed A-leg shall have a monotonically changing idle-epoch revision that uniquely identifies the current maintenance window within the process/generation lifetime.

3.2. At the start of the next **real** A-leg turn, after authoritative A-leg/session correlation is established and before B-leg planning/execution, the orchestrator shall synchronously invalidate the prior idle epoch and make all of its queued work stale.

3.3. Foreground turn admission shall not wait for provider cache-control cleanup; invalidation shall cancel in-flight maintenance contexts and detach queued targets immediately, while local handle release proceeds best-effort outside the foreground critical path.

3.4. A synthetic cache-control operation shall never create, extend, or reset an idle epoch and shall never be mistaken for a real A-leg turn.

3.5. A later successful foreground B-leg may create a new idle epoch using newly observed targets; refresh counters and cold-recreation counters shall reset only for that new real-turn-derived epoch.

3.6. When an A-leg/session ends or is administratively disabled, the orchestrator shall invalidate its current epoch, cancel in-flight maintenance, and release retained handles.

3.7. If a stale timer/job/result races with a newer epoch revision, the stale work/result shall be discarded and shall not mutate the newer target state.

## Requirement 4: Schedule From Declared Residency Semantics, Not Guessed TTLs

**Objective:** As an operator, I want renewal timing to be derived from backend-declared target facts, so that best-effort, fixed, sliding, and minimum-residency caches are not conflated.

### Acceptance Criteria

4.1. For a renewable target with a valid deterministic `ExpiresAt`, the scheduler shall plan renewal before that expiration using a bounded safety lead derived from the observation timing and host scheduling policy.

4.2. The default safety-lead algorithm shall scale with the observed residency window and shall be clamped to finite minimum/maximum bounds so both short and long TTLs receive useful margin without provider-specific constants.

4.3. `LifecycleSlidingExpiry` and renewable `LifecycleFixedExpiry` targets may be scheduled when the observation provides a usable expiration and the backend control capability supports renewal.

4.4. `LifecycleMinimumResidency` shall not be treated as an eviction deadline; the orchestrator shall not schedule from `MinimumResidentUntil` as though it were `ExpiresAt`.

4.5. `LifecycleBestEffort` and `LifecycleUnknown` targets, and minimum-residency targets without deterministic expiration, shall receive no automatic timer by default.

4.6. An operator may configure an explicit heuristic interval for an otherwise non-deterministic target class only if the issuing backend advertises safe renewal; such an interval shall be labeled/configured as an operator heuristic and shall never overwrite the backend's lifecycle facts.

4.7. A heuristic override shall be scoped by concrete backend instance/route identity with an optional model matcher through configuration data; core shall not ship provider-specific heuristic defaults.

4.8. After a successful renewal, future scheduling shall use the replacement authoritative observation returned by the backend rather than adding a fixed interval to the previous host deadline.

4.9. If a renewal result does not provide sufficient authoritative timing for another deterministic renewal and no explicit heuristic applies, the target shall stop scheduling.

## Requirement 5: Use One Generation-Owned Scheduler With Bounded Renewal Concurrency

**Objective:** As a runtime maintainer, I want keep-warm to scale to many sessions without one goroutine/timer per session and without retaining old backend generations.

### Acceptance Criteria

5.1. Each active runtime generation shall own at most one keep-warm scheduler/timer loop for all targets in that generation; the implementation shall not allocate one long-lived goroutine or `time.Ticker` per A-leg/target.

5.2. The scheduler shall use a bounded priority structure keyed by next due time and shall support efficient target registration, invalidation, replacement, and wake-up.

5.3. Provider renewal calls shall execute through a bounded worker/semaphore pool with configurable maximum concurrency rather than blocking the scheduler timer loop or spawning unbounded goroutines.

5.4. The scheduler shall enforce a configurable finite maximum active-target count; reaching the bound shall never reject a foreground request and shall evict/drop maintenance targets using a deterministic bounded policy with handle release and metrics.

5.5. Each dispatched job shall carry the A-leg idle-epoch revision, target identity/generation, target-state revision, and cancellable operation context; results shall apply only if all still match current state.

5.6. At most one renewal operation for the same target/generation shall be in flight at a time.

5.7. Renewal calls shall have a finite configurable timeout, and queue/worker saturation shall not cause retries past an already known cache expiration.

5.8. A generation quiesce/retire/close shall stop its scheduler, invalidate all targets, cancel/join workers, and release handles before the owning backend instances are closed; the scheduler shall never pin a retired generation.

5.9. An observation arriving from a foreground request after the generation scheduler has begun quiescing shall be rejected as a maintenance target and its handle shall be released while the foreground response remains valid.

## Requirement 6: Bound Maintenance by Refresh, Time, and Economic Safety Rules

**Objective:** As an operator, I want autonomous background cost to be strictly bounded, so that a forgotten coding session cannot generate unlimited cache traffic.

### Acceptance Criteria

6.1. The global configuration shall include a finite configurable `max_refreshes_per_idle_epoch`; each dispatched provider renewal attempt shall consume one refresh slot regardless of outcome.

6.2. The global configuration shall include a finite configurable maximum maintenance duration per idle epoch measured from the foreground turn that armed it.

6.3. When either the refresh-count or idle-duration limit is reached, the orchestrator shall stop scheduling that epoch and release its remaining targets without affecting the A-leg session itself.

6.4. The orchestrator shall separately count `ColdRecreated` outcomes because a cold cache rewrite is economically different from a successful warm renewal.

6.5. By default, a `ColdRecreated` outcome shall record its provider-billable evidence and stop further maintenance for that target in the current idle epoch rather than entering a repeated recreate loop.

6.6. If a future/operator setting allows continuation after a cold recreation, it shall have its own finite `max_cold_recreates_per_idle_epoch` bound and shall still consume the normal refresh-count budget.

6.7. `Stale` or `Unsupported` renewal results shall immediately retire/release the target and shall not be retried through another backend, account, model, or route.

6.8. A transport/provider renewal failure shall not enter a tight or exponential generic retry loop; the default policy shall retire that target for the idle epoch after recording the failure.

6.9. Where provider-billable usage/cost evidence is available, maintenance shall be accounted under a distinct maintenance operation identity and shall not be merged into the triggering foreground B-leg's usage.

6.10. The orchestrator shall support an optional finite per-idle-epoch maintenance cost/token budget when the existing accounting/cost projection can evaluate it; inability to estimate cost shall not fabricate a zero-cost allowance.

## Requirement 7: Provide Safe Global and Per-Session Administrative Control

**Objective:** As a proxy administrator, I want to disable keep-warm globally or for one live A-leg without changing the client protocol, so that I can control cost/behavior during a session.

### Acceptance Criteria

7.1. The keep-warm global master setting shall default to **enabled** for new configurations.

7.2. Global disable shall prevent new idle epochs and shall invalidate/cancel/release all current keep-warm targets during configuration-generation replacement; foreground inference shall continue normally.

7.3. The system shall provide an authenticated admin/control-plane operation to disable keep-warm for a concrete A-leg/session and a corresponding operation to clear that per-session disable state.

7.4. Per-session disable shall be keyed by validated proxy/A-leg authority, not by an untrusted request-body cache key or provider session identifier.

7.5. Applying per-session disable shall synchronously make queued/in-flight work stale and cancel the in-flight maintenance context without waiting for provider completion.

7.6. Clearing a per-session disable shall restore inheritance from the global setting but shall not retroactively create an idle epoch; the next eligible real foreground turn may arm one.

7.7. A per-session setting shall not override a globally disabled master setting to force background traffic.

7.8. Per-session disable state shall be process-owned, bounded, and cleaned with session expiry/end; it need not be persisted across process restart because keep-warm targets themselves are volatile.

7.9. The client-facing OpenAI/Anthropic/Gemini/Codex request schemas shall gain no field that lets an untrusted client enable or bypass administrative keep-warm policy.

## Requirement 8: Keep Cache Enrollment Separate From Keep-Warm Orchestration

**Objective:** As an operator, I want enabling maintenance to preserve existing foreground provider semantics, so that a background optimization cannot silently change cache-write pricing, retention posture, or provider request behavior.

### Acceptance Criteria

8.1. Global `keepwarm.enabled=true` shall not by itself add provider `cache_control`, create explicit cache resources, change provider retention/TTL modes, or modify a client/backend cache enrollment decision on foreground requests.

8.2. A backend may expose a separate provider-specific/operator-controlled cache enrollment configuration when active keep-warm requires provider caching to be enabled, but that setting shall be independent from the global orchestrator master switch.

8.3. Provider enrollment defaults shall preserve the backend's pre-feature foreground behavior unless a provider's existing API already performs automatic prompt caching without proxy request changes.

8.4. When provider enrollment changes write price, retention/data-control behavior, or cache breakpoint placement, the backend documentation/config validation shall make that change explicit and shall not hide it behind the keep-warm master default.

8.5. The orchestrator shall arm only after the backend emits an actual eligible residency observation; configured enrollment intent alone shall not be treated as evidence that a cache was written.

8.6. If provider usage evidence shows no cache creation/read and the backend cannot otherwise prove a resident renewable target, the orchestrator shall schedule no renewal.

## Requirement 9: Roll Out Providers by Evidence Level, Not Popularity

**Objective:** As a maintainer, I want initial provider support to reflect proven protocol semantics, so that popular but opaque backends are not given speculative autonomous traffic.

### Acceptance Criteria

9.1. A provider/backend shall be marked **active-renewal supported by default** only when its adapter has a safe control implementation, affinity preservation, cache-effect evidence, and integration tests demonstrating that renewal does not alter foreground continuation/tool/session semantics.

9.2. Anthropic direct may be the first active-renewal integration because the provider documents cache breakpoints, 5-minute/1-hour lifetimes, cache usage evidence, and zero-output `max_tokens: 0` prewarming; however, Go-LIP automatic cache enrollment for Anthropic shall remain a separate explicit backend setting because current foreground behavior does not enable caching.

9.3. The Anthropic renewal implementation shall preserve the exact effective cacheable prefix/breakpoint used by the observed target, sanitize fields incompatible with zero-output prewarm, preserve account/workspace affinity, and interpret cache-read/write usage to distinguish warm renewal from cold recreation/no-cache.

9.4. Codex/ChatGPT-subscription shall initially remain observation/affinity-only for this feature; `response.create(generate=false)` or any other operation shall not be enabled as autonomous renewal until controlled protocol/live tests prove prompt-cache lifetime effect, quota cost, continuation safety, and correct treatment of turn-scoped state.

9.5. OpenAI direct GPT-5.6+ minimum `prompt_cache_options.ttl` residency shall not be treated as a deterministic expiry; active timer renewal shall remain disabled unless a separately proven safe renewal primitive exists.

9.6. Gemini implicit/best-effort caching shall remain non-deterministic/no-timer by default; explicit `CachedContent` TTL extension may be supported only after Go-LIP actually manages/observes those resources through the residency contract, and the orchestrator shall not create them automatically.

9.7. DeepSeek, xAI, Mistral, OpenRouter/aggregator paths, Z.AI, and other providers with unknown/best-effort or route-dependent lifetimes shall default to observation-only/no-timer unless their backend implements safe renewal and either supplies deterministic expiration or the operator explicitly configures a heuristic interval.

9.8. Aggregator active renewal shall require the adapter to preserve the concrete downstream provider/cache-affinity domain; a model name alone shall not satisfy this gate.

9.9. Provider-specific live validation shall be integration-gated and shall not be required by default unit tests; failure of a live validation gate shall leave that provider observation-only rather than weakening the core contract.

## Requirement 10: Make Maintenance Observable Without Leaking Cache Identity

**Objective:** As an operator, I want to understand when keep-warm is useful or skipped, so that I can tune cost and diagnose provider behavior safely.

### Acceptance Criteria

10.1. The system shall expose bounded-cardinality metrics for active targets/epochs, armed/skipped epochs, dispatched renewals, renewal outcomes, cancellations, stale results, deadline misses, target-capacity eviction, provider/control failures, cold recreations, and provider-billable maintenance token/cost evidence where available.

10.2. Skip/outcome metrics shall distinguish at least disabled, no OS-command trigger, no residency observation, observation not renewable, non-deterministic lifetime, budget exhausted, target capacity, generation quiescing, stale handle, and control failure without embedding raw cache identifiers.

10.3. Logs/metrics/traces shall not include provider prompt-cache keys, target IDs, cache-generation IDs, opaque handles, prompt/request bodies, auth headers/tokens, or unbounded client/model strings as labels.

10.4. Maintenance operations shall use a distinct bounded operation/correlation identifier for diagnostics/accounting and shall never impersonate an A-leg/B-leg foreground request ID.

10.5. Foreground latency metrics shall exclude background control duration; maintenance duration shall be measured separately.

10.6. Administrative per-session disable/clear actions shall be auditable through the existing admin/control-plane diagnostics without exposing provider cache state.

## Requirement 11: Prove Timing and Race Safety With Deterministic TDD

**Objective:** As a maintainer, I want the scheduler proven under races and fake time, so that cache optimization cannot destabilize interactive traffic.

### Acceptance Criteria

11.1. Before implementation, RED tests shall use an injectable/fake clock to pin arm time, safety-lead scheduling, refresh-count/duration exhaustion, replacement observation timing, and no-timer lifecycle classes without wall-clock sleeps.

11.2. Before implementation, RED tests shall prove foreground-turn-start invalidation wins against queued, just-dispatched, in-flight, and just-completed renewal work and stale results cannot mutate a newer epoch.

11.3. RED tests shall prove one in-flight operation per target, bounded global worker concurrency, active-target capacity behavior, scheduler quiesce/join, and no per-target long-lived goroutine/ticker.

11.4. Tests shall prove a losing/uncommitted B-leg, failed/cancelled committed attempt, or cache observation without an OS-command trigger cannot arm maintenance.

11.5. Tests shall prove per-session disable is immediate, clear is non-retroactive, global disable is a master gate, and admin state is bounded/cleaned.

11.6. Tests shall prove `ColdRecreated`, stale/unsupported, control error, missing replacement timing, and budget exhaustion stop/reschedule exactly according to policy with no generic retry loop.

11.7. Race/leak tests shall cover concurrent foreground resume, renewal cancellation, generation quiesce/reload, session disable, target replacement, and worker completion.

11.8. Architecture tests shall fail if scheduling/provider timing branches enter adapters incorrectly, provider-specific TTL logic enters core, maintenance invokes normal routing/execution, or the scheduler retains retired generation/backend references after quiesce.

11.9. Default unit/contract tests shall require no provider credentials or network; Anthropic and future provider-effect verification shall live behind explicit integration gates.

11.10. Repository quality, race/leak, and architecture gates shall remain green with keep-warm enabled by default and with no eligible cache targets, proving the default no-op path is cheap and behavior-preserving.
