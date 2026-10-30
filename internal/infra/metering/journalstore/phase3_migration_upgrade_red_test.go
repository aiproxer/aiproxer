package journalstore_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func TestPhase3_FreshMigrate_ExactMigrationNamesOnceEach(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openEmptySQLiteBun(t)
	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	assertExactMigrationCounts(t, bunDB, map[string]int{
		journalstore.BaselineMigrationName:                      1,
		journalstore.StoreScopedSourceKeyMigrationName:          1,
		journalstore.StoreScopedFiltersMigrationName:            1,
		journalstore.SchemaV2MigrationName:                      1,
		journalstore.ObservationProjectionMigrationName:         1,
		journalstore.AccountWindowProjectionMigrationName:       1,
		journalstore.ObservationEconomicOutboxMigrationName:     1,
		journalstore.PresenceBooleanRepairMigrationName:         1,
		journalstore.LinkedStatementIndexMigrationName:          1,
		journalstore.LinkedStatementOrderedIndexMigrationName:   1,
		journalstore.LinkedStatementCandidateIndexMigrationName: 1,
	})
	if err := journalstore.VerifySchema(ctx, bunDB); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
}

func TestPhase3_UpgradeFromPrePhase3Baseline_AppliesStoreScopedAndV2(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openEmptySQLiteBun(t)
	seedPrePhase3BaselineSchema(t, bunDB)
	assertExactMigrationCounts(t, bunDB, map[string]int{journalstore.BaselineMigrationName: 1})

	var beforeFiltersStoreID int
	if err := bunDB.NewRaw(
		`SELECT COUNT(1) FROM pragma_table_info('metering_fact_filters') WHERE name='store_id'`,
	).Scan(ctx, &beforeFiltersStoreID); err != nil {
		t.Fatal(err)
	}
	if beforeFiltersStoreID != 0 {
		t.Fatal("pre-Phase3 seed must omit filters.store_id")
	}

	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatalf("Migrate upgrade: %v", err)
	}
	assertExactMigrationCounts(t, bunDB, map[string]int{
		journalstore.BaselineMigrationName:                      1,
		journalstore.StoreScopedSourceKeyMigrationName:          1,
		journalstore.StoreScopedFiltersMigrationName:            1,
		journalstore.SchemaV2MigrationName:                      1,
		journalstore.ObservationProjectionMigrationName:         1,
		journalstore.AccountWindowProjectionMigrationName:       1,
		journalstore.ObservationEconomicOutboxMigrationName:     1,
		journalstore.PresenceBooleanRepairMigrationName:         1,
		journalstore.LinkedStatementIndexMigrationName:          1,
		journalstore.LinkedStatementOrderedIndexMigrationName:   1,
		journalstore.LinkedStatementCandidateIndexMigrationName: 1,
	})

	var filtersStoreID int
	if err := bunDB.NewRaw(
		`SELECT COUNT(1) FROM pragma_table_info('metering_fact_filters') WHERE name='store_id'`,
	).Scan(ctx, &filtersStoreID); err != nil {
		t.Fatal(err)
	}
	if filtersStoreID != 1 {
		t.Fatal("upgrade must add metering_fact_filters.store_id")
	}
	for _, col := range []string{"identity_version", "source_revision", "source_event_kind", "source_id"} {
		var n int
		if err := bunDB.NewRaw(
			`SELECT COUNT(1) FROM pragma_table_info('metering_facts') WHERE name=?`, col,
		).Scan(ctx, &n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("missing V2 column %s", col)
		}
	}
	for _, name := range journalstore.V2BoundedIndexNames {
		var n int
		if err := bunDB.NewRaw(
			`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name=?`, name,
		).Scan(ctx, &n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("missing V2 index %s", name)
		}
	}
	if err := journalstore.VerifySchema(ctx, bunDB); err != nil {
		t.Fatalf("VerifySchema after upgrade: %v", err)
	}

	storeA, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	fact := upgradeProbeFact("f1", "stream-1")
	if err := storeA.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if err := storeB.Append(ctx, fact); err != nil {
		t.Fatalf("store-scoped source uniqueness must allow same key across stores: %v", err)
	}
}

