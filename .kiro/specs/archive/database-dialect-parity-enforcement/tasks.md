# Implementation Plan

## Execution Rules

- **Brownfield certification before enforcement.** Do not wire the new merge-blocking database-parity result into the required CI aggregate until the current registered SQLite + PostgreSQL-direct baseline is green and every failure has been repaired or explicitly classified as a legitimate backend capability difference.
- **Bun remains production infrastructure.** Do not add a second ORM/repository/service-locator layer. New catalog/runner code stays under test/QA infrastructure and must not be imported by production request paths.
- **One common contract, two engine fixtures.** For behavior promised on both backends, consolidate assertions into one component-owned suite and make SQLite/PostgreSQL wrappers call it. Do not “fix” a parity failure by weakening only one backend's assertions.
- **Capability differences are explicit.** PostgreSQL distributed guarantees, PostgreSQL pooler behavior, SQLite serialized-writer posture, and SQLite busy-retry behavior may differ only through cataloged capability rows with rationale and automated evidence.
- **Logical schema parity, not DDL text equality.** Preserve engine-appropriate DDL; verify equivalent constraints/indexes/protections through engine-specific metadata.
- **No silent PostgreSQL skip in the merge gate.** Mandatory direct mode must fail when the service/DSN is missing or unhealthy. Pooler-only tests are explicitly excluded from direct mode and remain under their specialized fail-closed gate.
- **Catalog is the package-list source of truth.** Makefile/workflow code must delegate to the typed runner; do not introduce a second hand-maintained list of parity packages.
- **Use TDD for guardrails and repairs.** Add RED architecture/contract/schema tests before changing the corresponding enforcement or production defect. Run focused race tests for concurrency fixes revealed by parity.
- **Preserve existing specialized evidence.** Do not delete or weaken `test-authority-postgres-*`, migration, billing-convergence, race, or release gates unless the new runner delegates to equivalent or stronger evidence and the old command remains compatibly reachable/documented.
- **Revalidate active adjacent specs.** Before normalizing continuity/secure-session contracts, inspect the implementation state of `high-concurrency-performance-hardening` and A-leg lifecycle hardening; update the implementation-time catalog to the authoritative post-merge store/schema surface instead of preserving stale test shapes.
- **No operator data migration.** This spec changes tests/CI and fixes defects only; it does not copy data between engines.

## Phase 1 — Freeze the Current Dual-Dialect Inventory and Enforcement Model

- [x] 1. Establish the typed parity catalog and deterministic repository discovery.

- [x] 1.1 Re-audit the implementation-time tree and freeze the initial component/contract inventory
  - Re-run searches for Bun migration families, SQLite/PostgreSQL runtime store branches, dialect-sensitive store packages, PostgreSQL integration tests, and compile-time store-interface assertions against current `main`.
  - Reconcile the eight current candidate families: continuity, secure sessions, control-plane ledger, usage authority, concurrency authority, metering journal, terminal work, and billing. Add/remove candidates only when the live code proves support changed since this spec was written.
  - For each component, record production `SourceRoots`, executable `TestPackages`, migration roots, important consumer-owned store interfaces/capabilities, and existing shared contract assets.
  - Record shared DB infrastructure (`internal/infra/db`, runtime pool registry/lifecycle helpers) separately so the later scanner does not treat infrastructure as a product-store component.
  - Observable completion: a reviewed inventory table exists in code/test fixtures with no unexplained dual-dialect candidate from the implementation-time search.
  - _Requirements: 1.1–1.6, 6.1–6.5, 8.1_
  - _Boundary: tests / architecture discovery_
  - _Depends: none_
  - _Validation: repository search + focused inventory test added in 1.2_

