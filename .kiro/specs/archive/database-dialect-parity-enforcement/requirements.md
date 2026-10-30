# Requirements Document

## Introduction

The Go LLM Interactive Proxy supports durable persistence on SQLite and PostgreSQL across several production subsystems. Bun already removes much of the duplicated CRUD implementation risk by allowing both engines to share the same Go store implementations, but it does not make database transaction semantics, locking, DDL, migration behavior, driver behavior, or engine capabilities identical. The repository therefore needs an executable parity system that proves the logical contracts promised on both engines and prevents future SQLite/PostgreSQL support from drifting silently.

This specification addresses issue #438 by first certifying and repairing the current dual-dialect persistence surface, then establishing fail-closed architectural and CI enforcement for future changes. The goal is not identical SQL. The goal is equivalent promised behavior on both supported durable backends, with every intentional backend-specific capability difference declared and tested explicitly.

## Boundary Context

- **In scope**: all production persistence components that support both SQLite and PostgreSQL; shared behavior contracts; migration/schema parity; transaction/concurrency invariants; explicit backend capability differences; dialect-sensitive code inventory; architecture guardrails; local parity commands; merge-blocking CI execution against real SQLite and PostgreSQL; remediation of current parity/enforcement gaps; and correction of database-parity documentation drift.
- **Current dual-dialect baseline**: continuity, secure sessions, control-plane ledger, usage authority, concurrency authority, metering journal, terminal work, and billing persistence are the initial catalog candidates and must be reconciled against the implementation-time tree before the catalog is frozen.
- **Out of scope**: replacing Bun; adding another ORM/repository abstraction; adding a third database product; automatic data migration between SQLite and PostgreSQL; making SQLite provide PostgreSQL's distributed multi-instance guarantees; changing public LLM protocols; changing billing/business policy; or redesigning unrelated store logic merely to make SQL text identical.
- **Adjacent expectations**: existing PostgreSQL direct, transaction-pooler, migration, billing-convergence, race, and release gates remain valid specialized evidence. This feature may consolidate or rename overlapping commands only when existing specialized semantics remain reachable and documented.
- **Boundary ownership**: test infrastructure, architecture/QA guardrails, CI/workflow wiring, store-contract tests, component-owned migration/schema tests, targeted driven-adapter fixes, and documentation. Production domain/core contracts remain consumer-owned.
- **Optional hexagonal lens**: database stores remain driven adapters behind existing consumer-owned ports. The new parity catalog and runner are test infrastructure and must not become a production service locator or persistence registry.
- **Revalidation triggers**: adding/removing a durable store; adding/removing SQLite or PostgreSQL support; adding a migration family or migration file; changing a consumer-owned store port; introducing a new dialect-specific branch/raw SQL/driver behavior; changing PostgreSQL pooler rules; changing schema-mode behavior; or changing the merge-gate topology.

## Requirements

### Requirement 1: Authoritative Dual-Dialect Persistence Inventory
**Objective:** As a maintainer, I want one fail-closed inventory of every production persistence component that claims SQLite and PostgreSQL support, so that a new dual-dialect store cannot exist outside parity enforcement.

#### Acceptance Criteria
1. When the parity inventory is validated against the repository, the system shall represent every production persistence component that supports both SQLite and PostgreSQL exactly once.
2. When implementation begins, the system shall reconcile the current tree and include the eight known candidate families—continuity, secure sessions, control-plane ledger, usage authority, concurrency authority, metering journal, terminal work, and billing—unless the codebase proves that a candidate no longer supports both engines.
3. If repository discovery identifies a new dual-dialect persistence candidate that is not registered, then architecture validation shall fail with the candidate package/path and the missing registration requirement.
4. If a registered component no longer has evidence of dual-dialect support, then architecture validation shall fail until the registration is updated or the production support change is reconciled.
5. Where a component implements multiple consumer-owned persistence ports or optional store capabilities, the inventory shall enumerate those contract surfaces rather than treating the package name alone as sufficient proof.
6. The inventory shall remain internal test/QA metadata and shall not become a runtime database registry, public API, plugin contract, or dependency of production request processing.

