# Research & Design Decisions

## Summary

- **Feature**: `core-feature-ownership-full-closure`
- **Discovery Scope**: Brownfield architecture completion / kernel-vs-feature ownership convergence
- **Authoring baseline**: `main` at `90379589551edd48a32a2fa4b18f43139771cf7f`, plus the approved target architecture in spec-only PR #557. **Implementation baseline is intentionally later**: execution is blocked until #557's `pre-oss-core-slimming` specification has itself been implemented and its Task 8.3 residual inventory exists on `main`.
- **Key Findings**:
  - The first spec correctly handles release-critical extension substrate/three obvious migrations, but generic process/runtime composition still contains several concrete optional-feature state/policy concepts.
  - `compactioncontinuity`, conversation steering, interleaved-thinking memo processing, keep-warm scheduling, and terminal-decision session policy are the material remaining feature-growth vectors.
  - Current `ProcessServices` demonstrates why package moves alone are insufficient: it directly carries concrete keep-warm, terminal-decision, compaction detector, branch coordinator, and compaction parent-port fields.
  - Current `pkg/lipruntime.Options` similarly contains feature-specific reasoning-compression host composition, so public runtime growth pressure remains unless host bindings become registrations rather than fields.
  - A fully generic DI/generation-binder runtime is still the wrong answer. The correct full-closure boundary is a **standard-distribution feature host**: one explicitly constructed composition facade, internally allowed to know standard features, while generic runtime/core remain feature-neutral.
  - Some historically feature-named core code is legitimate kernel behavior and must be **split rather than blindly moved**. Conversation-view projection/identity and `[thinker]` routing/B-leg continuation are examples.
  - Keep-warm was not in the minimum residual list written into PR #557, but brownfield inspection shows it is exactly the kind of optional policy this program is intended to remove from core.

## Research Log

### First-spec boundary and dependency

- **Context**: This SDD must schedule only work that remains after the first simplification SDD is implemented.
- **Sources Consulted**:
  - PR #557 `pre-oss-core-slimming` summary/design/tasks.
  - Its explicit Task 8.3 full-closure handoff requirement.
- **Findings**:
  - The first SDD intentionally excludes full decomposition of `compactioncontinuity`, `conversationview`, `interleavedthinking`, `interleavedstate`, and `terminaldecisionpolicy`.
  - It explicitly leaves feature-specific `pkg/lipruntime` options and dedicated feature compose adapters for full closure.
  - Task 8.3 requires a durable residual ownership census after the first implementation rather than leaving deferred work in chat history.
- **Implications**:
  - This SDD can be fully designed now, but the executor's first hard gate is post-first implementation revalidation. The implementation agent is not allowed to extrapolate package paths if the first implementation chose a materially different but compliant topology.
  - An unexpected residual that does not fit this SDD is a **spec repair trigger**, not permission for the weak executor to invent a new architecture.

### Core admission rule

- **Context**: “Fully close” cannot mean “move five packages”; the repo needs a durable answer to what belongs in core.
- **Sources Consulted**: root/`.kiro` steering, current package inventory, Go-LIP Cordis-v4 discipline.
- **Findings**:
  - Existing repository principles already say core owns routing/failover/B2BUA/canonical orchestration and concrete feature semantics belong outside core.
  - The useful Cordis-v4 adaptation is explicit ownership and dependency direction, not a component runtime.
- **Implications**:
  - The final ownership census uses a mechanical admission test: a core responsibility must be required when optional standard features are absent **or** be a generic extension/orchestration mechanism with independent reuse/universal invariant evidence.
  - Historical placement, difficult migration, or “core calls it today” is not evidence of kernel ownership.

### Compaction-continuity coordinator

- **Context**: PR #557 moves the concrete compaction detector but deliberately leaves continuity coordination.
- **Sources Consulted**: `internal/core/compactioncontinuity/*`, `internal/plugins/features/compactioncontinuity/*`, `internal/infra/compactioncompose/*`.
- **Findings**:
  - `internal/core/compactioncontinuity` owns feature-specific vocabulary and data: `lip.compaction-continuity` namespace, `BranchKey`, capsule/source JSON, preview intents, pending jobs, pending injection, injection watermarks, last compaction transaction, feature-specific bounds and revision rules.
  - The feature already exposes a `ParentPort` and feature-owned `ParentState` contract; `compactioncompose` is the adapter between authoritative core identity/state and the feature.
  - The process lifetime is real and must survive overlapping generations, but **process lifetime does not imply core semantic ownership**.
- **Decision**: Move branch coordinator domain/state under the feature; keep authoritative A-leg/session/principal facts in core and bind them through an adapter. Standard-feature host owns the process-scoped feature instance beneath `ProcessServices` lifetime.

### Conversation-view mixed ownership