- [x] 1.2 Implement `internal/testkit/dbparity` typed catalog and invariants
  - Add static Go metadata for component IDs, source/test/migration roots, consumer-contract IDs, capability rows, and backend classes (`common`, SQLite-specific, PostgreSQL-direct/distributed/pooler as needed).
  - Require non-common capability rows to include rationale and an automated evidence anchor; reject duplicate IDs, duplicate ownership, missing paths, empty common evidence, and nondeterministic ordering.
  - Keep migration IDs out of hand-maintained metadata; catalog owns roots and discovery owns versioned files.
  - Seed the catalog with the inventory from 1.1, including explicit initial topology exceptions such as concurrency-authority distributed strictness and SQLite-specific metering busy handling where confirmed by current code.
  - Add unit tests for catalog validation and deterministic listing.
  - _Requirements: 1, 5, 9.1, 10.1–10.3_
  - _Boundary: tests / internal testkit_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/testkit/dbparity/...`_

- [x] 1.3 Implement the fail-closed database-parity architecture discovery guard
  - Add `internal/archtest/database_parity_test.go` using Go AST/filesystem rules to discover Bun migration registries/versioned migration files, dual SQLite/PostgreSQL composition candidates, and dialect-sensitive source ownership.
  - Detect explicit dialect names, SQLite/PostgreSQL schema metadata, driver-specific SQLite error handling, raw dialect placeholder/locking constructs, and equivalent implementation-time patterns with narrow deterministic rules; classify `internal/infra/db` as shared infrastructure.
  - Compare discovered dual-dialect candidates/migration roots against the catalog; fail on unregistered candidates, stale catalog paths, multiple owners, or migration files outside declared roots.
  - Validate that important registered store ports have explicit compile-time interface assertions where practical; add missing assertions at the component boundary rather than using runtime reflection.
  - Add fixture/unit coverage proving that a synthetic new dual-dialect package/migration fails until registered and that legitimate shared DB infrastructure is not falsely classified.
  - _Requirements: 1.1–1.5, 4.3, 6, 10.6_
  - _Boundary: architecture tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/archtest -run DatabaseParity`_

## Phase 2 — Normalize Canonical Component Parity Entry Points

- [x] 2. Make every registered component expose the same stable SQLite/PostgreSQL-direct proof shape.

- [x] 2.1 Standardize continuity parity contracts
  - Consolidate A-leg lifecycle, B-leg allocation/order, attempt lineage, restart persistence, interleaved-state, conversation-view, and route-override common assertions into reusable component-owned suites, reusing existing `b2buatest` and routeoverride storecontract helpers.
  - Add thin stable `TestDBParity_SQLite` and integration-tagged `TestDBParity_PostgresDirect` entry points that invoke the same common suites with backend-specific fixtures.
  - Ensure the common suite explicitly covers concurrent/monotonic B-leg allocation so both PostgreSQL `UPDATE ... RETURNING` and SQLite transaction serialization are tested against the same logical invariant.
  - Keep PostgreSQL multi-handle visibility/locking tests as additional PostgreSQL evidence without forking the common contract.
  - _Requirements: 2, 3.1–3.4, 5, 8.2, 10_
  - _Boundary: continuity driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/core/continuity/bunstore -run '^TestDBParity_SQLite$'`; PostgreSQL mode via Phase 5 runner_

- [x] 2.2 Normalize secure-session parity entry points (P)
  - Reuse the existing `storecontract.RunAll` as the canonical behavioral contract rather than copying it.
  - Add/rename thin SQLite and PostgreSQL-direct wrappers to the stable parity entry-point convention and include restart/durability plus current optional store capabilities that are promised by both durable backends.
  - Preserve backend-specific fixture isolation and secret-safe PostgreSQL DSN handling.
  - _Requirements: 2, 3, 5, 8.2, 10_
  - _Boundary: secure-session driven-adapter/storecontract tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/core/securesession/storecontract -run '^TestDBParity_SQLite$'`; PostgreSQL mode via Phase 5 runner_

