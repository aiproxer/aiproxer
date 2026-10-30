# Requirements Document

## Introduction

The extension-plane declaration consolidation and its corrective follow-up have established the mechanics needed for feature plugins to contribute typed, immutable extension planes without the historical cross-layer mirror chain. The remaining pre-OSS problem is ownership: several optional UX-enhancement implementations or their concrete composition logic still live in `internal/core` or directly inside the generic `internal/infra/runtimebundle` composition root. In addition, the exported feature-plane API still exposes a map/reflection fallback for planes outside the generated standard manifest even though those planes are not carried consistently through all production freeze/replay paths.

This specification performs the **minimum release-critical simplification** needed before the OSS/plugin contract is exposed: make the production plane catalog explicitly closed and fail-closed; move three demonstrated concrete UX implementations out of core (`toolcallrepair`, secret-guard matching/source policy, and compaction detection); remove direct concrete-feature knowledge from `runtimebundle` by placing exceptional feature-specific assembly behind explicit typed composition adapters; and ratchet those boundaries so new features cannot casually regrow the core. It deliberately does **not** attempt the complete historical kernel-vs-feature decomposition. That exhaustive closure is owned by a required second follow-up Kiro SDD after this one.

## Boundary Context

- **In scope**: issue #554 closed-manifest decision; removal of ungenerated-plane production fallback; feature ownership of tool-call repair and secret-guard concrete matching/source machinery; relocation of the concrete compaction detector behind a consumer-owned runtime port; removal of direct `internal/plugins/features/*` imports from generic `runtimebundle`; dedicated explicit composition adapters for existing generation/process-bound feature assembly; architecture/core-size ratchets; external-style SDK authoring proof; documentation and release certification.
- **Out of scope**: fully supporting arbitrary dynamically declared planes; a generic runtime DI/service locator; a public generation-binder/dependency-injection framework; wholesale relocation of every `internal/core` package; redesign of routing, B2BUA, billing, secure session, terminal work, provider/backend/frontends, canonical models, or extension-stage semantics; full decomposition of compaction continuity, conversation view, interleaved-thinking/state, terminal-decision policy, or other mixed historical packages; #394 performance optimization/HOLD certification.
- **Adjacent expectations**: the completed `extension-plane-declaration-consolidation` and `extension-plane-review-corrections` archives are the settled substrate; this specification defines the intended production resolution for issue #554, which remains open until that contract is implemented and verified; the later full-closure SDD must start from the post-this-spec ownership inventory instead of reopening these decisions.
- **Boundary ownership**: `pkg/lipsdk/feature` owns the public closed-plane contract; `internal/core` owns generic orchestration and consumer ports only; `internal/plugins/features/<feature>` owns concrete optional feature policy/algorithms; `internal/infra/*compose` owns explicit feature-specific composition adapters when process/generation capabilities are needed; `internal/standardplugins` owns standard-distribution registration/factories; `runtimebundle` owns process/generation assembly without concrete feature implementation knowledge.
- **Optional hexagonal lens**: core/runtime is application orchestration; feature packages are policy/plugin implementations; `internal/infra/*compose` are composition adapters; `runtimebundle` remains the composition root; public SDK packages are ports/contracts.
- **Revalidation triggers**: changes to `Plane`/`ContributionSet`/`FrozenPlaneSet`; adding a standard plane; new direct concrete-feature import in core or runtimebundle; changes to feature process/generation prerequisites; compaction detector callback shape or lifetime; secret-guard access-mode/security semantics; request snapshot/reload semantics; any public SDK compatibility change.

## Requirements

### Requirement 1: Close the OSS Feature-Plane Contract

**Objective:** As an OSS plugin author, I want the supported extension-plane contract to be explicit and fail-closed, so that a plugin cannot appear to contribute a plane that production composition later drops or handles inconsistently.

#### Acceptance Criteria

