package db

import "time"

// DefaultPostgresOpenMigrateTimeout bounds composition-root PostgreSQL open (ping,
// pool apply) and initial schema migration when callers wrap I/O in [context.WithTimeout].
const DefaultPostgresOpenMigrateTimeout = 2 * time.Minute

// DefaultSqliteOpenMigrateTimeout bounds composition-root SQLite open and initial
// schema migration. SQLite open is a local file operation, so the bound is
// tighter than the Postgres default.
const DefaultSqliteOpenMigrateTimeout = 30 * time.Second
