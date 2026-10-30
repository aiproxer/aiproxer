# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the first requirements draft for `extension-scalability-and-architecture-simplification` with repository `main` at `95089eb4b74d5cf8d062f238a1121124ce0da878`.

The review covers:

- canonical contracts and projectors in `pkg/lipapi`;
- core admission/routing/execution;
- frontend composition and route ownership;
- built-in/compatible backend composition;
- executable backend-plugin ABI and connector-host adapters;
- OpenResponses continuation ownership;
- diagnostics/inventory projections;
- `internal/testkit/conformance`;
- independent refclient/refbackend suites;
- architecture tests and line-budget ratchets;
- the OpenResponses implementation history, especially PR #250;
- the prior runtime-convergence, connector-architecture, hexagonal-audit, and structural-deduplication work.

Classifications:

- **Missing** — required capability/contract does not exist.
- **Partial** — reusable machinery exists but does not satisfy the target architecture.
- **Duplicate** — two or more authorities implement substantially the same responsibility.
- **Constraint** — an existing public/ABI/security commitment constrains migration.
- **Deferred** — a possible cleanup has insufficient ROI for this specification.
- **Unknown** — implementation must measure or prove a brownfield assumption before deletion.

Effort:

- **S** — focused contract/test/refactor.
- **M** — multi-package migration on established seams.
- **L** — shared composition/conformance or public SDK migration.
- **XL** — ecosystem ABI or broad canonical migration; should be avoided unless the design makes it incremental.

## Current Assets Worth Preserving

### Canonical middle and explicit projectors

Current `pkg/lipapi` already has:

- message authority and ordered-item authority;
- deterministic validation;
- capability and exact dialect/extension requirements;
- item-authority→legacy-view and legacy-view→ordered-item projectors;
- candidate admission support;
- streaming canonical events.

This is the strongest foundation for replacing pairwise conformance. The target architecture should certify adapters against this contract rather than invent a second intermediate model.

### Core candidate admission

`internal/core/capabilities` and runtime admission already combine:

- effective semantic capabilities;
- transport capabilities;
- reasoning replay support;
- exact dialect/extension support;
- projection feasibility;
- frozen failover requirements.

This means backend legality is already mostly frontend-independent. The TCK should exercise this seam rather than reimplement compatibility logic.

### Frontend separation

The repository already prohibits frontend→backend imports and core→OpenResponses wire-codec imports. Ordinary HTTP frontends share `frontendpipe`; OpenResponses retains a richer protocol-owned stateful path for continuation/WebSocket behavior.

### Executable backend connectors

ADR 0008 and the merged connector work already provide:

- versioned gRPC negotiation;
- out-of-process optional connectors;
- manifest discovery/trust;
- host/connector lifecycle;
- connector-host parity tooling.

The target should reuse this architecture and expose a stable semantic contract-test entry point for connector authors rather than add another plugin model.

### Independent protocol evidence

`internal/refclient`, `internal/refbackend`, pinned OpenResponses sources, and protocol-owned compliance suites are valuable because they are independent of production codecs. They should survive the conformance refactor.

### Explicit composition

`internal/standardplugins` uses explicit Go registration rather than reflection or `init()` magic. The problem is not explicitness; it is repeated parallel metadata. The target should derive multiple views from one explicit contribution source.

### Architecture ratchets

`internal/archtest` already enforces import boundaries and line budgets. This makes it practical to add non-Cartesian and no-parallel-registry ratchets without introducing a new lint framework.

## Current Scalability Problem

The authoritative conformance list currently contains five frontends and nine backend identities, generating 45 cells. The OpenResponses evidence layer then classifies required features per cell and maintains special frontend-row/backend-column/general-cell machinery.

At 1,000 backend/provider identities and five frontends, the same completeness rule would create 5,000 cells before feature-level classification. With the current 17-feature OpenResponses evidence vocabulary, that conceptual space is 85,000 feature outcomes before protocol-specific variants. The problem is structural, not a test-runner optimization problem.

The design goal must therefore remove the product of frontend and backend/provider population from mandatory correctness proof.

## Gap Register