1. When a feature contributes through a plane generated from the canonical standard manifest, the SDK shall preserve the current typed validation, combination, freeze, replay, identity, nil-versus-empty, and defensive-copy semantics, and the canonical generated binding shall be authoritative for production plane policy so that mutating exported policy fields on a copied `Plane[T]` value cannot alter contribution/freeze/replay behavior.
2. If a feature attempts to contribute through a `Plane[T]` that has no canonical generated binding, then the SDK shall reject the contribution before mutating the `ContributionSet` with the stable errors.Is-compatible sentinel `ErrUngeneratedPlane`.
3. If an ungenerated plane is read from a valid `FrozenPlaneSet`, then the SDK shall return the zero value and shall not search map-backed production storage.
4. The production `ContributionSet` and `FrozenPlaneSet` shall not retain map-backed arbitrary-plane values, arbitrary-plane identities, reflection-based cloning, or type-assertion replay as an alternate production composition mechanism.
5. When contribution freeze, request freeze/materialization, `FeatureBundle`/`FrozenPlaneSet` validation, ordinary replay, or candidate replay occurs, the system shall use only generated typed plane storage and canonical generated policy metadata, shall preserve fail-before-mutate where mutation is possible, and shall not invoke an arbitrary-plane map/reflection fallback.
6. The `FeatureBundle` schema version and existing standard plane IDs shall remain unchanged by this compatibility decision.
7. Where tests need disposable plane declarations to exercise combination/source/identity behavior, they shall use isolated generated test bindings/fixtures; separate tests shall retain explicit rejection coverage for truly unbound planes so test scaffolding does not create a hidden production dynamic-plane contract.
8. Documentation shall state that v1 feature plugins may implement and contribute to published standard planes, while adding a new plane is a Go-LIP SDK/runtime change requiring a manifest declaration and regenerated adapters.

### Requirement 2: Make Tool-Call Repair a Self-Contained Feature

**Objective:** As a maintainer, I want malformed tool-call repair owned by its feature package, so that enabling, disabling, changing, or removing this UX enhancement does not require a concrete implementation under core.

#### Acceptance Criteria

1. When the standard `toolcallrepair` feature is enabled with an existing valid YAML configuration, it shall produce the same `toolcall.Finalizer` behavior, finalization byte cap, reason classifications, schema limits, ordering, and fail/pass/rewrite outcomes as before migration.
2. When the feature is disabled or absent, the runtime shall retain the existing generic no-finalizer behavior and shall not construct the repair engine.
3. The production repository shall contain no `internal/core/toolcallrepair` implementation package after migration.
4. The `toolcallrepair` feature implementation tree shall depend only on public canonical/SDK contracts, standard library, and feature-local code; it shall not import `internal/core/runtime`, routing, frontends, backends, or `runtimebundle`.
5. `internal/standardplugins` shall obtain the complete tool-call-repair `FeatureBundle` from the feature-owned implementation rather than constructing a core repair object itself.
6. Existing tool-call-repair fixtures, fuzz tests, schema validation tests, finalizer integration tests, and default-equality contracts shall remain green after relocation or shall be moved without weakening their assertions.

### Requirement 3: Move Secret-Guard Matching and Source Policy Out of Core

**Objective:** As a maintainer, I want the concrete secrets-guard catalog, matcher, and source-selection implementation owned by the feature, so that optional credential-scanning policy is not part of the proxy kernel.

#### Acceptance Criteria

1. When secrets-guard is enabled in single-user mode, the migrated feature-owned implementation shall preserve current catalog construction, sparse proxy-credential discovery, include/exclude rules, known-prefix handling, minimum-secret length, matcher behavior, and bounded inventory metadata.
2. When secrets-guard is enabled in multi-user mode, the migrated implementation shall preserve the invariant that process environment lookup/snapshot is never invoked and request-credential matching remains the only secret source.
3. When secrets-guard is disabled, the system shall perform no feature-owned environment catalog read and shall preserve zero-stage-work/no-behavior-change semantics.
4. The production repository shall contain no `internal/core/secretguard` concrete feature implementation package after migration.
5. The secret-guard feature tree shall not import `internal/core/*`, `internal/infra/runtimebundle`, frontends, backends, or `internal/stdhttp`; access-mode values needed for source selection shall cross the composition boundary as feature-neutral values rather than as a core package type.
6. Runtime assembly shall preserve audit failure policy, decision observer chaining, redaction behavior, matcher-resolver injection, catalog counts/categories, action/access-mode diagnostics, and startup failure classification.
7. Secret values, environment values, matcher contents, or irreversible fingerprints shall not be added to logs, diagnostics labels, generated plane metadata, or public configuration as part of the relocation.

### Requirement 4: Remove the Concrete Compaction Detector from Core

**Objective:** As a core maintainer, I want runtime to depend on a narrow detector capability rather than one concrete coding-agent heuristic implementation, so that compaction recognition remains optional infrastructure instead of kernel policy.