- [x] 2.3 Normalize control-plane ledger parity entry points (P)
  - Reuse the current ledger `contract.RunSuite` for both engines and wrap it in stable parity test names.
  - Ensure the common contract exercises filters, pagination, dedupe/source-key behavior, projections, retention, redaction, error mapping, and BOOLEAN/INTEGER presence semantics so the store's manual placeholder/bind adaptation is covered by both engines.
  - Keep PostgreSQL-specific schema/catalog assertions separate from common behavior while mapping them to common logical invariants in Phase 3.
  - _Requirements: 2, 3.2–3.3, 5, 8.2_
  - _Boundary: control-plane driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/controlplane/ledgerstore -run '^TestDBParity_SQLite$'`; PostgreSQL mode via Phase 5 runner_

- [x] 2.4 Normalize usage-authority parity entry points (P)
  - Reuse the existing authority `contract.RunSuite` for common behavior on both engines.
  - Include idempotent reserve/settle/release/apply-usage, replay/fact convergence, lost-update prevention, typed capacity failures, and common query/readiness semantics.
  - Preserve PostgreSQL cross-instance strict tests as `postgres-distributed` capability evidence and SQLite `BEGIN IMMEDIATE`/single-node behavior as SQLite posture evidence.
  - Do not weaken existing authority/pooler tests to fit the common wrapper.
  - _Requirements: 2, 3, 5, 8.2_
  - _Boundary: usage-authority driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/usageauthority/authoritystore -run '^TestDBParity_SQLite$'`; direct/pooler focused tests remain available_

- [x] 2.5 Normalize concurrency-authority parity entry points (P)
  - Extract/reuse the existing five-slot/acquire-renew-release-reclaim contract as the common logical suite for SQLite and PostgreSQL-direct.
  - Add stable parity wrappers and ensure common idempotency/CAS/capacity/query behaviors execute on both engines.
  - Catalog and retain PostgreSQL distributed-strict multi-instance/row-lock evidence separately from SQLite's explicitly single-node serialized-writer posture.
  - Keep pooler-specific tests under `postgres-pooler` evidence rather than allowing them to silently skip inside common parity.
  - _Requirements: 2, 3, 5, 8.2_
  - _Boundary: concurrency-authority driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/concurrencyauthority/leasestore -run '^TestDBParity_SQLite$'`; direct/pooler focused tests remain available_

- [x] 2.6 Normalize metering-journal parity entry points (P)
  - Identify the canonical common journal behaviors across existing phase/contract tests and consolidate them behind stable SQLite/PostgreSQL-direct wrappers without duplicating domain assertions.
  - Cover append/idempotency, corrections/reconcile-relevant persistence, query/readback, restart durability, store-scoped filters/keys, and common error semantics.
  - Keep SQLite BUSY/LOCKED bounded retry behavior as explicit SQLite-specific capability evidence and PostgreSQL pooled support as explicit pooler evidence.
  - _Requirements: 2, 3, 5, 8.2_
  - _Boundary: metering driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/metering/journalstore -run '^TestDBParity_SQLite$'`; direct/pooler focused tests remain available_

- [x] 2.7 Normalize terminal-work parity entry points (P)
  - Consolidate the durable work-store behaviors promised on both engines into one common suite and stable SQLite/PostgreSQL-direct wrappers.
  - Cover append/state-transition/idempotency, generation/instance identity persistence, restart/readback, contention-sensitive claims where common, and current migration compatibility.
  - Preserve PostgreSQL pooled/topology-specific evidence separately.
  - _Requirements: 2, 3, 5, 8.2_
  - _Boundary: terminal-work driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/terminalwork/workstore -run '^TestDBParity_SQLite$'`; direct/pooler focused tests remain available_

- [x] 2.8 Normalize billing-store parity entry points (P)
  - Inventory the existing billing store contract/invariant tests and identify the smallest canonical common suite covering persisted financial-record semantics without re-running every unrelated billing orchestration test.
  - Add stable SQLite/PostgreSQL-direct wrappers covering common account/opening/snapshot/exposure/usage/journal/provider-maintenance persistence invariants, idempotency/uniqueness, and error behavior already promised by the store.
  - Preserve existing billing-convergence certification as stronger domain evidence; the new parity wrapper must not replace financial correctness gates with a weaker generic test.
  - _Requirements: 2, 3, 5, 8.2–8.3, 10_
  - _Boundary: billing driven-adapter tests_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/infra/billingstore -run '^TestDBParity_SQLite$'`; direct PostgreSQL and billing convergence remain available_