func TestPhase3_CollapsedBaselineHistory_RecoversIdempotently(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openEmptySQLiteBun(t)
	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	// Simulate production histories that recorded store-scoped Ups under the baseline name.
	if _, err := bunDB.ExecContext(
		ctx, `
DELETE FROM bun_metering_journal_migrations
WHERE name IN (?, ?)`,
		journalstore.StoreScopedSourceKeyMigrationName,
		journalstore.StoreScopedFiltersMigrationName,
	); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := bunDB.ExecContext(ctx, `
INSERT INTO bun_metering_journal_migrations(name, group_id, migrated_at)
VALUES (?, 1, CURRENT_TIMESTAMP)`, journalstore.BaselineMigrationName); err != nil {
			t.Fatal(err)
		}
	}
	assertExactMigrationCounts(t, bunDB, map[string]int{
		journalstore.BaselineMigrationName:                      3,
		journalstore.SchemaV2MigrationName:                      1,
		journalstore.ObservationProjectionMigrationName:         1,
		journalstore.AccountWindowProjectionMigrationName:       1,
		journalstore.ObservationEconomicOutboxMigrationName:     1,
		journalstore.PresenceBooleanRepairMigrationName:         1,
		journalstore.LinkedStatementIndexMigrationName:          1,
		journalstore.LinkedStatementOrderedIndexMigrationName:   1,
		journalstore.LinkedStatementCandidateIndexMigrationName: 1,
	})

	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatalf("recover Migrate: %v", err)
	}
	assertExactMigrationCounts(t, bunDB, map[string]int{
		journalstore.BaselineMigrationName:                      3,
		journalstore.StoreScopedSourceKeyMigrationName:          1,
		journalstore.StoreScopedFiltersMigrationName:            1,
		journalstore.SchemaV2MigrationName:                      1,
		journalstore.ObservationProjectionMigrationName:         1,
		journalstore.AccountWindowProjectionMigrationName:       1,
		journalstore.ObservationEconomicOutboxMigrationName:     1,
		journalstore.PresenceBooleanRepairMigrationName:         1,
		journalstore.LinkedStatementIndexMigrationName:          1,
		journalstore.LinkedStatementOrderedIndexMigrationName:   1,
		journalstore.LinkedStatementCandidateIndexMigrationName: 1,
	})
	if err := journalstore.VerifySchema(ctx, bunDB); err != nil {
		t.Fatalf("VerifySchema after collapsed recovery: %v", err)
	}
	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatalf("second Migrate must be idempotent: %v", err)
	}
	assertExactMigrationCounts(t, bunDB, map[string]int{
		journalstore.BaselineMigrationName:                      3,
		journalstore.StoreScopedSourceKeyMigrationName:          1,
		journalstore.StoreScopedFiltersMigrationName:            1,
		journalstore.SchemaV2MigrationName:                      1,
		journalstore.ObservationProjectionMigrationName:         1,
		journalstore.AccountWindowProjectionMigrationName:       1,
		journalstore.ObservationEconomicOutboxMigrationName:     1,
		journalstore.PresenceBooleanRepairMigrationName:         1,
		journalstore.LinkedStatementIndexMigrationName:          1,
		journalstore.LinkedStatementOrderedIndexMigrationName:   1,
		journalstore.LinkedStatementCandidateIndexMigrationName: 1,
	})
}

