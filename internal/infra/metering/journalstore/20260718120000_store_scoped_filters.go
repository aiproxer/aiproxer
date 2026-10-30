package journalstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const StoreScopedFiltersMigrationName = "20260718120000"

func registerStoreScopedFiltersMigration() {
	migrations.MustRegister(storeScopedFiltersUp, func(context.Context, *bun.DB) error { return nil })
}

func storeScopedFiltersUp(ctx context.Context, db *bun.DB) error {
	switch db.Dialect().Name() {
	case dialect.PG:
		return storeScopedFiltersPostgres(ctx, db)
	case dialect.SQLite:
		return storeScopedFiltersSQLite(ctx, db)
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func storeScopedFiltersPostgres(ctx context.Context, db *bun.DB) error {
	stmts := []string{
		`ALTER TABLE metering_fact_filters ADD COLUMN IF NOT EXISTS store_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE metering_fact_filters ff
SET store_id = sub.store_id
FROM (
	SELECT fact_id, stream_id, MIN(store_id) AS store_id
	FROM metering_facts
	GROUP BY fact_id, stream_id
	HAVING COUNT(DISTINCT store_id) = 1
) sub
WHERE ff.store_id = ''
  AND ff.fact_id = sub.fact_id
  AND ff.stream_id = sub.stream_id`,
		`DELETE FROM metering_fact_filters WHERE store_id = ''`,
		`ALTER TABLE metering_fact_filters ALTER COLUMN store_id DROP DEFAULT`,
		`DROP INDEX IF EXISTS idx_metering_fact_filters_field`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_filters_field
			ON metering_fact_filters(store_id, field_name, field_value, stream_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store-scoped filters migration: %w", err)
		}
	}
	return nil
}

func storeScopedFiltersSQLite(ctx context.Context, db *bun.DB) error {
	var tableSQL string
	_ = db.NewRaw(`SELECT sql FROM sqlite_master WHERE type='table' AND name='metering_fact_filters'`).Scan(ctx, &tableSQL)
	if tableSQL == "" {
		return nil
	}
	if strings.Contains(tableSQL, "store_id") {
		var idxSQL string
		_ = db.NewRaw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_metering_fact_filters_field'`).Scan(ctx, &idxSQL)
		if strings.Contains(idxSQL, "store_id") {
			return nil
		}
	}
	stmts := []string{
		`ALTER TABLE metering_fact_filters RENAME TO metering_fact_filters_legacy_v1`,
		`CREATE TABLE metering_fact_filters (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL
		)`,
		`INSERT INTO metering_fact_filters(store_id, fact_id, stream_id, field_name, field_value)
		SELECT sub.store_id, legacy.fact_id, legacy.stream_id, legacy.field_name, legacy.field_value
		FROM metering_fact_filters_legacy_v1 AS legacy
		INNER JOIN (
			SELECT fact_id, stream_id, MIN(store_id) AS store_id
			FROM metering_facts
			GROUP BY fact_id, stream_id
			HAVING COUNT(DISTINCT store_id) = 1
		) AS sub
			ON legacy.fact_id = sub.fact_id AND legacy.stream_id = sub.stream_id`,
		`DROP TABLE metering_fact_filters_legacy_v1`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_filters_field
			ON metering_fact_filters(store_id, field_name, field_value, stream_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite store-scoped filters rebuild: %w", err)
		}
	}
	return nil
}
