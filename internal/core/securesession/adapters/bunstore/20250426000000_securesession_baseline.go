package bunstore

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

// BaselineMigrationName is the name bun/migrate records for this adapter's baseline (file prefix).
const BaselineMigrationName = "20250426000000"

// secureSessionMigrations holds versioned DDL for this adapter.
var secureSessionMigrations = migrate.NewMigrations()

var registerSecureSessionMigrationsOnce sync.Once

func registerSecureSessionBaselineMigration() {
	registerSecureSessionMigrationsOnce.Do(func() {
		secureSessionMigrations.MustRegister(secureSessionBaselineUp, func(ctx context.Context, db *bun.DB) error {
			_ = ctx
			_ = db
			return nil
		})
		registerSecureSessionUsageAccountingColumnsMigration()
		registerSecureSessionQuarantineColumnsMigration()
		registerSecureSessionAttemptOpenFailedMigration()
	})
}

func secureSessionBaselineUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("bunstore: pragma foreign_keys: %w", err)
		}
		stmts = sqliteSecureSessionDDL()
	case dialect.PG:
		stmts = postgresSecureSessionDDL()
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("bunstore: securesession baseline: %w", err)
		}
	}
	if db.Dialect().Name() == dialect.SQLite {
		if err := upgradeAttemptTraceOutcomeColumnsSQLite(ctx, db); err != nil {
			return err
		}
		if err := upgradeUsageAccountingColumns(ctx, db); err != nil {
			return err
		}
		if err := upgradeQuarantineColumns(ctx, db); err != nil {
			return err
		}
		if err := upgradeAttemptOpenFailedColumn(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

func runSecureSessionSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerSecureSessionBaselineMigration()
	if err := bunmigrate.Migrate(ctx, db, "bun_securesession_migrations", secureSessionMigrations); err != nil {
		return fmt.Errorf("bunstore: %w", err)
	}
	if err := upgradeUsageAccountingColumns(ctx, db); err != nil {
		return err
	}
	if err := upgradeQuarantineColumns(ctx, db); err != nil {
		return err
	}
	return nil
}

// RunSchemaMigrate runs the schema migrations for secure sessions against the provided DB.
func RunSchemaMigrate(ctx context.Context, db *bun.DB) error {
	return runSecureSessionSchemaMigrate(ctx, db)
}

func sqliteSecureSessionDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS aip_secure_sessions (
			session_id TEXT NOT NULL PRIMARY KEY,
			resume_fingerprint BLOB NOT NULL,
			owner_id TEXT NOT NULL DEFAULT '',
			owner_issuer TEXT NOT NULL DEFAULT '',
			owner_tenant TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			client_session_id TEXT NOT NULL DEFAULT '',
			agent_digest TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			transcript_enabled INTEGER NOT NULL DEFAULT 0,
			effective_treatment TEXT NOT NULL DEFAULT '',
			stricter_policy_resolution TEXT NOT NULL DEFAULT '',
			route_hint TEXT NOT NULL DEFAULT '',
			redaction_profile TEXT NOT NULL DEFAULT '',
			audit_mode TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			resume_eligible INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			quarantined_at_unix INTEGER NOT NULL DEFAULT 0,
			quarantine_reason_code TEXT NOT NULL DEFAULT '',
			quarantine_event_id TEXT NOT NULL DEFAULT '',
			last_activity_unix INTEGER NOT NULL,
			last_activity_source TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			usage_in BIGINT NOT NULL DEFAULT 0,
			usage_out BIGINT NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			latest_attempt_trace_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_outcome_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_accounting_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_aip_secure_sessions_resume_fp
			ON aip_secure_sessions(resume_fingerprint)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_aip_secure_sessions_a_leg_unique
			ON aip_secure_sessions(a_leg_id) WHERE a_leg_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_owner
			ON aip_secure_sessions(owner_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_workspace
			ON aip_secure_sessions(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_owner_workspace
			ON aip_secure_sessions(owner_id, workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_last_activity
			ON aip_secure_sessions(last_activity_unix DESC)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_turns (
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			PRIMARY KEY(session_id, turn_id),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_turns_session ON aip_secure_turns(session_id)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_attempt_traces (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			attempt_seq INTEGER NOT NULL,
			requested_model TEXT NOT NULL DEFAULT '',
			requested_alias TEXT NOT NULL DEFAULT '',
			resolved_backend TEXT NOT NULL DEFAULT '',
			resolved_model TEXT NOT NULL DEFAULT '',
			route_source TEXT NOT NULL DEFAULT '',
			route_reason TEXT NOT NULL DEFAULT '',
			settings_json TEXT NOT NULL DEFAULT '{}',
			started_at_unix INTEGER NOT NULL,
			ended_at_unix INTEGER NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			surface_state TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0,
			provider_status TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			timeout_class TEXT NOT NULL DEFAULT '',
			debug_reason TEXT NOT NULL DEFAULT '',
			outcome_json TEXT NOT NULL DEFAULT '{}',
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_session
			ON aip_secure_attempt_traces(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_b_leg
			ON aip_secure_attempt_traces(b_leg_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_resolved
			ON aip_secure_attempt_traces(resolved_backend, resolved_model)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_transcript (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			payload_ref TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_transcript_session_seq
			ON aip_secure_transcript(session_id, seq)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_usage (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			non_cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			non_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_nano_units INTEGER NOT NULL DEFAULT 0,
			cost_minor_units INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '',
			cost_source TEXT NOT NULL DEFAULT '',
			raw_usage_json TEXT NOT NULL DEFAULT '',
			billing_unavailable INTEGER NOT NULL DEFAULT 0,
			request_started_at_unix INTEGER NOT NULL DEFAULT 0,
			first_remote_event_at_unix INTEGER NOT NULL DEFAULT 0,
			first_meaningful_token_at_unix INTEGER NOT NULL DEFAULT 0,
			remote_completed_at_unix INTEGER NOT NULL DEFAULT 0,
			proxy_completed_at_unix INTEGER NOT NULL DEFAULT 0,
			ttft_millis INTEGER NOT NULL DEFAULT 0,
			remote_duration_millis INTEGER NOT NULL DEFAULT 0,
			completion_duration_millis INTEGER NOT NULL DEFAULT 0,
			completion_tps_milli INTEGER NOT NULL DEFAULT 0,
			created_at_unix INTEGER NOT NULL,
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_usage_session ON aip_secure_usage(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_usage_b_leg ON aip_secure_usage(b_leg_id)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_audit (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			action TEXT NOT NULL,
			result TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_audit_session_seq
			ON aip_secure_audit(session_id, seq)`,
	}
}

func postgresSecureSessionDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS aip_secure_sessions (
			session_id TEXT NOT NULL PRIMARY KEY,
			resume_fingerprint BYTEA NOT NULL,
			owner_id TEXT NOT NULL DEFAULT '',
			owner_issuer TEXT NOT NULL DEFAULT '',
			owner_tenant TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			client_session_id TEXT NOT NULL DEFAULT '',
			agent_digest TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			transcript_enabled INTEGER NOT NULL DEFAULT 0,
			effective_treatment TEXT NOT NULL DEFAULT '',
			stricter_policy_resolution TEXT NOT NULL DEFAULT '',
			route_hint TEXT NOT NULL DEFAULT '',
			redaction_profile TEXT NOT NULL DEFAULT '',
			audit_mode TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			resume_eligible INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			quarantined_at_unix BIGINT NOT NULL DEFAULT 0,
			quarantine_reason_code TEXT NOT NULL DEFAULT '',
			quarantine_event_id TEXT NOT NULL DEFAULT '',
			last_activity_unix BIGINT NOT NULL,
			last_activity_source TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			usage_in BIGINT NOT NULL DEFAULT 0,
			usage_out BIGINT NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			latest_attempt_trace_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_outcome_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_accounting_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_aip_secure_sessions_resume_fp
			ON aip_secure_sessions(resume_fingerprint)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_aip_secure_sessions_a_leg_unique
			ON aip_secure_sessions(a_leg_id) WHERE a_leg_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_owner
			ON aip_secure_sessions(owner_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_workspace
			ON aip_secure_sessions(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_owner_workspace
			ON aip_secure_sessions(owner_id, workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_sessions_last_activity
			ON aip_secure_sessions(last_activity_unix DESC)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_turns (
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			PRIMARY KEY(session_id, turn_id),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_turns_session ON aip_secure_turns(session_id)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_attempt_traces (
			id BIGSERIAL PRIMARY KEY,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			attempt_seq INTEGER NOT NULL,
			requested_model TEXT NOT NULL DEFAULT '',
			requested_alias TEXT NOT NULL DEFAULT '',
			resolved_backend TEXT NOT NULL DEFAULT '',
			resolved_model TEXT NOT NULL DEFAULT '',
			route_source TEXT NOT NULL DEFAULT '',
			route_reason TEXT NOT NULL DEFAULT '',
			settings_json TEXT NOT NULL DEFAULT '{}',
			started_at_unix BIGINT NOT NULL,
			ended_at_unix BIGINT NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			surface_state TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0,
			provider_status TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			timeout_class TEXT NOT NULL DEFAULT '',
			debug_reason TEXT NOT NULL DEFAULT '',
			outcome_json TEXT NOT NULL DEFAULT '{}',
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_session
			ON aip_secure_attempt_traces(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_b_leg
			ON aip_secure_attempt_traces(b_leg_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_attempt_traces_resolved
			ON aip_secure_attempt_traces(resolved_backend, resolved_model)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_transcript (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			payload_ref TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_transcript_session_seq
			ON aip_secure_transcript(session_id, seq)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_usage (
			id BIGSERIAL PRIMARY KEY,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL DEFAULT '',
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_write_tokens BIGINT NOT NULL DEFAULT 0,
			non_cached_input_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			non_reasoning_output_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cost_nano_units BIGINT NOT NULL DEFAULT 0,
			cost_minor_units BIGINT NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '',
			cost_source TEXT NOT NULL DEFAULT '',
			raw_usage_json TEXT NOT NULL DEFAULT '',
			billing_unavailable INTEGER NOT NULL DEFAULT 0,
			request_started_at_unix BIGINT NOT NULL DEFAULT 0,
			first_remote_event_at_unix BIGINT NOT NULL DEFAULT 0,
			first_meaningful_token_at_unix BIGINT NOT NULL DEFAULT 0,
			remote_completed_at_unix BIGINT NOT NULL DEFAULT 0,
			proxy_completed_at_unix BIGINT NOT NULL DEFAULT 0,
			ttft_millis BIGINT NOT NULL DEFAULT 0,
			remote_duration_millis BIGINT NOT NULL DEFAULT 0,
			completion_duration_millis BIGINT NOT NULL DEFAULT 0,
			completion_tps_milli BIGINT NOT NULL DEFAULT 0,
			created_at_unix BIGINT NOT NULL,
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_usage_session ON aip_secure_usage(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_usage_b_leg ON aip_secure_usage(b_leg_id)`,

		`CREATE TABLE IF NOT EXISTS aip_secure_audit (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			action TEXT NOT NULL,
			result TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES aip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aip_secure_audit_session_seq
			ON aip_secure_audit(session_id, seq)`,
	}
}

func upgradeUsageAccountingColumns(ctx context.Context, db *bun.DB) error {
	var alters []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		alters = []string{
			`ALTER TABLE aip_secure_usage ADD COLUMN reasoning_tokens INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN non_cached_input_tokens INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN non_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN cost_nano_units INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN cost_source TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_usage ADD COLUMN raw_usage_json TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_usage ADD COLUMN request_started_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN first_remote_event_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN first_meaningful_token_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN remote_completed_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN proxy_completed_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN ttft_millis INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN remote_duration_millis INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN completion_duration_millis INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN completion_tps_milli INTEGER NOT NULL DEFAULT 0`,
		}
	case dialect.PG:
		alters = []string{
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS reasoning_tokens BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS non_cached_input_tokens BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS non_reasoning_output_tokens BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS total_tokens BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS cost_nano_units BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS cost_source TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS raw_usage_json TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS request_started_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS first_remote_event_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS first_meaningful_token_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS remote_completed_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS proxy_completed_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS ttft_millis BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS remote_duration_millis BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS completion_duration_millis BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_usage ADD COLUMN IF NOT EXISTS completion_tps_milli BIGINT NOT NULL DEFAULT 0`,
		}
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, q := range alters {
		if _, err := db.ExecContext(ctx, q); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "duplicate column name") || strings.Contains(msg, "already exists") {
				continue
			}
			return fmt.Errorf("bunstore: migrate upgrade usage: %w", err)
		}
	}
	return nil
}

func upgradeQuarantineColumns(ctx context.Context, db *bun.DB) error {
	var alters []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		alters = []string{
			`ALTER TABLE aip_secure_sessions ADD COLUMN status TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN quarantined_at_unix INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN quarantine_reason_code TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN quarantine_event_id TEXT NOT NULL DEFAULT ''`,
		}
	case dialect.PG:
		alters = []string{
			`ALTER TABLE aip_secure_sessions ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN IF NOT EXISTS quarantined_at_unix BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN IF NOT EXISTS quarantine_reason_code TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aip_secure_sessions ADD COLUMN IF NOT EXISTS quarantine_event_id TEXT NOT NULL DEFAULT ''`,
		}
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, q := range alters {
		if _, err := db.ExecContext(ctx, q); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "duplicate column name") || strings.Contains(msg, "already exists") {
				continue
			}
			return fmt.Errorf("bunstore: migrate upgrade quarantine: %w", err)
		}
	}
	return nil
}

func upgradeAttemptTraceOutcomeColumnsSQLite(ctx context.Context, db *bun.DB) error {
	alters := []string{
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN ended_at_unix INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN success INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN surface_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN http_status INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN provider_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN timeout_class TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN debug_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE aip_secure_attempt_traces ADD COLUMN outcome_json TEXT NOT NULL DEFAULT '{}'`,
	}
	for _, q := range alters {
		if _, err := db.ExecContext(ctx, q); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "duplicate column name") {
				continue
			}
			return fmt.Errorf("bunstore: migrate upgrade attempt_traces: %w", err)
		}
	}
	return nil
}

func upgradeAttemptOpenFailedColumn(ctx context.Context, db *bun.DB) error {
	var q string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		q = `ALTER TABLE aip_secure_attempt_traces ADD COLUMN open_failed INTEGER NOT NULL DEFAULT 0`
	case dialect.PG:
		q = `ALTER TABLE aip_secure_attempt_traces ADD COLUMN IF NOT EXISTS open_failed INTEGER NOT NULL DEFAULT 0`
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, q); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column name") || strings.Contains(msg, "already exists") {
			return nil
		}
		return fmt.Errorf("bunstore: migrate upgrade attempt open_failed: %w", err)
	}
	return nil
}