func TestPhase3_SQLite_VerifySchemaRequiresFiltersStoreIDAndMigrationNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openEmptySQLiteBun(t)
	seedPrePhase3BaselineSchema(t, bunDB)
	// Partial upgrade: add V2-ish probes without filters.store_id / named migrations.
	for _, stmt := range []string{
		`ALTER TABLE metering_facts ADD COLUMN identity_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN source_revision INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN source_event_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN source_id TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE metering_fact_supersessions (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL, stream_id TEXT NOT NULL,
			from_fact_id TEXT NOT NULL, to_fact_id TEXT NOT NULL,
			UNIQUE(store_id, stream_id, from_fact_id, to_fact_id)
		)`,
		`CREATE UNIQUE INDEX metering_facts_store_stream_fact_id_key ON metering_facts(store_id, stream_id, fact_id)`,
		`CREATE INDEX idx_metering_facts_store_stream_seq ON metering_facts(store_id, stream_id, sequence)`,
		`CREATE INDEX idx_metering_facts_store_attempt ON metering_facts(store_id, attempt_id) WHERE attempt_id != ''`,
		`CREATE INDEX idx_metering_facts_store_recorded ON metering_facts(store_id, recorded_at_unix)`,
		`CREATE INDEX idx_metering_facts_store_plane ON metering_facts(store_id, perspective, boundary, lifecycle_scope)`,
		`CREATE INDEX idx_metering_fact_supersessions_to ON metering_fact_supersessions(store_id, stream_id, to_fact_id)`,
	} {
		if _, err := bunDB.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	err := journalstore.VerifySchema(ctx, bunDB)
	if err == nil {
		t.Fatal("VerifySchema must fail without filters.store_id and store-scoped migration names")
	}
}

func openEmptySQLiteBun(t *testing.T) *bun.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	return bunDB
}

func seedPrePhase3BaselineSchema(t *testing.T, bunDB *bun.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE metering_facts (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			source_event_key TEXT NOT NULL,
			fact_kind TEXT NOT NULL,
			perspective TEXT NOT NULL,
			boundary TEXT NOT NULL,
			lifecycle_scope TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			frontend_id TEXT NOT NULL DEFAULT '',
			backend_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			presence TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			authority TEXT NOT NULL DEFAULT '',
			recorded_at_unix INTEGER NOT NULL,
			payload_json TEXT NOT NULL,
			UNIQUE(source_event_key)
		)`,
		`CREATE INDEX idx_metering_facts_stream_seq ON metering_facts(stream_id, sequence)`,
		`CREATE INDEX idx_metering_facts_request ON metering_facts(request_id) WHERE request_id != ''`,
		`CREATE TABLE metering_fact_filters (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL
		)`,
		`CREATE INDEX idx_metering_fact_filters_field
			ON metering_fact_filters(field_name, field_value, stream_id)`,
		`CREATE TABLE bun_metering_journal_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name VARCHAR(255),
			group_id BIGINT,
			migrated_at DATETIME
		)`,
		`INSERT INTO bun_metering_journal_migrations(name, group_id, migrated_at)
			VALUES ('` + journalstore.BaselineMigrationName + `', 1, CURRENT_TIMESTAMP)`,
	} {
		if _, err := bunDB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExactMigrationCounts(t *testing.T, bunDB *bun.DB, want map[string]int) {
	t.Helper()
	ctx := context.Background()
	rows, err := bunDB.QueryContext(ctx, `SELECT name, COUNT(1) FROM bun_metering_journal_migrations GROUP BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		got[name] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for name, n := range want {
		if got[name] != n {
			t.Fatalf("migration %s count=%d want %d (got=%v)", name, got[name], n, got)
		}
	}
	for name, n := range got {
		if _, ok := want[name]; !ok && n > 0 {
			t.Fatalf("unexpected migration name %q count=%d (got=%v)", name, n, got)
		}
	}
}

func upgradeProbeFact(id, stream string) metering.Fact {
	return metering.Fact{
		FactID: id, StreamID: stream, Sequence: 1,
		Kind: metering.FactKindCumulative, Perspective: metering.PerspectiveCustomer,
		Boundary: metering.BoundaryFrontendIngress, Lifecycle: metering.LifecycleLogicalRequest,
		Correlation:     metering.Correlation{RequestID: "r1", ALegID: "a1"},
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		Presence:        metering.PresencePresent,
		RecordedAt:      time.Unix(1, 0).UTC(),
		Quantities:      []metering.Quantity{{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
		IdentityVersion: 1,
		SourceID:        id,
		SourceEventKind: string(metering.FactKindCumulative),
	}
}
