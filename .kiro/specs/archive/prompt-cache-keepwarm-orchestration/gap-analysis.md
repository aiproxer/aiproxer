# Brownfield Requirements Gap Analysis

## Scope and Dependency

This analysis compares `prompt-cache-keepwarm-orchestration` against current Go-LIP plus the chronologically prior `prompt-cache-residency-contract` foundation in PR #350. The orchestration spec is intentionally stacked on that foundation and must not duplicate its provider-neutral cache profile/observation/control ABI.

## 1. Current-State Investigation

### Existing assets to reuse

| Existing asset | Current role | Relevance |
|---|---|---|
| `pkg/lipapi.ToolEvent` + `ToolCategoryOSCommand` | canonical tool-call lifecycle/category | authoritative default trigger; avoids shell-text/name heuristics |
| A-leg/session authority | stable proxy correlation boundary | owner key for one idle epoch and admin disable state |
| B2BUA attempt commitment | distinguishes committed foreground B-leg from losing/fallback/race attempts | only committed successful B-leg may arm maintenance |
| `prompt-cache-residency-contract` | backend profile/observation/renew/release/accounting contract | sole provider-cache semantic input to orchestrator |
| `internal/core/routeoverride` command/store pattern | A-leg-keyed admin mutable session policy | precedent for validated admin per-session disable/clear service |
| immutable runtime generations + generation resource ledger | backend instance ownership/reload retirement | scheduler/control handles must share this lifetime and quiesce before backend close |
| `AccountingPlaneProviderBillable` and core accounting/cost helpers | provider-side usage/cost projection | maintenance evidence can be attributed without fake client usage |
| existing admin/control plane | authenticated operator mutations | home for per-session keep-warm disable/clear |
| config validation/defaulting | immutable config generation | home for global budgets/concurrency/heuristic overrides |
| Prometheus/diagnostic infrastructure | bounded operational visibility | maintenance metrics and skip reasons |
| fake/provider test doubles, race/goleak conventions | deterministic verification | scheduler can be proven without real providers |

### Current provider adapter reality

1. **Anthropic direct**: current Go-LIP Anthropic request construction contains no repository `cache_control` handling. Anthropic caching is provider opt-in. Therefore enabling the scheduler must not silently alter foreground cache policy; an Anthropic cache-enrollment option is a separate backend setting.
2. **Gemini**: the current official-genai adapter builds `generateContent` requests and does not currently manage `CachedContent` resources. Explicit cache TTL extension is therefore not an immediate orchestration target.
3. **Codex subscription**: Go-LIP already has prompt-cache-key/session affinity and cache-usage observability opportunities, but current upstream Codex has turn-scoped routing state and no documented prompt-cache renewal primitive. Observation-only is the safe initial posture.
4. **OpenAI direct**: current GPT-5.6+ cache `ttl=30m` is documented as a minimum lifetime; it is not a deterministic eviction deadline.
5. **DeepSeek/xAI/Mistral/OpenRouter/etc.**: useful cache affinity exists, but deterministic lifetime/renewal is incomplete or route-dependent. Generic core must not invent timers.

## 2. Requirement-to-Asset Map

| Requirements | Technical need | Existing asset | Gap |
|---|---|---|---|
| 1.1-1.7 | consume provider-neutral contract only | spec1 contract; execbackend | **Dependency gap:** spec1 must land; no scheduler consumer yet |
| 2.1-2.7 | committed OS-command arming | ToolEvent/category; B2BUA commitment | **Missing:** post-terminal arm decision and observation/tool-result aggregation |
| 3.1-3.7 | revisioned A-leg idle epochs | A-leg identity/session lifecycle | **Missing:** epoch registry + foreground-start invalidation hook |
| 4.1-4.9 | lifecycle-aware timer policy | spec1 observation timing | **Missing:** host scheduling policy/heuristic override rules |
| 5.1-5.9 | scalable scheduler/workers/lifecycle | runtime generation ownership | **Missing:** generation-owned heap/timer/worker manager and quiesce ordering |
| 6.1-6.10 | autonomous-cost bounds | config + accounting helpers | **Missing:** refresh/duration/cold/cost budgets and stop policies |
| 7.1-7.9 | global/per-session admin controls | config + routeoverride/admin patterns | **Missing:** keep-warm config and bounded A-leg disable state/service |
| 8.1-8.6 | separate enrollment semantics | backend config/adapters | **Missing:** explicit design rule; Anthropic optional enrollment integration |
| 9.1-9.9 | evidence-gated provider rollout | provider adapters/integration tests | **Missing:** support matrix and first active provider implementation |
| 10.1-10.6 | metrics/diagnostics | existing observability | **Missing:** bounded maintenance metrics/outcomes |
| 11.1-11.10 | deterministic scheduler/race TDD | testing/fake clock patterns | **Missing:** scheduler TCK, fake clock, race/leak gates |

