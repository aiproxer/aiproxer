# Design Validation Review

## Review Method

The design was validated as a brownfield architecture change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- current structure, API, technology, testing, and routing steering;
- `main` at `95089eb4b74d5cf8d062f238a1121124ce0da878`;
- PR #250 OpenResponses change surface;
- current `pkg/lipapi` item/projector/admission contracts;
- `internal/core/capabilities` and runtime admission;
- `internal/standardplugins` registration/compatible/route/diagnostic paths;
- `internal/testkit/conformance` 5×9 matrix/evidence machinery;
- backend-plugin ABI v1.0–v1.3;
- continuation SDK/core implementations;
- existing connector and architecture-test boundaries;
- all acceptance criteria in `requirements.md`;
- all gaps in `gap-analysis.md`.

The review used three rounds. Any unresolved correctness, scale, boundary, compatibility, or deletion issue returned NO-GO and required requirements/design remediation before the next round.

## Round 1

### Assessment

**Decision: NO-GO**

The first design replaced the matrix with TCKs but remained incomplete as an extension architecture.

### Critical Issue 1: TCKs solved test multiplication but not provider implementation multiplication

**Concern:** Hundreds of compatible providers could still become hundreds of Go backend packages, factory entries, diagnostics branches, and connector identities.  
**Impact:** CI would scale better while maintenance/code size would still grow unnecessarily.  
**Resolution:** Add the provider-family/provider-profile architecture and explicit profile→family→connector graduation rule. Project-shipped compatible providers become typed profile data by default.  
**Traceability:** Requirements 2, 6, 7; Design sections 6–7; D5–D8.

### Critical Issue 2: Removing the matrix eliminated an important composition signal

**Concern:** Independent frontend/core/backend tests cannot detect every broken standard registration, middleware, generation, or connector-host wiring error.  
**Impact:** Correct components could fail when assembled.  
**Resolution:** Add a small explicit bounded end-to-end sentinel whose only purpose is composition verification. Provider profiles in existing families never expand it.  
**Traceability:** Requirement 5; Design section 5; D15–D16.

### Critical Issue 3: The initial provider profile allowed arbitrary transformation hooks

**Concern:** A generic transformation DSL would become a hidden second proxy engine with weaker review/security semantics.  
**Impact:** Canonical requirement matching could be bypassed and provider integrations would become opaque configuration programs.  
**Resolution:** Use a typed bounded schema with a closed family-owned quirk vocabulary. Unique semantics graduate to code or executable connector.  
**Traceability:** Requirement 6.3–6.5; Design section 6.3–6.6; D5–D6.

### Critical Issue 4: One contribution descriptor became a service-locator bag

**Concern:** The initial descriptor mixed factories, runtime dependencies, routes, diagnostics, tests, lifecycle, and configuration.  
**Impact:** Parallel registries would be replaced by one broad unstable central interface violating SRP/ISP.  
**Resolution:** Compose focused registration/route/diagnostic/contract/family facets. Runtime dependencies remain in existing factory/composition inputs.  
**Traceability:** Requirement 7; Design section 7; D7–D8.

## Round 2

### Assessment

**Decision: NO-GO**

The provider/composition model was sound, but canonical/ABI and migration safety remained under-specified.

### Critical Issue 1: Generic ABI extension risked raw protocol tunneling

**Concern:** A generic opaque `bytes` field could allow adapters/connectors to bypass canonical semantics.  
**Impact:** Capability admission, audit, hooks, accounting, and projection could become advisory.  
**Resolution:** Generic semantic carriers must be identity-bearing, bounded, presence-aware, and matched against exact protocol requirements/dialect support. Complete raw provider request/response envelopes are forbidden.  
**Traceability:** Requirements 9.2–9.5, 10.4–10.9; Design sections 10–11; D9–D12.

### Critical Issue 2: Immediate ABI cleanup would break deployed/current connectors

**Concern:** Removing `exact_openresponses_fields` or forcing an ABI v2 creates ecosystem churn disproportionate to the architecture benefit.  
**Impact:** Connector compatibility and optional-provider stability regress.  
**Resolution:** Preserve v1.0–v1.3 wire behavior and treat current OpenResponses-named fields/features as legacy compatibility vocabulary. Future minor evolution is semantic. No v2 in this spec.  
**Traceability:** Requirement 10.1–10.3; Design section 11.1–11.4; D11–D12.

### Critical Issue 3: Canonical cleanup was turning into another broad protocol migration

**Concern:** Removing all vendor-named canonical fields for aesthetic purity could recreate the OpenResponses-sized blast radius.  
**Impact:** Large cost with little runtime benefit; possible Codex/reasoning regressions.  
**Resolution:** Establish a canonical-promotion rule and perform a targeted audit. Migrate only clear adapter-only fidelity safely representable by generic carriers; retain shared/core semantics.  
**Traceability:** Requirement 9; Design section 10; D9–D10.

### Critical Issue 4: Matrix deletion lacked proof that coverage had moved

**Concern:** The 45-cell suite contains real regression evidence. Deleting it immediately would create blind spots.  
**Impact:** Cross-composition/projection regressions could escape.  
**Resolution:** Freeze current evidence, build RED TCKs, dual-run, produce feature traceability, inject deliberate faults, then cut over and delete.  
**Traceability:** Requirements 5.10, 12.1–12.5; Design section 13; D14–D17.

### Critical Issue 5: The design did not force de-bloat

