// Package bunstore implements b2bua.Store using Bun over database/sql for managed
// durable continuity (PostgreSQL) and for dialect-backed tests (SQLite).
//
// It is a concrete adapter: Bun types and SQL stay inside this package.
//
// Schema evolution: migrations are applied with [github.com/uptrace/bun/migrate] using
// adapter-local Go migration functions registered from schema.go (sync.Once). The baseline
// uses CREATE IF NOT EXISTS so existing databases created before migration history still
// converge safely; the first successful run records migration [BaselineMigrationName] in table
// bun_continuity_migrations. Down migrations are no-ops by design.
package bunstore
