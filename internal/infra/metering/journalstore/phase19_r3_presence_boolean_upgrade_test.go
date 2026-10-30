package journalstore_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestPhase19R3_SQLite_PresenceRepairIdempotent ensures the forward repair is
// idempotent on SQLite (INTEGER is the dialect-native boolean) and restores
// missing observation-projection indexes without losing present/absent data
// or canonical hash linkage.
func TestPhase19R3_SQLite_PresenceRepairIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	require.NoError(t, journalstore.Migrate(ctx, bunDB))
	require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

	// Insert representative present/absent observation via public API.
	store, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "r3-upgrade-sqlite"})
	require.NoError(t, err)
	obs := phase4Observation("r3-upgrade-sqlite", "r3-obs-1", 1)
	require.NoError(t, store.AppendObservation(ctx, obs))

	before, err := store.ListObservationComponents(ctx, journalstore.ComponentQuery{StoreID: "r3-upgrade-sqlite", StreamID: obs.StreamID, Limit: 20})
	require.NoError(t, err)
	require.NotEmpty(t, before.Components)
	var presentTrue, presentFalse bool
	var hashLinked bool
	wantHash := obs.Measures[0].Key.Fingerprint()
	for _, c := range before.Components {
		if c.ValuePresent {
			presentTrue = true
		}
		if !c.ValuePresent {
			presentFalse = true
		}
		if c.ComponentKeyHash == wantHash && c.ComponentKey != "" {
			hashLinked = true
		}
	}
	require.True(t, presentTrue, "must have present=true component")
	require.True(t, presentFalse, "must have present=false component")
	require.True(t, hashLinked, "must preserve canonical hash linkage")

	// Simulate legacy missing indexes (as left by the pooled negative test
	// before its restore fix) and repair via forward migration.
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
	require.Error(t, journalstore.VerifySchema(ctx, bunDB))
	// Force forward repair to rerun (migrations only run once).
	_, err = bunDB.ExecContext(ctx, `DELETE FROM bun_metering_journal_migrations WHERE name = ?`, journalstore.PresenceBooleanRepairMigrationName)
	require.NoError(t, err)
	require.NoError(t, journalstore.Migrate(ctx, bunDB))
	require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
	require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

	after, err := store.ListObservationComponents(ctx, journalstore.ComponentQuery{StoreID: "r3-upgrade-sqlite", StreamID: obs.StreamID, Limit: 20})
	require.NoError(t, err)
	require.Equal(t, len(before.Components), len(after.Components))
	for i := range before.Components {
		require.Equal(t, before.Components[i].ValuePresent, after.Components[i].ValuePresent)
		require.Equal(t, before.Components[i].MoneyPresent, after.Components[i].MoneyPresent)
		require.Equal(t, before.Components[i].ComponentKeyHash, after.Components[i].ComponentKeyHash)
		require.Equal(t, before.Components[i].ComponentKey, after.Components[i].ComponentKey)
	}
	// Second Migrate must remain idempotent.
	require.NoError(t, journalstore.Migrate(ctx, bunDB))
}
