# Requirements Document

## Introduction

The pre-OSS `pre-oss-core-slimming` specification deliberately performs only the release-critical simplification work: it closes the extension-plane v1 contract, moves the clearest optional feature implementations out of `internal/core`, and prevents `internal/infra/runtimebundle` from directly importing concrete feature packages. That work is intentionally not the end-state architecture.

This specification is the **full-closure follow-up**. It completes the migration from a historically feature-growing core toward a small stable kernel plus explicit extension/standard-distribution composition. It is not a second rewrite of the extension-plane substrate. It starts only after the first specification has been implemented and certified, consumes that implementation's residual ownership inventory, and then removes every remaining optional UX/feature implementation, feature-specific process/generation service, and feature-specific public host seam from generic core/runtime composition unless the responsibility is proven to be a kernel invariant.

The completion criterion is deliberately stronger than “move the known packages”: after this specification, an exhaustive production ownership census must contain **no unclassified or deferred simplification item**. Every surviving core responsibility must have a durable kernel/generic-extension justification and an architecture ratchet; every optional standard feature implementation and its state/policy/configuration/composition must live outside the generic kernel.

## Boundary Context

- **Hard execution prerequisite**: the implementation of `.kiro/specs/pre-oss-core-slimming/` (PR #557 specification) must be merged, archived/certified, and its Task 8.3 residual ownership inventory must exist on `main`. Merging the spec-only PR is not sufficient. An executor must not begin migration work against the pre-first-spec topology.
- **In scope**: exhaustive post-first-spec ownership classification; compaction-continuity state/coordination extraction; conversation-view kernel/policy split; interleaved-thinking kernel/policy split; keep-warm extraction; terminal-decision session-policy extraction; standard-feature process/generation composition ownership; feature-specific public host-option cleanup; feature-specific configuration cleanup where generic core config currently owns optional UX policy; remaining feature-only support-package relocation; permanent architecture/core-size/change-surface ratchets; final no-residual-debt certification.
- **Out of scope**: changing provider protocols, routing semantics unrelated to extracting optional policy, B2BUA authority, billing/accounting semantics, secure-session security semantics, canonical `pkg/lipapi` wire meaning, backend/front-end plugin architecture, #394 performance/load optimization, a Cordis runtime, a DI container, service locator, generic workflow engine, native Go dynamic plugins, or re-opening the generated standard-plane v1 decision made by the first spec (beyond the sole reviewed exception below).
- **Boundary ownership**: core owns universal orchestration/invariants and generic extension execution; `pkg/lipsdk` owns stable public contracts; standard feature implementations live under `internal/plugins/features` or feature-owned support; feature-specific infrastructure/composition lives outside core and outside generic `runtimebundle`; `internal/standardplugins` owns knowledge of the concrete standard distribution.
- **Cordis-v4 adaptation**: preserve existing Go-LIP lifetime authorities (`ProcessServices`, `ResourceLedger`, `runtimehost.Manager`, request/attempt owners), use explicit typed construction, and do not introduce a second lifecycle/dependency runtime.
- **Revalidation triggers**: any contradiction between the first spec's final ownership inventory and this spec's expected post-first topology; any proposed new public SDK/ABI; any change to immutable generation publication, request pinning, output commitment, secure-session authority, B2BUA lineage, or routing grammar.
- **Reviewed exception (`secret_guard_execution`)**: Independent remediation required one new SDK plane to convey Secret Guard execution posture without leaking feature fields into generic runtime composition. `secret_guard_execution` is explicitly approved as the **sole reviewed exception** to “do not reopen the v1 plane set”. This closed set is exactly 26 planes after this reviewed exception and does **not** reopen arbitrary plane growth or dynamic extension. Future public SDK/plane additions remain revalidation events.

## Requirements

### Requirement 1: Enforce the post-first-spec execution prerequisite and complete ownership census
**Objective:** As a maintainer, I want full closure to start from the certified post-first-spec topology, so that the executor does not reason from stale package paths or silently skip newly exposed residual debt.

#### Acceptance Criteria
1. Before any production migration, the implementation shall verify that the pre-OSS simplification implementation is merged and certified and shall locate its durable residual ownership inventory.
2. If any first-spec completion invariant is false—including retired package resurrection, direct `runtimebundle -> internal/plugins/features/*` imports, dynamic plane fallback, missing external SDK fixture, or missing residual inventory—the implementation shall stop before migration and report the unmet prerequisite rather than adapting around it.
3. The implementation shall recursively inventory production code under `internal/core`, `internal/infra/*compose`, `internal/standardplugins`, `internal/pluginreg`, `internal/featurebundle`, `internal/infra/runtimebundle`, `pkg/lipruntime`, and feature-only support packages outside `internal/plugins/features`.
4. Each inventoried responsibility shall be classified as exactly one of: **kernel invariant**, **generic extension mechanism**, **optional feature implementation/policy**, **feature-specific infrastructure/composition**, **standard-distribution registration/composition**, or **obsolete/duplicate**.
5. A responsibility may remain in `internal/core` only when it is required by the base proxy with all optional standard features disabled, or when it is a feature-neutral extension/orchestration mechanism with at least two independent consumers/producers or an independently justified universal invariant.
6. A responsibility shall not qualify as core merely because an earlier spec placed it there or because moving it is difficult.
7. The final census shall contain zero `mixed/needs split`, `unknown`, `deferred`, or unclassified rows.

### Requirement 2: Preserve kernel authorities while removing feature semantics
**Objective:** As a maintainer, I want the refactor to shrink feature awareness without relocating core authority, so that simplification does not create a new orchestration or lifecycle system.

#### Acceptance Criteria
1. Core shall remain authoritative for routing/failover, B2BUA lineage, client-output commitment, canonical stream sequencing, request/attempt lifecycle, secure-session authority, and immutable-generation publication/retirement.
2. Process-owned feature resources shall remain physically owned beneath the existing process lifetime and shall not create an independent process shutdown coordinator parallel to `ProcessServices`.
3. Generation-owned feature resources/lifecycles shall be registered with the existing generation ownership/ledger path before publication and shall not live-mutate a published generation.
4. Feature extraction shall not move routing decisions, failover authority, or B-leg creation into feature plugins or a generic dependency graph.
5. The implementation shall introduce no runtime service locator, reflection-based dependency injection, `map[string]any` dependency bag, global mutable registry, generic effect engine, or live `requires/provides` graph.
6. Client-visible output and provider calls shall continue to be treated as irreversible external effects; no extraction shall add retry/failover after the existing output-commit boundary.

### Requirement 3: Make compaction-continuity state and policy feature-owned
**Objective:** As a maintainer, I want compaction-continuity branch/capsule/job/injection semantics owned by the compaction-continuity feature, so that core no longer carries one optional coding-agent UX feature's domain model.

#### Acceptance Criteria
1. The final tree shall contain no production `internal/core/compactioncontinuity` package.
2. Branch identity/binding, capsule/source state, preview intents, pending jobs, injection targets/watermarks, feature bounds, CAS/revision rules, and compaction-specific persistence namespace shall be owned under the compaction-continuity feature or its feature-owned support package.
3. Core shall expose only the minimal generic authority/correlation facts required for the feature to bind an authoritative parent; raw feature state shall not be added to B2BUA or secure-session domain models solely for compaction continuity.
4. The existing process lifetime, reload overlap behavior, bounded state, authoritative parent isolation, background-job validation, stale-result rejection, and race behavior shall remain observationally equivalent.
5. Generic `runtimebundle.ProcessServices` shall not contain a `BranchCoordinator`, compaction-continuity parent port, or other concrete compaction-continuity state field after migration.
6. Standard feature composition may know the concrete compaction-continuity implementation; core/runtimebundle shall not.

### Requirement 4: Split conversation-view kernel projection from optional steering/policy services
**Objective:** As a maintainer, I want universal backend-effective conversation projection to remain a kernel invariant while optional steering and trusted producer application services leave core, so that a useful generic boundary is preserved without keeping UX policy in the kernel.

#### Acceptance Criteria
1. Core shall retain only feature-neutral semantic message identity, `never_backend` exclusion enforcement, deterministic backend-effective projection/reassertion, anchor/provenance primitives required at the A-leg/B-leg boundary, and minimal read-only snapshot ports needed by runtime.
2. Persistent steering CRUD/state, steering placement/default/fallback policy, steering content bounds, cache-discontinuity UX policy, SDK writer/registrar application services, persistence adapters, and diagnostics specific to optional steering shall not remain in the core projection package.
3. Trusted `pkg/lipsdk/nonforwardable`, `pkg/lipsdk/steering`, and `pkg/lipsdk/localturn` producers shall continue to work through narrow explicit adapters without learning core implementation types.
4. Projection shall remain pure/deterministic for a frozen request snapshot and shall not add request-time storage reads, locks, reflection, or feature lookup.
5. Memory/SQLite/PostgreSQL behavior, anchor safety, never-backend atomicity, cache-prefix regression behavior, and no-plaintext diagnostics shall be preserved.
6. If the post-first inventory proves a conversation-view subcomponent is genuinely used as a generic kernel mechanism by multiple independent feature classes, the implementation may retain that subcomponent only with an explicit package-level ownership test and documentation; it shall not retain unrelated steering implementation alongside it for convenience.

### Requirement 5: Isolate interleaved-thinking UX processing from core routing authority
**Objective:** As a maintainer, I want `[thinker]` routing and B-leg orchestration to remain core-owned while memo/shaping/sanitization policy is isolated from the kernel, so that routing correctness is preserved without embedding an entire UX feature implementation in core.

#### Acceptance Criteria
1. Core shall retain selector grammar/validation, thinker-role route planning/cycle authority, B-leg continuation sequencing, output-commit behavior, and the minimal serializable state required for routing/continuity correctness.
2. Memo extraction, memo content/bounds policy, prompt shaping/injection, visible-stream sanitization, and other optional interleaved UX transformations shall move out of `internal/core/interleavedthinking` into a feature/feature-support implementation behind a narrow consumer-owned interface.
3. Core runtime shall not import the concrete memo/shaping implementation and shall not know its concrete configuration type.
4. The final ownership of `interleavedstate` shall be minimized: only values genuinely shared by routing and durable continuity may remain core-owned; feature-only memo payload/reference state shall move outward.
5. Hidden/visible thinker behavior, weighted cycle behavior, secure-session/A-leg authority, cancellation, and no-retry-after-visible-thinker-output semantics shall remain unchanged.
6. The implementation shall not generalize this extraction into a generic multi-step workflow engine or arbitrary agent orchestration framework.

### Requirement 6: Move keep-warm maintenance policy out of core
**Objective:** As a maintainer, I want prompt-cache keep-warm scheduling/policy to be optional standard-feature infrastructure rather than core policy, so that future cache-maintenance enhancements do not grow the kernel.

#### Acceptance Criteria
1. The final tree shall contain no feature-policy implementation under `internal/core/keepwarm`.
2. Keep-warm configuration, policy decisions, scheduling, manager/registry state, administrative disable policy, arm/run-due behavior, and feature-specific accounting adaptation shall be owned by a standard feature or feature-specific infrastructure package.
3. Core may retain only a narrow provider-neutral notification/consumer port for lifecycle facts that only core can authoritatively emit, such as real-turn start/session end/committed successful terminal, if existing SDK planes cannot represent those facts without widening semantics.
4. Existing `pkg/lipsdk/promptcache` residency/controller contracts shall remain provider-neutral and shall not absorb keep-warm scheduling policy.
5. Process/generation ownership, cancellation/quiesce ordering, no synchronous provider-control work on session end, provider-authoritative accounting, and reload behavior shall remain equivalent.
6. The extraction shall not add a generic scheduler framework solely to host keep-warm.

### Requirement 7: Remove terminal-decision feature policy storage from core
**Objective:** As a maintainer, I want session-scoped optional feature policy storage to live outside core execution, so that terminal-decision UX controls do not require a core package.

#### Acceptance Criteria
1. The final tree shall contain no production `internal/core/terminaldecisionpolicy` package.
2. The bounded process-owned store, client/operator tri-state semantics, feature-ID keying, authority validation, and HTTP/admin adaptation shall be owned outside core in feature/standard-distribution infrastructure.
3. Core request admission shall consume only an immutable effective policy snapshot or a narrow feature-neutral reader interface and shall not manipulate actor-specific terminal-decision policy state.
4. The exclusive `pkg/lipsdk/terminaldecision` provider slot and generic terminal-decision chokepoint shall remain unchanged unless a smaller compatible SDK correction is strictly required.
5. Session scope isolation, capacity/bounds behavior, process lifetime, and close semantics shall remain equivalent.

### Requirement 8: Establish one standard-distribution feature composition owner
**Objective:** As a maintainer, I want concrete standard-feature process/generation assembly to have one explicit home outside generic runtime composition, so that every new sophisticated feature does not add fields and branches to `runtimebundle`.

#### Acceptance Criteria
1. `internal/standardplugins` (or a dedicated child package owned by the standard distribution) shall become the only generic composition layer permitted to know the concrete set of bundled standard feature implementations.
2. Generic `runtimebundle.ProcessServices` shall contain at most one standard-feature-host aggregate/handle rather than individual fields for keep-warm, terminal-decision policy, compaction continuity, compaction detection, reasoning preservation, secret guard, or later standard features.
3. Generic generation compilation shall invoke one bounded standard-feature composition facade and consume ordinary `FeatureBundle`/`FrozenPlaneSet`, lifecycles, and narrow core consumer ports; it shall not switch on concrete feature IDs or import concrete feature packages.
4. The standard-feature composition owner may use small dedicated feature adapters internally, but it shall not expose a service locator, `any` bag, reflection registration, or a universal dependency graph.
5. Process-scoped feature resources shall be closed exactly once through the standard-feature host's process-owned nested cleanup registered with `ProcessServices`; generation resources shall remain owned by `ResourceLedger`.
6. A new ordinary existing-plane standard feature shall require edits only in the new feature package plus standard-distribution registration/composition and tests; it shall require zero production changes to core, runtimebundle, featurebundle, or public SDK.
7. A new host-bound standard feature that uses only already-modeled generic host facts/capabilities shall not require a new field in `ProcessServices`, `ExecutorConfig`, or `pkg/lipruntime.Options`.

### Requirement 9: Remove feature-specific public host composition from `pkg/lipruntime`
**Objective:** As a public host integrator, I want feature-specific trusted host bindings registered through a stable extension contract rather than adding one field/type family per feature to `pkg/lipruntime`, so that the public runtime facade does not grow with UX features.

#### Acceptance Criteria
1. `pkg/lipruntime` shall not import `internal/plugins/features/*` after migration.
2. The feature-specific `ReasoningCompressionOptions`/adapter family shall no longer be defined by `pkg/lipruntime` itself.
3. Where a standard feature legitimately requires a trusted host-provided policy/capability, the public contract shall live in a feature-neutral SDK registration envelope plus a narrowly scoped SDK package that owns that capability's typed contract; generic runtime code shall forward registrations without type-switching on feature implementations.
4. Host registrations shall be validated at build/generation time, immutable after publication, unique by stable registration identity, and absent from request-time lookup paths.
5. Unsupported or duplicate host registrations shall fail explicitly before serving; they shall not be silently ignored.
6. The new contract shall not become a general-purpose service locator: feature code shall receive only its statically composed typed binding, and no request handler shall resolve arbitrary services by string/type at runtime.
7. External compile-contract tests shall prove a host module can provide the supported reasoning egress/matcher binding without importing repository `internal` packages.

### Requirement 10: Move optional feature configuration out of generic core configuration ownership
**Objective:** As a maintainer, I want optional UX feature configuration to be decoded/validated by the owning feature registration, so that `internal/core/config` does not grow a new schema block for every feature.

#### Acceptance Criteria
1. Configuration that controls only an optional standard feature's behavior shall be migrated to that feature's `plugins.features` YAML payload or another feature-owned registration payload, unless the same value is independently required by a universal core invariant.
2. At minimum the implementation shall evaluate and resolve ownership for interleaved-thinking and keep-warm/prompt-cache-maintenance configuration identified by the post-first census.
3. Legacy top-level configuration aliases may be supported only through a bounded migration adapter at configuration/standard-distribution composition; no two semantic authorities shall remain after the compatibility window defined in design.
4. New optional feature settings after this migration shall not require fields or validators in `internal/core/config` unless an architecture test fixture proves the setting affects base kernel behavior with the feature absent.
5. Configuration migration shall preserve defaults and existing valid behavior or provide an explicit fail-fast migration error with documentation; silent semantic changes are forbidden.

### Requirement 11: Consolidate feature-only support packages and dedicated compose adapters
**Objective:** As a maintainer, I want feature implementation/support code physically colocated with its owner and composition adapters reduced to intentional boundaries, so that feature ownership is visible from package paths.

#### Acceptance Criteria
1. The implementation shall inspect feature-only support packages outside `internal/plugins/features`—including reasoning replay/compression helpers and all dedicated `*compose` packages—and move feature implementation code under its feature owner when no independent generic consumer exists.
2. Dedicated compose packages may remain only when they translate genuine host/process/generation infrastructure into a feature and cannot be expressed as a pure feature constructor without creating a core dependency.
3. Remaining dedicated compose packages shall be children/internal details of the standard-feature composition owner where practical and shall not be called directly from generic runtimebundle.
4. Feature packages shall not import `internal/core`, `internal/infra/runtimebundle`, frontends, or backends; dependencies on kernel facts shall flow through `pkg/lipapi`, `pkg/lipsdk`, or consumer-owned narrow ports/adapters.
5. Obsolete compatibility helpers, duplicate mirrors, and one-feature generic abstractions exposed by the census shall be deleted rather than renamed.

### Requirement 12: Permanently prevent core and composition regrowth
**Objective:** As a maintainer, I want executable architectural ratchets, so that the core does not slowly reacquire optional feature code after this cleanup.

#### Acceptance Criteria
1. Architecture tests shall reject concrete feature-package imports from `internal/core`, `internal/infra/runtimebundle`, and generic runtimehost/featurebundle/plugin-registry mechanisms.
2. Architecture tests shall reject production resurrection of packages retired by both simplification specs.
3. Architecture tests shall enforce the core-admission rule for new top-level core packages through an explicit ownership manifest/classification table rather than task-number-specific scanners.
4. The `internal/core` non-test line budget shall be reset downward to the measured final tree plus small fixed headroom; deleted feature LOC shall not remain as future growth allowance.
5. Standard-feature composition shall have its own bounded size/change-surface budget so moving growth out of core does not create an unchecked god package.
6. Existing request-path allocation/locking/goroutine guarantees from the extension-plane work shall not regress.
7. A disposable standard-feature probe shall demonstrate that adding a host-bound feature using existing contracts requires no production core/runtimebundle/public-runtime modification.

### Requirement 13: Finish with zero simplification debt
**Objective:** As a project owner, I want a definitive end to this simplification program, so that no third “core slimming” migration is implicitly required.

#### Acceptance Criteria
1. The final ownership census shall be regenerated from the merged implementation and every production responsibility shall have a final owner classification with zero deferred simplification item.
2. Every surviving top-level `internal/core` package shall have a concise durable justification identifying the universal kernel invariant or generic extension mechanism it owns.
3. Every optional standard UX/feature implementation identified by the census shall be outside core and generic runtime composition.
4. `ProcessServices`, `ExecutorConfig`, `runtimebundle` build inputs, and `pkg/lipruntime.Options` shall contain no concrete feature-specific field except a single standard-feature-host/registration aggregate permitted by Requirements 8–9.
5. Documentation and steering shall describe the final ownership model, not migration-era package locations.
6. The implementation shall pass full correctness, architecture, generated-code, docs, external-contract, race, fuzz where applicable, and fixed-cost allocation gates, followed by independent architecture review and clean merged-main certification.
7. If final review identifies any material feature-ownership/simplification defect, this specification shall remain incomplete until corrected; the work shall not be closed by creating a new follow-up simplification tracker.
