# Implementation Plan

Implementation is TDD-first and depends on the chronologically prior `prompt-cache-residency-contract`. The generic scheduler/control plane must be complete and safe with only reference backends before any real provider is promoted to autonomous renewal.

## 1. Freeze Scheduler, Policy, and Race Semantics With RED Tests

- [x] 1.1 Define RED fake-clock arming and scheduling tests
  - Prove only a committed successful B-leg containing at least one completed canonical `ToolCategoryOSCommand` plus an eligible renewable residency observation arms one idle epoch.
  - Prove losing/raced/uncommitted B-legs, failed/cancelled attempts, ordinary assistant responses, non-OS-command tools, and OS-command turns without eligible targets do not arm.
  - Pin deterministic expiry scheduling with `lead = clamp(window/10, 15s, 5m)` and bounded deterministic early-only spread; verify 5-minute and 1-hour windows without wall-clock sleeps.
  - Prove minimum-residency, best-effort, and unknown lifetimes get no default timer and exact operator heuristic overrides work only with safe backend renewal.
  - Prove a real foreground turn before due time produces zero provider maintenance calls.
  - _Requirements: 2.1-2.7, 4.1-4.9, 11.1, 11.4_
  - _Boundary: provider-neutral scheduler state machine only_
  - _Depends: prompt-cache-residency-contract_
  - _Validation: fake-clock unit tests, no external provider_

- [x] 1.2 (P) Define RED foreground-resume and stale-result race tests
  - Cover resume before due, while queued, after worker pickup but before controller call, during controller call, and concurrently with controller completion.
  - Prove `BeginForegroundTurn` invalidates the old idle epoch before B-leg planning, cancels in-flight contexts, never waits for provider release, and makes queued/late results stale.
  - Prove stale results cannot mutate a newer epoch/target but still return authoritative provider-billable accounting evidence when a provider charge occurred.
  - Prove target handle/timing replacement increments target revision and an old result cannot overwrite the replacement.
  - Prove one in-flight renewal per target under concurrent timer/admin/foreground events.
  - _Requirements: 3.1-3.7, 5.5-5.7, 11.2-11.3, 11.7_
  - _Boundary: A-leg epoch/target revision and worker-result application_
  - _Depends: prompt-cache-residency-contract_
  - _Validation: barrier-driven deterministic race tests plus focused `-race`_

- [x] 1.3 (P) Define RED budget, capacity, and result-policy tests
  - Pin `max_refreshes_per_idle_epoch=6`, `max_idle_duration=1h`, `max_active_targets=1024`, `max_concurrent_renewals=4`, `renew_timeout=15s`, and cold-recreate continuation defaults/validation.
  - Prove a refresh slot is consumed at dispatch regardless of outcome and refresh/time exhaustion stops and releases the whole epoch.
  - Prove capacity retains the earliest-due targets, rejects/releases a less-urgent new target, and evicts/releases the current latest-due target when a more urgent target arrives.
  - Prove expired-before-dispatch, stale, unsupported, control error, and default cold recreation retire the target with no generic retry.
  - Prove `StillResident` does not imply a moved expiry; rescheduling requires replacement authoritative timing or an explicit heuristic.
  - Prove optional provider-token budget fails closed as `budget_unknown` when no conservative next-call estimate exists.
  - _Requirements: 5.2-5.7, 6.1-6.10, 11.1, 11.3, 11.6_
  - _Boundary: scheduler budgets/capacity/result state machine_
  - _Depends: prompt-cache-residency-contract_
  - _Validation: fake-clock/state-machine unit tests_

- [x] 1.4 (P) Define RED admin/reload/lifecycle tests
  - Prove global keep-warm defaults enabled, global disable is a master gate, and no eligible target means zero provider calls.
  - Prove per-A-leg disable immediately invalidates queued/in-flight work, clear is non-retroactive, and global disable cannot be bypassed.
  - Pin process session-policy-store capacity at 4096; a new disable at capacity returns a bounded error instead of evicting an existing disable.
  - Prove manager registration/unregistration does not retain retired generations and generation quiesce rejects late arms, cancels work, releases handles, joins workers, and completes before backend close.
  - Prove per-session disable state survives generation replacement but is cleaned at session end/process restart semantics.
  - _Requirements: 5.8-5.9, 7.1-7.9, 11.5, 11.7, 11.10_
  - _Boundary: process policy store + generation manager lifecycle_
  - _Depends: prompt-cache-residency-contract_
  - _Validation: lifecycle/composition/race/goleak tests_

## 2. Implement Configuration and the Generation-Owned Keep-Warm Manager

