# Requirements - Introduce Hexagonal Architecture

Spec name: `introduce-hexagonal-architecture`

## Introduction
This feature defines how the Go-based LLM Interactive Proxy adopts a hexagonal architecture so that
product-defining orchestration remains in a small canonical core and transports, providers, storage,
and operational integrations stay replaceable at the edge.

The goal is to improve maintainability and testability in the existing codebase without weakening
streaming-first execution, core-owned routing, plugin-first extensibility, or explicit failure behavior.
The architecture shall be introduced as an incremental formalization of the existing core-plus-adapters
shape rather than as a directory renaming exercise or a full-system rewrite.

## Requirements

### Requirement 1: Core Application Boundary
**Objective:** As a platform maintainer, I want product-defining proxy behavior to stay in an application core, so that transports and integrations can change without altering the core contract.

#### Acceptance Criteria
1.1. The LLM Interactive Proxy shall define a transport-agnostic application core for canonical request handling, routing, capability validation, streaming orchestration, and continuity decisions.
1.2. When a request is executed, the LLM Interactive Proxy shall apply route planning, failover eligibility, attempt lineage, and recovery policy inside the application core rather than inside adapters.
1.3. If a behavior requires provider SDK types, transport protocol types, or persistence driver types, the LLM Interactive Proxy shall place that behavior outside the application core.
1.4. While a request is in progress, the LLM Interactive Proxy shall preserve the streaming-first execution path as the primary execution path of the application core.
1.5. The LLM Interactive Proxy shall keep the application core independent from concrete frontend, backend, and feature-plugin implementations.
1.6. The LLM Interactive Proxy shall be allowed to express the application core through existing package families when dependency direction, ownership, and invariants are explicit, without requiring a mandatory `app` or `domain` package rename.

### Requirement 2: Driving Adapters
**Objective:** As an integrator, I want client-facing protocols to remain isolated at the system edge, so that new frontend surfaces can be added without rewriting core logic.

#### Acceptance Criteria
2.1. When a supported client request arrives, the LLM Interactive Proxy shall have a driving adapter translate transport input into canonical core input before application logic runs.
2.2. When the application core emits canonical events, the LLM Interactive Proxy shall have a driving adapter translate those events into protocol-legal responses for the active frontend surface.
2.3. If client input is malformed or requests unsupported required semantics, the LLM Interactive Proxy shall have the driving adapter return a protocol-legal error without moving protocol-specific rules into the application core.
2.4. While handling a transport session, the LLM Interactive Proxy shall keep authentication challenges, protocol decoding, protocol encoding, and transport-level validation in driving adapters or transport seams.
2.5. Where a new client-facing protocol is included, the LLM Interactive Proxy shall integrate it through a driving adapter boundary instead of a pairwise protocol translator.

### Requirement 3: Driven Adapters and Ports
**Objective:** As a plugin author, I want outbound dependencies to sit behind stable ports, so that providers and infrastructure can change independently from core behavior.

#### Acceptance Criteria
3.1. When the application core needs external inference, storage, identity, or observation capabilities, the LLM Interactive Proxy shall invoke them through stable ports defined by the consuming core boundary.
3.2. While implementing an outbound port, the LLM Interactive Proxy shall keep provider SDKs, database drivers, HTTP clients, and other infrastructure concerns in driven adapters.
3.3. If an outbound integration returns infrastructure-specific failure details, the LLM Interactive Proxy shall translate them at the adapter boundary into core-understandable failure results.
3.4. Where multiple integrations satisfy the same port, the LLM Interactive Proxy shall allow them to be substituted without changing the core contract.
3.5. The LLM Interactive Proxy shall keep canonical contracts and plugin SDK contracts free of official provider SDK types.
3.6. When an existing seam is already sufficient and low-coupling, the LLM Interactive Proxy shall be allowed to keep that seam without extracting a new dedicated port solely for architectural symmetry.
3.7. When a new outbound port is introduced, the LLM Interactive Proxy shall define that port from the consuming core boundary rather than from an adapter package.
3.8. If a proposed interface exists only to support mocking, mirror one implementation, or satisfy naming symmetry, the LLM Interactive Proxy shall treat that interface as non-required.

### Requirement 4: Dependency Direction and Explicit Wiring
**Objective:** As a maintainer, I want dependencies to point inward and wiring to stay explicit, so that the architecture remains understandable and auditable.

#### Acceptance Criteria
4.1. The LLM Interactive Proxy shall assemble application-core services and concrete adapters through explicit composition roots.
4.2. When a new dependency is introduced, the LLM Interactive Proxy shall make its compile-time direction point from adapters toward core contracts rather than from the application core toward concrete adapters.
4.3. If a core package requires a concrete transport, provider, storage, or plugin implementation to compile, the LLM Interactive Proxy shall treat that dependency as invalid.
4.4. While the standard distribution is wired, the LLM Interactive Proxy shall register official integrations without requiring reflection-driven discovery or native Go plugin loading.
4.5. The LLM Interactive Proxy shall make architectural boundary compliance verifiable through automated tests or checks.
4.6. The LLM Interactive Proxy shall not require broad package relocation or directory renaming when the intended hexagonal boundary can be enforced through explicit contracts, dependency rules, and architecture tests.

