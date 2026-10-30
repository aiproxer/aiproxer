# Research & Design Decisions

## Summary

- **Feature**: Database Dialect Parity Enforcement (#438)
- **Discovery Scope**: Brownfield complex integration / repository-wide persistence correctness gate
- **Key Findings**:
  - Bun already eliminates much of the duplicate implementation risk: important durable stores now use one Go implementation for SQLite and PostgreSQL.
  - Bun is not a semantic parity mechanism. The live tree contains correctness-significant differences in transaction algorithms, row locking, DDL, placeholder/bind handling, driver-specific retry behavior, and capability posture.
  - The repository already has strong reusable contract-test patterns, but PostgreSQL execution is environment-gated and not a universal merge invariant.
  - The implementation-time source inventory contains eight `bun/migrate` families: continuity, secure sessions, control-plane ledger, usage authority, concurrency authority, metering journal, terminal work, and billing.
  - The existing `test-authority-postgres-*` Make targets are intentionally focused on dual-plane authority/concurrency/metering/terminal-work evidence and therefore do not constitute repository-wide SQLite/PostgreSQL parity proof.
  - Current testing/release documentation claims broader PR integration/PostgreSQL execution than the inspected `qa.yml` workflow actually performs; this drift is itself evidence that parity must be executable rather than documentary.

## Research Log

### Issue #438 and prior audit
- **Context**: The issue asks whether Bun already guarantees database-type parity or whether future features can diverge between SQLite and PostgreSQL.
- **Sources Consulted**: issue #438 and its audit comments; `internal/infra/db/bun.go`; runtime store composition; durable store implementations; migration files; contract tests; Makefile; GitHub Actions workflows; steering/testing docs.
- **Findings**:
  - `NewBunDB` selects either `pgdialect` or `sqlitedialect` around the supplied `*sql.DB`; it does not impose equivalent engine semantics.
  - The current tree has moved beyond the archived `bun-database-abstraction` design in an important way: continuity and secure-session SQLite runtime paths now also use their Bun-backed stores, rather than preserving entirely separate raw-SQL SQLite adapters.
  - The right problem statement is therefore “prove the remaining dialect-sensitive semantics,” not “invent a new database abstraction.”
- **Implications**: Preserve Bun and focus new architecture on test/QA enforcement, contract reuse, schema verification, and real-engine CI.

### Production dual-dialect inventory
- **Context**: Enforcement needs a finite current scope and a mechanism to discover future scope.
- **Sources Consulted**: repository search for `migrate.NewMigrations`, `DialectSQLite`, PostgreSQL integration tests, runtime store selection, and store packages.
- **Findings**:

| Component | Primary package/root | Current shared implementation posture | Important dialect-sensitive examples |
|---|---|---|---|
| Continuity | `internal/core/continuity/bunstore` | One Bun store used by SQLite and PostgreSQL | PostgreSQL `UPDATE ... RETURNING` B-leg allocation vs SQLite select/increment/update; PostgreSQL `FOR UPDATE`; separate DDL |
| Secure sessions | `internal/core/securesession/adapters/bunstore` | One Bun store, shared store contract already exists | separate SQLite/PostgreSQL DDL, SQLite PRAGMA/upgrade paths, BLOB/BYTEA and identity-column differences |
| Control-plane ledger | `internal/infra/controlplane/ledgerstore` | One durable store | underlying `database/sql` use, manual `$n`/`?` placeholders, BOOLEAN vs INTEGER bind adaptation |
| Usage authority | `internal/infra/usageauthority/authoritystore` | One durable store, shared contract suite | PostgreSQL row locks vs SQLite `BEGIN IMMEDIATE`; PostgreSQL schema catalog checks deeper than some SQLite probes |
| Concurrency authority | `internal/infra/concurrencyauthority/leasestore` | One durable store | PostgreSQL distributed-strict reference vs SQLite single-node serialized writers; direct/pooler-specific tests |
| Metering journal | `internal/infra/metering/journalstore` | One durable store | SQLite BUSY/LOCKED retry classification/backoff; direct/pooler PostgreSQL paths; migrations |
| Terminal work | `internal/infra/terminalwork/workstore` | Bun-backed dual-dialect durable store | dialect-sensitive migrations and PostgreSQL direct/pooler evidence |
| Billing | `internal/infra/billingstore` | Bun-backed dual-dialect store | large migration history, dialect-specific schema introspection/DDL, strong existing `VerifySchema` model |

- **Implications**: The first implementation task must re-run this inventory against then-current `main`; the catalog cannot assume the list stays eight forever.

### Existing contract-test assets
- **Context**: Avoid replacing good tests with a central mega-test that duplicates domain knowledge.
- **Sources Consulted**: secure-session storecontract, routeoverride storecontract, usage-authority contract package, control-plane ledger contract package, concurrency store tests, PostgreSQL integration tests.
- **Findings**:
  - Secure sessions already run `storecontract.RunAll` against SQLite and PostgreSQL.
  - Route overrides already run the same contract against both engines and add PostgreSQL cross-store visibility tests.
  - Usage authority and control-plane ledger already have reusable contract suites exercised by both SQLite and PostgreSQL fixtures.
  - Other components contain equivalent behavior tests but are less uniformly exposed as a single named dual-dialect contract entry point.
- **Implications**: Standardize thin component-local `TestDBParity_SQLite` and `TestDBParity_PostgresDirect` wrappers around existing common contract logic. Do not move all store behavior into one generic cross-domain test framework.

### Transaction semantics cannot be abstracted away
- **Context**: Determine whether additional ORM/repository layering could eliminate the concern.
- **Sources Consulted**: continuity `NextBLeg`, route-override locking, authority durable locking, SQLite DSN configuration, concurrency readiness posture.
- **Findings**:
  - PostgreSQL uses row locks and atomic `UPDATE ... RETURNING` in places where SQLite relies on write-transaction serialization.
  - SQLite writer semantics depend on connection/DSN configuration (`_txlock=immediate`, bounded connections) in several correctness-sensitive stores.
  - Some differences are product capability differences rather than implementation details: distributed strict concurrency is a PostgreSQL promise, not a SQLite promise.
- **Implications**: “Parity” must be capability-aware. Common logical results are shared-contract rows; topology-specific guarantees are explicit capability rows with separate evidence.

### Migration and schema drift risk
- **Context**: Bun query building does not remove handwritten per-dialect DDL.
- **Sources Consulted**: continuity and secure-session baseline migrations, billing migration family, `VerifySchema` implementations, search for all `migrate.NewMigrations` families.
- **Findings**:
  - Separate SQLite/PostgreSQL DDL bodies are common and legitimate.
  - Billing already demonstrates a stronger approach: verify logical protections through SQLite metadata and PostgreSQL catalogs rather than comparing SQL text.
  - Other components have less symmetric verification depth, creating room for a column/index/constraint to be present on one backend but weakly checked on the other.
- **Implications**: Migration parity must discover migration IDs from owned source roots, run both engines, and compare declared logical invariants through engine-specific introspection.

### Current PostgreSQL test gating and CI drift
- **Context**: Determine whether the existing tests already make parity merge-blocking.
- **Sources Consulted**: `internal/testkit/postgres_env.go`, Makefile PostgreSQL targets, `internal/testkit/postgres_makefile_gate_test.go`, `.github/workflows/ci.yml`, `.github/workflows/qa.yml`, `docs/release-gates.md`, `.kiro/steering/testing.md`.
- **Findings**:
  - PostgreSQL integration tests use `SkipUnlessPostgres`; they skip unless a DSN is configured and fail-closed mode is explicitly enabled.
  - `test-authority-postgres-direct` covers usage authority, concurrency authority, metering, and terminal work. That is valuable topology evidence but not the full eight-component parity scope.
  - The current QA workflow inspected during this research does not provision PostgreSQL or run a full `-tags=precommit,integration ./...` pass, despite documentation that describes broader PR evidence.
  - The CI workflow has an existing required `repo-hygiene` status pattern designed to fail closed rather than relying on skipped required jobs.
- **Implications**: Add a real PostgreSQL service-container job for all registered components and feed its result into an already merge-blocking aggregate status. Do not depend solely on adding a new branch-protection check outside the repository.

### PostgreSQL transaction-pooler topology
- **Context**: Decide whether every PR must also run PgBouncer/transaction-pooler evidence.
- **Sources Consulted**: steering `Database & PgBouncer Standards`, `SkipUnlessPostgresPooled`, Makefile pooled target and its QA pinning test.
- **Findings**:
  - Pooler safety is a separate topology contract requiring explicit runtime attestation and often separate admin/runtime endpoints.
  - A standard GitHub Actions PostgreSQL service container is a direct database, not a transaction pooler.
- **Implications**: The mandatory PR parity gate covers SQLite + direct PostgreSQL common behavior. Existing fail-closed pooled gates remain specialized release/CI evidence and are explicitly classified rather than silently skipped as “parity.”

## Brownfield Gap Analysis

### Existing strengths to preserve
1. Bun is already the common implementation layer for the main dual-dialect stores.
2. Consumer-owned store ports already isolate database mechanics from core orchestration.
3. Reusable contract suites exist in several high-value domains.
4. PostgreSQL test environment helpers already implement fail-closed vs optional semantics.
5. The repository already has architecture/QA tests that pin Makefile and release-gate contracts.
6. Billing has strong dual-engine schema verification that can serve as a pattern rather than being rewritten.

### Gaps that required requirements repair
1. **Scope gap**: initial issue language focused on “two DB types” abstractly, but the codebase has multiple independently evolved dual-dialect store families. Requirements were expanded to require an authoritative component/port inventory rather than one generic test.
2. **Capability gap**: strict equality would incorrectly classify PostgreSQL distributed guarantees and SQLite operational behavior as defects. Requirements now distinguish common logical parity from explicit backend/topology capabilities.
3. **Migration gap**: shared Bun DML alone cannot catch duplicated DDL drift. Requirements now include migration-ID discovery, empty/current/upgrade evidence, and logical schema invariants.
4. **Enforcement gap**: optional integration tests do not satisfy the user's “from now on” requirement. Requirements now demand fail-closed real PostgreSQL execution on every test-relevant PR.
5. **Merge-blocking gap**: a new workflow job is not necessarily a protected check. Requirements now require parity failure to propagate through an already merge-blocking aggregate status.
6. **Coverage gap**: the existing direct PostgreSQL Make target intentionally omits several dual-dialect domains. Requirements now call for a repository-wide parity command derived from the inventory while preserving specialized authority/pooler gates.
7. **Documentation gap**: steering/release docs currently describe evidence that does not match the inspected workflow. Requirements now make executable catalog/CI behavior authoritative and QA-test documentation consistency.
8. **Architecture gap**: an enforcement solution could accidentally become a generic production DB registry. Requirements explicitly confine the catalog/runner to test/QA infrastructure and preserve current runtime wiring.

### Active-spec overlap / design validation
- `high-concurrency-performance-hardening` is active and explicitly names secure-session durable-store work plus database/pooler restrictions as revalidation-sensitive surfaces. This parity spec must not freeze today's secure-session persistence shape as permanent; implementation must re-inventory after any high-concurrency store/schema work lands and must make the new/changed contract part of the parity catalog.
- `aiproxer-complete-rebranding`, `agent-loop-breach-prevention`, and reasoning-preservation work do not currently establish competing database ownership.
- A-leg lifecycle hardening may change continuity behavior; if its implementation changes consumer-owned B2BUA/continuity store contracts, the parity component contract must revalidate rather than duplicating an older contract surface.
- No active spec justifies delaying the parity catalog itself: the catalog is intentionally designed to discover implementation-time store/migration changes instead of hard-coding the current tree.

### Compatibility constraints
- No public API or plugin contract should gain database types.
- No runtime reflection/DI/`init()` registry.
- No automatic data migration between engines.
- No weakening of pooler restrictions or secret-safe DSN handling.
- Existing component-specific integration tests should be reused and renamed/wrapped where possible rather than deleted wholesale.
- Test infrastructure must remain usable on developer machines without forcing all default unit tests to require PostgreSQL; the merge parity target is the fail-closed external-engine proof.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| New production repository abstraction | Add another interface/implementation layer above Bun | Could centralize some SQL choices | Duplicates existing consumer-owned ports; cannot erase engine semantics; production churn | Reject |
| Bun-only trust | Treat shared Bun code as parity proof | Zero new infrastructure | Does not prove DDL, locking, driver or capability semantics; current dialect branches disprove premise | Reject |
| Central generic cross-domain store implementation | Move all persistence behavior into one generic store framework | One place to test | Violates domain ownership; unsuitable for very different stores; high migration risk | Reject |
| Test-side parity catalog + component-owned shared contracts + real-engine CI | Keep stores where they are; enumerate common capabilities/migrations; run both engines fail-closed | Preserves boundaries; reuses tests; catches future unregistered stores/migrations; no runtime cost | Requires initial normalization of test entry points and static discovery rules | **Select** |

## Design Decisions

### Decision: Keep Bun as the production database abstraction
- **Context**: #438 questions whether Bun is sufficient.
- **Alternatives Considered**: replace Bun, add a repository layer, or retain Bun and add executable proof.
- **Selected Approach**: retain `database/sql` + Bun + existing drivers and store ports; add test/QA parity infrastructure.
- **Rationale**: the current problem is semantic verification, not lack of a query abstraction.
- **Trade-offs**: dialect branches remain visible; tests must exercise them.
- **Follow-up**: architecture guard classifies new dialect-sensitive code and forces registered component suites to run.

### Decision: Use a typed test-side component/capability catalog
- **Context**: hand-maintained Makefile package lists already represent only subsets of database concerns.
- **Alternatives Considered**: duplicate package lists in workflows, generated YAML, runtime registry.
- **Selected Approach**: a typed Go catalog under `internal/testkit/dbparity` listing component IDs, package/source roots, consumer-owned contract IDs, migration roots, common capabilities, and explicit backend-specific capabilities/evidence.
- **Rationale**: Go validation is deterministic, refactor-friendly, and does not leak into production.
- **Trade-offs**: metadata needs upkeep; architecture discovery prevents silent omission.
- **Follow-up**: catalog validation compares against migration roots, dual-dialect composition/candidate scans, and component-local parity test entry points.

### Decision: Standardize component-local parity test entry points
- **Context**: domains already use different contract-test organizations.
- **Selected Approach**: each cataloged component exposes thin test wrappers with stable names such as `TestDBParity_SQLite` and `TestDBParity_PostgresDirect`; the wrappers call the domain's existing canonical contract/migration/schema suite.
- **Rationale**: the central runner does not need domain knowledge and the same domain contract remains authoritative.
- **Trade-offs**: some packages need extraction/refactoring of current test helpers before wrappers can be thin.
- **Follow-up**: archtest verifies wrapper presence in the registered test package(s).

### Decision: Prove migrations by logical invariants, not DDL equality
- **Context**: engine-specific DDL is necessary (`BIGSERIAL` vs SQLite autoincrement, `BYTEA` vs BLOB, catalog mechanisms, triggers, partial-index syntax).
- **Selected Approach**: discover migration IDs, run both engines, verify migration history and a component-owned logical invariant set using engine-specific introspection.
- **Rationale**: equivalent protection is what matters; textual DDL equality would be both brittle and wrong.
- **Trade-offs**: invariant declarations need careful maintenance.
- **Follow-up**: use billing's current schema verification depth as a reference and strengthen weaker SQLite/PostgreSQL verifiers.

### Decision: Direct PostgreSQL is the mandatory PR engine; pooler remains a separate topology gate
- **Context**: a standard ephemeral PostgreSQL service is cheap and secretless, while transaction-pooler evidence requires different topology and explicit attestation.
- **Selected Approach**: run SQLite + direct PostgreSQL parity on every test-relevant PR; keep pooler-specific tests under the existing fail-closed pooled gate/release evidence.
- **Rationale**: common SQL/transaction/migration parity becomes universal without misrepresenting a direct DB as a pooler.
- **Trade-offs**: pooler regressions are not covered by the new ephemeral service alone, so existing pooled gate remains mandatory where its contract applies.

### Decision: Propagate parity through an already merge-blocking aggregate status
- **Context**: adding a new workflow job does not itself guarantee branch protection.
- **Selected Approach**: make the existing required CI aggregator inspect the database-parity job result and fail closed for test-relevant changes when parity did not succeed.
- **Rationale**: enforcement ships in repository code rather than depending on a later manual settings change.
- **Trade-offs**: CI job dependencies must preserve docs-only success semantics and avoid a skipped required aggregate.

## Risks & Mitigations

- **Risk: parity catalog becomes another stale list** — add AST/filesystem discovery of migration families, dual-dialect composition/candidate packages, stable parity wrapper names, and QA consistency tests.
- **Risk: “same tests” still miss a new feature** — inventory consumer-owned contract surfaces, require common capabilities to live in canonical component contract suites, auto-discover migrations, and make new dialect-sensitive component changes always run the full suite.
- **Risk: excessive CI time** — run one ephemeral PostgreSQL service, execute only registered parity packages through stable wrappers, keep pooler/soak/race outside this direct parity job, and skip only documentation-only changes.
- **Risk: false-positive static dialect scanner** — classify shared DB infrastructure explicitly and use AST/path rules rather than an unrestricted string grep; require actionable errors and narrow allowlists.
- **Risk: agents “fix” parity by weakening tests** — common contract definitions are shared by both backend wrappers; capability exceptions require rationale plus dedicated evidence.
- **Risk: migration history is large** — discover IDs automatically and verify current migration history; use supported upgrade fixtures rather than fabricating a fixture for every historical intermediate state.
- **Risk: runtime architecture churn** — parity catalog/runner is test-only; production changes are limited to actual defects revealed by the common contracts.

## References

- GitHub issue #438 — feature request and audit comments defining the parity problem and recommended enforcement model.
- `.kiro/steering/product.md` — product boundaries and consumer-owned core/control-plane expectations.
- `.kiro/steering/tech.md` — Bun/SQLite/PostgreSQL stack and PgBouncer rules.
- `.kiro/steering/structure.md` — package ownership and architecture guardrails.
- `.kiro/steering/testing.md` — existing test/build-tag conventions and durable-store guidance.
- `.kiro/specs/archive/bun-database-abstraction/` — historical persistence feature; useful background but partially stale relative to current shared-Bun SQLite paths.
- `internal/infra/db/` — Bun dialect wrapper, PostgreSQL/SQLite opening, pool registry, schema helpers.
- `internal/testkit/postgres_env.go` and `postgres_makefile_gate_test.go` — existing fail-closed direct/pooler test semantics.
- `docs/release-gates.md`, `Makefile`, `.github/workflows/ci.yml`, `.github/workflows/qa.yml` — current executable/documented verification surfaces.
