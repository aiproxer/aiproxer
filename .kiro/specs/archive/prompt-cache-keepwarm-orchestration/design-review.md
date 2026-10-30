# Brownfield Design Validation

## Verdict

**GO with evidence-gated provider activation.** The orchestration design is compatible with Go-LIP's A-leg/B-leg ownership, immutable runtime generations, canonical tool lifecycle, admin-control patterns, and the chronologically prior `prompt-cache-residency-contract`. No architectural blocker remains for implementing the generic scheduler and control-plane integration.

The provider rollout remains deliberately asymmetric: the generic scheduler can be implemented and verified entirely with reference backends, while a concrete provider is promoted to autonomous renewal only after its own protocol/live evidence gate passes. Direct Anthropic is the first candidate; Codex/OpenAI/Gemini/DeepSeek/etc. remain observation-only/no-timer until their independent evidence requirements are satisfied.

This document records the completed implementation and design-validation result. The generic orchestration and its provider-safe rollout gate are archived as completed in `spec.json`. The explicitly credentialed live Anthropic cache-effect test is implemented but was not run locally; autonomous direct-Anthropic renewal therefore remains disabled or experimental until real-provider evidence is collected.

## Critical Brownfield Concerns and Resolutions

### 1. Process-global scheduler would violate backend generation ownership — RESOLVED

**Concern:** An earlier architecture sketch proposed one process-wide timer/heap. The foundation contract scopes maintenance handles/controllers to configured backend instances and immutable runtime generations.

**Impact:** A process-global manager retaining controller callbacks could keep retired backend generations/connector sessions reachable after reload, or would need unsafe target migration/re-resolution through normal routing/account logic.

**Resolution:** The final design owns one scheduler manager per runtime generation. The manager contains one priority heap/timer loop and a bounded worker pool for all A-legs in that generation, so it still avoids one ticker/goroutine per session while matching backend lifetime exactly. Quiesce unregisters the manager, rejects late arms, invalidates/cancels/releases targets, joins workers, and only then permits backend close. Targets do not migrate across generations.

**Traceability:** 5.1-5.9, 11.3, 11.7-11.8.

### 2. Proxy cannot observe actual local command duration — RESOLVED

**Concern:** The original issue language suggested monitoring long-running bash/exec duration, but the coding harness executes the tool outside Go-LIP.

**Impact:** Polling or guessing command duration from shell text would be brittle, client-specific, and outside proxy authority.

**Resolution:** The final idle epoch is defined by observable proxy events: the committed successful B-leg completed an `os_command` tool call and no subsequent real A-leg turn has arrived. Renewal is naturally delayed until near cache expiry, so short commands cause zero provider maintenance calls because the next real turn cancels the epoch first.

**Traceability:** 2.1-2.7, 3.1-3.7.

### 3. Default-on keep-warm could silently change provider caching/billing semantics — RESOLVED

**Concern:** Direct Anthropic prompt caching is opt-in, and the current Go-LIP Anthropic request construction does not enroll prompts into caching. Gemini explicit `CachedContent` resources are likewise not currently managed by the Go-LIP Gemini adapter.

**Impact:** Coupling `keepwarm.enabled=true` to cache enrollment could silently change foreground cache-write prices, retention/data-control behavior, cache breakpoints, or provider request semantics.

**Resolution:** The global orchestration switch defaults on but operates only on actual renewable residency observations. Provider cache enrollment is a separate backend-owned setting that preserves pre-feature behavior by default. Direct Anthropic gets a separate explicit enrollment option; Gemini explicit resource creation is not introduced by the generic orchestrator.

**Traceability:** 7.1-7.9, 8.1-8.6, 9.2, 9.6.

### 4. Fixed refresh lead would not scale across cache lifetimes — RESOLVED

**Concern:** A fixed 30-second or 5-minute lead is inappropriate across 5-minute, 30-minute, and 1-hour residency windows.

**Impact:** Too-small lead increases deadline misses under queue/provider latency; too-large lead wastes cache lifetime and increases renewal frequency.

**Resolution:** Deterministic expiry uses a provider-neutral proportional lead, `window/10`, clamped to 15 seconds..5 minutes, plus a deterministic early-only spread bounded by the lead. The provider supplies the actual `ObservedAt`/`ExpiresAt`; core never uses a vendor TTL constant. Minimum-residency/best-effort/unknown lifetimes remain no-timer unless an operator supplies an explicit heuristic and the backend has safe renewal.

**Traceability:** 4.1-4.9, 11.1.

### 5. Successful control call is not necessarily successful keep-warm economics — RESOLVED

**Concern:** A provider call may complete after the cache expired, causing a full rewrite, or may return no evidence that the intended cache target was renewed.

**Impact:** Treating every 2xx/control completion as renewal can create repeated cold writes and unbounded background cost.

**Resolution:** The scheduler consumes the foundation's classified `Renewed`, `StillResident`, `ColdRecreated`, `Stale`, and `Unsupported` outcomes. Every dispatched call consumes a refresh slot. Cold recreation stops the target by default; stale/unsupported/control error/deadline miss also stop with no generic retry. Rescheduling requires an authoritative replacement observation or an explicit safe heuristic.

**Traceability:** 6.1-6.10, 11.6.

### 6. Popular provider does not imply safe autonomous renewal — RESOLVED

