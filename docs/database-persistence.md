# Database Persistence Configuration

This project lets operators choose persistence backends directly in `config/config.yaml`.
The settings below control continuity, secure sessions, and managed PostgreSQL pool tuning.

## Backend selection

Both store domains support the same backend names:

- `memory` - in-process, non-durable
- `sqlite` - local durable file-backed storage
- `postgres` - managed durable PostgreSQL storage

### Continuity

Use `continuity.store` to choose the continuity backend.

Relevant fields:

- `continuity.store`
- `continuity.in_memory` (legacy spelling of `store: memory`)
- `continuity.sqlite_path` when `store: sqlite`
- `continuity.postgres_dsn` when `store: postgres`

Behavior:

- `memory` preserves the existing in-memory behavior.
- `sqlite` preserves the existing local durable behavior.
- `postgres` uses the Bun-backed managed durable path.
- Unsupported values fail validation before startup.
- `continuity.postgres_dsn` is required for `postgres` and rejected for other backends.
- `continuity.in_memory: true` is the legacy spelling of `store: memory`. It may accompany an omitted `store` or an explicit `store: memory`. Paired with `store: sqlite` or `store: postgres` it fails validation before startup, because the process-local backing would otherwise silently replace the durable store. Remove `in_memory` or set `store: memory`.

### Secure sessions

Use `secure_session.store` to choose the secure-session backend.

Relevant fields:

- `secure_session.store`
- `secure_session.sqlite_path` when `store: sqlite`
- `secure_session.postgres_dsn` when `store: postgres`
- `secure_session.token_fingerprint_key` for durable stores
- `secure_session.audit_durability`

Behavior:

- `memory` keeps the existing non-durable secure-session behavior.
- `sqlite` preserves the existing local durable secure-session path.
- `postgres` uses the Bun-backed managed durable path.
- Durable audit is allowed only with `sqlite` or `postgres`.
- `secure_session.postgres_dsn` is required for `postgres` and rejected for other backends.

## Managed PostgreSQL pool tuning

The top-level `database` block applies pool tuning to managed PostgreSQL handles opened by this feature.

Relevant fields:

- `database.connection_mode` (managed PostgreSQL runtime stores only: `direct` or `transaction_pool`)
- `database.schema_mode` (managed PostgreSQL runtime stores only: `auto_migrate` or `verify_only`)
- `database.max_open_conns`
- `database.max_idle_conns`
- `database.conn_max_lifetime`
- `database.conn_max_idle_time`

Notes:

- When any managed path selects `store: postgres`, `database.max_open_conns` must be greater than zero (fail-closed; unlimited driver defaults are rejected).
- `connection_mode` and `schema_mode` configure only the managed authority, concurrency, and metering runtime stores. Continuity, secure-session, control-plane, and financial-ledger PostgreSQL paths retain their existing owning lifecycle.
- `transaction_pool` requires `verify_only`; `transaction_pool` plus `auto_migrate` is rejected during validation.
- For the managed authority, concurrency, and metering stores sharing one registry pool, size `max_open_conns` for peak concurrent transactions; watch `aip_postgres_pool_in_use_connections` vs `aip_postgres_pool_max_open_connections` and wait rates.
- Other omitted or zero pool fields still use driver defaults.
- Duration values use Go duration strings such as `30m` or `90s`.
- Invalid or negative values fail validation before startup.

## Example

```yaml
database:
  max_open_conns: 8
  max_idle_conns: 2
  conn_max_lifetime: 30m
  conn_max_idle_time: 2m

continuity:
  store: postgres
  postgres_dsn: "postgres://user:pass@host:5432/continuity?sslmode=require"

secure_session:
  store: postgres
  postgres_dsn: "postgres://user:pass@host:5432/secure_session?sslmode=require"
  token_fingerprint_key: "replace-with-32+byte-secret----------------"
  audit_durability: durable
```

## Validation behavior

- Configuration fails fast for unsupported backends and missing DSNs.
- The proxy does not silently fall back to another backend if the selected durable store cannot open.
- Sample config comments in `config/config.yaml` mirror these fields.

## Maintainer guide: dual-dialect parity & enforcement

All production persistence components in this repository supporting SQLite and PostgreSQL must maintain logical contract, transactional invariant, and migration parity across both engines.

### Parity catalog & discovery

- The authoritative list of dual-dialect persistence components is defined in package [`internal/testkit/dbparity`](../internal/testkit/dbparity/catalog.go) via [`dbparity.DefaultCatalog()`](../internal/testkit/dbparity/catalog.go). It captures the production component families of this tree, including `continuity`, `secure-sessions`, `session-classification`, `control-plane-ledger`, `concurrency-authority`, `metering-journal`, `proxy-memory`, and `terminal-work`. The catalog still registers the billing and usage-authority families as well: their implementations have not been relocated to the Enterprise tree yet.
- Architecture guardrails (`internal/archtest/database_parity_test.go`) discover versioned migration roots and deterministic dialect-sensitive indicators across packages, asserting explicit ownership against `internal/testkit/dbparity` and failing closed if any unregistered versioned migration root or package containing discovered deterministic dialect-sensitive indicators is introduced.

### Verification commands

- `make test-db-parity-sqlite` — Runs canonical SQLite parity tests across all registered components.
- `make test-db-parity-postgres-direct` — Runs fail-closed direct PostgreSQL parity tests (requires direct `AIP_TEST_POSTGRES_DSN` with runner fallback to `AIP_TEST_POSTGRES_ADMIN_DSN`; Make sets `AIP_REQUIRE_POSTGRES=1`).
- `make test-db-parity` — Runs SQLite and direct PostgreSQL parity for the whole repository; the two backends run concurrently, each backend's packages in order.

### CI enforcement

- Every test-relevant pull request executes `make test-db-parity` against an ephemeral direct PostgreSQL service container (`postgres:17-alpine`) in `.github/workflows/ci.yml`.
- The `db-parity` job result feeds directly into the required `repo-hygiene` aggregate status check (`if: always() && needs.db-parity.result != 'success'`).
- Changes classified as non-test-relevant by `scripts/ci-scope.sh` report an explicit bypass while satisfying the required `repo-hygiene` aggregate status check.

### Rules for adding or modifying persistence code

1. **Dual-Dialect Support**: When adding a new durable store or modifying an existing one, ensure equivalent logical contracts and schema invariants are satisfied on both SQLite and PostgreSQL.
2. **Stable Entry Points**: Each registered test package must provide stable `TestDBParity_SQLite` and `TestDBParity_PostgresDirect` test entry points executing the component's shared contract.
3. **Migration Parity**: Versioned migrations must be applied from empty on both engines, prove idempotency on rerun, and assert logical schema invariants (tables, columns, nullability, unique keys, and correctness-critical indexes).
4. **No Operator Impact**: The parity catalog and test runner are test-side infrastructure only. Operator runtime configuration remains strictly defined by the fields documented above in `config/config.yaml`.
