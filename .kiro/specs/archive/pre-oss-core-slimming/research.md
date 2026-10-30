# Research & Design Decisions

## Summary

- **Feature**: `pre-oss-core-slimming`
- **Discovery Scope**: Complex brownfield architecture integration / release-bound refactor
- **Reviewed baseline**: `main` at `90379589551edd48a32a2fa4b18f43139771cf7f` after the extension-plane review corrections were completed and archived.
- **Predecessors**:
  - `.kiro/specs/archive/extension-plane-declaration-consolidation/`
  - `.kiro/specs/archive/extension-plane-review-corrections/`
  - issue #554 (`sdk(feature): Decide support contract for dynamically declared planes`)
- **Key Findings**:
  - The declaration-consolidation work solved the expensive horizontal mirror problem and explicitly deferred physical kernel-vs-stage/feature relocation. Its accepted ROI probe proved that an existing-plane feature can be integrated without core/runtime edits.
  - The corrected feature SDK still contains exported map/reflection fallback behavior for ungenerated `Plane[T]` values even though generated standard planes are the only production-complete freeze/replay path. This is a misleading OSS compatibility surface, not a useful extension capability.
  - A generated-storage eligibility check alone is insufficient because `Plane[T]` also exposes mutable policy fields (`Rules`, `NilPolicy`, validators, combiner, identity, conflict policy). A same-ID copy of a canonical `PlaneX` can currently retain the unexported generated storage closures while changing those exported fields. Closing the catalog therefore requires canonical **generated policy metadata** to be authoritative, not merely an ID/binding check.
  - Several SDK characterization tests use raw local planes specifically to exercise combiner/source-rule behavior. Once unbound production planes are rejected, those tests must use isolated generated test bindings or they will stop testing their intended failure paths.
  - `toolcallrepair` is a direct ownership violation: configuration is feature-owned, but `internal/standardplugins` imports `internal/core/toolcallrepair` to construct the actual finalizer.
  - Secret guard is split across a feature package and `internal/core/secretguard`; the latter contains the concrete catalog, matcher, environment/source policy, and Aho-Corasick implementation for one optional UX/security feature.
  - `internal/core/compactiondetect` is a concrete coding-agent heuristic detector, while core runtime imports its concrete type. Its state is process-owned but its algorithm is not a universal proxy invariant.
  - `runtimebundle` still directly knows concrete feature implementations for reasoning compression and secret guard. Compaction continuity already demonstrates the safer pattern: feature-specific typed composition is placed in a dedicated `internal/infra/*compose` adapter and generic runtimebundle delegates to it.
  - A public/generic generation-time dependency injection framework is **not required** to reach a defensible OSS boundary. Introducing one now would add a new public abstraction and service-discovery semantics under release pressure.
  - The current `internal/core` architecture budget is 95,095 non-test lines and has historically been ratcheted upward as feature work landed. This spec needs a downward ratchet after concrete feature code is removed, but a wholesale 95k-line decomposition would exceed the release budget and belongs in the second full-closure spec.

## Brownfield Authority Map

The CORDIS-v4 discipline used by this repository requires existing authorities to be identified before moving ownership. This spec preserves them rather than introducing a new component runtime.

| Concern / resource | Current authority | Current construction | Required outcome |
| --- | --- | --- | --- |
| Immutable feature planes | `pkg/lipsdk/feature` generated storage | feature factory / generated contribution set | Keep authority; close ungenerated fallback and make generated plane policy authoritative |
| Feature registration | `internal/standardplugins` + `internal/pluginreg` | explicit static registration | Keep explicit; no dynamic registry |
| Generation publication/pinning | `GenerationRuntime` / `runtimehost.Manager` | `runtimebundle` | Unchanged |
| Process services | `ProcessServices` | `runtimebundle` | Unchanged |
| Generation resources | `ResourceLedger` | `runtimebundle` | Unchanged |
| Tool-call repair algorithm | incorrectly `internal/core/toolcallrepair` | standardplugins creates core finalizer | Move under feature tree |
| Secret matching/source implementation | incorrectly `internal/core/secretguard` | `runtimebundle` feature-specific assembly | Move under feature tree; adapter owns assembly |
| Compaction detector object | `ProcessServices` owns reference, concrete implementation in core | `adoptBackgroundAuxAndDetector` | Keep process ownership; relocate implementation behind core consumer port |
| Reasoning-compression feature binding | generation composition, implemented directly in `runtimebundle` | special named binder | Move concrete logic to dedicated typed infra adapter |
| Compaction-continuity feature binding | process/generation composition via `internal/infra/compactioncompose` | dedicated adapter | Keep as reference pattern |

