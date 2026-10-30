package proxymemorystore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// PhaseFailuresMigrationName is the version prefix of the additive D3 failure
// evidence migration. It matches the file name so the dbparity discovery and
// the bun_proxy_memory_migrations history agree.
const PhaseFailuresMigrationName = "20261023000000"

// phaseFailuresDDL creates the owner-scoped memory-processing failure facts.
// The table is additive and separate from the provider/monetary record: a
// provider success followed by a memory parse failure is one unchanged payable
// operation plus one `invalid_result` memory fact.
func phaseFailuresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS proxy_memory_phase_failures (
			store_id TEXT NOT NULL,
			billing_call_id TEXT NOT NULL,
			phase TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			project_key TEXT NOT NULL,
			claim_id TEXT NOT NULL,
			claim_revision BIGINT NOT NULL,
			claim_seq BIGINT NOT NULL,
			lease_owner TEXT NOT NULL,
			lease_until_ms BIGINT NOT NULL,
			erasure_epoch BIGINT NOT NULL,
			failure_kind TEXT NOT NULL,
			completed_at_ms BIGINT NOT NULL,
			expires_at_ms BIGINT NOT NULL,
			recorded_at_ms BIGINT NOT NULL,
			PRIMARY KEY (store_id, billing_call_id, phase)
		)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_phase_failures_owner ON proxy_memory_phase_failures(tenant_id, principal_id, project_key)`,
		`CREATE INDEX IF NOT EXISTS proxy_memory_phase_failures_expiry ON proxy_memory_phase_failures(expires_at_ms)`,
	}
}

// registerPhaseFailuresMigration registers the additive migration from this
// file so Bun records the file's version prefix as its history name.
func registerPhaseFailuresMigration() {
	migrations.MustRegister(phaseFailuresUp, func(context.Context, *bun.DB) error { return nil })
}

func phaseFailuresUp(ctx context.Context, db *bun.DB) error {
	for _, stmt := range phaseFailuresDDL() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("proxy memory phase failures: %w", err)
		}
	}
	return nil
}
