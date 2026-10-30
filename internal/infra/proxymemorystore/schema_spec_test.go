package proxymemorystore_test

import (
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

// proxyMemoryLogicalSchemaSpec declares the logical schema invariants the
// proxy memory migrations must establish identically on SQLite and PostgreSQL
// (design §12.1, §12.7). Both parity entry points verify it against their
// engine's metadata, so a migration that diverges between the engines fails
// the canonical dbparity gate.
func proxyMemoryLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "proxy-memory",
		Tables: []dbparity.TableSpec{
			{
				Name: "proxy_memory_sessions",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_label", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "transcript", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "last_activity_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_owner", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_until_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "attempts", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "next_attempt_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
			},
			{
				Name: "proxy_memory_extractions",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "session_ref", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "raw_memory", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "rollout_summary", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "rollout_slug", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "source_updated_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "generated_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "consolidated_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "extraction_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
			},
			{
				Name: "proxy_memory_notes",
				Columns: []dbparity.ColumnSpec{
					{Name: "note_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "note", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "created_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"note_id"},
			},
			{
				Name: "proxy_memory_banks",
				Columns: []dbparity.ColumnSpec{
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "project_label", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "version", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "memory_md", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "memory_summary", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "published_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "consolidated_names", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "dirty_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "consolidated_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_owner", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_until_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "attempts", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "next_attempt_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"tenant_id", "principal_id", "project_key"},
			},
			{
				Name: "proxy_memory_phase_failures",
				Columns: []dbparity.ColumnSpec{
					{Name: "store_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "billing_call_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "phase", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "claim_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "claim_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "claim_seq", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_owner", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "lease_until_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "erasure_epoch", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "failure_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "completed_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "expires_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "recorded_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"store_id", "billing_call_id", "phase"},
			},
			{
				Name: "proxy_memory_fence",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "forgotten_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
			},
			{
				Name: "proxy_memory_extraction_ledger",
				Columns: []dbparity.ColumnSpec{
					{Name: "a_leg_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "project_key", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "source_activity_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "last_activity_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
					{Name: "recorded_at_ms", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"a_leg_id"},
			},
			{
				// The owner-scoped settings of design §4: the owner's own
				// unprojected per-phase overrides. The key holds no project,
				// so the settings are global for that owner.
				Name: "proxy_memory_settings",
				Columns: []dbparity.ColumnSpec{
					{Name: "tenant_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "principal_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "phase", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
					{Name: "backend", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "reasoning_effort", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "dependency_present", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "dependency_backend", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "dependency_model", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
					{Name: "inherited_backend", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
					{Name: "inherited_model", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				},
				PrimaryKey: []string{"tenant_id", "principal_id", "phase"},
			},
		},
		Indexes: []dbparity.IndexSpec{
			{Name: "proxy_memory_sessions_owner", Table: "proxy_memory_sessions", Columns: []string{"tenant_id", "principal_id", "project_key"}},
			{Name: "proxy_memory_sessions_activity", Table: "proxy_memory_sessions", Columns: []string{"last_activity_ms"}},
			{Name: "proxy_memory_extractions_owner", Table: "proxy_memory_extractions", Columns: []string{"tenant_id", "principal_id", "project_key", "generated_at_ms"}},
			{Name: "proxy_memory_notes_owner", Table: "proxy_memory_notes", Columns: []string{"tenant_id", "principal_id", "project_key", "created_at_ms"}},
			{Name: "proxy_memory_extraction_ledger_owner", Table: "proxy_memory_extraction_ledger", Columns: []string{"tenant_id", "principal_id", "project_key"}},
			{Name: "proxy_memory_phase_failures_owner", Table: "proxy_memory_phase_failures", Columns: []string{"tenant_id", "principal_id", "project_key"}},
			{Name: "proxy_memory_phase_failures_expiry", Table: "proxy_memory_phase_failures", Columns: []string{"expires_at_ms"}},
		},
	}
}