No resource in this spec requires a new lifetime owner. Physical package relocation must not change process/generation/request lifetime authority.

## Effect Inventory

| Effect | Classification | Owner before/after | Notes |
| --- | --- | --- | --- |
| Building/freeze of feature planes | generation construction | existing candidate/generation composition | No request-time registration; generated policy remains canonical |
| Tool-call repair | request/stream feature callback | frozen feature finalizer | Deterministic in-process policy; no new I/O |
| Secret catalog snapshot | process/startup configuration effect | composition path | Must preserve disabled/multi-user no-environment-read semantics |
| Secret matching | request feature callback | frozen feature services | No new persistent owner |
| Compaction detector state | process-owned bounded reversible state | `ProcessServices` | Concrete package moves; no additional Close/goroutine |
| Background auxiliary scheduler | process-owned worker | existing `ProcessServices` | Not redesigned by this spec |
| Reasoning-compression binder | candidate-generation composition | existing candidate compile | Must remain fail-before-publication |
| Provider calls / client output | irreversible external effects | existing runtime | Completely out of scope; retry/commit semantics unchanged |

## Research Log

### Corrected extension-plane substrate

- **Context**: This work was originally expected to start after the extension-plane consolidation settled.
- **Sources Consulted**: archived consolidation requirements/research/design/tasks/closeout evidence; archived review-corrections requirements/research/design/tasks/evidence; current `pkg/lipsdk/feature` implementation.
- **Findings**:
  - The standard catalog is now one manifest with generated typed contribution/frozen/request/hook projections.
  - The old named bundle mirror and lifecycle-only legacy surface are gone after corrective closeout.
  - `FeatureBundle` is `SchemaVersion + FrozenPlaneSet + Lifecycles` and validates fail-closed.
  - The original spec explicitly called kernel-vs-stages decomposition a required follow-up rather than part of declaration consolidation.
- **Implications**: This spec must use the corrected generated substrate as fixed infrastructure. It must not reopen the plane redesign or create a second plane registry.

### Gap analysis: dynamic / ungenerated plane API

- **Context**: issue #554 asks whether ungenerated planes are rejected or fully supported.
- **Sources Consulted**: issue #554; `pkg/lipsdk/feature/contributions.go`, `frozen.go`, `plane.go`, `bundle.go`, `errors.go`, `plane_generated.go`, `export_test.go`, SDK docs.
- **Findings**:
  - `ContributionSet` still contains `map[string]any` values/identities alongside generated storage.
  - `ContributeSource` enters the map path when `p.generated.contribute` is absent.
  - `FrozenPlaneSet.Get`, cloning, candidate replay, ordinary replay, and validation retain map/type-assertion/reflection fallback paths.
  - Request freeze/materialization and `FeatureBundle.Validate` are explicit compatibility surfaces in #554 and must be covered by the selected contract, not inferred from contribution tests.
  - Comments call the dynamic path test-only, but exported APIs permit external callers to exercise it.
  - Production request snapshots and generated all-plane replay are complete only for the canonical generated manifest.
  - `Plane[T]` exposes contribution-policy callbacks/metadata. The current generated path still reads `p.Rules`, `p.NilPolicy`, `p.IsNil`, `p.Validate`, `p.Identity`, and conflict policy before generated storage. Therefore an ID-only generated binding marker would leave a same-ID copied plane able to redefine standard-plane semantics.
  - Existing raw-plane tests such as `TestContribute_FailBeforeMutate_TableDriven` and `TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn` need test-only generated bindings after the production contract closes.
- **Options**:
  1. Fully support dynamic planes across every freeze/replay/request/diagnostic projection — large public framework expansion.
  2. Reject ungenerated planes, make generated policy/storage authoritative for standard planes, and remove map/reflection fallback — small truthful v1 contract.