- [x] 2.1 Add keep-warm configuration defaults, validation, and heuristic-override parsing
  - Add the immutable generation config for master enable, refresh/time/target/concurrency/timeout/cold/token budgets using the validated defaults from the design.
  - Add exact backend-instance plus optional exact canonical-model heuristic overrides; deterministic backend `ExpiresAt` must take precedence over a heuristic.
  - Reject zero/negative/unbounded values, contradictory cold-recreate settings, and malformed heuristic intervals.
  - Keep provider cache enrollment settings outside this generic configuration block.
  - _Requirements: 4.2, 4.6-4.7, 5.3-5.7, 6.1-6.10, 7.1-7.2, 8.1-8.4_
  - _Boundary: core immutable configuration only_
  - _Depends: 1.1, 1.3_
  - _Design: Configuration; Heuristic overrides_

- [x] 2.2 Implement the revisioned generation manager, priority heap, and fake-clock seam
  - Implement A-leg idle epochs, epoch/target revisions, due-time heap, active-target count, deterministic insertion sequence, and overflow fail-closed behavior.
  - Implement deterministic expiry calculation exactly from backend `ObservedAt`/`ExpiresAt`; never transform minimum residency into expiry or add provider-name TTL branches.
  - Keep non-schedulable observations out of manager state and release their handles because the manager has no legal future use for them.
  - Implement hard capacity behavior that preserves the earliest-due targets and releases rejected/evicted handles.
  - Use an injectable clock/timer abstraction; no production test path may rely on sleeps for timing correctness.
  - _Requirements: 1.1-1.7, 3.1, 3.5, 4.1-4.9, 5.1-5.5, 11.1, 11.3_
  - _Boundary: `internal/core` provider-neutral keep-warm state machine_
  - _Depends: 2.1_
  - _Design: Core Domain Model; Scheduling Algorithm; Target Capacity_

- [x] 2.3 Implement bounded lazy renewal workers and control-result processing
  - Lazily start at most `max_concurrent_renewals` generation-owned workers on the first eligible dispatch so default-on/no-target configurations create no idle worker goroutines.
  - Recheck epoch/target revision, policy, budgets, in-flight state, and known expiry immediately before dispatch.
  - Consume the refresh slot before the provider call, allocate a distinct bounded maintenance `OperationID`, enforce `renew_timeout`, and invoke only the issuing spec1 controller/handle.
  - Apply `Renewed`, `StillResident`, `ColdRecreated`, `Stale`, `Unsupported`, error/cancellation exactly as the design state machine specifies; generic scheduler performs no retry.
  - Account authoritative provider evidence even for stale results, but discard stale scheduling mutations.
  - _Requirements: 1.4-1.6, 3.2-3.7, 5.3, 5.5-5.7, 6.1, 6.4-6.9, 10.4-10.5, 11.2-11.3, 11.6_
  - _Boundary: generation renewal dispatch/result plane_
  - _Depends: 1.2, 1.3, 2.2_
  - _Design: Scheduler and Worker Design; Renewal Result State Machine_

- [x] 2.4 Implement non-blocking handle release and generation quiesce
  - Make foreground/admin invalidation detach scheduler state synchronously and cancel in-flight contexts without waiting for provider/connector release.
  - Queue idempotent local-forget releases through bounded background cleanup that never outranks due renewals and may safely drop explicit release when saturated/closing because backend target stores are bounded and close invalidates handles.
  - Register manager shutdown in the runtime generation lifecycle so manager unregister/quiesce/cancel/release/worker join completes before backend/connector close.
  - Reject and release observations arriving after quiesce begins; do not migrate targets to the new generation.
  - Prove lazy workers/timer/cleanup own no goroutine leak when the generation never receives an eligible target.
  - _Requirements: 3.2-3.7, 5.1, 5.4-5.9, 11.3, 11.7, 11.10_
  - _Boundary: generation-owned maintenance resource lifecycle_
  - _Depends: 2.2, 2.3_
  - _Design: Lifecycle rule; Release cleanup; Configuration Reload_

## 3. Integrate Foreground Turn and Committed B-Leg Hooks

- [x] 3.1 Add the real-foreground-turn invalidation hook before B-leg planning
  - Call manager invalidation immediately after authoritative A-leg/session correlation for every real incoming turn, independent of whether keep-warm is currently globally/session disabled.
  - Guarantee invalidation occurs before route planning, backend/account selection, or opening the next B-leg.
  - Keep the hook in-memory/non-blocking with respect to provider cleanup; no network/DB/provider wait is allowed on the foreground critical path.
  - Ensure synthetic maintenance control operations cannot enter this hook or reset/create an idle epoch.
  - _Requirements: 1.4, 3.2-3.4, 5.5, 11.2_
  - _Boundary: A-leg foreground admission/normal execution ordering_
  - _Depends: 2.2, 2.4_
  - _Design: Runtime Integration — Foreground turn gate_

