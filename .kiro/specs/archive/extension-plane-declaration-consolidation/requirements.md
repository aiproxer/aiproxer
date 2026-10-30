# Requirements Document

## Introduction

Go-LIP's feature-plugin extension surface currently declares each typed extension plane once but hand-mirrors it across roughly ten layers: the SDK bundle contract, the merged feature surface, the request snapshot, the generation bundle, composition code, standard-distribution registration, executor integration, evidence projection vocabulary, diagnostics inventory, and architecture-test baselines. Measured consequences (August 2026): landing a self-contained feature such as Agent Loop Guard required 132 changed files (+15.2k LOC) although the feature logic itself is ~1,150 LOC; a simpler content-tagging feature required 107 files; the SDK bundle carries 26 extension-plane fields (27 fields including `SchemaVersion`) mirrored by 31 snapshot accessors. This specification consolidates extension-plane declaration so a plane is declared exactly once and all derived views are produced generically, making feature integration additive instead of shotgun-shaped.

The consolidation is a maintainability change: every externally observable behavior of extension composition must be preserved. Its primary "users" are maintainers and contributors who add extension planes and feature plugins, plus operators who depend on unchanged runtime semantics during config reload and plugin enable/disable.

## Boundary Context

- **In scope**: the feature-plugin extension surface end to end — SDK-side plane/bundle contracts, merging of enabled feature contributions, composition projection onto executor extension options, hook-bus configuration, request-snapshot and generation-bundle projection of those contributions, composition-root wiring for feature plugins, standard-distribution feature registration entries, diagnostics inventory of enabled contributions, architecture gates and baselines covering this surface, and migration of all existing planes and feature plugins onto the consolidated mechanism.
- **Out of scope**: changing any plane's runtime semantics (what contributed handlers/policies do at their execution stage); physically relocating stage/policy code out of `internal/core` (owned by a required follow-up core kernel-vs-stages decomposition spec); the frontend×backend/provider extension axis governed by the completed extension-scalability spec; terminal-decision chokepoint and continuation-transaction behavior; high-concurrency diagnosis or optimization owned by issue #394 and the approved `high-concurrency-performance-hardening` spec; billing, auth, secure-session, routing, or connector ABI changes; introducing DI containers, reflection registries, service locators, global mutable registries, `init()` registration, or Go native plugins; breaking YAML configuration formats for existing plugins.
- **Adjacent expectations**: exclusive-slot semantics introduced by the terminal-decision feature-extension spec (one provider, conflict rejection, no-provider fallback) must survive unchanged as one instance of the general exclusive-slot capability; provider-profile/TCK certification flows from the extension-scalability spec are untouched; the standard distribution keeps explicit construction and registration; #394 Phase 1 harness work may proceed independently, but its Phase 2 baseline shall be captured after this consolidation completes or its OBSERVE, DELTA-allocation, and HOLD fixed-cost scenarios shall be refreshed after consolidation.
- **Boundary ownership**: SDK contracts and composition mechanics belong to the plugin SDK and the composition root; stage integration points belong to core; feature policy remains entirely inside feature plugins. Core must never import concrete plugins.
- **Revalidation triggers**: any later change to the SDK bundle schema, request-snapshot or generation-bundle shape, plugin registration contracts (`pkg/lipsdk`, standard-distribution tables), diagnostics inventory structure, or package ownership assumed by the follow-up core decomposition; if this work lands after #394 baseline capture begins, refresh the affected OBSERVE, DELTA-allocation, and HOLD fixed-cost scenarios before the next performance experiment.

## Requirements

### Requirement 1: Preserve Existing Extension Composition Semantics
**Objective:** As an operator, I want extension composition behavior to remain identical after consolidation, so that no runtime or reload behavior regresses.

#### Acceptance Criteria
1. When multiple enabled feature plugins contribute to a plane whose declared rule is concatenation, the composed surface shall concatenate contributions in plugin registration order, identical to pre-consolidation behavior.
2. If two enabled plugins attempt to occupy one exclusive slot, then the composition system shall preserve the pre-consolidation candidate-rejection behavior and operator-visible error semantics defined by Requirement 4.2.
3. When a feature plugin is registered but disabled, it shall contribute nothing to the composed surface.
4. If any contribution fails validation during composition, then the candidate generation shall be rejected before publication and the previously published generation shall continue serving unchanged.
5. While requests execute against a published generation, the composed extension surface shall remain frozen with no live rebinding of any plane; deterministic concurrent reload/request tests shall preserve that invariant under the race detector on supported platforms.

### Requirement 2: Single-Site Declaration of New Extension Planes
**Objective:** As a maintainer adding an extension plane, I want to declare it in one place, so that adding a plane stops requiring mirrored edits across every composition layer.

