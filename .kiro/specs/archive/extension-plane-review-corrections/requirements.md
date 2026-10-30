# Requirements Document

## Introduction

The completed extension-plane declaration consolidation established a canonical 25-plane manifest, generated typed contribution and frozen storage, immutable request snapshots, declaration-derived diagnostics, and a minimal `FeatureBundle`. Independent review subsequently found a nil-receiver regression, a schema-negotiation hole at generated merge boundaries, and a hand-written hook projection hidden behind an architecture exemption. This corrective feature closes those verification gaps without redesigning the sound consolidation architecture.

## Boundary Context

- **In scope**: Restore nil-safe generation access, reject invalid feature-bundle schema versions at every merge entry point, derive hook-bus plane projection from the canonical declaration system, remove obsolete lifecycle-only merge compatibility, and refresh consolidation verification evidence.
- **Out of scope**: Redesigning the 25-plane model, supporting arbitrary dynamically declared SDK planes, optimizing high-concurrency latency, moving core packages, or changing provider/plugin semantics.
- **Adjacent expectations**: Dynamic map/reflection fallback hardening is tracked as separate SDK work. Issue #394 retains performance diagnosis, load, latency, and HOLD certification; this feature preserves and reports allocation and fixed-cost benchmark evidence only.
- **Boundary ownership**: Public feature contracts remain in the SDK; feature composition and generation publication remain at internal composition boundaries; core remains provider-neutral.

## Requirements

### Requirement 1: Nil-Safe Generation Access

**Objective:** As a runtime integrator, I want generation accessors to retain their documented zero behavior for absent generations, so that no-provider and shutdown paths cannot panic.

#### Acceptance Criteria

1. When terminal-decision access is requested from an absent generation, the runtime shall return no provider without panicking.
2. When terminal-decision access is requested from a published generation, the runtime shall return the provider frozen into that generation.
3. While a request is pinned to a generation, the runtime shall preserve the same terminal-decision provider for the request lifetime.
4. Where other generation pointer accessors define zero behavior for an absent generation, the runtime shall preserve that behavior after the correction.
5. If no terminal-decision provider is contributed, the runtime shall preserve generic no-provider behavior.

### Requirement 2: Fail-Closed Feature Bundle Schema Negotiation

**Objective:** As an SDK consumer, I want every feature-bundle assembly path to enforce the declared schema contract, so that malformed or unsupported bundles cannot enter a runtime generation.

#### Acceptance Criteria

1. When a non-empty feature-plane bundle declares the supported schema version, the feature composition system shall accept it subject to normal plane validation and conflict rules.
2. When a lifecycle-only bundle declares the supported schema version, the feature composition system shall preserve its lifecycles in registration order.
3. When an empty bundle declares either the compatibility zero version or the supported schema version, the feature composition system shall accept it.
4. If a non-empty feature-plane bundle declares the compatibility zero version, the feature composition system shall reject it before publishing any contribution.
5. If a lifecycle-only bundle declares the compatibility zero version, the feature composition system shall reject it before publishing any lifecycle.
6. If any bundle declares an unsupported schema version, the feature composition system shall reject it before publishing planes or lifecycles.
7. If a registry implementation returns a malformed bundle, every registry-based merge path shall reject it without assuming that the registry already validated it.
8. If bundle validation or replay fails, the feature composition system shall leave the destination contribution state unchanged and identify the responsible contributor in the returned error.

### Requirement 3: Declaration-Derived Hook Projection

**Objective:** As a feature-plane maintainer, I want hook-bus projection to follow the canonical declaration workflow, so that hook-plane changes do not require a hidden hand-written mirror.

#### Acceptance Criteria

1. When a frozen feature plane set is projected into hook-bus configuration, the system shall preserve submit hooks, request-part hooks, response-part hooks, tool reactors, nil-versus-empty semantics, and registration order.
2. When hook-bus error policy is supplied by the host, the system shall preserve it independently of feature-plane contributions.
3. When the canonical hook-plane declaration changes, regeneration shall produce the corresponding hook projection without requiring a second hand-authored per-plane projection.
4. If a hand-authored production projection enumerates hook planes after the correction, the architecture gate shall report a forbidden mirror.
5. The architecture report shall count hook projections without excluding named production functions through a hook-specific exemption.
6. The generated-output check shall fail if the checked-in hook projection differs from the canonical declarations.