- **Context**: `conversationview` contains both universal A-leg/B-leg projection safety and optional steering UX.
- **Sources Consulted**: `internal/core/conversationview/doc.go`, identity/anchor/projection/store files, sdkadapter, persistence/tests.
- **Findings**:
  - `Project`, semantic message identity, never-backend exclusion and final reassertion are required to prevent proxy-owned/local content from leaking to backends and to preserve canonical backend-effective request semantics.
  - The same package also owns optional persistent steering: overlay IDs, model-visible text, placement (`stable_prefix` / `after_message`), missing-anchor fallback policy, CRUD, cache-discontinuity policy, stores, SDK writer/registrar and feature-specific diagnostics.
  - Moving the entire package would force core execution to call an optional feature for a safety invariant; leaving the entire package keeps optional UX policy in core.
- **Decision**: Split. Retain a small kernel projection/identity package (final name chosen mechanically during move, design defaults to `internal/core/conversationprojection`) and move stateful steering/nonforwardable application services/persistence into `internal/infra/conversationview` plus feature-facing SDK adapters. Preserve the existing SDK producers.
- **Trade-off**: The word “conversation view” may survive outside core; package renaming is less important than semantic ownership and dependency direction.

### Interleaved thinking

- **Context**: Historical porting spec explicitly classified interleaved thinking as core because it crosses routing, runtime, streaming and continuity.
- **Sources Consulted**: archived `port-interleaved-thinking` design, `internal/core/interleavedthinking`, `internal/core/interleavedstate`, routing/runtime consumers, config.
- **Findings**:
  - `[thinker]` is routing grammar; weighted thinker cycles and continuation B-leg opening are routing/runtime authority and cannot be delegated to a normal feature hook.
  - `interleavedthinking`, however, also contains optional UX policy: a large built-in thinker prompt, instruction-file loading, memo extraction/bounds/storage, memo injection and visible stream sanitation.
  - `interleavedstate` mixes cycle state required by routing with memo references needed by the UX implementation.
- **Decision**: Preserve the routing operator and output-commit orchestration in core; extract memo/shaping/sanitization policy to `internal/plugins/features/interleavedthinking` (or a feature-owned subpackage) behind a narrow core consumer port. Split `interleavedstate` so only route/cycle values remain in core; memo payload/ref semantics move with the feature.
- **Rejected**: a generic workflow engine. There is no evidence that arbitrary multi-step agent workflows should become a core abstraction.

### Keep-warm policy

- **Context**: Full current-tree audit exposed a material optional feature not named in the first spec's minimum handoff list.
- **Sources Consulted**: `internal/core/keepwarm/*`, `pkg/lipsdk/promptcache/*`, runtime composition/config.
- **Findings**:
  - Core package owns configuration, policy, scheduling, manager registry, lifecycle, administration and accounting adaptation for prompt-cache maintenance.
  - The SDK already has a provider-neutral prompt-cache contract (`Observation`, `Controller`, lifecycle kinds, accounting evidence) and deliberately excludes scheduling policy.
  - The runtime-facing `Orchestrator` only needs authoritative lifecycle notifications such as foreground turn start, session end and committed successful turn.
- **Decision**: Move keep-warm implementation to `internal/plugins/features/keepwarm`; use a narrow core consumer interface for the few lifecycle facts that only core can emit. Standard-feature host constructs/owns the feature. Do not add keep-warm scheduling to `pkg/lipsdk/promptcache`.

### Terminal-decision policy store

- **Context**: Terminal decision execution is already a generic exclusive plane/provider, but a process store for client/operator enablement remains in core.
- **Sources Consulted**: `internal/core/terminaldecisionpolicy/*`, terminal-decision SDK/runtime consumers.
- **Findings**:
  - Store data is bounded and safe but is optional feature policy: key includes `FeatureID`, values are client/operator tri-state, and the package description explicitly says it is used by the terminal-decision feature.
  - Core request admission needs only a frozen effective enabled state; it does not need the mutable actor policy domain.
- **Decision**: Move mutable policy service outside core (design default `internal/standardplugins/featurehost/sessionpolicy` or `internal/infra/sessionfeaturepolicy`). Standard-feature host owns it and supplies a narrow immutable reader/snapshot to core.

### Generic `ProcessServices` still exposes concrete features

- **Context**: The first spec removes direct runtimebundle imports of concrete feature packages but does not eliminate feature-specific process fields.
- **Sources Consulted**: `internal/infra/runtimebundle/process_services_types.go`.
- **Findings**:
  - Current fields include `KeepwarmPolicy`, `KeepwarmRegistry`, `TerminalDecisionPolicy`, `CompactionDetector`, `BranchCoordinator`, `CompactionParentPort` and generic `BackgroundAux`.
  - Moving packages but retaining one field per feature would preserve the same core/runtime growth vector.
