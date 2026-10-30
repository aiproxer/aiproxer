# Implementation Gap Analysis: bun-database-abstraction

Generated: 2026-04-26

## Executive Summary

The codebase already has durable SQLite persistence for the two target domains: B2BUA continuity and secure sessions. Both are wired through narrow store interfaces and explicit composition-root factories, which gives this feature good integration seams. The main gap is not persistence in general; it is cross-dialect durable persistence, managed-database configuration, connection handling, and parity validation across SQLite and PostgreSQL.

Requirements are feasible without changing client-facing protocols or canonical request/event contracts. The highest-risk work is secure-session parity because the SQLite adapter is large and behavior-rich. The second major risk is preserving continuity transaction semantics under PostgreSQL while keeping the existing store contracts unchanged.

Requirements are currently generated but not approved in `spec.json`. Gap analysis proceeds because it can inform design and requirement revisions.

## Current State Investigation

### Existing Assets

| Area | Existing assets | Notes |
| --- | --- | --- |
| Continuity store port | `internal/core/b2bua/store.go`, `pkg/lipsdk/continuity/store.go` | Seven-method store contract used by executor and mirrored in SDK. |
| Continuity factory | `internal/core/continuity/store.go` | Opens `memory` or `sqlite`; validates in-memory compatibility at construction. |
| Continuity SQLite | `internal/core/continuity/sqlitestore/store.go` | Raw `database/sql`, `modernc.org/sqlite`, inline DDL, `SetMaxOpenConns(1)`, file DSN pragmas. |
| Secure-session port | `internal/core/securesession/app/ports.go` | `app.Store` plus `SessionUsageRollup`; consumed by manager, recorder, diagnostics. |
| Secure-session memory | `internal/core/securesession/adapters/memory/store.go` | Non-durable store used by default when enabled unless SQLite selected. |
| Secure-session SQLite | `internal/core/securesession/adapters/sqlite/store.go`, `internal/core/securesession/adapters/sqlite/schema.go` | Large raw-SQL adapter with sessions, attempts, transcript, audit, usage, summary, and readiness behavior. |
| Lineage bridge | `internal/core/securesession/adapters/b2bualineage/store.go` | Secure-session manager uses the same continuity store for A-leg/B-leg lineage. |
| Runtime composition | `internal/infra/runtimebundle/build.go`, `internal/infra/runtimebundle/secure_session.go`, `internal/infra/runtimebundle/built.go` | Builds continuity first, then secure-session runtime; collects closers for opened stores. |
| Config model | `internal/core/config/model.go` | Has `ContinuityConfig` and `SecureSessionConfig` with `store` and `sqlite_path`; no PostgreSQL fields. |
| Config validation | `internal/core/config/validate.go`, `internal/core/config/validate_test.go` | Allows `memory`/`sqlite` only; durable secure-session audit currently requires SQLite. |
| Sample config | `config/config.yaml` | Documents memory and SQLite examples only. |
| Tests | `internal/core/continuity/sqlitestore/store_test.go`, `internal/core/securesession/storecontract/*`, `internal/infra/runtimebundle/*` | Good SQLite/memory coverage; no PostgreSQL path or Bun path. |
| Dependencies | `go.mod` | Includes `modernc.org/sqlite`; no `uptrace/bun`, PostgreSQL driver, or shared database helper package. |

### Relevant Patterns and Constraints

- Existing store interfaces are consumer-owned and should remain unchanged.
- Runtime wiring is explicit, constructor-based, and located in composition roots rather than a DI container.
- SQLite driver registration intentionally lives inside store packages today, documented as a package-level exception.
- SQLite stores set `MaxOpenConns(1)`, which is a local-file concurrency behavior that should not be copied blindly to PostgreSQL.
- Durable startup failures should fail fast rather than silently fall back to memory, matching the existing explicit-store behavior.
- Secure-session durable audit is currently coupled to `store: sqlite`; this must generalize to any supported durable secure-session store.
- Steering requires no changes to client protocol surfaces, provider SDK boundaries, canonical models, or routing semantics for this feature.

## Requirement-to-Asset Map