**Concern:** Teams could keep both TCKs and matrix forever.  
**Impact:** More code and slower CI—the opposite of the goal.  
**Resolution:** Baseline Cartesian-only surface, require ≥80% removal and no net growth across reviewed shared affected surfaces.  
**Traceability:** Requirement 12.4–12.7; Design sections 13, 17; D17.

### Critical Issue 6: Continuation mirrors remained outside the design

**Concern:** Known duplicate `MemoryStore`/`StreamRecorder` implementations contradict the project's prior mirror-removal work.  
**Impact:** Future drift and redundant maintenance.  
**Resolution:** Select `pkg/lipsdk/continuation` as the default protocol-neutral implementation authority, keep infra durability adapters, reduce core to orchestration, and add mirror gates.  
**Traceability:** Requirement 11; Design section 12; D13.

## Round 3

### Requirements Traceability Review

- Every final requirement maps to a named design section.
- Provider scale is tested through a synthetic ≥1,000-profile catalog.
- Frontend/backend additions have independent TCK ownership.
- Provider-profile-only additions have zero expected core/frontend/ABI/global-matrix footprint.
- Stateful OpenResponses lifecycle generalization is explicitly deferred.
- Independent emulators/compliance suites remain protected evidence.
- ABI v1.3 compatibility is explicit.
- Matrix cutover is deletion-oriented, not additive.
- Confirmed continuation mirrors have one selected authority.
- Implementation remains approval-gated.

### SOLID Review

**Single Responsibility — PASS**

- TCKs are separated by frontend/core/backend responsibility.
- Provider profiles own declarative variation only.
- Contribution facets own metadata projection only.
- Runtime/provider behavior remains in existing adapters/core.

**Open/Closed — PASS**

- A provider profile extends data without central switches.
- A backend family extends through one contribution and one TCK.
- A frontend extends through one contribution and one TCK.
- Central derived registries do not require protocol/provider case statements.

**Liskov Substitution — PASS**

- Backend capability/dialect declarations become executable substitution contracts.
- A backend claiming semantics must pass the same semantic scenarios.
- Unsupported hard semantics remain fail-closed.

**Interface Segregation — PASS**

- Contribution descriptors are composed from focused facets.
- TCK harnesses expose narrow test probes.
- Stateful protocol-specific lifecycle remains outside common frontend contracts.

**Dependency Inversion — PASS**

- Core remains dependent on canonical/SDK contracts.
- Provider profile catalog binds at composition/adapter edges.
- Connector contract test depends on supported SDK ABI, not concrete root backends.

### Hexagonal Review

**Decision: PASS**

- Driving adapters are independently certified against the canonical application boundary.
- Driven adapters are independently certified against the same boundary.
- Provider SDKs stay at edges.
- Provider profile data does not enter core.
- Composition roots derive metadata without becoming policy owners.
- TCKs are test architecture, not production orchestration.
- Sentinel verifies assembly without becoming a second semantic contract.

### Security Review

**Decision: PASS**

- No dynamic code loading.
- No arbitrary profile transformation language.
- Credentials remain references/modes, not values.
- Generic ABI residuals are bounded and negotiated.
- Required unknown semantics do not silently drop.
- Diagnostics remain bounded/redacted.
- Profile validation performs no provider network/process activation.

### Scalability Review

**Decision: PASS**

Required deterministic proof:

- 5 frontends + 1,000 profiles do not create 5,000 mandatory pairs.
- Provider profiles do not create backend-family factories one-for-one.
- Sentinel count does not increase with profiles inside existing families.
- TCK scenario selection is capability-driven.
- No full Cartesian evidence object is required for release.

### Brownfield Compatibility Review

**Decision: PASS**

- Existing custom compatible YAML remains valid.
- Existing frontend/backend wire behavior remains characterized.
- ABI v1.0–v1.3 remains valid.
- Matrix runs in parallel until replacement evidence is proven.
- Diagnostics compatibility is characterized before DTO migration.
- Public continuation helpers receive source-compatibility handling if needed.

### De-Bloat/ROI Review

**Decision: PASS**

High-cost/high-ROI work retained:

1. TCK certification.
2. Provider profiles.
3. contribution-derived registries.
4. bounded sentinel.
5. semantic ABI evolution guard.
6. continuation mirror deletion.
7. matrix-only code deletion.

Lower-ROI speculative work removed/deferred:

- generalized stateful frontend framework;
- ABI v2;
- wholesale canonical rewrite;
- arbitrary provider scripting;
- release-manifest sharding already covered by adjacent work.

### Testing Review

**Decision: PASS**

- RED characterization precedes migration.
- Contract suites use real canonical types.
- Negative scenarios prove zero upstream work.
- Connector path uses the real host/ABI.
- Matrix/TCK dual-run protects cutover.
- Synthetic scale test is deterministic.
- Race/fuzz obligations remain on parser/stream/concurrency paths.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The final design addresses the original maintainability concern without turning the response into another architecture rewrite. It removes the frontend×backend product from mandatory proof, makes most provider growth data-driven, centralizes extension metadata without introducing a service locator, preserves current hexagonal boundaries, and includes explicit deletion gates so the new architecture cannot remain layered on top of the old one indefinitely.

No implementation work is authorized by this review.

## Implementation Gate

Implementation shall begin only after maintainers:

1. set `approvals.requirements.approved` to `true`;
2. set `approvals.design.approved` to `true`;
3. set `approvals.tasks.approved` to `true`;
4. set `ready_for_implementation` to `true` in `spec.json`.

The implementation must begin with RED characterization/TCK contracts. Production architecture changes shall not precede those tests.