### Requirement 2: Common Behavioral Contract Parity
**Objective:** As a maintainer, I want each behavior promised on both durable backends to have one canonical contract definition executed against both engines, so that feature semantics cannot diverge through duplicated or one-sided tests.

#### Acceptance Criteria
1. When a persistence capability is promised on both SQLite and PostgreSQL, the parity suite shall execute the same logical contract assertions against a SQLite fixture and a PostgreSQL fixture.
2. When a component already has reusable contract suites, the parity system shall reuse or compose them rather than creating a second independent source of behavioral truth.
3. Where a component currently has equivalent SQLite and PostgreSQL tests but no canonical shared contract, the implementation shall extract or consolidate the common assertions before declaring that component parity-certified.
4. The common contracts shall cover externally observable persistence semantics relevant to each component, including successful operations, typed error behavior, idempotency/replay, ordering, restart persistence, uniqueness, and query/readback semantics where those behaviors are part of the consumed port.
5. When a new consumer-owned durable-store capability is added to a registered dual-dialect component, parity certification shall require that capability to be represented in the component's common contract evidence before the merge gate can pass.
6. A backend-specific test shall not by itself count as proof of common parity when the corresponding behavior is promised on both engines.

### Requirement 3: Transaction and Concurrency Invariant Parity
**Objective:** As a maintainer, I want correctness-sensitive transactional invariants tested on the real engines, so that differences in SQLite serialization and PostgreSQL row locking cannot produce silent semantic drift.

#### Acceptance Criteria
1. Where a component allocates monotonic sequences or revisions on both backends, the parity contracts shall verify monotonicity and duplicate prevention under the supported concurrency model of each backend.
2. Where a component performs idempotent insert/update/replay behavior, the parity contracts shall verify that repeated and competing operations converge to the same promised logical outcome on both backends.
3. Where lost-update prevention, capacity enforcement, claim exclusivity, uniqueness, or compare-and-swap behavior is part of a common contract, the parity system shall exercise those invariants on both real database engines rather than relying only on query construction or mocks.
4. When PostgreSQL provides an additional distributed multi-instance guarantee that SQLite does not promise, the PostgreSQL guarantee shall be tested separately and the SQLite capability posture shall be declared explicitly rather than being reported as a parity failure.
5. When SQLite depends on engine-specific writer serialization, busy handling, or transaction-opening behavior to satisfy its promised contract, the relevant SQLite-specific behavior shall remain covered by explicit tests.
6. If either backend violates a common transactional invariant, then the parity gate shall fail even when the other backend passes.

### Requirement 4: Schema and Migration Parity
**Objective:** As a maintainer, I want every dual-dialect migration family to prove equivalent logical schema protections on both engines, so that columns, constraints, indexes, and migration history cannot drift independently.

#### Acceptance Criteria
1. When a registered component owns schema migrations, the parity suite shall apply its migration family from an empty database on SQLite and PostgreSQL and verify the resulting schema on both engines.
2. When a migration suite is rerun on an already-current database, both backends shall prove idempotent startup/migration behavior according to the component's existing migration contract.
3. The parity system shall discover the component's versioned migration files and verify that all required migration IDs are represented in the applied migration history for each backend, excluding only explicitly classified non-migration numeric files.
4. Where supported upgrade fixtures exist, the parity suite shall exercise the documented upgrade path on both engines and verify that the final logical schema and stored behavior satisfy the current contract.
5. Schema parity shall compare logical invariants rather than raw DDL text, including required columns/types by semantic category, nullability, primary/foreign keys, uniqueness, correctness-critical indexes, check constraints, immutability protections, and retired artifacts where applicable.
6. If the engines require different mechanisms for the same logical protection, then separate engine-specific verification is allowed only when both verifiers assert the same declared logical invariant.
7. If a new migration is added to a registered dual-dialect component without corresponding SQLite and PostgreSQL migration/schema evidence, then the parity gate shall fail.