- **Decision**: Option 2. The v1 OSS extension catalog is closed. Adding a new plane is a platform change; implementing an existing plane remains ordinary plugin work. The stable public sentinel is `ErrUngeneratedPlane`.
- **Requirements repair caused by gap analysis**: Initial planning considered leaving #554 to the later full-closure spec. Brownfield inspection showed the current exported fallback can mislead OSS authors immediately, so closed-manifest hardening was promoted into pre-OSS Requirement 1. Review then exposed two additional completeness defects: request-freeze/bundle-validation were not explicitly enumerated, and canonical generated policy had to be authoritative against same-ID descriptor copies. Both are now requirements/design obligations.
- **Issue lifecycle**: this SDD defines #554's intended resolution, but #554 remains open until production implementation and verification complete; merging the spec alone does not close it.

### Tool-call repair ownership

- **Context**: Find the simplest proof that concrete UX implementation can leave core now that the planes exist.
- **Sources Consulted**: `internal/core/toolcallrepair/*`, `internal/plugins/features/toolcallrepair/*`, `internal/standardplugins/features_install.go`, `pkg/lipsdk/toolcall`.
- **Findings**:
  - The repair engine uses canonical `lipapi`, `lipsdk/toolcall`, standard library, and feature policy; it does not require routing/executor internals.
  - Feature YAML/config already lives under the feature package.
  - The standard factory currently imports core solely to instantiate the implementation, then immediately contributes it to an existing feature plane.
- **Implications**: This is a mechanical ownership migration with high confidence. The feature root should expose one complete bundle constructor; standardplugins should not translate config into a core implementation.

### Secret-guard ownership and security boundary

- **Context**: Secret guard is optional feature behavior but some implementation was intentionally kept in core to prevent feature code from reading process environment directly.
- **Sources Consulted**: `internal/core/secretguard/*`, `internal/plugins/features/secretguard/*`, feature import-boundary test, `runtimebundle/secret_guard_runtime.go`, SDK `secretguard` contracts.
- **Findings**:
  - `internal/core/secretguard` contains feature-specific matcher/catalog/source code, including Aho-Corasick and environment inventory.
  - The feature root correctly forbids imports of core/runtime/frontends/backends.
  - The actual security requirement is not "the code must live in core"; it is that environment access is injected at composition, multi-user never reads it, disabled never reads it, and feature callbacks only receive opaque matcher services.
  - `core/accessmode.Mode` currently leaks into the concrete source package and would prevent a direct move under the feature tree.
- **Decision**: Move concrete source/matcher/catalog logic under the secret-guard feature tree, replace the `core/accessmode` dependency with a feature-local closed mode input, and keep translation from effective core access mode in a dedicated composition adapter. Do not give request handlers an environment reader.
- **Implications**: Security invariants remain architectural; package placement no longer falsely makes feature algorithm "core".

### Compaction detector ownership

- **Context**: `internal/core/compactiondetect` is one of the largest obvious feature-specific algorithms remaining in core.
- **Sources Consulted**: detector package, `runtime/executor_compaction.go`, `runtime/executor_config.go`, `runtimebundle/background_aux_lifecycle.go`.
- **Findings**:
  - The detector is a concrete coding-agent compaction heuristic/rule engine with bounded process-local maps and a mutex; it has no background worker or external I/O.
  - Runtime uses only three operations: request-open commit, response preview, response-release commit; outputs are public SDK `compaction.Event`/`ResponsePreview` values.
  - Runtime currently imports concrete detector metadata and pointer types, creating a policy dependency.
  - `ProcessServices` constructs one detector and shares it across generations; that lifetime is sound and should remain unchanged.
- **Decision**: Define the smallest consumer-owned detector port in core runtime and express correlation using existing `pkg/lipsdk/compaction.PreservationMeta` fields. Move the concrete implementation outside core and inject it from runtimebundle. Do not create a public detector plugin type or new lifecycle interface.
- **Requirements repair caused by design discovery**: Initial planning considered moving the detector directly into a feature package. That would either make core import the feature or require a new public detector SDK. The corrected design instead uses a private core consumer port and an infra concrete implementation, preserving dependency direction and release scope.