- [x] 2.9 Add architecture checks for stable wrapper coverage after component normalization
  - Extend the discovery guard to verify that every catalog component has exactly the required stable SQLite and PostgreSQL-direct parity entry point(s) in its registered test package(s).
  - Verify non-common capability evidence anchors resolve to real tests and cannot be represented by a missing/empty pattern.
  - Add a negative fixture for a cataloged component with a missing PostgreSQL wrapper.
  - _Requirements: 1, 2.5, 5.2–5.4, 6, 9.5_
  - _Boundary: architecture tests_
  - _Depends: 2.1–2.8_
  - _Validation: `go test ./internal/archtest -run DatabaseParity`_

## Phase 3 — Enforce Migration History and Logical Schema Parity

- [x] 3. Build dual-engine migration/schema proof for every registered component.

- [x] 3.1 Implement reusable migration-file discovery and applied-history assertions
  - Discover versioned migration IDs from each catalog `MigrationRoot` using the repository's timestamped Go migration naming convention, excluding `_test.go` and explicitly recognized non-migration files deterministically.
  - Give each component parity suite a helper that verifies every discovered migration ID is recorded after empty-to-current migration on SQLite and PostgreSQL.
  - Fail on a new versioned migration file that is not exercised/applied by the component's migration registry on either backend.
  - Test duplicate/missing/out-of-order discovery and error messages without connecting to a database.
  - _Requirements: 4.1–4.4, 4.7, 6.1, 8.6_
  - _Boundary: tests / migration parity support_
  - _Depends: 1.2–1.3, 2.9_
  - _Validation: `go test ./internal/testkit/dbparity/... ./internal/archtest -run 'Migration|DatabaseParity'`_

- [x] 3.2 Define common logical schema invariant sets per component
  - Inventory the correctness-relevant tables, columns, nullability/defaults, PK/FK/unique/check constraints, partial/correctness-critical indexes, immutability protections, and retired artifacts relied upon by each component.
  - Reuse existing `VerifySchema`/migration tests where they already express those invariants; billing's dual-engine schema verification is the reference pattern.
  - Keep invariant declarations component-owned; do not create a generic table-schema DSL more complex than the current need.
  - Document intentional engine mechanism differences while keeping the logical invariant common.
  - _Requirements: 4.5–4.6, 8.4, 10_
  - _Boundary: component migration/schema tests_
  - _Depends: 2.1–2.8, 3.1_
  - _Validation: focused component schema test packages_

- [x] 3.3 Strengthen continuity and secure-session schema parity (P)
  - Verify both engines contain equivalent logical A-leg/B-leg/attempt, interleaved-state, route-override, conversation-view, secure-session, transcript/audit/usage/attempt/quarantine structures and correctness-critical indexes/constraints after all migrations.
  - Verify migration history contains every discovered migration ID and rerunning migration is idempotent.
  - Exercise existing legacy SQLite compatibility fixtures and add PostgreSQL upgrade/current-state evidence where the same logical migration contract applies.
  - _Requirements: 4, 8.4–8.6_
  - _Boundary: continuity + secure-session migration tests_
  - _Depends: 3.1–3.2_
  - _Validation: stable parity wrappers plus focused migration tests_

- [x] 3.4 Strengthen control-plane, usage-authority, concurrency, metering, and terminal-work schema parity (P)
  - Replace “table exists” proof with equivalent correctness-relevant invariant checks wherever PostgreSQL currently receives materially deeper catalog validation than SQLite or vice versa.
  - Verify store-scoped keys, uniqueness/capacity indexes, filter indexes, foreign keys/checks, and migration history required by current behavior.
  - Preserve backend-specific performance-only indexes as backend-specific capability/evidence if they are not part of common correctness semantics.
  - _Requirements: 4, 5, 8.4–8.6_
  - _Boundary: dual-plane/control-plane/terminal migration tests_
  - _Depends: 3.1–3.2_
  - _Validation: stable parity wrappers plus focused component migration tests_