| Requirement | Current support | Gap classification | Notes |
| --- | --- | --- | --- |
| Req 1: Configurable persistence selection | Memory/SQLite selection exists for both domains. | Missing | No managed durable backend option; no PostgreSQL DSN or pool config. |
| Req 2: Continuity behavior parity | SQLite continuity covers A-leg, B-leg, attempts, restart survival. | Missing / Constraint | Need PostgreSQL-capable continuity adapter and parity tests; transaction semantics must match. |
| Req 3: Secure-session behavior parity | Memory and SQLite stores exist; contract tests cover behavior. | Missing / High complexity | Need PostgreSQL-capable secure-session adapter; large behavior surface and schema portability work. |
| Req 4: Operator-safe DB configuration | SQLite path validation exists; some startup errors wrap context. | Missing / Unknown | Need DSN validation/redaction policy, pool tuning validation, and sample config updates. |
| Req 5: Backward compatibility/non-migration | Existing behavior can be preserved by keeping memory/SQLite paths. | Constraint | Design must decide whether `sqlite` stays raw SQL or moves through Bun without observable change. |
| Req 6: Verification/scope guardrails | Existing SQLite/memory tests; repo has contract-test patterns. | Missing | Need optional PostgreSQL validation path and explicit skip behavior. |

## Missing Capabilities

### Configuration and Validation

- No `postgres` or equivalent managed durable store enum for either continuity or secure sessions.
- No PostgreSQL DSN field, no shared or per-store pool tuning fields, and no validation for pool durations/counts.
- No secret-redaction helper specifically for database connection errors.
- Existing validation messages list only `memory` and `sqlite`.
- `audit_durability: durable` currently requires `secure_session.store: sqlite`, not any durable backend.

### Store Implementations

- No Bun-backed store adapter for continuity.
- No Bun-backed store adapter for secure sessions.
- No shared infrastructure for opening PostgreSQL or wrapping `*sql.DB` with Bun.
- No dialect-aware migration helpers for SQLite/PostgreSQL differences.
- No connection lifecycle sharing between continuity and secure-session stores.

### Testing and Operations

- No PostgreSQL integration test harness or environment variable gate.
- No CI service container or documented local PostgreSQL validation path.
- No architecture guard explicitly preventing Bun from leaking into public contracts or unrelated core packages.
- No docs/sample config for managed durable persistence.

## External Dependency Research

`uptrace/bun` supports wrapping an existing `*sql.DB` with dialect-specific constructors. Documentation shows `bun.NewDB(sqldb, sqlitedialect.New())` for SQLite and `bun.NewDB(sqldb, pgdialect.New())` with `pgdriver.NewConnector(pgdriver.WithDSN(dsn))` for PostgreSQL. Bun supports transaction management through `RunInTx`, model tags, inserts/selects/updates, `Ignore`, and `On("CONFLICT ...")` upsert patterns.

Research implications for design:

- A small `internal/infra/db` package can own common open/wrap helpers without changing store interfaces.
- DDL should remain explicit SQL where dialect differences matter, especially partial unique indexes and autoincrement identity columns.
- Bun can reduce query boilerplate, but it will not eliminate migration and dialect decisions.
- Design should choose exact dependency versions and decide whether to use Bun's `pgdriver` directly or a different PostgreSQL driver behind `database/sql`.

## Implementation Approach Options

### Option A: Extend Existing Raw-SQL Stores

Extend `sqlitestore` and secure-session `sqlite` patterns by adding parallel raw-SQL PostgreSQL adapters or conditional SQL in the existing stores.

**Files/modules to extend or mirror**
- `internal/core/continuity/store.go`
- `internal/core/continuity/sqlitestore/store.go`
- `internal/core/securesession/adapters/sqlite/store.go`
- `internal/core/securesession/adapters/sqlite/schema.go`
- `internal/infra/runtimebundle/secure_session.go`
- `internal/core/config/model.go`
- `internal/core/config/validate.go`

**Trade-offs**
- Pros: minimal new abstraction dependency; follows current raw-SQL style closely; straightforward behavior comparison.
- Cons: duplicates SQL across dialects; larger maintenance burden; does not satisfy the planned Bun abstraction direction cleanly; secure-session SQL surface remains very large.

**Fit**
- Feasible, but weaker alignment with the feature name and planning intent.

### Option B: Create New Bun-Backed Components

Add new Bun-backed adapters for both store domains plus shared database infrastructure. Existing SQLite stores remain intact and selectable.

**New components likely needed**
- `internal/infra/db/` for open/wrap helpers and pool tuning utilities.
- `internal/core/continuity/bunstore/` implementing `b2bua.Store`.
- `internal/core/securesession/adapters/bunstore/` implementing `app.Store` and `SessionUsageRollup`.
- New contract/parity tests under both store domains.