### Requirement 5: Explicit Backend Capability and Exception Model
**Objective:** As a maintainer, I want intentional SQLite/PostgreSQL differences represented as explicit tested capabilities, so that legitimate differences are distinguishable from accidental missing implementations.

#### Acceptance Criteria
1. The parity inventory shall classify each persistence capability as common, SQLite-specific, PostgreSQL-direct-specific, PostgreSQL-distributed-specific, PostgreSQL-pooler-specific, or another explicitly named backend/topology class justified by the implementation.
2. If a capability is not common to both durable backends, then its inventory entry shall include a non-empty rationale and automated evidence for the supported posture.
3. Known intentional differences such as SQLite single-node serialized-writer semantics, PostgreSQL distributed-strict concurrency semantics, SQLite BUSY/LOCKED retry behavior, and PostgreSQL transaction-pooler restrictions shall not be hidden as generic skipped parity tests.
4. If a backend-specific exception loses its rationale, evidence, or supported implementation path, then parity inventory validation shall fail.
5. No exception classification shall weaken a behavior that existing product/store contracts already promise on both backends.
6. The parity report/listing shall make common capabilities and backend-specific exceptions distinguishable without reading production SQL.

### Requirement 6: Dialect-Sensitive Change Guardrails
**Objective:** As a maintainer, I want direct dialect escape hatches to remain visible and bounded, so that Bun usage cannot conceal unreviewed database-specific behavior.

#### Acceptance Criteria
1. When architecture validation scans production persistence code, it shall identify dialect-sensitive candidates such as explicit SQLite/PostgreSQL dialect branches, dialect-specific DDL/catalog queries, raw placeholder handling, driver-specific error handling, and backend-specific locking/transaction constructs using deterministic repository rules.
2. If dialect-sensitive code exists outside shared database infrastructure or a registered persistence component's owned source roots, then architecture validation shall fail unless the path is explicitly classified with a justified ownership rule.
3. Adding a new dialect-sensitive source file to a registered component shall cause that component's mandatory dual-engine parity suite to run in merge CI without requiring a developer to remember a separate database-specific command.
4. The guardrail shall not forbid necessary database-specific SQL or force equivalent SQL text when different engine mechanisms are required for the same contract.
5. Shared database infrastructure such as connection opening, Bun dialect wrapping, pool management, DSN sanitization, and schema-introspection helpers shall have explicit ownership in the inventory/guard rules and shall not be mistaken for product-store parity components.
6. The implementation shall not add reflection-based registration, `init()` registration, a DI container, or a generic runtime service locator to implement these guardrails.

### Requirement 7: Mandatory Merge-Blocking Real-Engine Parity CI
**Objective:** As a maintainer, I want every test-relevant pull request to receive real SQLite and PostgreSQL parity evidence automatically, so that a one-engine regression cannot merge because an integration environment was absent.

#### Acceptance Criteria
1. When a pull request contains test-relevant code changes, CI shall execute the complete registered SQLite parity suite and a PostgreSQL-direct parity suite against a real ephemeral PostgreSQL service.
2. The PostgreSQL merge gate shall run fail-closed: a missing/unhealthy PostgreSQL service, missing required DSN wiring, or unexpected test skip in a required direct-parity wrapper shall fail the parity result rather than silently reducing evidence.
3. The merge gate shall set the existing PostgreSQL test environment contract consistently, including required runtime/admin DSN values and fail-closed PostgreSQL test mode, without requiring repository secrets for the ephemeral CI service.
4. PostgreSQL transaction-pooler-only tests may remain outside the per-PR ephemeral direct gate, but their exclusion shall be explicit and the existing fail-closed pooler/release gate shall remain available.
5. Documentation-only changes may bypass the expensive database execution only when existing change-scope detection classifies them as non-test-relevant and the repository's required aggregate status still reports success.
6. If the dedicated parity execution fails, is cancelled, or is unexpectedly bypassed for a test-relevant change, then an already merge-blocking required CI status shall fail closed on that result so parity enforcement does not depend on a future manual branch-protection update.
7. The CI workflow shall pin and document the PostgreSQL image/runtime assumptions used for direct parity and shall not infer transaction-pooler topology from a direct service container.