- [x] 3.5 Reconcile billing migration/schema parity with the catalog (P)
  - Reuse the existing deep SQLite/PostgreSQL `VerifySchema` logic and required migration protections rather than reimplementing them in testkit.
  - Connect discovered migration-file inventory to the billing parity wrapper so adding a migration file without registration/history/schema evidence fails.
  - Confirm retired tables/columns, immutable ledger protections, unique indexes, sequence/nullability semantics, and provider-maintenance integrity are verified on both engines at the current contract level.
  - _Requirements: 4, 8.4–8.6_
  - _Boundary: billing migration tests_
  - _Depends: 3.1–3.2_
  - _Validation: billing parity wrapper + existing billing migration/verification tests_

## Phase 4 — Run the Brownfield Matrix and Fix Current Divergence

- [x] 4. Certify the current tree on real SQLite and PostgreSQL before turning enforcement on.

- [x] 4.1 Add a temporary/local implementation-time full-matrix command and execute the untouched baseline
  - Using the catalog package list and stable wrappers, execute all SQLite parity suites.
  - Execute all PostgreSQL-direct parity suites against a real direct PostgreSQL endpoint with `LIP_REQUIRE_POSTGRES=1`; set admin/runtime DSNs explicitly and exclude pooler-only tests by selection, not by accidental skip.
  - Record each component result and every common-contract/schema/migration failure in the implementation PR notes or test logs; do not add permanent scratch artifacts to the spec directory.
  - _Requirements: 7.1–7.4, 8.1, 8.6, 9.1–9.2_
  - _Boundary: tests / evidence_
  - _Depends: 2.1–3.5_
  - _Validation: implementation-time parity runner on real SQLite/PostgreSQL_

- [x] 4.2 Repair common behavioral/transactional divergences revealed by the baseline
  - For each failure, classify whether the canonical contract is correct, stale, or missing an intentional capability distinction before editing production code.
  - Fix real parity defects at the owning adapter/migration boundary; add RED regression subtests to the shared common suite before the fix.
  - Run focused `-race` tests when fixing sequence/locking/lost-update/idempotency concurrency behavior.
  - Do not resolve a failure by moving a common promise into a backend-specific exception unless the existing product/store contract genuinely differs.
  - Observable completion: all common behavioral/transactional subtests are green on both engines.
  - _Requirements: 2, 3, 5.5, 8.1–8.2, 8.6, 10.4_
  - _Boundary: affected driven adapters + shared contract tests_
  - _Depends: 4.1_
  - _Validation: affected component parity wrappers + targeted `-race`_

- [x] 4.3 Repair migration/schema divergences revealed by the baseline
  - Fix missing columns/constraints/indexes/immutability protections/history registration or incorrect schema verifiers exposed by Phase 4.1.
  - Preserve existing operator data compatibility; use additive/idempotent migration repairs consistent with each component's migration policy rather than destructive test-only shortcuts.
  - If a difference is intentional and backend-specific, add the explicit capability/invariant classification and dedicated evidence rather than weakening the common schema contract.
  - Observable completion: every discovered migration ID and common logical schema invariant is green on both engines.
  - _Requirements: 4, 5, 8.1–8.6, 10.4–10.6_
  - _Boundary: affected driven adapters / migrations / tests_
  - _Depends: 4.1_
  - _Validation: affected component parity wrappers + migration tests_

- [x] 4.4 Freeze the certified catalog baseline
  - Re-run architecture discovery and full SQLite/PostgreSQL-direct parity after all repairs.
  - Confirm no remaining unclassified skip/exception exists for a common capability and no registered component is red.
  - Update catalog capability rationale/evidence only for verified intentional differences.
  - This green baseline is the prerequisite for Phase 6 merge-blocking CI.
  - _Requirements: 1, 5, 8.6_
  - _Boundary: tests / evidence_
  - _Depends: 4.2–4.3_
  - _Validation: full local parity matrix + `go test ./internal/archtest -run DatabaseParity`_

## Phase 5 — Make the Catalog the Canonical Local/CI Runner