- **Decision**: Introduce one **standard-feature host** aggregate owned under `internal/standardplugins/featurehost`. `ProcessServices` owns exactly that aggregate as one nested process resource. The aggregate internally composes concrete standard features using small typed feature-specific helpers.
- **Important non-goal**: this is not a dynamic DI container. It is the explicit composition root for the built-in standard distribution; it has no runtime lookup API and is not used on request hot paths.

### Standard-feature host shape

- **Selected responsibilities**:
  - process construction/cleanup of standard feature state/services;
  - generation compilation/binding of standard features into ordinary `FeatureBundle`/planes/lifecycles;
  - production of a small fixed set of **generic core consumer ports** for facts that cannot be represented by existing planes (for example prompt-cache maintenance lifecycle, terminal policy effective snapshot, compaction detector port);
  - ownership of feature-specific dedicated compose helpers created by the first spec.
- **Not responsible for**:
  - routing, B2BUA, runtimehost publication, ResourceLedger, HTTP serving, provider/backend construction, billing authority, dynamic plugin discovery.
- **Dependency rule**: `featurehost` may import concrete standard feature packages; concrete features may not import featurehost/runtimebundle/core.

### Public `pkg/lipruntime` feature-specific options

- **Context**: `pkg/lipruntime.Options.ReasoningCompression` is a public feature-specific field and its adapter imports the concrete internal feature.
- **Findings**:
  - This makes the public runtime facade a future growth point for every host-bound feature.
  - The host needs a typed trusted policy seam, but generic runtime does not need to know its semantics.
- **Options Considered**:
  1. Keep one public field per host-bound feature — simple now, repeats the growth problem.
  2. `map[string]any`/service locator — flexible but violates explicit typed composition and becomes runtime DI.
  3. A startup-only typed **feature host registration envelope** — generic runtime validates uniqueness/forwards registrations; the standard-feature host interprets supported typed bindings during immutable generation composition.
- **Selected Approach**: Option 3.
- **Contract constraints**:
  - new `pkg/lipsdk/featurehost` (name may be `hostfeature` if package naming review requires) defines only `Registration`/`Binding` identity+validation mechanics;
  - concrete public binding types live in the narrow SDK package that owns the capability (for current reasoning egress policy, a dedicated SDK reasoning package rather than `pkg/lipruntime`);
  - no request-time resolution, no arbitrary services, no reflection; registration is startup/generation input only;
  - `pkg/lipruntime.Options` gets one `FeatureHostRegistrations []featurehost.Registration` field and deletes `ReasoningCompression` after a bounded alpha migration alias if source-compatibility policy requires it.

### Optional feature configuration in core config

- **Context**: Current `internal/core/config` directly imports/owns optional feature policy.
- **Evidence**:
  - `prompt_cache.go` imports `internal/core/keepwarm`, calls `keepwarm.DefaultConfig()` / `ConfigFromYAML`, and stores keep-warm config under core `PromptCacheConfig`.
  - `interleaved.go` contains the complete interleaved feature config plus a large built-in thinker prompt and file-loading policy.
- **Decision**:
  - Move keep-warm configuration to the standard feature payload (`plugins.features` registration for `keepwarm`).
  - Split interleaved configuration: routing-enablement/selector legality that is genuinely required by core stays as a minimal routing flag only if still necessary; memo/prompt/visibility/budget/instructions configuration moves to the interleaved-thinking feature payload.
  - Provide a short alpha migration decoder for existing top-level YAML only if repository compatibility policy requires it; the decoder translates once into feature config and is deleted by the end of this spec unless an explicit compatibility requirement says otherwise. Do not leave two semantic sources.

### Feature-only support packages

- **Context**: Feature implementation can remain outside core yet still be scattered through repository-level helper packages.
- **Decision**: The final census must inspect `internal/reasoningreplay`, dedicated compose packages, and similar one-feature helpers. If there is only one feature consumer and no generic domain contract, move under the feature. Keep a dedicated compose adapter only for a genuine infrastructure-to-feature translation.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Verdict |
| --- | --- | --- | --- | --- |
| Do nothing after first spec | Treat remaining mixed packages as acceptable | Lowest effort | Core/process/public facade keep feature growth vectors | Reject |
| Mechanical package moves only | Relocate files but retain concrete fields/branches | Visible LOC shrink | Does not solve future growth; dependency direction still concrete | Reject |
| Generic DI / dynamic generation binder | Runtime `requires/provides`, service map, generic feature services | Highly flexible | New runtime, weak typing, lifetime ambiguity, request/composition complexity | Reject |
| One universal feature plane for lifecycle/composition | Encode every process/generation dependency into plane payloads | Superficial uniformity | Super-hook / service-bag anti-pattern; public ABI explosion | Reject |
| **Standard-distribution feature host + narrow core ports** | One explicit standard-feature composition root; ordinary planes where possible; narrow consumer ports only for core-authored facts | Removes feature knowledge from generic runtime, preserves typed construction/lifetimes, supports future growth | Standard featurehost must be budgeted to avoid god-package growth | **Selected** |