**Concern:** Codex/ChatGPT subscription is extremely important in real agent usage, but current upstream behavior includes prompt-cache affinity, continuation state, and turn-scoped routing state without a documented cache-renewal TTL/control contract. OpenAI direct currently documents a minimum cache residency rather than a deterministic eviction deadline. Gemini explicit cache resources are renewable but Go-LIP does not currently own them.

**Impact:** Enabling autonomous provider traffic based on popularity or a superficially similar API could alter quota, continuation, account affinity, or transcript semantics.

**Resolution:** Provider rollout is evidence-gated. Core branches only on the provider-neutral profile/observation/control capability. Codex/OpenAI/Gemini implicit/other unknown-lifetime paths remain observation-only/no-timer initially. Direct Anthropic is the first active candidate because the provider documents cache lifecycle, usage evidence, and zero-output prewarming, but promotion to supported renewal requires a real-provider cache-effect test.

**Traceability:** 9.1-9.9.

## Anthropic SDK Validation

Current Go-LIP pins `github.com/anthropics/anthropic-sdk-go v1.62.0`. The pinned SDK exposes top-level message cache-control request fields and accepts a zero max-token value at the SDK validation/type level, so the design does not require raw-JSON bypasses or an SDK upgrade merely to express the documented direct-Anthropic cache enrollment/prewarm shape.

This does **not** prove that the generated zero-output request will renew the same cache generation as the foreground request. That remains a provider-effect question and is correctly isolated behind the live integration promotion gate:

1. establish a foreground cache write/read;
2. wait a controlled interval;
3. issue the exact-prefix zero-output renewal through the backend control seam;
4. send the subsequent real request;
5. verify cache-read/write usage and absence of maintenance output;
6. separately validate 5-minute and 1-hour modes and same-account/workspace affinity.

If that gate fails, the direct Anthropic implementation remains experimental/disabled without weakening the scheduler abstraction.

## Validation Checklist

### Dependency discipline — PASS

- Orchestration consumes the spec1 residency/control contract rather than redefining provider cache identity or ABI.
- No provider SDK/cache key/request body enters the scheduler.

### A-leg/B-leg semantics — PASS

- A-leg owns the idle epoch/admin policy.
- Only the committed successful B-leg may arm.
- Losing/fallback/race attempts cannot start autonomous maintenance.
- Real-turn start cancels the old epoch before B-leg planning.

### Tool semantics — PASS

- Uses the existing canonical `ToolCategoryOSCommand` result.
- No shell-text, tool-argument, description, or provider-name duration heuristics.

### Generation lifecycle — PASS

- Scheduler/controller lifetime aligns with immutable generation/backend ownership.
- No target migration or old-generation retention across reload.
- Late arms while quiescing are rejected/released.

### Concurrency — PASS with mandatory fake-clock/race tests

- One heap/timer manager per generation; no per-target long-lived ticker/goroutine.
- Bounded renewal worker pool.
- Epoch + target revisions protect queued/in-flight/late result races.
- Foreground invalidation never waits for provider cleanup.
- One in-flight renewal per target.

### Cost bounds — PASS

- Refresh count and wall-clock duration are hard budgets available even with no pricing data.
- Worker concurrency and operation timeout are finite.
- Cold recreation stops by default.
- Optional provider-token budget fails closed when a conservative estimate is unavailable.
- No generic retry loop.

### Administrative control — PASS

- Global default-on master setting remains authoritative.
- Per-A-leg disable/clear uses validated admin/session authority and no client request field.
- Process store contains policy only; at capacity a new disable is rejected instead of evicting an existing disable.
- Live managers are registered/unregistered explicitly so the process registry cannot retain retired generations.

### Provider enrollment separation — PASS

- Global keep-warm enablement does not add cache-control fields, create provider cache resources, or alter retention modes.
- Direct Anthropic enrollment defaults disabled/preserve-current-behavior.
- Enrollment intent alone never arms; an actual renewable observation is required.

### Provider rollout — PASS

- Autonomous support is based on backend capability/evidence, not central provider switches.
- Codex/OpenAI/Gemini/DeepSeek/etc. can remain useful observation-only paths.
- Real-provider live tests are integration-gated and do not burden default unit tests.

### Observability/privacy — PASS

- Metrics use bounded outcome/skip categories.
- Provider cache keys, target IDs, generation IDs, handles, prompts, credentials and arbitrary model/session strings are prohibited labels.
- Stale maintenance results can still report provider-billable evidence without mutating scheduler state.

## Requirement-to-Design Validation

| Requirement | Result |
|---|---|
| 1 Residency-contract-only core | PASS |
| 2 Observable OS-command arm | PASS |
| 3 Revisioned A-leg idle epoch | PASS |
| 4 Lifecycle-aware scheduling | PASS |
| 5 Generation-owned bounded scheduler | PASS |
| 6 Refresh/time/economic bounds | PASS |
| 7 Global/per-session administration | PASS |
| 8 Enrollment separation | PASS |
| 9 Evidence-gated provider rollout | PASS |
| 10 Bounded observability | PASS |
| 11 Deterministic race-safe TDD | PASS |

## Final Assessment

**PASS for implementation and design validation.** The generic orchestration is complete without requiring any real provider to be active and remains behavior-preserving when no eligible renewable observations exist. The direct Anthropic live-effect test is intentionally credential-gated and not claimed as passed; fake-provider tests prove request/control mechanics, while live cache-effect evidence determines whether autonomous renewal can be enabled as supported behavior. No provider-specific exception weakens the core scheduler, generation lifetime, foreground precedence, or bounded-cost invariants.