- [x] 5. Implement the durable developer/CI entry points without duplicate package lists.

- [x] 5.1 Implement the typed database-parity runner
  - Add the internal runner with `list`, `sqlite`, `postgres-direct`, and `all` modes.
  - Construct `go test` package lists and stable `-run` selectors from the catalog; direct mode adds integration tags, fail-closed PostgreSQL environment, and explicit pooled-test exclusion.
  - Preserve repository test parallelism/timeouts where appropriate; propagate subprocess failures/cancellation exactly.
  - Sanitize output so DSNs/credentials are never echoed; identify failures by component/package/backend.
  - Add unit tests for command planning, mode validation, environment handling, deterministic ordering, and redaction.
  - _Requirements: 7.2–7.4, 9.1–9.2, 9.6, 10.3, 10.6_
  - _Boundary: tests / internal testkit command_
  - _Depends: 4.4_
  - _Validation: `go test ./internal/testkit/dbparity/...` plus runner `list`/SQLite smoke_

- [x] 5.2 Add canonical Makefile targets and preserve specialized PostgreSQL gates
  - Add `test-db-parity-sqlite`, `test-db-parity-postgres-direct`, and `test-db-parity` targets that delegate to the runner and do not repeat component package lists.
  - Add Makefile help text distinguishing repository-wide direct parity from the existing `test-authority-postgres-direct`, `test-authority-postgres-pooled`, `test-postgres-migrations`, and billing-convergence/release gates.
  - Keep current pooler normal-parallelism/fail-closed assertions green and add policy tests proving the new general targets delegate to the catalog runner.
  - _Requirements: 7, 9.1–9.3, 10.6_
  - _Boundary: build/test orchestration_
  - _Depends: 5.1_
  - _Validation: `go test ./internal/testkit -run 'Makefile.*Postgres|Makefile.*DBParity'`; `make test-db-parity-sqlite`_

## Phase 6 — Make Direct DB Parity a Merge-Blocking PR Invariant

- [x] 6. Wire real PostgreSQL parity into PR CI and fail closed through an existing required status.

- [x] 6.1 Add the ephemeral direct-PostgreSQL database-parity CI job
  - Extend `.github/workflows/ci.yml` using the existing change-scope output; run the DB job for every test-relevant PR and emit an explicit success/bypass step for documentation-only changes.
  - Provision a pinned direct PostgreSQL service container with test-only credentials and a health check; do not use repository secrets or claim the service is a transaction pooler.
  - Export both runtime and admin test DSNs to the service plus `LIP_REQUIRE_POSTGRES=1`; do not set `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER`.
  - Run `make test-db-parity` (or the exact canonical full mode) so SQLite and all registered PostgreSQL-direct wrappers execute from the catalog.
  - Ensure service-health, missing-env, test failure, timeout, or cancellation produces a failed job; no optional `continue-on-error`.
  - _Requirements: 7.1–7.5, 9.1–9.2, 10.6_
  - _Boundary: CI / tests_
  - _Depends: 5.2_
  - _Validation: workflow syntax + QA workflow-policy test; PR run on implementation branch_

- [x] 6.2 Propagate the DB parity result through the existing merge-blocking aggregate status
  - Extend the existing required `repo-hygiene`/equivalent fail-closed aggregator using `if: always()` so test-relevant changes fail when database parity is not successful.
  - Preserve the existing rule that docs-only PRs do not leave a required check in `skipped` state; the aggregate must report success after an explicit parity bypass.
  - Add repository QA tests that inspect workflow dependency/result handling and fail if future edits detach the DB parity job from the required aggregate.
  - Do not rely solely on a manual branch-protection update for correctness of this spec.
  - _Requirements: 7.5–7.7, 9.5_
  - _Boundary: CI policy / QA tests_
  - _Depends: 6.1_
  - _Validation: `go test ./internal/qa -run 'DatabaseParity|CI'`; implementation PR required-status behavior_

