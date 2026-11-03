package proxymemorystore

import (
	"context"

	"github.com/uptrace/bun"
)

// EnsureSchema applies the proxy memory baseline schema (design §12.1) through
// the bunmigrate registry, recording it in bun_proxy_memory_migrations. It is
// idempotent: already-applied migrations are skipped.
func EnsureSchema(ctx context.Context, db *bun.DB) error {
	return runSchemaMigrate(ctx, db)
}

// baselineDDL returns the proxy memory schema statements of design §12.1.
// The types are TEXT, BIGINT and INTEGER with identical semantics on SQLite
// and PostgreSQL, so one statement list serves both engines. Timestamps and
// counters are unix milliseconds; there are no foreign keys to a_legs because
// memory outlives A-leg retirement.
//
// The binding table also carries the frozen first-turn decision and the
// owner-scoped erasure epoch table; the extraction ledger keeps the
// content-free identity of an A-leg that already contributed to a bank, the
// exact activity of that extracted source and the monotone activity watermark
// of the A-leg.
//
// The first-release baseline is frozen: an applied migration is recorded in
// bun_proxy_memory_migrations and never replayed, so editing these statements
// upgrades no existing database. A schema change after the freeze is a new
// forward migration file with a later version prefix. Databases created by a
// pre-release build against an earlier baseline shape are fresh-install only;
// they are neither detected nor repaired here (docs/proxy-memory.md,
// "Schema lifetime").
func baselineDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS proxy_memory_session_bindings (
			a_leg_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			project_label TEXT NOT NULL,
			first_turn_outcome TEXT NOT NULL DEFAULT '',
			first_turn_injection TEXT NOT NULL DEFAULT '',
			first_turn_summary_version BIGINT NOT NULL DEFAULT 0,
			first_turn_epoch BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_erasure_epochs (
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			epoch BIGINT NOT NULL,
			PRIMARY KEY (tenant_id, principal_id, project_key)
		)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_sessions (
			a_leg_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			project_label TEXT NOT NULL,
			transcript TEXT NOT NULL,
			revision BIGINT NOT NULL,
			last_activity_ms BIGINT NOT NULL,
			source_seq BIGINT NOT NULL,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_ms BIGINT NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_ms BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_sessions_owner ON proxy_memory_sessions(tenant_id, principal_id, project_key)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_sessions_activity ON proxy_memory_sessions(last_activity_ms)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_extractions (
			a_leg_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			session_ref TEXT NOT NULL,
			raw_memory TEXT NOT NULL,
			rollout_summary TEXT NOT NULL,
			rollout_slug TEXT NOT NULL,
			source_updated_ms BIGINT NOT NULL,
			source_seq BIGINT NOT NULL DEFAULT 0,
			generated_at_ms BIGINT NOT NULL,
			consolidated_at_ms BIGINT NOT NULL DEFAULT 0,
			extraction_revision BIGINT NOT NULL DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_extractions_owner ON proxy_memory_extractions(tenant_id, principal_id, project_key, generated_at_ms)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_notes (
			note_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			note TEXT NOT NULL,
			created_at_ms BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_notes_owner ON proxy_memory_notes(tenant_id, principal_id, project_key, created_at_ms)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_banks (
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			project_label TEXT NOT NULL,
			version BIGINT NOT NULL DEFAULT 0,
			memory_md TEXT NOT NULL DEFAULT '',
			memory_summary TEXT NOT NULL DEFAULT '',
			published_at_ms BIGINT NOT NULL DEFAULT 0,
			consolidated_names TEXT NOT NULL DEFAULT '[]',
			dirty_seq BIGINT NOT NULL DEFAULT 0,
			consolidated_seq BIGINT NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_ms BIGINT NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_ms BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (tenant_id, principal_id, project_key)
		)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_fence (
			a_leg_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			forgotten_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS proxy_memory_extraction_ledger (
			a_leg_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			recorded_at_ms BIGINT NOT NULL,
			source_activity_ms BIGINT NOT NULL,
			last_activity_ms BIGINT NOT NULL DEFAULT 0,
			source_seq BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_extraction_ledger_owner ON proxy_memory_extraction_ledger(tenant_id, principal_id, project_key)`,
	}
}

// settingsDDL returns the owner-scoped settings statements of design §4.
//
// The table holds the owner's own unprojected per-field overrides, one row
// per phase. The key is the tenant, the principal and the phase: there is no
// project column, so the settings are global for that owner and apply to
// every project of that owner. The types are TEXT with identical semantics on
// SQLite and PostgreSQL, so one statement list serves both engines.
func settingsDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS proxy_memory_settings (
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			phase TEXT NOT NULL,
			backend TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (tenant_id, principal_id, phase)
		)`,
	}
}
