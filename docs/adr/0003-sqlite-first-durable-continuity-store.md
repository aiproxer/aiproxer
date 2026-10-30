# ADR 0003: SQLite-first durable continuity store

## Status

Accepted (stage two).

## Context

Operators need optional durability for A-leg continuity and attempt lineage across process restarts. In-memory defaults remain valid for tests and single-node dev.

## Decision

- `continuity` configuration selects `memory` or `sqlite` via `internal/core/continuity.OpenStore`.
- SQLite stores live under `internal/core/continuity/sqlitestore` with explicit schema bootstrap.
- Diagnostics read attempt lineage from the **same** store instance selected at composition time.

## Consequences

- Hermetic tests use temp-file SQLite; production paths document `sqlite_path` and backup expectations separately from this ADR.