### Requirement 4: Lifecycle Side-Channel Consolidation

**Objective:** As a runtime maintainer, I want one truthful generated feature surface with a separate lifecycle side channel, so that legacy helper names cannot imply validation or plane-merge behavior they no longer provide.

#### Acceptance Criteria

1. When feature bundles are merged, the composition system shall expose plane state through the generated frozen surface and lifecycles through the separate ordered lifecycle side channel.
2. When multiple valid bundles contribute lifecycles, the composition system shall preserve registration order and nil-versus-empty behavior.
3. If a bundle is invalid or conflicts with an earlier contribution, the composition system shall not publish its lifecycle or a partial generated candidate.
4. The production composition surface shall not expose a lifecycle-only helper under a name or contract that claims to merge or validate feature planes.
5. The correction shall preserve conflict, rollback, lifecycle ordering, and nil-versus-empty test coverage when obsolete compatibility helpers are removed.

### Requirement 5: Architecture and SDK Boundary Preservation

**Objective:** As an SDK and architecture owner, I want the correction to remain narrowly scoped, so that it strengthens the consolidation contract without creating another extension mechanism or core dependency.

#### Acceptance Criteria

1. The feature-plane system shall retain the canonical manifest as the declaration source for standard production planes.
2. The feature-plane system shall retain generated typed storage and generated replay for all standard production planes.
3. The runtime shall not add provider-specific branches, reflection-based registries, dynamic loading, or request-path map searches as part of this correction.
4. The core shall remain independent of concrete feature plugins and provider SDKs.
5. The correction shall not claim that arbitrary dynamically declared SDK planes are fully supported or fully removed.
6. The correction shall record dynamic-plane map/reflection fallback cleanup as separate SDK-hardening work rather than silently changing that public contract.

### Requirement 6: Performance and Verification Evidence

**Objective:** As a release reviewer, I want fresh evidence against the corrective merge, so that VERIFIED status reflects the fixed implementation rather than the superseded closeout.

#### Acceptance Criteria

1. When the corrective implementation is complete, the project shall pass focused feature, generation, hook, and architecture tests.
2. When generated output is checked, the project shall report that the committed feature-plane output matches the canonical declarations.
3. When the architecture report is generated repeatedly from the same tree, the project shall produce deterministic results with zero forbidden mirrors and no hook-projection exemption.
4. When the consolidation benchmark suite is rerun, the project shall record allocations, bytes, and fixed-cost timing results against the established baseline.
5. If allocation or request-path structural guarantees regress, the correction shall not be certified complete.
6. The correction shall not describe the result as full performance neutrality or #394 certification solely because allocation targets pass.
7. When Linux race verification runs against the final corrective commit, the extension snapshot and runtime composition packages shall pass without a detected race.
8. Before VERIFIED status is restored, the project shall pass repository quality, test, QA, runtime smoke, independent review, and merged-main verification gates.

### Requirement 7: Corrective Delivery and Adjacent Tracking

**Objective:** As a project maintainer, I want corrective status and follow-up ownership to be explicit, so that completion claims remain auditable.

#### Acceptance Criteria

1. While any must-fix corrective requirement remains incomplete, the project shall not claim that the independent review is discharged.
2. When corrective evidence supersedes the original closeout, the project shall retain the historical evidence and identify the new certified baseline.
3. When dynamic-plane hardening is deferred, the project shall link a dedicated follow-up that states the compatibility decision still required.
4. When fixed-cost benchmark evidence is refreshed, the project shall communicate the relevant deltas to #394 without transferring #394 certification into this feature.
5. When all corrective requirements and merged-main gates pass, the project shall archive this corrective specification and may restore VERIFIED status for the consolidation outcome.