### Runtimebundle concrete feature knowledge

- **Context**: A slim core is insufficient if the generic composition root becomes the next feature switchboard.
- **Sources Consulted**: `runtimebundle/reasoning_preservation_compression.go`, `secret_guard_runtime.go`, `compaction_continuity_generation.go`, `internal/infra/compactioncompose`.
- **Findings**:
  - Reasoning compression directly scans feature registrations, decodes concrete feature config, resolves feature-specific capabilities, constructs a feature bundle, extracts two planes, and applies binders inside runtimebundle.
  - Secret guard likewise directly imports and composes the concrete feature.
  - Compaction continuity already hides its concrete binding in a dedicated adapter and leaves runtimebundle with a narrow call.
- **Alternatives**:
  1. Create a public generic generation binder / dependency registry.
  2. Add a private generic `map[string]any` service locator.
  3. Move each demonstrated exceptional composition into a dedicated typed infra adapter and defer broader unification.
- **Decision**: Option 3. It is smaller, explicit, compatible with CORDIS-v4 guidance, and does not freeze a speculative OSS ABI.
- **Requirements repair caused by design validation**: A generic generation-time feature composition seam was originally considered release-critical. Brownfield validation found only a few special cases and an already-working explicit adapter pattern, so the public/generic seam was removed from pre-OSS scope and explicitly deferred to full closure.

### Core budget and follow-up boundary

- **Context**: User budget cannot support a multi-week repo-wide decomposition before OSS.
- **Sources Consulted**: `internal/archtest/budgets.go`, steering structure, package inventory, preceding architecture specs.
- **Findings**:
  - The core line budget currently permits 95,095 non-test lines and carries a long history of upward bumps for successive features.
  - Many remaining packages are mixed: some contain legitimate proxy invariants, some optional policy, some domain mechanisms used by multiple features.
  - Correctly splitting conversation view, compaction continuity coordination, interleaved-thinking/state, terminal-decision policy, and other historical areas requires independent ownership analysis and is not prerequisite to making the OSS extension contract truthful.
- **Decision**: Pre-OSS moves only the three high-confidence concrete implementations and ratchets them out. The complete census/decomposition is a required second spec.

### Verification evidence policy

- **Context**: This SDD must prove request-path structural neutrality without duplicating #394's performance program, and must leave a durable handoff to the full-closure SDD.
- **Sources Consulted**: archived extension-plane correction verification evidence, current Makefile/quality scripts, existing external-module fixtures, #394 boundary.
- **Findings**:
  - The prior extension-plane benchmark evidence used a single `-count=1` local run. That is useful for allocation shape but too noisy for a meaningful timing comparison.
  - `make quality-checks` and `make qa` do not subsume the separate Linux race gate or a new nested external module unless explicitly wired.
  - Existing external modules use `require root v0.0.0` plus `replace root => ../..`, allowing `GOWORK=off` to validate the checkout rather than a published version.
- **Decision**:
  - Task 1.1 and 8.2 use the same benchmark selector/environment with `-count=10` and preserve raw output. `allocs/op` is a blocking exact structural gate; `B/op` is also blocking when its median increases for a benchmark with otherwise unchanged semantics. A >10% median `ns/op` increase is an investigation trigger requiring a second 10-sample batch on the same quiet host, not an automatic #394 release failure. Timing remains evidence, not a cross-machine latency budget.
  - The external feature fixture has a fixed path `testdata/external_feature_sdk` and the established local-module replace pattern.
  - Task 8.3 writes `.kiro/specs/pre-oss-core-slimming/residual-ownership-inventory.md` with a fixed table schema and a small `tools/kiro/speccheck` existence/content contract.
  - Final certification explicitly runs the external fixture and exact Linux race command in addition to aggregate Make targets.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
