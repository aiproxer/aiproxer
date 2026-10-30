//go:build integration

package journalstore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for metering-journal on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-"+testkit.UniquePostgresStoreID("journal"))
		_ = store
	})
	t.Run("AppendIdempotent", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-append-"+testkit.UniquePostgresStoreID("journal"))
		f := validFact("parity-fact-pg", "parity-stream-pg", 1)
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append pg: %v", err)
		}
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append idempotent pg: %v", err)
		}
	})
	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 4)
		defer func() { _ = bunDB.Close() }()
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_metering_journal_migrations")
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		recorded := make(map[string]bool)
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
			id := name
			if len(name) >= 14 {
				id = name[:14]
			}
			recorded[id] = true
		}
		require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))

		// Verify migration rerun idempotency
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_metering_journal_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}

func TestDBParity_PostgresDirect_AccountWindowGaugeHistoryAndProjection(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("account-window")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	reset := time.Unix(1_000, 0).UTC()
	older := phase11JournalAccountWindowObservation("pg-older", "acct-a", "pool-a", "window-a", reset, time.Unix(10, 0), phase11JournalMeasure("used_percent", "12.5"))
	newer := phase11JournalAccountWindowObservation("pg-newer", "acct-a", "pool-a", "window-a", reset, time.Unix(20, 0), phase11JournalMeasure("remaining_percent", "87.5"))
	for _, observation := range []metering.Observation{newer, older} {
		observation.Subject.StoreID = storeID
		observation.Correlation.StoreID = storeID
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}
	history, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{StoreID: storeID, ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"pg-older", "pg-newer"}, []string{history.Observations[0].ID, history.Observations[1].ID})
	projection, err := store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: storeID, ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, projection.Projections, 1)
	require.Equal(t, "125/1", phase11ProjectionValue(t, projection.Projections[0], "used_percent"))
	require.Equal(t, "875/1", phase11ProjectionValue(t, projection.Projections[0], "remaining_percent"))
}