| ID | Severity | Class | Effort | Current finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Constraint | L | `internal/testkit/conformance/matrix.go` defines authoritative frontend and backend lists and materializes their full Cartesian product. | Retire Cartesian completeness as a release invariant after TCK equivalence. |
| G-02 | P0 | Partial | L | `matrix_evidence_45.go` classifies all 45 cells and branches specially for the OpenResponses row/column. | Replace cell evidence with feature-contract certification evidence. |
| G-03 | P0 | Partial | M | OpenResponses frontend-row and backend-column scenario registries duplicate feature classification around one protocol addition. | Move reusable semantic scenarios into frontend/backend/core TCK corpora. |
| G-04 | P0 | Missing | L | No backend TCK certifies a backend once against canonical semantics independently of frontends. | Add capability-driven backend TCK. |
| G-05 | P0 | Missing | L | No frontend TCK certifies wire↔canonical behavior once independently of backend population. | Add frontend TCK. |
| G-06 | P0 | Partial | M | Core projection/admission behavior is well tested but not packaged as an explicit canonical-core certification layer. | Add focused core contract suite and traceability. |
| G-07 | P0 | Partial | M | Connector-host tests exist but external connector authors lack one supported semantic TCK entry point. | Expose connector contract-test package/entry point. |
| G-08 | P0 | Missing | M | Capability declarations do not automatically drive a common executable positive/negative scenario corpus. | Make TCK selection capability/dialect driven. |
| G-09 | P0 | Missing | M | Negative compatibility evidence is often matrix-cell metadata rather than one reusable zero-upstream contract. | Add upstream probe and deterministic pre-network rejection scenarios. |
| G-10 | P0 | Constraint | M | Release/parity tooling currently assumes full matrix completeness. | Migrate gates to certification + bounded sentinel. |
| G-11 | P0 | Partial | M | `EssentialBackendKinds` is a central allowlist separate from backend registration data. | Derive essential-kind view from one backend contribution source. |
| G-12 | P0 | Partial | M | `CompatibleBackendKinds` repeats compatible identities separately. | Derive compatible-family view from contributions. |
| G-13 | P0 | Partial | M | `StandardBundle` separately registers frontends and backend factories while route/diagnostics/conformance metadata live elsewhere. | Introduce composed contribution descriptors. |
| G-14 | P0 | Partial | M | `StandardFrontendRouteClaims` is another frontend-ID→provider map. | Derive route claims from frontend contributions. |
| G-15 | P0 | Partial | M | Compatible diagnostics switch on OpenResponses to select a protocol-specific decoder/projection. | Move diagnostics provider onto backend contribution/family. |
| G-16 | P1 | Partial | M | OpenResponses frontend diagnostics use a dedicated core diagnostic DTO and projector. | Replace with generic bounded plugin-instance projection plus extension details. |
| G-17 | P1 | Partial | S | `stdhttp/contract.RouteKind` centrally enumerates protocol-specific route kinds. | Make kind/operation ID opaque and extension-owned. |
| G-18 | P0 | Missing | L | There is no first-class distinction between backend family implementation and a large catalog of compatible provider profiles. | Add typed provider-profile architecture. |
| G-19 | P0 | Constraint | M | Current compatible modes are instance-configurable but project-shipped provider integrations still tend toward new code/connector identities. | Add catalog profiles that bind to existing family implementations. |
| G-20 | P0 | Missing | M | No versioned provider-profile schema defines the safe declarative boundary. | Add bounded schema and validation. |
| G-21 | P0 | Missing | M | There is no explicit rule for when a provider profile must graduate to a family adapter or connector. | Add architectural decision rule and tests/docs. |
| G-22 | P0 | Constraint | M | Arbitrary profile transformation would risk becoming an unreviewed proxy scripting layer. | Prohibit arbitrary executable/template/regex transformation languages; use closed family quirks. |
| G-23 | P0 | Missing | M | No synthetic large-profile test guards against future per-profile goroutine/process/network work during config validation. | Add ≥1,000-profile bounded scale test. |
| G-24 | P0 | Constraint | L | Backend-plugin v1.3 has `exact_openresponses_fields`, a protocol-named feature. | Preserve compatibility but forbid this pattern for future minors. |
| G-25 | P0 | Partial | L | ABI DTOs contain exact OpenResponses-specific presence fields even though generic ordered-item/extension mechanisms now exist. | Add/complete semantic generic carriers and compatibility bridging. |
| G-26 | P0 | Missing | M | No architecture rule prevents future protocol-named ABI features/proto fields. | Add ABI naming/semantic architecture gate with explicit legacy exceptions. |
| G-27 | P1 | Partial | M | `pkg/lipapi.Call.PromptCacheKey` is documented as a remote OpenResponses passthrough hint rather than clear core policy state. | Audit and migrate if it is adapter-only fidelity. |
| G-28 | P1 | Partial | M | Reasoning/compaction exact fields include OpenResponses/OpenAI vocabulary; some now also support Codex workflows. | Classify shared/core semantics versus adapter-only fidelity before changing. |
| G-29 | P0 | Missing | S | No documented canonical-promotion rule determines when a new wire field becomes first-class `lipapi`. | Add rule and review gate. |
| G-30 | P0 | Duplicate | S/M | `pkg/lipsdk/continuation` and `internal/core/continuation` both contain bounded in-memory store implementations. | Select one authority and delete mirror. |
| G-31 | P0 | Duplicate | S/M | The same two packages contain near-identical continuation `StreamRecorder` implementations. | Select one authority and delete mirror. |
| G-32 | P1 | Partial | S | Core continuation materialization already delegates to SDK materialization, showing the desired ownership direction. | Keep thin orchestration only; prohibit algorithm forks. |
| G-33 | P0 | Missing | M | No deterministic change-surface report distinguishes 600 generated/test files from shared architectural churn. | Add extension impact report with boundary categories. |
| G-34 | P1 | Partial | M | Architecture tests contain OpenResponses-specific dependency checks that could grow one file/rule set per new protocol. | Move reusable rules to zone/contribution policies; retain protocol-specific rules only for real protocol exceptions. |
| G-35 | P0 | Missing | M | There is no gate proving a provider-profile-only addition has zero shared-core/ABI/frontend registration footprint. | Add fixture/architecture test for zero shared-boundary registration. |
| G-36 | P0 | Unknown | M | Removing the matrix too early could lose real cross-composition bug coverage. | Dual-run old matrix and TCK until traceability/equivalence is proven. |
| G-37 | P1 | Unknown | M | Exact current Cartesian-only line footprint is not locked as a baseline. | Add implementation-time baseline selector and ≥80% removal gate. |
| G-38 | P1 | Partial | M | Current integration tests can catch composition failures but conflate that purpose with exhaustive compatibility proof. | Retain a small explicit sentinel for composition only. |
| G-39 | P2 | Deferred | L | OpenResponses does not use the ordinary `frontendpipe` because it owns auth-before-body, continuation, compaction, and WebSocket behavior. | Do not generalize frontendpipe until a second stateful frontend demonstrates the same lifecycle; keep protocol-owned composition now. |
| G-40 | P2 | Deferred | M | The monolithic `.release-files` manifest causes unrelated maintenance churn. An active separate proposal already addresses fragments. | Do not duplicate that work in this spec; consume it if merged. |
| G-41 | P1 | Partial | M | Current architecture line budgets measure size but not extension coupling/change amplification. | Add change-surface categories and extension-specific ratchets. |
| G-42 | P0 | Missing | M | No machine-readable certification record describes which semantic scenarios a frontend/backend/profile passed. | Add certification evidence DTO/artifact. |
| G-43 | P1 | Constraint | M | Third-party connectors should not need to import `internal/testkit` to certify themselves. | Put connector-facing TCK entry point under supported SDK namespace. |
| G-44 | P1 | Constraint | M | A single giant contribution struct would simply relocate service-locator coupling. | Compose focused contribution facets and keep runtime dependencies out. |
| G-45 | P1 | Constraint | M | A generic opaque ABI carrier could become raw protocol tunneling if not tied to canonical requirements. | Require bounded identity/presence plus exact negotiation/admission. |