| --- | --- | --- | --- | --- |
| No change after plane consolidation | Publish with current package ownership | Lowest immediate effort | Exposes misleading dynamic-plane API and preserves feature/core/composition coupling | Rejected |
| Full CORDIS-style/core decomposition before OSS | Relocate all mixed packages and unify composition | Clean theoretical endpoint | Multi-week scope, high regression risk, speculative abstractions | Rejected for pre-OSS; follow-up owns it |
| Generic feature dependency/runtime container | Runtime providers/requirements registry | Could unify special binders | New framework, service lookup, public contract risk; violates explicit construction | Rejected |
| Release-bounded ownership closure | Closed plane catalog + 3 obvious core moves + dedicated explicit compose adapters + ratchets | High ROI, bounded, uses landed planes, no new runtime framework | Leaves known debt for second spec | **Selected** |

## Design Decisions

### Decision: v1 standard plane catalog is closed

- **Context**: Exported generic `Plane[T]` construction currently implies more support than production can deliver.
- **Selected Approach**: `ContributeSource` rejects planes without generated bindings using `ErrUngeneratedPlane`; generated canonical policy/storage is authoritative for bound standard planes; remove map/reflection arbitrary-plane storage/replay from production `ContributionSet`/`FrozenPlaneSet`.
- **Rationale**: Truthful behavior is safer for OSS than half-support. Adding a new plane remains cheap because declaration generation is now consolidated. Canonical generated policy also prevents a copied descriptor from redefining standard-plane behavior.
- **Trade-offs**: Out-of-tree code experimenting with undocumented dynamic planes must migrate to an existing standard plane or upstream a new declaration. Test-only local planes need generated fixture bindings when testing standard contribution semantics.
- **Follow-up**: Fully dynamic planes may be reconsidered only if real external demand justifies the framework cost.

### Decision: feature implementations move; generic execution stages stay core

- **Context**: "Slim core" must not mean moving universal orchestration out of core.
- **Selected Approach**: Move concrete repair/matching/detection algorithms; retain extension stage runners, frozen snapshots, ordering/error isolation, routing/B2BUA/terminal authorities in core.
- **Rationale**: Preserves stable kernel semantics while removing optional product policy.
- **Trade-offs**: Some feature-specific composition remains in infra adapters until full closure.

### Decision: use dedicated typed compose adapters, not a new feature DI framework

- **Context**: Reasoning compression and secret guard need process/generation capabilities after config decode.
- **Selected Approach**: `internal/infra/reasoningcompose` and `internal/infra/secretguardcompose` own concrete binding logic; runtimebundle passes explicit typed inputs and consumes generic outputs.
- **Rationale**: Mirrors the existing compactioncompose pattern and makes runtimebundle feature-implementation agnostic without introducing service discovery.
- **Trade-offs**: Multiple dedicated adapters remain. Full-closure spec will decide whether any common private abstraction is justified by measured duplication.

### Decision: compaction detector port is private and consumer-owned

- **Context**: Core must invoke detection at exact irreversible/release seams but should not own heuristic implementation.
- **Selected Approach**: Runtime defines a narrow unexported/internal interface using canonical `lipapi` inputs and SDK compaction metadata/results; `internal/infra/compactiondetect` implements it; runtimebundle injects one process-owned instance.
- **Rationale**: Dependency inversion without expanding public SDK or changing lifetime ownership.
- **Trade-offs**: Core still knows the abstract concept of compaction observation because it owns the exact stage/release position. The later full spec may reassess whether even this stage belongs elsewhere.

### Decision: downward budget is measured after moves, not pre-guessed

- **Context**: Physical moves add a few interface/adapter lines while deleting large core trees.
- **Selected Approach**: Capture baseline non-test core LOC, perform the named moves, measure final tree, then set the permanent budget to final measured count + existing 25-line standard headroom. Record moved/deleted paths separately.
- **Rationale**: Deterministic, truthful, and resistant to gaming.
- **Trade-offs**: The spec does not promise an arbitrary percentage reduction.

### Decision: full closure is mandatory follow-up but not an OSS blocker

- **Context**: Remaining mixed areas are real but much broader.
- **Selected Approach**: Produce a concrete residual ownership inventory during closeout and seed the second SDD from it. Do not opportunistically expand this implementation.
- **Rationale**: Protects release time/budget while preventing debt from disappearing into chat history.

## CORDIS-v4 Simplification Gate