#### Acceptance Criteria
1. When a contributor adds one new typed extension plane, hand-authored changes outside the plane's own SDK contract package shall be confined to the packages that consume that plane at its execution stage or stages; deterministic generated projections shall be reported separately by the repository change-surface reporter and shall require no manual editing.
2. The system shall not require hand-written per-plane mirroring in any derived layer — merging, composition projection onto executor options, hook-bus configuration, request-snapshot projection, or generation projection — for a new plane.
3. The architecture gate shall fail any change that reintroduces a forbidden per-plane mirror shape enumerated by the design in shared composition or derived-projection layers, while allowing explicitly whitelisted stage-consumer integrations and thin compatibility accessors.
4. The plane-declaration generation/check step shall reject, before normal tests run, a declaration that omits the information needed to compose, freeze, diagnose, and project contributions for that plane.

### Requirement 3: Additive Integration of New Feature Plugins
**Objective:** As a plugin author shipping a new feature, I want integration to touch only my own plugin, so that feature delivery is fast and reviewable.

#### Acceptance Criteria
1. When a contributor adds a feature plugin that uses only existing planes, the production change set shall not modify shared composition, snapshot, or generation-projection code; the feature package and one explicit standard-distribution registration/wiring entry are permitted.
2. If a feature plugin contributes a value that violates a plane's rules, then candidate publication shall fail with an error attributing the offending plugin ID and plane.
3. The Plugin SDK shall enforce contribution typing at compile time without reflection-based registration or runtime type discovery.

### Requirement 4: Plane Multiplicity and Combination Semantics
**Objective:** As a maintainer, I want each plane to declare its multiplicity and source-specific combination rules, so that singular capabilities stay singular and every contribution source composes deterministically by declaration.

#### Acceptance Criteria
1. The declaration of each extension plane shall specify whether it accepts multiple ordered contributions or a single exclusive contribution.
2. When a second contribution targets an occupied exclusive slot, the system shall reject it before candidate publication with an operator-visible error naming both validated provider identities, preserving today's exclusive-conflict error classification and text shape.
3. Where no enabled plugin occupies an exclusive slot, the runtime shall exhibit exactly the established generic no-provider behavior.
4. When multiple enabled contributions target a plane whose declared combination rule is a deterministic reduction rather than concatenation, the composed surface shall equal the result of applying that reduction across all enabled contributions in registration order.
5. Where a plane accepts contributions from more than one composition source, its declaration shall state the combination policy for each source.

### Requirement 5: Compatibility and Staged Migration of Existing Planes and Features
**Objective:** As a maintainer of existing feature plugins, I want a deterministic migration path, so that consolidation lands without breaking any current consumer.

#### Acceptance Criteria
1. After migration, all existing official, reference, and test feature plugins shall exhibit unchanged external behavior.
2. If a public SDK contract consumed by out-of-tree plugin authors must change shape, then the change shall be additive or provide a documented deterministic migration path.
3. Migration shall proceed in bounded stages where each stage keeps the full default test suite green and fits within the repository's source-change gate.
4. When migration completes, zero forbidden per-plane mirror shapes enumerated by the design shall remain in shared composition or derived-projection layers, verifiable by the architecture gate.
5. Each completed migration stage shall leave zero forbidden mirror shapes for the planes it migrated, verified by the architecture gate scoped to that stage.

### Requirement 6: Derived Diagnostics Inventory
**Objective:** As an operator diagnosing a deployment, I want the extension inventory to stay complete and accurate, so that consolidation does not create observability blind spots.

#### Acceptance Criteria
1. When a generation is built, the diagnostics inventory shall report every materialized occupant retained by each plane's frozen combined value, with content equivalent to the pre-consolidation inventory.
2. When a new plane is declared, diagnostics coverage shall extend to it without per-plane hand-written inventory code.

### Requirement 7: Request Hot-Path Neutrality
**Objective:** As a client of the proxy, I want request processing performance to be unaffected, so that maintainability gains do not cost latency.

#### Acceptance Criteria
1. While serving requests, per-contribution dispatch through the consolidated surface shall read only the frozen request snapshot, introduce no new locks or key-search loops, and show equal-or-better `allocs/op` on fixed seam-view benchmarks versus the pre-consolidation baseline; any existing defensive-copy allocations shall remain explicitly characterized for #394 DELTA analysis.

### Requirement 8: Architecture Gates and Baseline Maintenance
**Objective:** As a maintainer, I want the architecture ratchets to encode the new invariants cheaply, so that regression protection does not itself become per-change overhead.

#### Acceptance Criteria
1. The architecture gate shall verify that every extension plane is declared at exactly one hand-authored site and projected only through current deterministic generated output plus explicitly allowed stage-consumer integrations.
2. When this spec's changes affect recorded architecture baselines, regeneration shall be achievable through one documented command producing deterministic output.
3. When migration completes, a disposable representative plane and a disposable feature using only existing planes shall be applied as separate change-surface probes, and the measured paths shall satisfy Requirements 2.1 and 3.1 before the probes are removed.