#### Acceptance Criteria

1. When a detector is configured, the runtime shall preserve request-open detection, pure response preview, released-response detection, transaction correlation, panic isolation, observer dispatch, and preserver callback ordering exactly as before relocation.
2. When no detector is configured, the runtime shall preserve the current safe no-op behavior.
3. The core runtime shall depend on a consumer-owned narrow detector interface using canonical/SDK values and shall not import the concrete detector implementation package.
4. The concrete detector implementation shall live outside `internal/core`, retain its existing bounded process-local state, locking, TTL/cap behavior, and no-background-worker property, and remain safe for one process-owned instance shared by immutable generations.
5. `ProcessServices` shall remain the lifetime owner of the detector reference; relocation shall not add a second closer, worker owner, mutable global, generation-local copy, or live rebind of a published generation.
6. The production repository shall contain no `internal/core/compactiondetect` concrete implementation package after migration.
7. Existing detector unit/fuzz/transaction tests and runtime wiring/order tests shall retain equivalent coverage after the package move and port introduction.

### Requirement 5: Keep Generic Runtime Composition Free of Concrete Feature Implementations

**Objective:** As a runtime maintainer, I want `runtimebundle` to assemble generic process/generation contracts without implementing named feature policy, so that sophisticated features cannot regrow the composition root with special-case business logic.

#### Acceptance Criteria

1. When a feature requires process- or generation-bound capabilities that cannot be constructed by its configuration-only registry factory, its concrete decoding/prerequisite/binding logic shall live behind a dedicated explicit typed composition adapter outside `internal/infra/runtimebundle`.
2. The final production `internal/infra/runtimebundle` tree shall contain zero direct imports of `internal/plugins/features/*`.
3. Existing reasoning-preservation semantic-compression composition shall preserve its BackgroundAux/BackgroundPoller prerequisite checks, trusted egress policy lookup, secret matcher/sanitizer requirement, companion policy, attempt-transform binding, stream-observer binding, and candidate rollback behavior after its concrete logic moves behind its dedicated adapter.
4. Existing secret-guard runtime composition shall preserve Requirement 3 semantics after its concrete feature assembly moves behind its dedicated adapter.
5. Existing compaction-continuity composition may continue through its dedicated `internal/infra/compactioncompose` adapter and shall not be redesigned merely for naming symmetry.
6. Runtimebundle shall pass only explicit typed process/generation capabilities into these adapters; it shall not expose `ProcessServices`, `BuildOptions`, arbitrary maps of services, `any` dependency bags, service locators, reflection registries, or feature-name dispatch callbacks to them.
7. This spec shall not introduce a public generic generation-binding SDK. Any broader unification of dedicated feature composition adapters is deferred to the full-closure follow-up unless implementation evidence proves an already-existing public contract can express it without new framework machinery.

### Requirement 6: Preserve Immutable Generation and Request Semantics

**Objective:** As an operator, I want simplification to be behavior-preserving across startup and reload, so that architectural cleanup cannot change live request semantics.

#### Acceptance Criteria

1. When a candidate generation is compiled successfully, all migrated feature contributions and bound capabilities shall be frozen before publication and shall not be live-rebound afterward.
2. If migrated feature construction, prerequisite validation, contribution, or binding fails, then candidate publication shall fail closed and the previously published generation shall continue serving unchanged.
3. While a request is pinned to a generation, its extension planes, detector capability, feature-bound services, and ordering shall remain those of the pinned generation for the request lifetime.
4. When a migrated optional feature is removed or disabled on reload, new requests shall observe the corresponding generic no-feature behavior without mutating requests already pinned to the previous generation.
5. The migration shall not alter routing authority, failover candidate semantics, retry-after-output prohibition, B2BUA identity, billing authority, secure-session ownership, frontend/backend protocol behavior, or canonical `lipapi` data contracts.

### Requirement 7: Ratchet Core and Composition Against Feature Regrowth

**Objective:** As a project owner, I want permanent architecture gates for the simplified ownership model, so that the OSS core does not immediately accumulate concrete UX policy again.

#### Acceptance Criteria

