---
name: golang-database
description: "Implement and review Go database access with database/sql, sqlx, and pgx: SQL safety, transaction invariants, scanning, isolation, pooling, migrations, and tests."
---

# Go database access

Choose database/sql for portability and a small dependency surface. sqlx can reduce repetitive scanning while retaining SQL. pgx is a PostgreSQL-native client with context-aware operations and features such as COPY and LISTEN. An ORM is a valid product choice in some codebases, but do not hide an existing SQL contract behind one or claim that any library is universally superior.

Before editing, identify the driver, placeholder syntax, transaction boundaries, null semantics, migration tool, and pool ownership.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace the business invariant across reads, writes, transaction handle use, isolation, constraints, and concurrent requests. Check RowsAffected and missing-row behavior before reporting success.
- Look for calls through db inside an active tx, nested acquisition with a small pool, SQL identifier interpolation, and scan/iteration failures that yield partial results as success.
- Check pagination has a total order and tie-breaker, query plans/indexes match predicates, and returned driver buffers such as sql.RawBytes are copied before retention.
- Walk rollback, serialization retry, canceled transaction, ambiguous commit, and external side-effect paths. Verify migrations and SQL semantics against each supported dialect.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Query boundary

- Every value supplied by a caller is a query argument. Use placeholders; never concatenate values into SQL.
- SQL identifiers cannot be bound as arguments. For dynamic table, column, or sort names, map an enum or allowlist to fixed SQL fragments.
- Use QueryContext, ExecContext, and the corresponding library context methods. The context carries cancellation and deadlines; it is not a substitute for a database timeout policy.
- Use ExecContext for statements that do not return rows. If QueryContext is used, close and inspect the rows.
- Treat sql.ErrNoRows as a domain outcome when appropriate, using errors.Is after wrapping.
- Log operation and safe identifiers, never passwords, tokens, raw query arguments, or untrusted error text as a public response.

~~~go
rows, err := db.QueryContext(ctx,
    "SELECT id, name FROM users WHERE tenant_id = $1 ORDER BY id", tenantID)
if err != nil {
    return fmt.Errorf("list users: %w", err)
}
defer rows.Close()

for rows.Next() {
    var u User
    if scanErr := rows.Scan(&u.ID, &u.Name); scanErr != nil {
        return fmt.Errorf("scan user: %w", scanErr)
    }
    users = append(users, u)
}
if err := rows.Err(); err != nil {
    return fmt.Errorf("iterate users: %w", err)
}
~~~

For code that must surface a close error, use a named result and a deliberate deferred Close assignment; otherwise a simple defer rows.Close is adequate for read-only code where close failure has no useful recovery.

For sqlx.In, build the statement from a fixed template, expand only the argument list, and call Rebind for the selected driver. For pgx, use the driver's placeholder syntax and current row-collection helpers as documented by the installed version.

## Scanning and nullability

Scan into the smallest stable domain shape. Use sql.NullString and its siblings, pointers, or driver-specific nullable types when NULL is meaningful; do not conflate NULL with an empty value without a domain decision. Keep database tags separate from JSON tags when the wire name differs.

Check every Scan error. If a query returns a nullable column, test both NULL and a real value. For bulk reads, bound memory or stream rows and preserve ordering only when the query specifies it.

## Transactions and locking

Use a transaction when several reads and writes must share one atomic invariant. Begin with the requested isolation level only when the data invariant requires it and the driver supports it. Serializable isolation is not a universal performance or correctness switch; it may require retrying the whole transaction on serialization failure.

Lock rows only when the lock protects a demonstrated race. SELECT ... FOR UPDATE locks the rows selected under the database's rules; it does not protect arbitrary predicates or external resources. Keep lock order consistent to reduce deadlocks and keep the transaction short.

A robust transaction shape is:

~~~go
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
if err != nil {
    return fmt.Errorf("begin: %w", err)
}
defer func() { _ = tx.Rollback() }()

if _, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = balance - $1 WHERE id = $2", amount, from); err != nil {
    return fmt.Errorf("debit: %w", err)
}
if _, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = balance + $1 WHERE id = $2", amount, to); err != nil {
    return fmt.Errorf("credit: %w", err)
}
if err := tx.Commit(); err != nil {
    return fmt.Errorf("commit: %w", err)
}
return nil
~~~

Retry only errors documented as safe to retry, and rerun the complete transaction with a fresh context-aware attempt. Do not retry after a commit result is ambiguous without an idempotency/reconciliation design.

## Pools and operations

sql.Open creates a pool handle; it may not establish a connection. Call PingContext when startup connectivity must be checked. Configure SetMaxOpenConns, SetMaxIdleConns, SetConnMaxLifetime, and SetConnMaxIdleTime from workload, server limits, and observed wait time. There is no portable best setting or fixed speedup claim.

The component that opens a pool owns its shutdown and calls Close. Expose health and pool metrics without assuming a metric name from another driver. Migrations belong to a reviewed, versioned migration tool and deployment process. Application code may need schema changes, but migration safety requires rollout, locking, backfill, and rollback analysis rather than an automatic blanket rule.

## Tests

Use a narrow unit seam for query construction and domain mapping, then integration tests against the actual driver/database behavior when SQL semantics matter. A mock must implement the same return arity and error behavior as the real interface. For integration setup, use PingContext with a bounded deadline, fail on fixture-read errors, and report teardown errors. Do not use fixed sleeps for readiness.

When reviewing database code, verify context propagation, placeholders, rows.Close and rows.Err, Scan errors, transaction rollback/commit, pool ownership, and sensitive-data handling. Run gofmt and focused tests; use the selected driver's current documentation for API details.