## Requirements Review Round 1

The first requirements draft focused on replacing the matrix with TCKs and provider profiles. Brownfield review returned **NO-GO** because it under-specified several architecture risks.

### Finding R1-A: TCK-only design did not define provider integration economics

Without an explicit provider-profile layer, Go-LIP could remove Cartesian tests yet still accumulate hundreds of provider-specific Go packages and registrations.

**Remediation added:**

- Requirement 2.2–2.7 — explicit additive extension footprint.
- Requirement 6 — family/profile/connector taxonomy, safe schema, scale test.
- Requirement 7.5 — provider profiles do not enter central kind lists.

### Finding R1-B: Removing the matrix could erase composition confidence

The matrix has caught real cross-path regressions. Eliminating it without a replacement end-to-end signal would overfit to unit boundaries.

**Remediation added:**

- Requirement 5.5–5.10 — bounded real-stack sentinel.
- Requirement 12.1–12.3 — dual-run migration and traceability before deletion.

### Finding R1-C: External connectors were not covered

An internal-only backend TCK would not scale an external connector ecosystem.

**Remediation added:**

- Requirements 3.7–3.10 and 10.10 — shared semantic corpus plus supported connector-facing TCK entry point.

### Finding R1-D: Contribution consolidation risked a new god object

A single descriptor carrying runtime dependencies, factories, diagnostics, routes, tests, and policy would violate ISP/SRP.