- [x] 3.2 Add one committed-terminal arm adapter using canonical tool events and residency sidebands
  - Reuse the existing lifecycle-enriched/correlated tool events and spec1 observation drain; do not parse shell text, tool arguments, descriptions, or raw provider events.
  - Call `ArmFromCommittedTurn` once after successful committed terminal, with only the committed B-leg's observations/controller binding.
  - Require at least one finished canonical OS-command tool event and at least one eligible renewable target; record bounded skip reasons otherwise.
  - Losing race/fallback attempts and failed/cancelled attempts may produce provider accounting but must never arm keep-warm.
  - Avoid a second stream traversal or duplicate tool classifier/lifecycle implementation.
  - _Requirements: 1.1-1.7, 2.1-2.7, 5.9, 11.4_
  - _Boundary: post-commit B-leg orchestration adapter only_
  - _Depends: 3.1, prompt-cache-residency-contract observation source_
  - _Design: Runtime Integration — Committed terminal arm adapter_

- [x] 3.3 Wire session-end cleanup and configuration-generation replacement
  - On session end/expiry, forget process-owned per-A-leg keep-warm policy and invalidate any live generation epoch for that A-leg.
  - On reload, construct the new generation manager from new validated config and quiesce the old manager with no target migration.
  - Preserve process-owned disabled-session policy across config generation replacement while ensuring the active-manager registry never retains an unregistered old manager.
  - Accept that an A-leg idle across reload loses maintenance until its next eligible real turn rather than retaining/reconstructing old provider state.
  - _Requirements: 3.6, 5.8-5.9, 7.2, 7.8, 11.7-11.8_
  - _Boundary: session/runtime generation lifecycle integration_
  - _Depends: 2.4, 3.1, 3.2_
  - _Design: Session end; Configuration Reload_

## 4. Add Administrative Policy, Accounting, and Observability

- [x] 4.1 Implement the bounded process-owned per-A-leg policy store and manager registry
  - Implement inherit/disabled state keyed only by validated A-leg authority with revision/updated time and the 4096-entry default bound.
  - Reject a new disable at capacity; never silently evict an existing administrative disable.
  - Implement `Disable`, `Clear`, `Get`, and session-end forget semantics; clear restores inheritance but does not arm retroactively.
  - Register only narrow invalidator interfaces for live generation managers and guarantee unregister before quiesce to avoid retaining backend generations.
  - Broadcast disable after committing policy state so queued/in-flight manager work is immediately stale/cancelled.
  - _Requirements: 7.3-7.8, 11.5, 11.7_
  - _Boundary: process admin policy state; no provider handles/controllers_
  - _Depends: 1.4, 2.4_
  - _Design: Process-Owned Per-Session Administrative Policy_

- [x] 4.2 Expose authenticated admin/control-plane disable, clear, and get operations
  - Reuse existing admin/session authority resolution; reject untrusted request-body cache/provider identifiers as policy keys.
  - Global disable remains the master gate; per-session clear cannot force background traffic when the master is off.
  - Return bounded capacity/not-found/state responses and make mutations auditable without exposing provider cache identity.
  - Add no field or endpoint behavior to OpenAI/Anthropic/Gemini/Codex client inference schemas.
  - _Requirements: 7.3-7.9, 10.6_
  - _Boundary: authenticated admin/control plane only_
  - _Depends: 4.1_
  - _Validation: admin authorization/API tests_

- [x] 4.3 Integrate maintenance provider-billable accounting and optional token budget
  - Record each control operation under its maintenance `OperationID`, separate from the triggering foreground A/B-leg usage while reusing the existing provider-billable accounting authority/plane.
  - Preserve explicit cache-read/cache-write/input/output/total evidence and never fabricate zero for absent evidence.
  - For configured provider-token budget, derive a conservative next-call estimate from prior observation/control evidence; if unavailable, retire/skip as `budget_unknown` instead of allowing an unbounded unknown-cost call.
  - Replace estimates with authoritative post-call evidence where available and aggregate per idle epoch across all targets.
  - Ensure stale/cancelled scheduling results still record authoritative provider charges when the provider call happened.
  - _Requirements: 6.9-6.10, 10.4-10.5_
  - _Boundary: host maintenance accounting/budget projection_
  - _Depends: 2.3, 3.2_
  - _Design: Budget Model; Accounting Integration_