### Requirement 8: Current-State Parity Remediation and Certification
**Objective:** As a maintainer, I want the existing repository certified before the new gate becomes authoritative, so that enforcement does not merely freeze unknown current divergence.

#### Acceptance Criteria
1. Before the parity gate is declared complete, the implementation shall run the full registered component matrix on SQLite and PostgreSQL-direct and classify every failure as an implementation defect, an invalid/stale test assumption, or an intentional backend capability difference.
2. When a common-contract divergence is found, the implementation shall repair the store/migration behavior or the canonical contract at its owning boundary and shall not resolve it by weakening only one backend's test.
3. The current PostgreSQL gate coverage shall be reconciled so that continuity, secure sessions, control-plane persistence, billing, and any other registered dual-dialect components are not omitted merely because the existing `test-authority-postgres-*` targets focus on dual-plane authority components.
4. Existing schema-verification asymmetry shall be reviewed component by component; where one backend currently checks only table existence while the other verifies correctness-critical constraints/indexes, the weaker verifier shall be augmented for the common logical invariants.
5. The current documentation/CI mismatch around PostgreSQL and tagged integration execution shall be corrected so repository guidance describes the workflow that actually runs.
6. At completion, every registered common capability and required migration family shall be green on both SQLite and PostgreSQL-direct, with any remaining backend-specific behavior represented only through explicit capability entries.

### Requirement 9: Stable Developer and Release Workflow
**Objective:** As a contributor, I want one discoverable parity command and consistent documentation, so that local verification, CI, and release evidence do not drift into different package lists or semantics.

#### Acceptance Criteria
1. The repository shall expose a canonical database-parity command that derives its component/package scope from the authoritative inventory rather than maintaining an independent hand-written package list.
2. The workflow shall provide a fast SQLite-only parity mode and a fail-closed PostgreSQL-direct/full parity mode suitable for CI and developers with a configured PostgreSQL endpoint.
3. Existing specialized PostgreSQL migration, direct distributed-authority, transaction-pooler, billing-convergence, race, and release commands shall either remain available or be explicitly delegated to the new canonical runner without losing their stronger topology-specific evidence.
4. `.kiro/steering/testing.md`, `.kiro/steering/tech.md`, `docs/release-gates.md`, database persistence documentation, and Makefile help text shall describe the same canonical parity commands and CI posture.
5. If the documented component inventory, command behavior, or CI wiring drifts from the executable parity catalog, a repository QA/architecture test shall fail.
6. The parity runner/reporting shall identify the component and backend that failed without printing raw database credentials or secret DSNs.

### Requirement 10: Preserve Runtime Architecture and Compatibility
**Objective:** As an operator and maintainer, I want parity enforcement to improve proof without changing normal runtime behavior, so that the hardening itself does not create a new persistence architecture or deployment migration.

#### Acceptance Criteria
1. The implementation shall continue using Bun plus the existing SQLite and PostgreSQL drivers as the production database abstraction unless an independent issue explicitly changes that architecture.
2. The implementation shall not introduce a second production store implementation solely to satisfy parity testing when the current shared Bun store can serve both engines.
3. Normal proxy request processing shall incur no parity-catalog or parity-runner runtime overhead.
4. Existing database configuration names, supported store selections, public contracts, and operator data shall remain backward compatible unless a separately justified bug fix is required by a failing common contract.
5. The implementation shall not automatically copy or migrate operator data between SQLite and PostgreSQL.
6. Existing PostgreSQL transaction-pooler restrictions, secret-safe DSN handling, fail-closed startup behavior, and resource ownership rules shall not be weakened by the parity work.