## 3. Critical Brownfield Gaps and Applied Requirement Corrections

### Gap A — A process-owned scheduler would retain generation-owned backend control

A first conceptual sketch used one process-wide scheduler. The residency foundation deliberately scopes handles and control functions to configured backend instances/generations. A process-owned scheduler that stores controller closures or backend references could accidentally keep a retired generation reachable and violate reload ownership.

**Correction applied:** Requirements 5.1 and 5.8-5.9 make the scheduler **generation-owned**. Each generation has one timer/manager and a bounded renewal worker pool, not one goroutine per session. Quiesce invalidates targets before backend close. Process-wide per-session administrative disable state may remain separate because it carries no provider handle/backend reference.

### Gap B — the proxy cannot observe the local command lifetime

Go-LIP sees the assistant emit an OS-command tool call, then the coding harness executes it outside the proxy. There is no reliable proxy event for "process has run N minutes" until the harness sends the next tool-result turn.

**Correction applied:** Requirements 2 and 3 define the observable idle window as `committed OS-command tool call -> absence of next real A-leg turn`. The first renewal is naturally delayed until near cache expiry, so short commands cost nothing; a new real turn cancels before it becomes a maintenance event.

### Gap C — default-on keep-warm could be misread as default-on provider cache enrollment

Anthropic prompt caching is opt-in and carries write-price/retention semantics. Current Go-LIP does not emit Anthropic `cache_control`. Gemini explicit cache resources also do not exist in the current adapter. If the scheduler master switch changed these foreground semantics implicitly, a background optimization would silently change billing/data behavior.

**Correction applied:** Requirement 8 separates **orchestration** from **cache enrollment**. `keepwarm.enabled` defaults true, but it only acts on actual renewable observations. Provider enrollment remains backend-specific and preserves pre-feature defaults; Anthropic auto-enrollment is explicit opt-in.

### Gap D — one TTL margin does not fit 5-minute and 1-hour caches

A fixed `refresh 30s before expiry` is adequate for a 5-minute cache but needlessly late for a 1-hour cache during congestion; `refresh 5m before expiry` is too aggressive for a 5-minute cache.

**Correction applied:** Requirement 4 calls for a proportional safety lead clamped by host minimum/maximum bounds and based on the backend-supplied observed residency window. No provider-specific timing branch is needed.

### Gap E — successful HTTP/control completion is not equivalent to economic success

A prewarm may discover the cache already expired and rewrite it, or may return no cache evidence. Repeatedly treating that as "renewed" can cause a costly background loop.

**Correction applied:** Requirement 6 treats `ColdRecreated` separately, consumes every dispatched call from the refresh budget, stops by default after cold recreation/control failure, and requires authoritative replacement timing to continue.

### Gap F — broad provider support cannot be inferred from popularity

Codex is popular but its current subscription backend has undocumented prompt-cache retention and turn-scoped routing state; Gemini explicit resource extension is documented but Go-LIP does not currently own such resources; OpenAI current TTL is a minimum lifetime.

**Correction applied:** Requirement 9 defines evidence levels and observation-only defaults. Active renewal requires adapter control semantics plus live/protocol evidence. Anthropic direct is the first candidate because the provider documents explicit zero-output prewarm and cache usage, but its foreground enrollment remains opt-in.

## 4. Implementation Approach Options

### Option A: One process-wide manager storing backend callbacks