- [x] 6.3 Prove mandatory PostgreSQL mode cannot silently skip
  - Add/extend testkit tests for the canonical runner and stable PostgreSQL wrappers so mandatory direct mode fails with actionable env/service errors when no DSN exists.
  - Ensure pooler-only wrappers remain explicitly out of the direct selector instead of being treated as common parity skips.
  - Preserve optional `SkipUnlessPostgres` behavior for ad-hoc integration runs that are not invoked through the mandatory parity mode.
  - _Requirements: 5.3–5.4, 7.2–7.4, 9.2–9.3_
  - _Boundary: testkit / component integration tests_
  - _Depends: 6.1–6.2_
  - _Validation: testkit env/runner tests + CI parity run_

## Phase 7 — Reconcile Documentation, Release Gates, and Final Proof

- [x] 7. Make executable parity the documented source of truth and certify the finished system.

- [x] 7.1 Align steering, database docs, release gates, and Make help with actual execution
  - Update `.kiro/steering/testing.md` and `.kiro/steering/tech.md` to distinguish default unit tests, repository-wide direct DB parity, specialized PostgreSQL distributed/pooler gates, and release/race evidence.
  - Update `docs/release-gates.md` so PR CI claims match the actual workflow and name the canonical DB parity command/component catalog.
  - Update `docs/database-persistence.md` with a maintainer-facing parity/enforcement note without exposing test implementation as operator configuration.
  - Update `AGENTS.md` only with the concise rule that dual-dialect persistence changes must pass/extend the canonical parity contract, if that guidance belongs there.
  - Remove stale claims that PR QA runs a full tagged suite when the workflow does not; document the actual job ownership.
  - _Requirements: 8.5, 9.3–9.5_
  - _Boundary: docs / steering_
  - _Depends: 6.1–6.3_
  - _Validation: docs/knowledge checks + QA database-parity policy test_

- [x] 7.2 Add documentation/CI/catalog drift guardrails
  - Extend `internal/qa` so the canonical Make targets, workflow job/required aggregation, and named steering/release-gate references remain present and consistent.
  - Keep the executable catalog authoritative; tests should fail when docs or Make/workflow wiring names a stale target/component scope rather than relying on reviewers to notice drift.
  - Avoid brittle full-file snapshots; assert stable contract markers and catalog-derived expectations.
  - _Requirements: 9.4–9.5_
  - _Boundary: QA tests_
  - _Depends: 7.1_
  - _Validation: `go test ./internal/qa -run DatabaseParity`_

- [x] 7.3 Run final whole-repository database parity and regression certification
  - Run `make test-db-parity` against real direct PostgreSQL and confirm all registered SQLite/PostgreSQL common capabilities and migrations are green.
  - Run existing PostgreSQL direct/pooled specialized gates applicable to authority/concurrency/metering/terminal-work; preserve explicit topology attestation requirements.
  - Run billing convergence/migration evidence applicable to billing-store changes made during remediation.
  - Run `make quality-checks`, `make test`, focused `-race` for any concurrency fixes, and architecture/QA database-parity gates.
  - Confirm normal runtime configuration, public contracts, store selection, pooler rules, and secret-safe errors are unchanged except for specifically fixed parity defects.
  - _Requirements: 1–10_
  - _Boundary: tests / final certification_
  - _Depends: 7.1–7.2 and all remediation tasks_
  - _Validation: `make test-db-parity`; existing specialized PostgreSQL/billing gates; `make quality-checks`; `make test`; focused race tests_

- [x] 7.4 Close #438 only after enforcement is demonstrably fail-closed
  - Post implementation evidence to #438 naming the cataloged components, common vs backend-specific capability model, canonical commands, CI merge-blocking path, and any real parity defects repaired during the baseline wave.
  - Include evidence that an intentionally broken/unregistered dual-dialect candidate or failing PostgreSQL parity wrapper blocks the relevant architecture/CI gate.
  - Do not close the issue merely because Bun is shared; close only when the executable enforcement model is in place and green.
  - _Requirements: 1, 6–9_
  - _Boundary: issue tracking / evidence_
  - _Depends: 7.3_
  - _Validation: #438 implementation evidence links to merged CI/tests_