- [x] 4.4 Add bounded-cardinality metrics and diagnostics
  - Export active epoch/target gauges, arm/skip counts, dispatch/result/cancel counters, deadline/capacity/provider failures, cold recreation, maintenance duration, and provider token evidence using finite reason/result/token-kind labels.
  - Exclude cache keys, target/generation IDs, handles, prompts, auth material, A-leg IDs, arbitrary model strings, and unbounded provider errors from labels/logs.
  - Keep foreground latency metrics free of background control duration and report maintenance duration independently.
  - Add cheap default-on/no-target instrumentation without spawning renewal workers/provider calls.
  - _Requirements: 10.1-10.6, 11.10_
  - _Boundary: keep-warm observability only_
  - _Depends: 2.3, 3.2, 4.1, 4.3_
  - _Design: Observability_

## 5. Implement Direct-Anthropic Enrollment and Renewal Behind Evidence Gates

- [x] 5.1 Define RED direct-Anthropic cache-enrollment compatibility tests
  - Prove default enrollment `disabled` preserves existing foreground request behavior and global keep-warm enablement alone never adds Anthropic cache control.
  - Pin explicit `automatic` enrollment plus only supported 5m/1h TTL values and reject unsupported combinations at backend configuration validation.
  - Prove automatic enrollment adds the provider-supported top-level cache-control shape without changing unrelated request semantics.
  - Prove zero cache creation/read evidence does not emit a renewable residency observation.
  - Scope this behavior to the direct Anthropic Messages product; compatible/Bedrock/Vertex routes do not inherit it implicitly.
  - _Requirements: 8.1-8.6, 9.2_
  - _Boundary: direct Anthropic backend foreground cache enrollment_
  - _Depends: prompt-cache-residency-contract_
  - _Validation: fake SDK/HTTP request-shape tests_

- [x] 5.2 Implement direct-Anthropic bounded target capture and zero-output control renewal
  - On proven cache residency, retain only the spec1-permitted bounded provider-local effective cacheable prefix/breakpoint and account/workspace affinity behind the issued handle; never retain raw auth when credentials can be re-resolved.
  - Construct non-streaming `max_tokens: 0` renewal using the exact observed prefix/breakpoint and provider-documented cache TTL semantics.
  - Remove/disable request features incompatible with zero-output prewarm: streaming, enabled thinking, structured output formatting, and forced/`any` tool choice.
  - Do not invent a placeholder after the cache breakpoint unless a live contract test proves that shape retains the intended cache identity.
  - Resolve fresh credentials at control time while enforcing the same provider account/workspace binding.
  - _Requirements: 1.1-1.6, 8.2-8.6, 9.1-9.3_
  - _Boundary: direct Anthropic provider adapter/control implementation only_
  - _Depends: 5.1, prompt-cache-residency-contract controller/target-store plumbing_
  - _Design: Provider Integration — Direct Anthropic First_

- [x] 5.3 Map Anthropic cache usage to authoritative residency/control outcomes
  - Convert confirmed cache reads/refreshes to `Renewed` with replacement observation/timing.
  - Convert an unexpected full cache creation for a previously warm target to `ColdRecreated` rather than generic success.
  - Treat no-cache evidence as stale/unsupported when the target cannot be proven and classify API/auth/transport failures as control errors without normal route fallback.
  - Anchor observation timing conservatively no earlier than the point the cache entry is known usable, so `ExpiresAt` never overstates remaining lifetime.
  - Return provider-billable cache usage separately from foreground usage.
  - _Requirements: 4.8-4.9, 6.4-6.9, 9.2-9.3, 10.4-10.5_
  - _Boundary: direct Anthropic provider evidence interpretation_
  - _Depends: 5.2_
  - _Validation: fake-provider usage matrix + scheduler integration with reference clock_

- [x] 5.4 Add the gated live direct-Anthropic cache-effect promotion test
  - Establish foreground cache creation/read, wait a controlled interval appropriate to the selected provider mode, issue zero-output renewal through the spec1 control seam, then verify a subsequent real request receives the expected cache-read evidence and maintenance emitted no model output.
  - Validate 5-minute mode first; validate 1-hour mode separately because provider TTL/write-pricing semantics differ.
  - Verify exact prefix/breakpoint reuse, incompatible-field sanitization, same account/workspace affinity, and provider-billable usage capture.
  - Keep the test behind explicit integration credentials/tag/environment and exclude it from default unit tests.
  - If the live gate cannot prove the effect, leave direct Anthropic renewal experimental/disabled; do not weaken the core scheduler or invent another provider-independent heartbeat shape.
  - _Requirements: 9.1-9.3, 9.9, 11.9_
  - _Boundary: real direct Anthropic integration gate only_
  - _Depends: 5.3_
  - _Validation: opt-in live provider integration test
  - _Closeout: The opt-in test is implemented and credential-gated; it was not executed locally without provider credentials, so autonomous direct-Anthropic renewal remains disabled or experimental until live cache-effect evidence is collected._

