package journalstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// AccountWindowProjectionMigrationName identifies the additive search-column
// migration for provider account-window gauges. The canonical observation
// envelope remains the sole immutable source; these columns are only indexed
// query accelerators and consistency checks.
const AccountWindowProjectionMigrationName = "20260914000000"

const (
	accountWindowFactsIndex                  = "idx_metering_facts_store_account_window"
	accountWindowProjectionBackfillBatchSize = 256
)

func registerAccountWindowProjectionMigration() {
	migrations.MustRegister(accountWindowProjectionSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func accountWindowProjectionSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering account-window projection schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return accountWindowProjectionSQLite(ctx, db)
	case dialect.PG:
		return accountWindowProjectionPostgres(ctx, db)
	default:
		return fmt.Errorf("metering account-window projection schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func accountWindowProjectionSQLite(ctx context.Context, db *bun.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"observation_pool_id", `ALTER TABLE metering_facts ADD COLUMN observation_pool_id TEXT NOT NULL DEFAULT ''`},
		{"observation_window_id", `ALTER TABLE metering_facts ADD COLUMN observation_window_id TEXT NOT NULL DEFAULT ''`},
		{"observation_reset_at_unix", `ALTER TABLE metering_facts ADD COLUMN observation_reset_at_unix INTEGER NOT NULL DEFAULT 0`},
		{"observation_observed_at_unix", `ALTER TABLE metering_facts ADD COLUMN observation_observed_at_unix INTEGER NOT NULL DEFAULT 0`},
		{"observation_received_at_unix", `ALTER TABLE metering_facts ADD COLUMN observation_received_at_unix INTEGER NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", column.name, column.ddl); err != nil {
			return fmt.Errorf("metering account-window projection sqlite: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_account_window
			ON metering_facts(store_id, observation_provider_account_key, observation_pool_id, observation_window_id, observation_reset_at_unix, observation_observed_at_unix, observation_received_at_unix, stream_id, sequence, observation_id, observation_revision, id)
			WHERE payload_kind = 'observation' AND observation_subject_kind = 'account_window'`); err != nil {
		return fmt.Errorf("metering account-window projection sqlite index: %w", err)
	}
	return backfillAccountWindowProjection(ctx, db)
}

func accountWindowProjectionPostgres(ctx context.Context, db *bun.DB) error {
	statements := []string{
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_pool_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_window_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_reset_at_unix BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_observed_at_unix BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_received_at_unix BIGINT NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_account_window
			ON metering_facts(store_id, observation_provider_account_key, observation_pool_id, observation_window_id, observation_reset_at_unix, observation_observed_at_unix, observation_received_at_unix, stream_id, sequence, observation_id, observation_revision, id)
			WHERE payload_kind = 'observation' AND observation_subject_kind = 'account_window'`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering account-window projection postgres: %w", err)
		}
	}
	return backfillAccountWindowProjection(ctx, db)
}

// accountWindowProjectionBackfillRow contains only immutable source data and
// the row identity needed to update denormalized search columns. Canonical
// payload_json and observation_fingerprint are never written by the backfill.
type accountWindowProjectionBackfillRow struct {
	ID                     int64  `bun:"id"`
	StoreID                string `bun:"store_id"`
	Payload                string `bun:"payload_json"`
	PayloadKind            string `bun:"payload_kind"`
	ObservationSubjectKind string `bun:"observation_subject_kind"`
	ObservationFingerprint string `bun:"observation_fingerprint"`
}

type accountWindowProjectionBackfillValues struct {
	ID                     int64
	StreamID               string
	Sequence               int64
	ObservationID          string
	ObservationRevision    int64
	ObservationSubjectKind string
	ObservationSubjectID   string
	ObservationTenantID    string
	ObservationOrigin      string
	ObservationAcquisition string
	ProviderAccountKey     string
	PoolID                 string
	WindowID               string
	ResetAt                int64
	ObservedAt             int64
	ReceivedAt             int64
}

// backfillAccountWindowProjection repairs the denormalized search columns for
// pre-existing V2 account-window observations. Rows are read in stable primary
// key order in fixed-size batches so neither dialect needs an unbounded query.
// Every candidate is validated before any update is committed; one malformed
// canonical account-window payload therefore fails the whole upgrade without a
// partially published search projection.
func backfillAccountWindowProjection(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("metering account-window projection backfill: nil context")
	}
	if db == nil {
		return fmt.Errorf("metering account-window projection backfill: nil database")
	}
	return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var lastID int64
		for {
			rows := make([]accountWindowProjectionBackfillRow, 0, accountWindowProjectionBackfillBatchSize)
			if err := tx.NewRaw(`
				SELECT id, store_id, payload_json, payload_kind, observation_subject_kind, observation_fingerprint
				FROM metering_facts
				WHERE id > ?
				ORDER BY id ASC
				LIMIT ?`, lastID, accountWindowProjectionBackfillBatchSize).Scan(ctx, &rows); err != nil {
				return fmt.Errorf("metering account-window projection backfill scan after id %d: %w", lastID, err)
			}
			if len(rows) == 0 {
				return nil
			}

			updates := make([]accountWindowProjectionBackfillValues, 0, len(rows))
			for _, row := range rows {
				lastID = row.ID
				values, ok, err := accountWindowProjectionBackfillValuesFromRow(row)
				if err != nil {
					return err
				}
				if ok {
					updates = append(updates, values)
				}
			}
			for _, values := range updates {
				if err := updateAccountWindowProjection(ctx, tx, values); err != nil {
					return err
				}
			}
		}
	})
}

func accountWindowProjectionBackfillValuesFromRow(row accountWindowProjectionBackfillRow) (accountWindowProjectionBackfillValues, bool, error) {
	// V1 facts and unrelated historical rows may share metering_facts. A row
	// already marked as an observation must be a valid canonical V2 envelope;
	// fail closed rather than allowing malformed history to be hidden by the
	// upgrade. Legacy rows without that marker are first classified by payload
	// subject so ordinary V1 facts remain untouched.
	if row.PayloadKind == "observation" {
		var observation aipsdkmetering.Observation
		if err := json.Unmarshal([]byte(row.Payload), &observation); err != nil {
			return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: decode canonical observation: %w", row.ID, err)
		}
		if err := observation.Validate(); err != nil {
			return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: invalid canonical observation: %w", row.ID, err)
		}
		if row.ObservationSubjectKind == string(aipsdkmetering.SubjectAccountWindow) && observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
			return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("%w: metering account-window projection backfill row %d: stored subject kind does not match canonical payload", ErrIdentityCollision, row.ID)
		}
		if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
			return accountWindowProjectionBackfillValues{}, false, nil
		}
		return accountWindowProjectionBackfillValuesFromObservation(row, observation)
	}

	var envelope struct {
		Subject struct {
			Kind aipsdkmetering.SubjectKind `json:"kind"`
		} `json:"subject"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &envelope); err != nil {
		if row.ObservationSubjectKind == string(aipsdkmetering.SubjectAccountWindow) {
			return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: invalid canonical account-window JSON: %w", row.ID, err)
		}
		return accountWindowProjectionBackfillValues{}, false, nil
	}
	if envelope.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
		if row.ObservationSubjectKind == string(aipsdkmetering.SubjectAccountWindow) {
			return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("%w: metering account-window projection backfill row %d: stored subject kind does not match canonical payload", ErrIdentityCollision, row.ID)
		}
		return accountWindowProjectionBackfillValues{}, false, nil
	}

	var observation aipsdkmetering.Observation
	if err := json.Unmarshal([]byte(row.Payload), &observation); err != nil {
		return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: decode canonical observation: %w", row.ID, err)
	}
	return accountWindowProjectionBackfillValuesFromObservation(row, observation)
}

func accountWindowProjectionBackfillValuesFromObservation(row accountWindowProjectionBackfillRow, observation aipsdkmetering.Observation) (accountWindowProjectionBackfillValues, bool, error) {
	if err := observation.Validate(); err != nil {
		return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: invalid canonical observation: %w", row.ID, err)
	}
	// Generic account-window observations may be local or statement-origin.
	// They remain valid journal evidence, but this provider allowance search
	// projection must not classify them as provider gauges during backfill.
	if err := validateProviderAllowanceObservation(observation); err != nil {
		return accountWindowProjectionBackfillValues{}, false, nil
	}
	if observation.Subject.StoreID != row.StoreID || observation.Correlation.StoreID != row.StoreID {
		return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("%w: metering account-window projection backfill row %d: canonical store scope does not match row", ErrIdentityCollision, row.ID)
	}
	if row.ObservationFingerprint != "" && observation.Fingerprint() != row.ObservationFingerprint {
		return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("%w: metering account-window projection backfill row %d: canonical fingerprint does not match row", ErrIdentityCollision, row.ID)
	}
	if observation.Sequence > math.MaxInt64 || observation.Revision > math.MaxInt64 {
		return accountWindowProjectionBackfillValues{}, false, fmt.Errorf("metering account-window projection backfill row %d: observation integer identity exceeds database range", row.ID)
	}

	return accountWindowProjectionBackfillValues{
		ID:                     row.ID,
		StreamID:               observation.StreamID,
		Sequence:               int64(observation.Sequence),
		ObservationID:          observation.ID,
		ObservationRevision:    int64(observation.Revision),
		ObservationSubjectKind: string(observation.Subject.Kind),
		ObservationSubjectID:   subjectID(observation.Subject),
		ObservationTenantID:    observationTenant(observation),
		ObservationOrigin:      observation.Origin,
		ObservationAcquisition: observation.Acquisition,
		ProviderAccountKey:     observationProviderAccount(observation),
		PoolID:                 observation.Subject.PoolID,
		WindowID:               observation.Subject.WindowID,
		ResetAt:                observation.Subject.ResetAt.UTC().UnixNano(),
		ObservedAt:             observation.ObservedAt.UTC().UnixNano(),
		ReceivedAt:             observation.ReceivedAt.UTC().UnixNano(),
	}, true, nil
}

func updateAccountWindowProjection(ctx context.Context, tx bun.Tx, values accountWindowProjectionBackfillValues) error {
	_, err := tx.NewRaw(`
		UPDATE metering_facts SET
			stream_id = ?, sequence = ?, payload_kind = 'observation', observation_id = ?, observation_revision = ?,
			observation_subject_kind = ?, observation_subject_id = ?, observation_tenant_id = ?,
			observation_origin = ?, observation_acquisition = ?, observation_provider_account_key = ?,
			observation_pool_id = ?, observation_window_id = ?, observation_reset_at_unix = ?,
			observation_observed_at_unix = ?, observation_received_at_unix = ?
		WHERE id = ?`,
		values.StreamID, values.Sequence, values.ObservationID, values.ObservationRevision,
		values.ObservationSubjectKind, values.ObservationSubjectID, values.ObservationTenantID,
		values.ObservationOrigin, values.ObservationAcquisition, values.ProviderAccountKey,
		values.PoolID, values.WindowID, values.ResetAt, values.ObservedAt, values.ReceivedAt,
		values.ID,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("metering account-window projection backfill row %d: %w", values.ID, err)
	}
	return nil
}