**Integration points**
- Extend `ContinuityConfig` and `SecureSessionConfig`.
- Extend validation and sample config.
- Extend `continuity.OpenStore` and `buildSecureSessionRuntime` with managed durable cases.
- Add closers for opened Bun/database handles.

**Trade-offs**
- Pros: clean separation, avoids bloating existing SQLite files, preserves fallback path, aligns with Bun abstraction goal, supports dialect-aware evolution.
- Cons: more files and new dependency; requires parity work across two adapter stacks; design must prevent abstraction leakage.

**Fit**
- Strong fit for requirements and steering if Bun remains inside adapter/infra packages.

### Option C: Hybrid Incremental Approach

Introduce shared database infrastructure and Bun adapters, but initially wire only PostgreSQL through Bun while preserving current SQLite runtime paths. Optionally route SQLite through Bun after parity confidence is high.

**Combination strategy**
- Add config and validation for managed durable stores.
- Add Bun continuity and secure-session stores for PostgreSQL.
- Keep current SQLite stores active for `store: sqlite` to reduce backwards-compatibility risk.
- Add optional Bun SQLite tests to prove cross-dialect behavior before changing runtime defaults.

**Trade-offs**
- Pros: safest backward compatibility, incremental adoption, easier rollback, satisfies managed database requirements first.
- Cons: two durable code paths remain; more parity matrix complexity; delayed simplification.

**Fit**
- Strong low-risk implementation path, especially for preserving existing SQLite behavior.

## Integration Challenges

1. **Secure-session adapter breadth**: the secure-session SQLite adapter covers create/load, activity touch, attempt trace/outcome, transcript, audit, usage, summaries, evidence merge, and readiness. This is the largest parity risk.
2. **Transaction and sequence semantics**: continuity `NextBLeg` and secure-session append/update operations rely on transactional behavior. PostgreSQL concurrency behavior must preserve monotonic sequences and not regress lineage ordering.
3. **Schema portability**: SQLite `AUTOINCREMENT`, `BLOB`, integer booleans, partial indexes, `INSERT OR IGNORE`, and duplicate-column migration behavior need PostgreSQL equivalents.
4. **Configuration ergonomics**: requirements describe local durable and managed durable behavior, but design must decide exact YAML field names, shared vs per-store pool tuning, and redaction rules.
5. **Lifecycle ownership**: continuity and secure-session stores may target the same PostgreSQL database or separate ones. Shared connection pooling is an optimization but impacts closer ownership and error attribution.
6. **Testing reliability**: local/default tests should remain deterministic and fast. PostgreSQL validation should be environment-gated with explicit skips.

## Complexity and Risk

- **Effort: L (1-2 weeks)** — two store domains, config/runtime changes, dependency addition, schema portability, and test expansion.
- **Risk: Medium-High** — interfaces are stable and seams are clear, but secure-session parity and PostgreSQL integration introduce meaningful behavioral and operational risk.

## Research Needed for Design Phase

- Decide whether `store: sqlite` should remain on raw SQL initially or route through Bun after parity tests.
- Decide shared top-level database pool config versus per-store pool config.
- Decide exact PostgreSQL connection string validation and redaction strategy.
- Decide PostgreSQL integration testing shape: env-gated local tests, CI service container, or both.
- Verify Bun/pgdriver version compatibility with the project's Go toolchain and transitive dependency budget.
- Define dialect-specific DDL policy for current and future migrations, including upgrade behavior for existing SQLite files.

## Design Phase Recommendations

- Prefer Option C for risk-managed delivery: add new Bun-backed managed-durable paths while preserving existing SQLite behavior first.
- Keep store interfaces unchanged and treat Bun as an adapter/infrastructure implementation detail.
- Use existing store contract tests as the primary parity mechanism, then add optional PostgreSQL integration tests with explicit skip messaging.
- Treat connection redaction and pool validation as first-class operator-facing requirements, not incidental plumbing.
- Keep automatic data migration between database products out of scope; document this explicitly in sample config or operator docs.

---

# Design Discovery Update

Generated: 2026-04-26T00:10:01.8215115+02:00

## Summary
- **Feature**: `bun-database-abstraction`
- **Discovery Scope**: Extension-focused discovery for a brownfield persistence feature.
- **Key Findings**:
  - Existing store ports and runtime factories are sufficient extension seams; no public contract changes are needed.
  - The safest design preserves raw-SQL SQLite runtime paths and adds Bun-backed PostgreSQL adapters first.
  - Connection ownership, secret-safe errors, and optional PostgreSQL tests are design-critical because they affect operator behavior.

## Research Log

