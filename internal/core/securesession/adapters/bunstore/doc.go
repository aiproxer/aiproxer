// Package bunstore implements securesession [app.Store] and [app.SessionUsageRollup] using Bun
// over database/sql. It is an internal adapter for managed durable storage; callers construct
// it from composition roots with an already-open [github.com/uptrace/bun.DB] handle.
//
// Schema evolution: migrations use [github.com/uptrace/bun/migrate]; baseline registration runs from schema.go (sync.Once).
// Baseline DDL is idempotent (IF NOT EXISTS) and SQLite legacy upgrades tolerate duplicate columns;
// the first run records [BaselineMigrationName] in bun_securesession_migrations. Down is a no-op.
package bunstore