- **Second owner created?** No. ProcessServices, ResourceLedger, Manager, request/attempt owners remain authoritative.
- **Generic registry/container created?** No; explicitly forbidden.
- **Domain semantics moved into infrastructure?** No. Infra adapters only assemble explicit feature-owned policy; concrete algorithms remain feature-owned or implementation-side.
- **Request-path lookup/locking added?** No. Closed generated planes remove fallback lookup/reflection.
- **More lifecycle concepts than deleted?** No new lifecycle type is required.
- **Existing authority sufficient?** Yes. The work is dependency/ownership relocation on existing generation/process authorities.
- **Measurable ROI?** Zero retired concrete core packages, zero runtimebundle concrete-feature imports, zero dynamic plane fallback, lower core budget, existing-plane feature probe with zero core/runtimebundle edits.

## Failure Schedule Analysis

1. **Ungenerated contribution**: reject with `ErrUngeneratedPlane` before any generated/map state mutation; later valid contribution must behave as if rejection never happened.
2. **Copied/mutated standard-plane descriptor**: changed-ID copy is rejected; same-ID policy mutation cannot alter canonical generated validation/source/nil/identity/combine behavior.
3. **Feature bundle construction failure**: candidate remains unpublished; last-good generation survives.
4. **Secret catalog failure**: fail startup/candidate at the same pre-publication stage; no partial matcher or observer binding escapes.
5. **Reasoning composition prerequisite missing**: same classified failure; no partial attempt-transform/observer rebinding.
6. **Reload removes feature**: old request remains pinned; new generation has no corresponding contribution.
7. **Detector panic**: runtime safe wrapper preserves fail-open observation behavior; no request failure.
8. **Detector/process shutdown**: no new shutdown semantics are introduced; detector has no worker/Close and dies with ProcessServices references.
9. **Linux race schedule**: detector concurrent request/release/state cleanup remains race-free in its new package.

## Risks & Mitigations

- **Hidden source compatibility reliance on dynamic planes** — add an external-style test and document the intentional v1 closed contract; repository is pre-v1 and current fallback is undocumented/incomplete.
- **Copied standard plane mutates exported policy** — all production policy comes from canonical generated metadata; adversarial same-ID mutation tests prevent descriptor copies from becoming a second declaration authority.
- **Raw-plane SDK tests collapse into unsupported-plane tests** — migrate behavior-oriented local planes through a test-only generated binding while retaining explicit raw-plane rejection coverage.
- **Mechanical package moves lose tests or build tags** — move tests first or in the same PR wave; compare test names/fixtures and require package-specific gates before deleting old packages.
- **Secret-guard security regression during access-mode decoupling** — RED tests for zero env access, disabled behavior, catalog categories, audit policy before move.
- **Compaction detector interface accidentally leaks implementation details** — interface uses only existing canonical/SDK values and three runtime-consumed operations.
- **Dedicated compose adapters become a permanent dumping ground** — architecture inventory records them as residual debt and the full-closure spec must re-evaluate them; runtimebundle import ratchet prevents regression meanwhile.
- **Scope creep into every core package** — Requirement 10 forbids extra package relocation unless strictly necessary for the three named moves.
- **Performance regression from abstraction** — detector interface dispatch is one bounded call at existing observation seams; plane fallback removal simplifies rather than expands request access; repeated benchmark/allocation and race gates are mandatory without duplicating #394.

## References

- `.kiro/specs/archive/extension-plane-declaration-consolidation/` — declaration consolidation, ROI probes, explicit kernel-vs-stages deferral.
- `.kiro/specs/archive/extension-plane-review-corrections/` — corrected nil/schema/hook/merge contract and dynamic-plane follow-up ownership.
- GitHub issue #554 — dynamic-plane compatibility decision and explicit freeze/validation/replay acceptance surface.
- `.agents/skills/go-lip-cordis-v4/SKILL.md` — explicit ownership, immutable generations, no DI/service locator, measurable simplification rules.
- `.kiro/steering/structure.md` and `.kiro/steering/tech.md` — package ownership and runtime construction invariants.
- `AGENTS.md` and `.kiro/AGENTS.md` — small-core/plugin boundary and TDD/spec delivery rules.
- `pkg/lipsdk/feature/doc.go` — corrected public feature-plane authoring contract.