## 6. Ratchet Provider Rollout and Architecture Boundaries

- [x] 6.1 Pin observation-only/no-timer behavior for currently unproven providers
  - Add backend/profile/contract tests documenting that Codex subscription remains observation/affinity-only and no `generate=false` or turn-scoped state is used autonomously.
  - Pin OpenAI direct minimum-residency semantics so current `prompt_cache_options.ttl` is never treated as deterministic expiry by the scheduler.
  - Pin Gemini implicit as no-timer and prevent the generic orchestrator from creating explicit `CachedContent`; future explicit-resource support must originate in the Gemini adapter through spec1 observations.
  - Pin DeepSeek/xAI/Mistral/OpenRouter/Z.AI/other unknown or route-dependent lifetimes as no-timer absent a safe controller plus deterministic expiration or an explicit operator heuristic.
  - Require aggregator active renewal to preserve concrete downstream residency/affinity in its backend implementation.
  - _Requirements: 4.4-4.7, 9.4-9.9_
  - _Boundary: provider rollout contracts/documentation; no provider-specific scheduler branches_
  - _Depends: 2.1, 2.2, prompt-cache-residency-contract profiles_
  - _Validation: backend contract/profile tests_

- [x] 6.2 Add architecture guards against scheduler/provider/enrollment leakage
  - Fail if provider names/models/TTL constants or provider SDKs enter the core scheduler to decide renewal timing/shape.
  - Fail if maintenance invokes normal route selection, `Backend.Open`/canonical executor flow, fallback/racing/account substitution, or a synthetic `lipapi.Call`.
  - Fail if global keep-warm enablement mutates provider foreground cache enrollment/retention or if provider-ready request/auth state enters manager/admin/config persistence.
  - Fail if a process registry retains generation managers after unregister/quiesce, if old generations are pinned for cache targets, or if one long-lived ticker/goroutine appears per target/session.
  - Fail if autonomous Codex/Gemini/other provider renewal is enabled without the independent evidence gate defined by its backend contract.
  - _Requirements: 1.1-1.7, 5.1, 5.8-5.9, 8.1-8.6, 9.1-9.9, 11.8_
  - _Boundary: architecture/source-shape/lifecycle guardrails_
  - _Depends: 3.1-3.3, 4.1, 5.1-5.3, 6.1_
  - _Validation: `internal/archtest` + focused source-shape tests_

- [x] 6.3 Run race/leak, compatibility, and repository quality gates
  - Run fake-clock scheduler/unit suites with global default enabled and no eligible targets.
  - Run focused `go test -race` and `goleak` scenarios for resume, disable, generation reload/quiesce, target replacement, worker cancellation/completion, lazy-worker startup, manager registry unregister, and release cleanup.
  - Run spec1 residency/connector contract tests to prove orchestration did not widen provider/canonical ABI semantics.
  - Run config/admin/accounting/metrics tests and default repository unit/quality/architecture gates without provider credentials.
  - Run live Anthropic only when explicitly enabled; its absence/failure must not make generic scheduler tests depend on network.
  - _Requirements: 11.1-11.10_
  - _Boundary: verification only_
  - _Depends: 4.4, 5.4, 6.2_
  - _Validation: focused tests; `make quality-checks`; `make test-unit`; race/goleak suites_

- [x] 6.4 Perform final orchestration/provider-scope review
  - Verify every autonomous provider call is reachable only from an eligible spec1 renewable target and an active OS-command-derived idle epoch.
  - Verify every autonomous path has refresh/time/concurrency/timeout bounds and no generic retry loop.
  - Verify foreground turn start/admin disable/generation quiesce all make stale work unable to mutate later state.
  - Verify global default-on behavior is a cheap no-op without renewable targets and provider enrollment remains independently explicit where it changes foreground behavior.
  - Remove duplicated scheduler/provider state or provider-specific core exceptions that are not required by the final design.
  - _Requirements: 1.1-1.7, 2.1-2.7, 3.1-3.7, 5.1-5.9, 6.1-6.10, 8.1-8.6, 9.1-9.9, 11.10_
  - _Boundary: final implementation scope/traceability review_
  - _Depends: 6.3_
  - _Validation: final diff + requirements/design traceability review_