### Requirement 5: Preserve Product-Defining Proxy Semantics
**Objective:** As an operator, I want the new architecture to preserve current proxy guarantees, so that refactoring does not weaken user-visible behavior.

#### Acceptance Criteria
5.1. When a request crosses architectural boundaries, the LLM Interactive Proxy shall preserve canonical-in-the-middle translation through canonical requests and canonical event streams.
5.2. When a recoverable failure occurs before client-visible output, the LLM Interactive Proxy shall allow core-owned pre-output recovery without requiring provider-specific failover logic in adapters.
5.3. If client-visible output has begun, the LLM Interactive Proxy shall not perform transparent retry or failover.
5.4. While evaluating backend candidates, the LLM Interactive Proxy shall keep capability checks, eligibility filtering, weighted selection, and explicit failure surfacing as core-owned decisions.
5.5. The LLM Interactive Proxy shall preserve observability of route plans, attempts, surfaced outcomes, and swallowed outcomes independently from the attached adapters.

### Requirement 6: Incremental Migration and Coexistence
**Objective:** As a development team, I want the architecture to support safe incremental migration, so that existing functionality can move without destabilizing the product.

#### Acceptance Criteria
6.1. When an existing component is migrated to the hexagonal architecture, the LLM Interactive Proxy shall allow that component to move behind a stable port and adapter boundary without requiring a full-system rewrite.
6.2. While migrated and not yet migrated components coexist, the LLM Interactive Proxy shall preserve core-owned invariants and externally supported behavior.
6.3. If a migration changes an existing protocol behavior or core routing invariant, the LLM Interactive Proxy shall make that change explicit through updated requirements and regression coverage.
6.4. Where a legacy component cannot yet be fully isolated, the LLM Interactive Proxy shall expose the remaining coupling as a documented exception rather than hiding it inside the application core.
6.5. The LLM Interactive Proxy shall support replacing migrated adapters incrementally without changing unrelated application-core behavior.
6.6. During migration planning, the LLM Interactive Proxy shall classify existing components as already aligned, requiring seam extraction, or requiring a documented temporary exception.

### Requirement 7: Testability and Cross-Cutting Concerns
**Objective:** As a development team, I want boundary-focused testing and disciplined cross-cutting concern placement, so that architecture rules remain enforceable over time.

#### Acceptance Criteria
7.1. Where application-core behavior is tested, the LLM Interactive Proxy shall allow driven adapters to be replaced with deterministic test doubles.
7.2. Where an adapter boundary is tested, the LLM Interactive Proxy shall allow contract tests to verify canonical input and output behavior independently from unrelated adapters.
7.3. When logging, tracing, metrics, or diagnostics are enabled, the LLM Interactive Proxy shall propagate request and attempt identifiers across ports without coupling core use cases to transport frameworks.
7.4. While cross-cutting transport concerns such as authentication are enabled, the LLM Interactive Proxy shall place transport-specific handling at the system edge and expose only stable core-facing context.
7.5. The LLM Interactive Proxy shall make it possible to verify architectural boundary violations through targeted architecture or integration tests.
7.6. When read-only admin, diagnostics, or reporting flows are simpler as projections than as aggregate persistence, the LLM Interactive Proxy shall allow dedicated query adapters and read DTOs instead of forcing repository-shaped write seams.

### Requirement 8: Pragmatic Hexagonal Formalization
**Objective:** As a maintainer, I want hexagonal architecture to be adopted through clear ownership and dependency rules rather than textbook ceremony, so that maintainability improves without unnecessary churn.

#### Acceptance Criteria
8.1. When the hexagonal architecture is documented, the LLM Interactive Proxy shall describe the current codebase in terms of core policy, driving adapters, driven adapters, composition roots, and support-only packages.
8.2. If a proposed refactor only changes package names or directory layout without materially improving dependency direction, port clarity, or testability, the LLM Interactive Proxy shall treat that refactor as non-required.
8.3. When a new explicit port is introduced, the LLM Interactive Proxy shall justify it by a real substitution, ownership, or coupling problem rather than by a desire for textbook layer symmetry.
8.4. The LLM Interactive Proxy shall prefer selective extraction of high-value ports and stronger architecture guardrails over broad package churn.
8.5. Where the current structure already behaves as a valid hexagonal boundary, the LLM Interactive Proxy shall allow the design to formalize that boundary rather than replace it.
8.6. When driving adapters call into the core, the LLM Interactive Proxy shall prefer concrete use-case services or narrow function seams unless multiple real consumers justify an inbound interface.
8.7. When a new internal seam is extracted, the LLM Interactive Proxy shall place it near the consuming core capability or a narrowly scoped shared boundary rather than in a generic repo-wide `ports`, `interfaces`, or `services` bucket.