### Extension Points
- **Context**: Design needs concrete seams for store selection and lifecycle.
- **Sources Consulted**: `internal/core/continuity/store.go`, `internal/infra/runtimebundle/build.go`, `internal/infra/runtimebundle/secure_session.go`, `internal/core/securesession/app/ports.go`.
- **Findings**: Continuity opens first, secure sessions use the same B2BUA store for lineage, and runtimebundle already collects closers.
- **Implications**: New adapters can plug into existing composition roots; tasks should avoid changes to executor and public contracts.

### Dependency Verification
- **Context**: Bun is a new dependency and must not leak into core contracts.
- **Sources Consulted**: Context7 Bun documentation, project steering, `go.mod`.
- **Findings**: Bun supports wrapping `*sql.DB` with `sqlitedialect` or `pgdialect`; PostgreSQL can be opened using Bun `pgdriver` connector.
- **Implications**: `internal/infra/db` can own open/wrap/pool helpers, while adapters own store behavior and schema preparation.

### Testing and Risk Assessment
- **Context**: Requirements demand durable parity and optional external DB validation.
- **Sources Consulted**: `internal/core/securesession/storecontract`, continuity SQLite tests, runtimebundle tests.
- **Findings**: Secure-session contract tests are reusable; continuity needs comparable Bun-store parity tests; PostgreSQL tests need env gating.
- **Implications**: Design uses contract tests as the primary parity gate and `LIP_TEST_POSTGRES_DSN` for optional PostgreSQL validation.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
| --- | --- | --- | --- | --- |
| Extend raw SQL | Add PostgreSQL raw-SQL stores beside SQLite stores | Minimal new dependency | Duplicates SQL and weakens Bun goal | Not selected. |
| New Bun adapters | Add Bun-backed stores and DB infra | Clean boundary, aligns with feature | Larger new surface | Viable. |
| Hybrid incremental | Keep SQLite paths, add Bun PostgreSQL path | Best compatibility and rollback | Two durable paths remain | Selected for design. |

## Design Decisions

### Decision: Preserve `store: sqlite` Runtime Path
- **Context**: Requirements require compatibility with existing local durable deployments.
- **Alternatives Considered**:
  1. Route SQLite through Bun immediately.
  2. Preserve existing SQLite adapters and add Bun only for PostgreSQL.
- **Selected Approach**: Preserve existing SQLite adapters in v1 and add Bun-backed PostgreSQL adapters.
- **Rationale**: This minimizes behavior drift and isolates the managed-durable change.
- **Trade-offs**: More parity matrix complexity until/unless SQLite is later moved to Bun.
- **Follow-up**: Revalidate if a later phase changes `store: sqlite` wiring.

### Decision: Top-Level Shared Pool Settings
- **Context**: Requirements allow database pool tuning without forcing per-store duplication.
- **Alternatives Considered**:
  1. Per-store pool settings.
  2. Top-level shared `database` settings.
- **Selected Approach**: Add top-level `DatabaseConfig` applied to PostgreSQL handles opened by this feature.
- **Rationale**: Simpler operator surface and sufficient for initial managed-durable support.
- **Trade-offs**: Per-store tuning is deferred.
- **Follow-up**: Add per-store tuning only if operational evidence requires it.

### Decision: No Shared PostgreSQL Handle in Initial Design
- **Context**: Continuity and secure-session stores may use the same DSN.
- **Alternatives Considered**:
  1. Share one `*bun.DB` when DSNs match.
  2. Keep each store handle independently owned.
- **Selected Approach**: Independent handles in this design.
- **Rationale**: Avoids ambiguous closer ownership and failure attribution.
- **Trade-offs**: Two pools may open to the same database.
- **Follow-up**: Optimize sharing later with an explicit ownership design.

## Risks & Mitigations
- Secure-session parity risk — mitigate with `storecontract.RunAll` and focused evidence/usage tests.
- Dialect-specific DDL risk — keep DDL explicit and adapter-owned; do not rely solely on model tags.
- Secret leakage risk — centralize DSN redaction and wrap startup errors through redacted helpers.
- PostgreSQL test flakiness risk — gate external tests with `LIP_TEST_POSTGRES_DSN` and explicit skips.

## References
- `internal/core/continuity/store.go` — continuity factory extension point.
- `internal/infra/runtimebundle/secure_session.go` — secure-session builder extension point.
- `internal/core/securesession/storecontract/contract.go` — secure-session behavior contract.
- `github.com/uptrace/bun` documentation — Bun dialect and transaction support.
