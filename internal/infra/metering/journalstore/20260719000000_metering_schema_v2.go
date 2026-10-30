package journalstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const SchemaV2MigrationName = "20260719000000"

// V2BoundedIndexNames are store-scoped indexes required by VerifySchema (task 3.4 / D12).
var V2BoundedIndexNames = []string{
	"metering_facts_store_stream_fact_id_key",
	"idx_metering_facts_store_stream_seq",
	"idx_metering_facts_store_attempt",
	"idx_metering_facts_store_recorded",
	"idx_metering_facts_store_plane",
	"idx_metering_fact_supersessions_to",
	"metering_facts_store_id_key",
	"metering_facts_store_observation_revision_key",
	"idx_metering_components_store_subject",
	"idx_metering_components_store_component",
	"idx_metering_components_store_provider_account",
	"idx_metering_components_observation",
	accountWindowFactsIndex,
	meteringFactsStoreBLegIndex,
	meteringFactsStoreBLegStatementIndex,
}

func registerSchemaV2Migration() {
	migrations.MustRegister(schemaV2Up, func(context.Context, *bun.DB) error { return nil })
}

func schemaV2Up(ctx context.Context, db *bun.DB) error {
	switch db.Dialect().Name() {
	case dialect.PG:
		return schemaV2Postgres(ctx, db)
	case dialect.SQLite:
		return schemaV2SQLite(ctx, db)
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func schemaV2Postgres(ctx context.Context, db *bun.DB) error {
	stmts := []string{
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS identity_version BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS source_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS source_event_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS source_id TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_stream_fact_id_key
			ON metering_facts(store_id, stream_id, fact_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_stream_seq
			ON metering_facts(store_id, stream_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_attempt
			ON metering_facts(store_id, attempt_id) WHERE attempt_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_recorded
			ON metering_facts(store_id, recorded_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_plane
			ON metering_facts(store_id, perspective, boundary, lifecycle_scope)`,
		`CREATE TABLE IF NOT EXISTS metering_fact_supersessions (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			from_fact_id TEXT NOT NULL,
			to_fact_id TEXT NOT NULL,
			CONSTRAINT metering_fact_supersessions_edge_key
				UNIQUE (store_id, stream_id, from_fact_id, to_fact_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_supersessions_to
			ON metering_fact_supersessions(store_id, stream_id, to_fact_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("metering schema v2 postgres: %w", err)
		}
	}
	if err := backfillIdentityColumns(ctx, db); err != nil {
		return err
	}
	if err := backfillSupersessionEdges(ctx, db); err != nil {
		return err
	}
	return nil
}

func schemaV2SQLite(ctx context.Context, db *bun.DB) error {
	if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", "identity_version",
		`ALTER TABLE metering_facts ADD COLUMN identity_version INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", "source_revision",
		`ALTER TABLE metering_facts ADD COLUMN source_revision INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", "source_event_kind",
		`ALTER TABLE metering_facts ADD COLUMN source_event_kind TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", "source_id",
		`ALTER TABLE metering_facts ADD COLUMN source_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	stmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_stream_fact_id_key
			ON metering_facts(store_id, stream_id, fact_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_stream_seq
			ON metering_facts(store_id, stream_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_attempt
			ON metering_facts(store_id, attempt_id) WHERE attempt_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_recorded
			ON metering_facts(store_id, recorded_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_plane
			ON metering_facts(store_id, perspective, boundary, lifecycle_scope)`,
		`CREATE TABLE IF NOT EXISTS metering_fact_supersessions (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			from_fact_id TEXT NOT NULL,
			to_fact_id TEXT NOT NULL,
			UNIQUE(store_id, stream_id, from_fact_id, to_fact_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_supersessions_to
			ON metering_fact_supersessions(store_id, stream_id, to_fact_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("metering schema v2 sqlite: %w", err)
		}
	}
	if err := backfillIdentityColumns(ctx, db); err != nil {
		return err
	}
	if err := backfillSupersessionEdges(ctx, db); err != nil {
		return err
	}
	return nil
}

func sqliteAddColumnIfMissing(ctx context.Context, db *bun.DB, table, column, alterSQL string) error {
	var n int
	err := db.NewRaw(
		`SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?`,
		table,
		column,
	).Scan(ctx, &n)
	if err != nil {
		return fmt.Errorf("metering schema v2 sqlite column probe %s.%s: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, alterSQL); err != nil {
		return fmt.Errorf("metering schema v2 sqlite add %s.%s: %w", table, column, err)
	}
	return nil
}

// backfillIdentityColumns denormalizes identity fields from payload_json.
// identity_version uses EffectiveIdentityVersion: raw 0/absent → 1.
func backfillIdentityColumns(ctx context.Context, db *bun.DB) error {
	var stmt string
	switch db.Dialect().Name() {
	case dialect.PG:
		stmt = `
UPDATE metering_facts SET
	identity_version = CASE
		WHEN COALESCE((payload_json::jsonb->>'identity_version')::bigint, 0) = 0 THEN 1
		ELSE (payload_json::jsonb->>'identity_version')::bigint
	END,
	source_revision = COALESCE((payload_json::jsonb->>'source_revision')::bigint, source_revision),
	source_event_kind = COALESCE(NULLIF(payload_json::jsonb->>'source_event_kind', ''), NULLIF(fact_kind, ''), source_event_kind),
	source_id = COALESCE(NULLIF(payload_json::jsonb->>'source_id', ''), NULLIF(fact_id, ''), source_id)
WHERE identity_version = 0
   OR source_event_kind = ''
   OR source_id = ''
   OR (
		source_revision = 0
		AND COALESCE((payload_json::jsonb->>'source_revision')::bigint, 0) <> 0
   )`
	case dialect.SQLite:
		stmt = `
UPDATE metering_facts SET
	identity_version = CASE
		WHEN COALESCE(CAST(json_extract(payload_json, '$.identity_version') AS INTEGER), 0) = 0 THEN 1
		ELSE CAST(json_extract(payload_json, '$.identity_version') AS INTEGER)
	END,
	source_revision = COALESCE(CAST(json_extract(payload_json, '$.source_revision') AS INTEGER), source_revision),
	source_event_kind = COALESCE(
		NULLIF(json_extract(payload_json, '$.source_event_kind'), ''),
		NULLIF(fact_kind, ''),
		source_event_kind
	),
	source_id = COALESCE(
		NULLIF(json_extract(payload_json, '$.source_id'), ''),
		NULLIF(fact_id, ''),
		source_id
	)
WHERE identity_version = 0
   OR source_event_kind = ''
   OR source_id = ''
   OR (
		source_revision = 0
		AND COALESCE(CAST(json_extract(payload_json, '$.source_revision') AS INTEGER), 0) <> 0
   )`
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("metering schema v2 identity backfill: %w", err)
	}
	return nil
}

func backfillSupersessionEdges(ctx context.Context, db *bun.DB) error {
	type row struct {
		StoreID  string `bun:"store_id"`
		StreamID string `bun:"stream_id"`
		FactID   string `bun:"fact_id"`
		Payload  string `bun:"payload_json"`
	}
	var rows []row
	err := db.NewRaw(`SELECT store_id, stream_id, fact_id, payload_json FROM metering_facts`).Scan(ctx, &rows)
	if err != nil {
		return fmt.Errorf("metering schema v2 supersession backfill scan: %w", err)
	}
	insertSQL := `INSERT INTO metering_fact_supersessions(store_id, stream_id, from_fact_id, to_fact_id)
VALUES (?,?,?,?) ON CONFLICT DO NOTHING`
	if db.Dialect().Name() == dialect.SQLite {
		insertSQL = `INSERT OR IGNORE INTO metering_fact_supersessions(store_id, stream_id, from_fact_id, to_fact_id)
VALUES (?,?,?,?)`
	}
	for _, r := range rows {
		var fact metering.Fact
		if uerr := json.Unmarshal([]byte(r.Payload), &fact); uerr != nil {
			return fmt.Errorf("metering schema v2 supersession backfill decode: %w", uerr)
		}
		from := strings.TrimSpace(fact.FactID)
		if from == "" {
			from = strings.TrimSpace(r.FactID)
		}
		stream := strings.TrimSpace(fact.StreamID)
		if stream == "" {
			stream = strings.TrimSpace(r.StreamID)
		}
		for _, raw := range fact.Supersedes {
			to := strings.TrimSpace(raw)
			if to == "" {
				continue
			}
			if _, ierr := db.NewRaw(insertSQL, r.StoreID, stream, from, to).Exec(ctx); ierr != nil {
				return fmt.Errorf("metering schema v2 supersession backfill insert: %w", ierr)
			}
		}
	}
	return nil
}

// BackfillSchemaV2IdentityForTest re-runs identity column backfill (tests only).
func BackfillSchemaV2IdentityForTest(ctx context.Context, db *bun.DB) error {
	return backfillIdentityColumns(ctx, db)
}

// BackfillSchemaV2SupersessionsForTest re-runs supersession edge backfill (tests only).
func BackfillSchemaV2SupersessionsForTest(ctx context.Context, db *bun.DB) error {
	return backfillSupersessionEdges(ctx, db)
}