**Remediation added:**

- Requirement 7.2–7.3 — focused contribution facets.
- Requirement 7.8 — metadata only; runtime behavior remains in owners.
- G-44 recorded as a design constraint.

### Finding R1-E: Provider profiles could become an unsafe scripting language

An unrestricted data-driven provider system could trade Go duplication for opaque configuration programs.

**Remediation added:**

- Requirement 6.3–6.5 — bounded typed schema, closed family quirks, explicit graduation to code/connector.
- G-22.

### Finding R1-F: Canonical and ABI growth remained unconstrained

The draft did not prevent the next rich protocol from repeating `PromptCacheKey`/`exact_openresponses_fields` style expansion.

**Remediation added:**

- Requirement 9 — canonical promotion rule and bounded dialect carriers.
- Requirement 10 — semantic ABI evolution, v1.3 compatibility, protocol-name guard.
- G-24–G-29 and G-45.

### Finding R1-G: Confirmed continuation duplication was omitted

The requested architecture cleanup should remove known mirrors rather than only improve future seams.

**Remediation added:**

- Requirement 11.
- G-30–G-32.

### Finding R1-H: No objective de-bloat gate existed

A new TCK framework could be layered on top of the 45-cell machinery indefinitely.

**Remediation added:**

- Requirement 12.4–12.7 — delete matrix-only scaffolding, ≥80% legacy-surface reduction, no net affected-surface growth.
- Requirement 13.11 — final deletion/evidence report.

## Requirements Review Round 2

After the first remediation, the requirements were rechecked against steering and current code. The second review returned **GO FOR REQUIREMENTS QUALITY** after the following clarifications were incorporated directly into the final requirements:

1. Provider-profile additions must have zero core/frontend/ABI/global-matrix footprint.
2. Existing user-defined compatible instances remain supported; the profile catalog is additive convenience, not lock-in.
3. Stateful OpenResponses frontend-pipeline generalization is explicitly deferred as low-ROI speculation.
4. `.release-files` sharding is explicitly excluded to avoid overlap with active separate work.
5. Wall-clock CI timing is evidence, not a flaky deterministic gate; structural non-Cartesian scaling is the gate.
6. ABI v1.3 `exact_openresponses_fields` is preserved as compatibility vocabulary while future protocol-named additions are prohibited.
7. Independent wire emulators/compliance tooling are protected from deletion during de-bloat.
8. Public contract changes require source-compatibility handling where practical.
9. Synthetic scale evidence uses at least 1,000 provider profiles.
10. Implementation remains gated by maintainer approval.

## Requirements Quality Gate

**Decision: PASS**

The final requirements:

- directly address the thousand-provider scale assumption;
- distinguish provider profiles from backend families/connectors;
- remove Cartesian completeness without discarding real integration evidence;
- preserve current hexagonal dependency direction;
- constrain canonical and ABI growth;
- require actual deletion rather than layering another framework;
- include confirmed continuation deduplication;
- explicitly defer lower-ROI speculative frontend-pipeline and release-manifest work;
- are testable through deterministic architecture/TCK/scale contracts;
- introduce no product behavior change by themselves.
