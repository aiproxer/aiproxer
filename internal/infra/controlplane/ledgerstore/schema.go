package ledgerstore

// sqliteControlPlaneDDL returns the dialect-aware DDL for the SQLite control-
// plane event ledger. One append-only table holds identity, timing,
// correlation, state, safe scope dimensions, and bounded safe JSON columns
// (requirement 1.7, 1.8, 2.5, 2.6, 4.2, 4.3, 4.4, 4.5, 6.1, 7.1, 7.6).
//
// Scope dimensions are columnized as presence-aware ({dim}_known INTEGER,
// {dim}_value TEXT) pairs so filters can match unknown vs known-empty
// distinctly without parsing JSON (requirement 4.3). Detail, scope, and
// summary JSON are bounded at the Go boundary before persistence.
func sqliteControlPlaneDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS control_plane_events (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			source_event_key TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL,
			occurred_at_unix INTEGER NOT NULL,
			recorded_at_unix INTEGER NOT NULL,
			trace_id TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			attempt_seq INTEGER NOT NULL DEFAULT 0,
			frontend_id TEXT NOT NULL DEFAULT '',
			backend_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			parent_trace_id TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL DEFAULT '',
			effect TEXT NOT NULL DEFAULT '',
			reason_code TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL DEFAULT '',
			surfaced TEXT NOT NULL DEFAULT '',
			usage_plane TEXT NOT NULL DEFAULT '',
			usage_availability TEXT NOT NULL DEFAULT '',
			evidence_state TEXT NOT NULL DEFAULT '',
			redaction_state TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			source_version TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			summary_json TEXT NOT NULL DEFAULT '{}',
			scope_json TEXT NOT NULL DEFAULT '{}',
			detail_json TEXT NOT NULL DEFAULT '{}',
			principal_known INTEGER NOT NULL DEFAULT 0,
			principal_value TEXT NOT NULL DEFAULT '',
			credential_known INTEGER NOT NULL DEFAULT 0,
			credential_value TEXT NOT NULL DEFAULT '',
			tenant_known INTEGER NOT NULL DEFAULT 0,
			tenant_value TEXT NOT NULL DEFAULT '',
			organization_known INTEGER NOT NULL DEFAULT 0,
			organization_value TEXT NOT NULL DEFAULT '',
			workspace_known INTEGER NOT NULL DEFAULT 0,
			workspace_value TEXT NOT NULL DEFAULT '',
			project_known INTEGER NOT NULL DEFAULT 0,
			project_value TEXT NOT NULL DEFAULT '',
			department_known INTEGER NOT NULL DEFAULT 0,
			department_value TEXT NOT NULL DEFAULT '',
			cost_center_known INTEGER NOT NULL DEFAULT 0,
			cost_center_value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_control_plane_events_source_key
			ON control_plane_events(source_event_key) WHERE source_event_key != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_order
			ON control_plane_events(id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_category_time
			ON control_plane_events(category, occurred_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_trace
			ON control_plane_events(trace_id) WHERE trace_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_session
			ON control_plane_events(session_id) WHERE session_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_a_leg
			ON control_plane_events(a_leg_id) WHERE a_leg_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_b_leg
			ON control_plane_events(b_leg_id) WHERE b_leg_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_backend_model
			ON control_plane_events(backend_id, model)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_outcome
			ON control_plane_events(outcome) WHERE outcome != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_reason
			ON control_plane_events(reason_code) WHERE reason_code != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_surfaced
			ON control_plane_events(surfaced) WHERE surfaced != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_usage_plane
			ON control_plane_events(usage_plane) WHERE usage_plane != ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_principal
			ON control_plane_events(principal_known, principal_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_tenant
			ON control_plane_events(tenant_known, tenant_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_workspace
			ON control_plane_events(workspace_known, workspace_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_project
			ON control_plane_events(project_known, project_value)`,
	}
}

// postgresControlPlaneDDL returns the dialect-aware DDL for the Postgres
// control-plane event ledger. Semantics mirror the SQLite DDL; presence-aware
// dimensions use BOOLEAN, sequences use BIGINT, and partial indexes use the
// Postgres WHERE clause (requirement 1.7, 4.3).
func postgresControlPlaneDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS control_plane_events (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			source_event_key TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL,
			occurred_at_unix BIGINT NOT NULL,
			recorded_at_unix BIGINT NOT NULL,
			trace_id TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			attempt_seq INTEGER NOT NULL DEFAULT 0,
			frontend_id TEXT NOT NULL DEFAULT '',
			backend_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			parent_trace_id TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL DEFAULT '',
			effect TEXT NOT NULL DEFAULT '',
			reason_code TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL DEFAULT '',
			surfaced TEXT NOT NULL DEFAULT '',
			usage_plane TEXT NOT NULL DEFAULT '',
			usage_availability TEXT NOT NULL DEFAULT '',
			evidence_state TEXT NOT NULL DEFAULT '',
			redaction_state TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			source_version TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			summary_json TEXT NOT NULL DEFAULT '{}',
			scope_json TEXT NOT NULL DEFAULT '{}',
			detail_json TEXT NOT NULL DEFAULT '{}',
			principal_known BOOLEAN NOT NULL DEFAULT FALSE,
			principal_value TEXT NOT NULL DEFAULT '',
			credential_known BOOLEAN NOT NULL DEFAULT FALSE,
			credential_value TEXT NOT NULL DEFAULT '',
			tenant_known BOOLEAN NOT NULL DEFAULT FALSE,
			tenant_value TEXT NOT NULL DEFAULT '',
			organization_known BOOLEAN NOT NULL DEFAULT FALSE,
			organization_value TEXT NOT NULL DEFAULT '',
			workspace_known BOOLEAN NOT NULL DEFAULT FALSE,
			workspace_value TEXT NOT NULL DEFAULT '',
			project_known BOOLEAN NOT NULL DEFAULT FALSE,
			project_value TEXT NOT NULL DEFAULT '',
			department_known BOOLEAN NOT NULL DEFAULT FALSE,
			department_value TEXT NOT NULL DEFAULT '',
			cost_center_known BOOLEAN NOT NULL DEFAULT FALSE,
			cost_center_value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_control_plane_events_source_key
			ON control_plane_events(source_event_key) WHERE source_event_key <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_order
			ON control_plane_events(id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_category_time
			ON control_plane_events(category, occurred_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_trace
			ON control_plane_events(trace_id) WHERE trace_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_session
			ON control_plane_events(session_id) WHERE session_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_a_leg
			ON control_plane_events(a_leg_id) WHERE a_leg_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_b_leg
			ON control_plane_events(b_leg_id) WHERE b_leg_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_backend_model
			ON control_plane_events(backend_id, model)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_outcome
			ON control_plane_events(outcome) WHERE outcome <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_reason
			ON control_plane_events(reason_code) WHERE reason_code <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_surfaced
			ON control_plane_events(surfaced) WHERE surfaced <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_usage_plane
			ON control_plane_events(usage_plane) WHERE usage_plane <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_principal
			ON control_plane_events(principal_known, principal_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_tenant
			ON control_plane_events(tenant_known, tenant_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_workspace
			ON control_plane_events(workspace_known, workspace_value)`,
		`CREATE INDEX IF NOT EXISTS idx_control_plane_events_project
			ON control_plane_events(project_known, project_value)`,
	}
}