1. The architecture gate shall reject any production import from `internal/core` to `internal/plugins/features/*`.
2. The architecture gate shall reject any production import from `internal/infra/runtimebundle` to `internal/plugins/features/*`.
3. The architecture gate shall fail if the retired `internal/core/toolcallrepair`, `internal/core/secretguard`, or `internal/core/compactiondetect` production package is reintroduced.
4. The architecture gate shall fail if map/reflection-backed ungenerated feature-plane contribution/replay is reintroduced as a production path.
5. After the three concrete core implementations are removed, the `internal/core` non-test line budget shall be reset downward to the measured final tree plus only the repository-standard narrow headroom; the implementation shall not preserve their deleted lines as reusable budget.
6. When an existing-plane disposable feature is applied as a change-surface probe, its production integration shall require no `internal/core` or `internal/infra/runtimebundle` edit and shall be removed after the proof.
7. Architecture rules shall allow dedicated `internal/infra/*compose` adapters to import their concrete feature implementation while preventing generic core/runtime composition from doing so.
8. No architecture exemption introduced by this work shall be feature-name based inside core or runtimebundle.

### Requirement 8: Prove a Truthful OSS Feature-Authoring Contract

**Objective:** As an external OSS contributor, I want executable proof of the supported SDK path, so that documentation does not advertise an extension model that only works for in-repository code.

#### Acceptance Criteria

1. A separate-module or equivalent external-style compile fixture shall build a feature using only exported `pkg/lipsdk`/`pkg/lipapi` contracts and standard-library dependencies, with no imports from repository `internal` packages.
2. The fixture shall contribute to at least one ordered standard plane and shall prove freeze/bundle/replay behavior through the public contract.
3. The fixture shall include a compile/runtime contract test showing that an ungenerated plane contribution fails with `ErrUngeneratedPlane` rather than being silently accepted.
4. The feature authoring documentation shall describe `ContributionSet` → `Contribute` → `Freeze` → `BundleFromPlanes`, the closed standard-plane catalog, the one standard-distribution registration boundary, and the rule that concrete feature policy does not belong in core/runtimebundle.
5. Stale documentation describing named `FeatureBundle` plane fields or arbitrary additional bundle fields shall be corrected or removed.

### Requirement 9: Preserve Security and Request-Path Performance

**Objective:** As an operator, I want the ownership cleanup to avoid security or hot-path regressions, so that simplification does not trade maintainability for production risk.

#### Acceptance Criteria

1. Request execution through migrated plane reads shall introduce no new reflection, map/key-search loop, mutable global lookup, or synchronization lock relative to the corrected extension-plane baseline.
2. The existing extension-plane seam benchmark suite shall show no `allocs/op` regression attributable to this work; fixed-cost `ns/op`/`B/op` deltas shall be recorded using a repeated same-host/same-toolchain comparison without claiming #394 performance certification.
3. Secret-guard security and redaction tests shall pass after relocation, including multi-user no-environment-read and audit/redaction guarantees.
4. Compaction detector concurrency tests shall pass under the Linux race detector on its final package location and runtime integration scope.
5. The work shall introduce no new provider SDK dependency in core, SDK, feature-generic composition, or canonical packages.

### Requirement 10: Bound Pre-OSS Completion and Hand Off Full Closure Explicitly

**Objective:** As the OSS release owner, I want this work to stop at a deliberate release-safe boundary, so that it cannot expand into a multi-week cleanup while remaining architectural debt is still owned and visible.

#### Acceptance Criteria

1. The implementation shall not relocate an additional `internal/core` subsystem solely because it appears feature-adjacent unless that subsystem is one of the three named migrations in Requirements 2–4 or a move is strictly required to complete them without violating another requirement.
2. The implementation shall not create a generic Cordis runtime, DI container, effect registry, reactive component graph, universal lifecycle abstraction, or service locator.
3. Before completion, maintainers shall record a post-migration ownership inventory identifying remaining mixed/feature-adjacent core and composition surfaces for the second full-closure SDD, including at minimum compaction continuity coordination, conversation-view optional policy, interleaved-thinking/state, terminal-decision policy, and any remaining dedicated feature composition adapters.
4. The second full-closure SDD shall be explicitly identified as follow-up work and shall not block this spec once all pre-OSS acceptance criteria pass.
5. When all requirements are implemented, the repository shall pass generated-plane checks, focused migrated-feature tests, architecture gates, the external-module feature SDK fixture, the exact Linux race gate for changed concurrent scopes, `make quality-checks`, `make test`, `make qa`, deterministic `make arch-report`, runtime smoke, independent review, and merged-main verification before this spec is archived.