**Shape:** singleton heap/timer across all generations, targets retain backend/controller closures.

**Advantages**
- One scheduler for the whole process.
- Targets could theoretically survive config reload.

**Disadvantages**
- Retains generation-owned backends/connector sessions or needs a complex weak indirection registry.
- Handle transfer across generations is forbidden by the foundation contract.
- Reload becomes a target-migration problem with account/backend-equivalence assumptions.

**Assessment:** reject.

### Option B: One timer/goroutine per session/target

**Shape:** arm a `time.Timer`/goroutine when an OS-command turn finishes.

**Advantages**
- Simple local code.
- Natural cancellation context per session.

**Disadvantages**
- Unbounded goroutine/timer scaling.
- Harder race/revision handling and target capacity enforcement.
- Provider calls can re-enter/overlap unpredictably.
- Duplicated lifecycle logic per target.

**Assessment:** reject.

### Option C: One generation-owned priority scheduler + bounded workers

**Shape:** one manager/heap/timer per active runtime generation, shared across all A-legs; bounded worker concurrency; target/epoch revisions; process-owned bounded admin disable state.

**Advantages**
- Aligns exactly with backend handle/controller lifetime.
- No per-target long-lived goroutine/ticker.
- Simple reload: quiesce -> cancel/release -> backend close; no migration.
- Deterministic target/refresh/concurrency budgets.
- Fake-clock tests can cover all timing in one state machine.

**Disadvantages**
- An idle session loses maintenance across reload until its next foreground turn.
- Requires explicit integration at foreground-turn start and committed B-leg terminal.

**Assessment:** preferred. Losing cache optimization across reload is safer than retaining or reconstructing provider state.

## 5. Recommended Design Direction

Use **Option C**:

1. Create a provider-neutral `internal/core/promptcachekeepwarm` manager/state machine that imports only `lipapi` tool categories and the spec1 SDK contract.
2. Construct one manager per immutable runtime generation. Register it with generation quiesce/close before backend close; it owns one timer loop plus a finite worker pool.
3. Call `BeginForegroundTurn(aLegID)` as soon as A-leg authority is resolved, before B-leg planning. It atomically invalidates the idle epoch and cancels in-flight control without waiting on cleanup.
4. At committed successful B-leg terminal, combine completed canonical tool lifecycle information with drained residency observations. Arm only if an OS-command trigger and at least one eligible renewable target coexist.
5. Schedule deterministic targets from backend `ExpiresAt` using a proportional/clamped lead; unknown/best-effort/minimum-only targets require an explicit operator heuristic and safe backend renewal.
6. Count provider calls at dispatch. Stop on refresh count, idle duration, optional cost budget, stale/unsupported, control failure, or default cold recreation.
7. Keep global config generation-owned but per-session disable state process-owned/bounded, keyed by validated A-leg authority. Disable immediately cancels; clear is non-retroactive.
8. Add provider-specific enrollment/renewal only in concrete backends. First active target is Anthropic direct behind explicit cache enrollment and live cache-effect validation. Codex/OpenAI/Gemini/etc. remain observation-only/no-timer until their gates are satisfied.

## 6. Effort and Risk

- **Effort: L/XL (2–4 weeks)** — scheduler state machine, runtime hooks, admin/config/metrics, provider-billable accounting, and the first real provider integration all require careful TDD/race coverage.
- **Risk: High initially, Medium after gates** — main risks are foreground-vs-background races, lifecycle leaks across reload, and hidden provider cost/semantic changes. Revisioned epochs, generation ownership, bounded workers/budgets, no generic retries, and opt-in provider enrollment reduce those risks.

## 7. Requirement Changes Resulting From Gap Analysis

The generated requirements already incorporate the corrections above. No requirement is removed. The design phase must freeze:

- exact default budget/concurrency values;
- exact proportional safety-lead function;
- deterministic active-target eviction policy;
- scheduler-to-runtime hook points and lifecycle ordering;
- process-owned per-session disable store bound/cleanup;
- maintenance cost-budget behavior when authoritative cost is absent;
- Anthropic enrollment/prewarm request transformation and live validation gate.
