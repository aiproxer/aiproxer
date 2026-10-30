//go:build integration

package journalstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestPhase19R3_Postgres_PresenceUpgradeFromInt4 simulates a legacy store
// where metering_components.value_present/money_present are INTEGER with
// representative 0/1 values and the five observation-projection indexes are
// missing. Migrate must convert to BOOLEAN with validation, restore indexes,
// and preserve present/absent values plus canonical hash linkage.
//
// Isolation follows the shared-schema repository pattern: the shared direct
// schema is bootstrapped once, rows are keyed by a unique store_id, and
// admin cleanup removes them. DDL under test runs sequentially within this
// package and Migrate restores the shared schema before the test ends.
func TestPhase19R3_Postgres_PresenceUpgradeFromInt4(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)
	ctx := context.Background()
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 4)

	require.NoError(t, journalstore.Migrate(ctx, bunDB))
	require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

	// Seed representative present/absent observation via public API.
	storeID := testkit.UniquePostgresStoreID("r3-upgrade-pg")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	// NewDurableStore takes ownership; keep bunDB open via store.DB() for raw SQL.
	t.Cleanup(func() { _ = store.Close() })
	// Use a second handle for migration reruns to avoid closing the store's DB.
	obs := phase4Observation(storeID, "r3-pg-obs-1", 1)
	require.NoError(t, store.AppendObservation(ctx, obs))
	before, err := store.ListObservationComponents(ctx, journalstore.ComponentQuery{StoreID: storeID, StreamID: obs.StreamID, Limit: 20})
	require.NoError(t, err)
	require.NotEmpty(t, before.Components)
	wantHash := obs.Measures[0].Key.Fingerprint()
	var wantPresentTrue, wantPresentFalse bool
	for _, c := range before.Components {
		if c.ComponentKeyHash == wantHash {
			wantPresentTrue = c.ValuePresent
		}
		if !c.ValuePresent {
			wantPresentFalse = true
		}
	}
	_ = wantPresentTrue
	require.True(t, wantPresentFalse, "fixture must contain absent value")

	// Downgrade to legacy INTEGER to simulate old store.
	for _, column := range []string{"value_present", "money_present"} {
		_, err := bunDB.NewRaw("ALTER TABLE metering_components ALTER COLUMN " + column + " DROP DEFAULT").Exec(ctx)
		require.NoError(t, err)
		_, err = bunDB.NewRaw("ALTER TABLE metering_components ALTER COLUMN " + column + " TYPE INTEGER USING (CASE WHEN " + column + " THEN 1 ELSE 0 END)").Exec(ctx)
		require.NoError(t, err)
		_, err = bunDB.NewRaw("ALTER TABLE metering_components ALTER COLUMN " + column + " SET DEFAULT 0").Exec(ctx)
		require.NoError(t, err)
	}
	// Drop the five indexes to simulate legacy missing-index state.
	for _, idx := range []string{
		"metering_facts_store_observation_revision_key",
		"idx_metering_components_store_subject",
		"idx_metering_components_store_component",
		"idx_metering_components_store_provider_account",
		"idx_metering_components_observation",
	} {
		_, err := bunDB.ExecContext(ctx, `DROP INDEX IF EXISTS `+idx)
		require.NoError(t, err)
	}
	// Force repair migration to rerun.
	_, err = bunDB.NewRaw("DELETE FROM bun_metering_journal_migrations WHERE name = ?", journalstore.PresenceBooleanRepairMigrationName).Exec(ctx)
	require.NoError(t, err)

	// Verify pre-upgrade state is INTEGER (catalog must fail).
	var dataType string
	require.NoError(t, bunDB.NewRaw(`SELECT data_type FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='metering_components' AND column_name='value_present'`).Scan(ctx, &dataType))
	require.Equal(t, "integer", strings.ToLower(dataType))

	require.NoError(t, journalstore.Migrate(ctx, bunDB))
	// Idempotent rerun.
	require.NoError(t, journalstore.Migrate(ctx, bunDB))
	require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
	require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

	// Verify BOOLEAN type and defaults.
	for _, column := range []string{"value_present", "money_present"} {
		var dt, nullable, def string
		require.NoError(t, bunDB.NewRaw(`SELECT data_type, is_nullable, COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='metering_components' AND column_name=?`, column).Scan(ctx, &dt, &nullable, &def))
		require.Equal(t, "boolean", strings.ToLower(dt), column)
		require.Equal(t, "NO", nullable, column)
		require.Contains(t, strings.ToLower(def), "false", column)
	}

	// Verify data preserved with proper bool types via read path.
	after, err := store.ListObservationComponents(ctx, journalstore.ComponentQuery{StoreID: storeID, StreamID: obs.StreamID, Limit: 20})
	require.NoError(t, err)
	require.Equal(t, len(before.Components), len(after.Components))
	for i := range before.Components {
		require.Equal(t, before.Components[i].ValuePresent, after.Components[i].ValuePresent, "row %d value_present", i)
		require.Equal(t, before.Components[i].MoneyPresent, after.Components[i].MoneyPresent, "row %d money_present", i)
		require.Equal(t, before.Components[i].ComponentKeyHash, after.Components[i].ComponentKeyHash, "row %d hash", i)
		require.Equal(t, before.Components[i].ComponentKey, after.Components[i].ComponentKey, "row %d key", i)
	}
	// Direct bool scan proves reads use proper bool types (would fail on int4).
	var vp, mp bool
	require.NoError(t, bunDB.NewRaw(`SELECT value_present, money_present FROM metering_components WHERE store_id=? LIMIT 1`, storeID).Scan(ctx, &vp, &mp))

	// Writes must use proper bool types after repair (would fail with 42804 on int4).
	obs2 := phase4Observation(storeID, "r3-pg-obs-2", 2)
	obs2.Sequence = 2
	require.NoError(t, store.AppendObservation(ctx, obs2))
}