## Design Decisions

### Decision: Full closure is classification-driven, not package-list-driven
- **Selected Approach**: Known migrations are explicitly designed, but completion requires an exhaustive final census and zero unresolved classifications.
- **Rationale**: The first inventory is intentionally produced after implementation and may expose support packages not visible in today's pre-first topology.
- **Executor rule**: unexpected residual code is handled only if it cleanly matches an already-defined destination/category. If it requires a new public contract or new orchestration semantics, stop and repair the spec before implementing it.

### Decision: Standard-feature host is the concrete composition boundary
- **Selected Approach**: `internal/standardplugins/featurehost` knows concrete standard features; generic runtimebundle knows one featurehost facade/aggregate.
- **Rationale**: Growth is legitimate in a distribution composition package. It is not legitimate in the kernel/runtime assembler.
- **Trade-offs**: One new aggregate exists, so enforce subpackage decomposition and size budgets. It must never become a request-time service locator.

### Decision: Use existing planes before adding core consumer ports
- **Rule**: Execution-time mutation/observation goes through existing generated planes. Add a private core consumer port only for authoritative lifecycle/orchestration facts that cannot be represented without corrupting existing plane semantics.
- **Examples**:
  - compaction detector interface: justified because core owns exact request-open/final-release observation point;
  - keep-warm lifecycle consumer: justified for committed terminal/session lifecycle;
  - reasoning attempt transforms/observers: ordinary planes, not a core port.

### Decision: Public host feature bindings are immutable registrations, not option fields
- **Selected Approach**: one `pkg/lipruntime.Options` slice of validated SDK host-feature registrations.
- **Rationale**: prevents per-feature public runtime growth while retaining typed feature-owned contracts.
- **Guardrail**: no arbitrary object lookup, no reflection, no `Get(featureID) any` API exposed to request code.

### Decision: Preserve kernel portions of mixed features
- **Conversation view**: semantic identity/exclusion/projection/reassertion remain kernel; optional steering application/state leaves.
- **Interleaved thinking**: `[thinker]` selector/cycle/B-leg orchestration/output commitment remain kernel; prompt/memo/shape/sanitize policy leaves.
- **Terminal decision**: generic terminal provider chokepoint remains; mutable session policy store leaves.
- **Prompt cache**: provider-neutral SDK residency/control contract remains; keep-warm policy leaves.

## Risks & Mitigations

- **Risk: first implementation changes exact paths** — hard post-first revalidation gate; use semantic owners, not stale path assumptions.
- **Risk: standard featurehost becomes a god object** — facade only, feature-specific child packages, explicit line/file budgets, no request-time API.
- **Risk: accidental routing authority migration while extracting interleaved thinking** — core retains selector/cycle/B-leg/output commitment; tests pin hidden/visible continuation and recovery rules before movement.
- **Risk: conversation projection safety weakened by steering split** — characterize never-backend/projection/reassertion/anchor invariants first; move stateful policy only after pure kernel facade is established.
- **Risk: configuration compatibility leaves two authorities** — migration adapter is one-way into feature config and deleted or explicitly time-bounded; final gate searches for duplicate defaults/validators.
- **Risk: public registration envelope becomes DI** — startup-only immutable registrations, typed bindings, unique IDs, no request lookup, no arbitrary service APIs, architecture tests.
- **Risk: process cleanup ownership duplicates** — featurehost is one nested process resource; ProcessServices owns its Close; featurehost owns its internal resources once; generation resources remain ledger-owned.
- **Risk: scope balloons into unrelated core rewrite** — classification rule excludes routing/B2BUA/billing/secure-session/protocol work unless needed to remove a concrete feature dependency.

## References

- `.kiro/specs/pre-oss-core-slimming/` in PR #557 — required predecessor and residual inventory producer.
- `.kiro/specs/archive/extension-plane-declaration-consolidation/` — generated plane substrate and original follow-up commitment.
- `.kiro/specs/archive/extension-plane-review-corrections/` — corrected settled extension substrate.
- `.kiro/specs/archive/port-interleaved-thinking/` — original routing/continuation ownership rationale preserved by the split.
- `.agents/skills/go-lip-cordis-v4/SKILL.md` — existing lifetime authorities, explicit dependency rules, no generic runtime.
- `AGENTS.md`, `.kiro/AGENTS.md`, `.kiro/steering/*` — repository boundary and verification rules.
