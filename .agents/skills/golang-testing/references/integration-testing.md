# Integration testing

Use integration tests when behavior depends on a real protocol, database, filesystem, process, or network boundary. Keep unit tests for domain logic and define an explicit prerequisite policy: skip only when absence is expected and visible, otherwise fail with actionable setup information.

## Readiness and cleanup

Do not sleep and assume a service is ready. Poll a health check with a context deadline, or verify a database with `PingContext` after `sql.Open`:

```go
ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
defer cancel()

db, err := sql.Open("postgres", dsn)
if err != nil { t.Fatal(err) }
t.Cleanup(func() {
    if err := db.Close(); err != nil { t.Errorf("close database: %v", err) }
})
if err := db.PingContext(ctx); err != nil { t.Fatal(err) }
```

`sql.Open` validates driver setup and creates a pool; it does not prove a connection works. Read fixture files with checked errors and fail before using empty data. Register teardown immediately after acquisition, stop child processes, close bodies/connections, and report cleanup failures without overwriting a more useful primary failure.

Use unique schemas, temporary directories, ports, or test IDs. Reset state between tests or provision an isolated fixture. Bound all retries and external calls with context. Capture logs/artifacts on failure without storing credentials.
