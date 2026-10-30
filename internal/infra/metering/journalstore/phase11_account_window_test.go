package journalstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase11AccountWindowJournal_ProviderAllowanceAppendRequiresObservedProviderClaim(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		acquisition string
		authority   string
	}{
		{
			name:        "local observed claim",
			origin:      aipsdkmetering.OriginLocal,
			acquisition: aipsdkmetering.AcquisitionLocalTokenizer,
			authority:   aipsdkmetering.AuthorityObservedClaim,
		},
		{
			name:        "statement verified claim",
			origin:      aipsdkmetering.OriginStatement,
			acquisition: aipsdkmetering.AcquisitionStatementImporter,
			authority:   aipsdkmetering.AuthorityVerifiedStatement,
		},
		{
			name:        "provider estimated claim",
			origin:      aipsdkmetering.OriginProvider,
			acquisition: aipsdkmetering.AcquisitionProviderHeader,
			authority:   aipsdkmetering.AuthorityEstimatedClaim,
		},
		{
			name:        "provider unavailable claim",
			origin:      aipsdkmetering.OriginProvider,
			acquisition: aipsdkmetering.AcquisitionProviderHeader,
			authority:   aipsdkmetering.AuthorityUnavailableClaim,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteJournal(t)
			observation := phase11JournalAccountWindowObservation("append-"+tc.name, "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
			observation.Origin = tc.origin
			observation.Acquisition = tc.acquisition
			observation.Authority = tc.authority
			err := store.AppendAccountWindowObservation(context.Background(), observation)
			require.Error(t, err, "provider allowance append must reject non-provider or non-observed evidence")
			require.ErrorIs(t, err, aipsdkmetering.ErrInvalidObservation)
		})
	}
}

func TestPhase11AccountWindowJournal_ProviderAllowanceAppendInTxRequiresObservedProviderClaim(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("append-in-tx-local", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	observation.Origin = aipsdkmetering.OriginLocal
	observation.Acquisition = aipsdkmetering.AcquisitionLocalTokenizer

	tx, err := store.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	err = store.AppendAccountWindowObservationInTx(ctx, tx, observation)
	require.ErrorIs(t, err, aipsdkmetering.ErrInvalidObservation)
}

func TestPhase11AccountWindowJournal_GenericAppendStillAllowsNonProviderOrigins(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	local := phase11JournalAccountWindowObservation("generic-local", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	local.Origin = aipsdkmetering.OriginLocal
	local.Acquisition = aipsdkmetering.AcquisitionLocalTokenizer
	statement := phase11JournalAccountWindowObservation("generic-statement", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(11, 0), phase11JournalMeasure("used_percent", "11"))
	statement.Origin = aipsdkmetering.OriginStatement
	statement.Acquisition = aipsdkmetering.AcquisitionStatementImporter
	statement.Authority = aipsdkmetering.AuthorityVerifiedStatement

	require.NoError(t, store.AppendObservation(ctx, local))
	require.NoError(t, store.AppendObservation(ctx, statement))
}

func TestPhase11AccountWindowJournal_ProviderAllowanceQueryRejectsNonProviderRows(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		acquisition string
		authority   string
	}{
		{
			name:        "local",
			origin:      aipsdkmetering.OriginLocal,
			acquisition: aipsdkmetering.AcquisitionLocalTokenizer,
			authority:   aipsdkmetering.AuthorityObservedClaim,
		},
		{
			name:        "statement",
			origin:      aipsdkmetering.OriginStatement,
			acquisition: aipsdkmetering.AcquisitionStatementImporter,
			authority:   aipsdkmetering.AuthorityVerifiedStatement,
		},
		{
			name:        "provider estimate",
			origin:      aipsdkmetering.OriginProvider,
			acquisition: aipsdkmetering.AcquisitionProviderHeader,
			authority:   aipsdkmetering.AuthorityEstimatedClaim,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newSQLiteJournal(t)
			observation := phase11JournalAccountWindowObservation("query-"+tc.name, "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
			observation.Origin = tc.origin
			observation.Acquisition = tc.acquisition
			observation.Authority = tc.authority
			require.NoError(t, store.AppendObservation(ctx, observation), "generic account-window observations remain appendable")

			_, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
			require.ErrorIs(t, err, aipsdkmetering.ErrInvalidObservation)
			_, err = store.QueryAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
			require.ErrorIs(t, err, aipsdkmetering.ErrInvalidObservation)
			require.NoError(t, store.RebuildObservationProjections(ctx), "generic projection rebuild must remain available for non-provider evidence")
			_, err = store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
			require.ErrorIs(t, err, aipsdkmetering.ErrInvalidObservation)
		})
	}
}

func TestPhase11AccountWindowJournal_ProviderOriginReplayQueryAndProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("provider-rebuild", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation), "exact provider replay remains idempotent")

	query := journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10}
	history, err := store.ListAccountWindowObservations(ctx, query)
	require.NoError(t, err)
	require.Len(t, history.Observations, 1)
	require.Equal(t, observation.ID, history.Observations[0].ID)

	before, err := store.ProjectAccountWindows(ctx, query)
	require.NoError(t, err)
	require.Len(t, before.Projections, 1)
	require.NoError(t, store.RebuildObservationProjections(ctx))
	after, err := store.ProjectAccountWindows(ctx, query)
	require.NoError(t, err)
	require.Equal(t, before.Projections, after.Projections, "provider-origin projection survives component rebuild")
}

func TestPhase11AccountWindowJournal_PersistsHistoryAndProjectsCurrentAsOf(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	reset := time.Unix(1_000, 0).UTC()

	newer := phase11JournalAccountWindowObservation("newer", "acct-a", "pool-a", "window-a", reset, time.Unix(20, 0),
		phase11JournalMeasure("used_percent", "13.0"))
	older := phase11JournalAccountWindowObservation("older", "acct-a", "pool-a", "window-a", reset, time.Unix(10, 0),
		phase11JournalMeasure("used_percent", "12.5"), phase11JournalMeasure("limit", "100"))
	partial := phase11JournalAccountWindowObservation("partial", "acct-a", "pool-a", "window-a", reset, time.Unix(30, 0),
		phase11JournalMeasure("remaining_percent", "87.0"))
	otherPool := phase11JournalAccountWindowObservation("other-pool", "acct-a", "pool-b", resetWindowID(), reset, time.Unix(31, 0),
		phase11JournalMeasure("used_percent", "99.0"))
	otherAccount := phase11JournalAccountWindowObservation("other-account", "acct-b", "pool-a", "window-a", reset, time.Unix(32, 0),
		phase11JournalMeasure("used_percent", "1.0"))

	// Deliberately append out of effective order. Query and projection must use
	// observed_at, not arrival/insertion order.
	for _, observation := range []aipsdkmetering.Observation{newer, otherPool, older, otherAccount, partial} {
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}
	require.NoError(t, store.AppendAccountWindowObservation(ctx, partial), "identical replay must be idempotent")

	conflict := partial.Clone()
	conflict.Measures[0].Value = phase11JournalDecimal(t, "88.0")
	require.ErrorIs(t, store.AppendAccountWindowObservation(ctx, conflict), journalstore.ErrIdentityCollision)

	query := journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 20}
	history, err := store.ListAccountWindowObservations(ctx, query)
	require.NoError(t, err)
	require.Len(t, history.Observations, 3)
	require.Equal(t, []string{"older", "newer", "partial"}, []string{history.Observations[0].ID, history.Observations[1].ID, history.Observations[2].ID})
	require.Equal(t, reset, history.Observations[0].Subject.ResetAt)
	require.Equal(t, "acct-a", history.Observations[0].Subject.ProviderAccountKey)
	require.Equal(t, "informational-older", history.Observations[0].Correlation.RequestID, "request association is retained as informational evidence")

	projections, err := store.ProjectAccountWindows(ctx, query)
	require.NoError(t, err)
	require.Len(t, projections.Projections, 1)
	projection := projections.Projections[0]
	require.Equal(t, time.Unix(30, 0).UTC(), projection.ObservedAt)
	require.Equal(t, "13/0", phase11ProjectionValue(t, projection, "used_percent"), "older late snapshot must not replace newer gauge")
	require.Equal(t, "100/0", phase11ProjectionValue(t, projection, "limit"), "partial gauge fields must be retained")
	require.Equal(t, "87/0", phase11ProjectionValue(t, projection, "remaining_percent"))
	require.Len(t, projection.ObservationRefs, 3)
	require.Empty(t, projection.Subject.RequestID, "request association must not create a second account-window subject")

	asOfQuery := query
	asOfQuery.AsOf = time.Unix(25, 0).UTC()
	asOf, err := store.ProjectAccountWindows(ctx, asOfQuery)
	require.NoError(t, err)
	require.Len(t, asOf.Projections, 1)
	require.Equal(t, "13/0", phase11ProjectionValue(t, asOf.Projections[0], "used_percent"))
	require.Empty(t, phase11ProjectionMeasure(asOf.Projections[0], "remaining_percent"))

	allPools, err := store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", Limit: 20})
	require.NoError(t, err)
	require.Len(t, allPools.Projections, 2, "pool identities must not be combined")

	other, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-b", Limit: 20})
	require.NoError(t, err)
	require.Len(t, other.Observations, 1)
	require.Equal(t, "other-account", other.Observations[0].ID)
}

func TestPhase11AccountWindowJournal_HistoryAndProjectionCursorsAreFilterBound(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	reset := time.Unix(1_000, 0).UTC()
	for i, value := range []string{"1", "2", "3"} {
		observation := phase11JournalAccountWindowObservation("cursor-"+value, "acct-a", "pool-a", "window-a", reset, time.Unix(int64(10+i), 0), phase11JournalMeasure("used_percent", value))
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}
	require.NoError(t, store.AppendAccountWindowObservation(ctx, phase11JournalAccountWindowObservation("cursor-pool-b", "acct-a", "pool-b", "window-a", reset, time.Unix(40, 0), phase11JournalMeasure("used_percent", "4"))))

	query := journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 1}
	first, err := store.ListAccountWindowObservations(ctx, query)
	require.NoError(t, err)
	require.Len(t, first.Observations, 1)
	require.NotEmpty(t, first.NextCursor)
	query.Cursor = first.NextCursor
	second, err := store.ListAccountWindowObservations(ctx, query)
	require.NoError(t, err)
	require.Equal(t, "cursor-2", second.Observations[0].ID)
	query.PoolID = "pool-b"
	require.ErrorIs(t, mustListAccountWindowObservations(ctx, store, query), journalstore.ErrInvalidCursor)

	projectionQuery := journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", Limit: 1}
	projectionPage, err := store.ProjectAccountWindows(ctx, projectionQuery)
	require.NoError(t, err)
	require.Len(t, projectionPage.Projections, 1)
	require.NotEmpty(t, projectionPage.NextCursor)
	projectionQuery.Cursor = projectionPage.NextCursor
	projectionPage, err = store.ProjectAccountWindows(ctx, projectionQuery)
	require.NoError(t, err)
	require.Len(t, projectionPage.Projections, 1)
	require.Equal(t, "pool-b", projectionPage.Projections[0].Subject.PoolID)
}

func TestPhase11AccountWindowJournal_HistoryResetFilterCursorBinding(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	epochReset := time.Unix(0, 0).UTC()
	otherReset := time.Unix(1_000, 0).UTC()
	for _, observation := range []aipsdkmetering.Observation{
		phase11JournalAccountWindowObservation("history-epoch-one", "acct-a", "pool-a", "window-a", epochReset, time.Unix(10, 0), phase11JournalMeasure("used_percent", "1")),
		phase11JournalAccountWindowObservation("history-epoch-two", "acct-a", "pool-a", "window-a", epochReset, time.Unix(11, 0), phase11JournalMeasure("used_percent", "2")),
		phase11JournalAccountWindowObservation("history-other-reset", "acct-a", "pool-a", "window-a", otherReset, time.Unix(12, 0), phase11JournalMeasure("used_percent", "3")),
	} {
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}

	// The reset-less query and the explicit Unix-epoch query have different SQL
	// predicates even though the epoch's Unix-nanosecond value is zero.
	broadQuery := journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 1}
	broadFirst, err := store.ListAccountWindowObservations(ctx, broadQuery)
	require.NoError(t, err)
	require.Len(t, broadFirst.Observations, 1)
	require.Equal(t, "history-epoch-one", broadFirst.Observations[0].ID)
	require.NotEmpty(t, broadFirst.NextCursor)
	broadQuery.Cursor = broadFirst.NextCursor
	broadSecond, err := store.ListAccountWindowObservations(ctx, broadQuery)
	require.NoError(t, err)
	require.Len(t, broadSecond.Observations, 1)
	require.Equal(t, "history-epoch-two", broadSecond.Observations[0].ID, "same-filter pagination must remain stable")

	epochQuery := broadQuery
	epochQuery.Cursor = ""
	epochQuery.ResetAt = &epochReset
	epochFirst, err := store.ListAccountWindowObservations(ctx, epochQuery)
	require.NoError(t, err)
	require.Len(t, epochFirst.Observations, 1)
	require.Equal(t, "history-epoch-one", epochFirst.Observations[0].ID)
	require.NotEmpty(t, epochFirst.NextCursor)
	epochQuery.Cursor = epochFirst.NextCursor
	epochSecond, err := store.ListAccountWindowObservations(ctx, epochQuery)
	require.NoError(t, err)
	require.Len(t, epochSecond.Observations, 1)
	require.Equal(t, "history-epoch-two", epochSecond.Observations[0].ID, "same-filter pagination must remain stable")

	crossFilter := epochQuery
	crossFilter.Cursor = broadFirst.NextCursor
	_, err = store.ListAccountWindowObservations(ctx, crossFilter)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "a cursor from the reset-less filter must not enter the epoch filter")

	crossFilter = broadQuery
	crossFilter.Cursor = epochFirst.NextCursor
	_, err = store.ListAccountWindowObservations(ctx, crossFilter)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "a cursor from the epoch filter must not enter the reset-less filter")
}

func TestPhase11AccountWindowJournal_ProjectionAsOfResetFilterCursorBinding(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	epochReset := time.Unix(0, 0).UTC()
	otherReset := time.Unix(1_000, 0).UTC()
	for _, observation := range []aipsdkmetering.Observation{
		phase11JournalAccountWindowObservation("projection-epoch-pool-a", "acct-a", "pool-a", "window-a", epochReset, time.Unix(10, 0), phase11JournalMeasure("used_percent", "1")),
		phase11JournalAccountWindowObservation("projection-epoch-pool-b", "acct-a", "pool-b", "window-a", epochReset, time.Unix(11, 0), phase11JournalMeasure("used_percent", "2")),
		phase11JournalAccountWindowObservation("projection-other-reset", "acct-a", "pool-c", "window-a", otherReset, time.Unix(12, 0), phase11JournalMeasure("used_percent", "3")),
	} {
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}

	asOf := time.Unix(100, 0).UTC()
	broadQuery := journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", AsOf: asOf, Limit: 1}
	broadFirst, err := store.ProjectAccountWindows(ctx, broadQuery)
	require.NoError(t, err)
	require.Len(t, broadFirst.Projections, 1)
	require.Equal(t, "pool-a", broadFirst.Projections[0].Subject.PoolID)
	require.NotEmpty(t, broadFirst.NextCursor)
	broadQuery.Cursor = broadFirst.NextCursor
	broadSecond, err := store.ProjectAccountWindows(ctx, broadQuery)
	require.NoError(t, err)
	require.Len(t, broadSecond.Projections, 1)
	require.Equal(t, "pool-b", broadSecond.Projections[0].Subject.PoolID, "same-filter as-of pagination must remain stable")

	epochQuery := broadQuery
	epochQuery.Cursor = ""
	epochQuery.ResetAt = &epochReset
	epochFirst, err := store.ProjectAccountWindows(ctx, epochQuery)
	require.NoError(t, err)
	require.Len(t, epochFirst.Projections, 1)
	require.Equal(t, "pool-a", epochFirst.Projections[0].Subject.PoolID)
	require.NotEmpty(t, epochFirst.NextCursor)
	epochQuery.Cursor = epochFirst.NextCursor
	epochSecond, err := store.ProjectAccountWindows(ctx, epochQuery)
	require.NoError(t, err)
	require.Len(t, epochSecond.Projections, 1)
	require.Equal(t, "pool-b", epochSecond.Projections[0].Subject.PoolID, "same-filter as-of pagination must remain stable")

	crossFilter := epochQuery
	crossFilter.Cursor = broadFirst.NextCursor
	_, err = store.ProjectAccountWindows(ctx, crossFilter)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "a cursor from the reset-less as-of filter must not enter the epoch filter")

	crossFilter = broadQuery
	crossFilter.Cursor = epochFirst.NextCursor
	_, err = store.ProjectAccountWindows(ctx, crossFilter)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "a cursor from the epoch as-of filter must not enter the reset-less filter")
}

func mustListAccountWindowObservations(ctx context.Context, store *journalstore.DurableStore, query journalstore.AccountWindowQuery) error {
	_, err := store.ListAccountWindowObservations(ctx, query)
	return err
}

func TestPhase11AccountWindowJournal_ResetEpochAndRestartRemainDistinct(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	resetOne := time.Unix(1_000, 0).UTC()
	resetTwo := time.Unix(2_000, 0).UTC()
	for _, observation := range []aipsdkmetering.Observation{
		phase11JournalAccountWindowObservation("reset-one", "acct-a", "pool-a", "window-a", resetOne, time.Unix(10, 0), phase11JournalMeasure("used_percent", "80")),
		phase11JournalAccountWindowObservation("reset-two", "acct-a", "pool-a", "window-a", resetTwo, time.Unix(11, 0), phase11JournalMeasure("used_percent", "2")),
	} {
		require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))
	}
	page, err := store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 20})
	require.NoError(t, err)
	require.Len(t, page.Projections, 2)
	require.Equal(t, "2/0", phase11ProjectionValue(t, page.Projections[1], "used_percent"))

	// OpenStore on the same durable handle models a host restart: the account
	// gauge projection is rebuilt from immutable observation history.
	reopened, err := journalstore.OpenStore(ctx, store.DB(), journalstore.DurableConfig{StoreID: "sqlite-test"})
	require.NoError(t, err)
	reopenedPage, err := reopened.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", Limit: 20})
	require.NoError(t, err)
	require.Equal(t, page.Projections, reopenedPage.Projections)
}

func TestPhase11AccountWindowJournal_FileRestartRebuildsCurrentProjection(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(filepath.Join(t.TempDir(), "metering.db")))
	firstSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	firstBun, err := db.NewBunDB(firstSQL, db.DialectSQLite)
	require.NoError(t, err)
	first, err := journalstore.NewDurableStore(ctx, firstBun, journalstore.DurableConfig{StoreID: "restart-store"})
	require.NoError(t, err)
	observation := phase11JournalAccountWindowObservation("restart", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("remaining_percent", "42.25"))
	observation.Subject.StoreID = "restart-store"
	observation.Correlation.StoreID = "restart-store"
	require.NoError(t, first.AppendAccountWindowObservation(ctx, observation))
	require.NoError(t, first.Close())
	require.NoError(t, firstSQL.Close())

	secondSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondSQL.Close() })
	secondBun, err := db.NewBunDB(secondSQL, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondBun.Close() })
	second, err := journalstore.OpenStore(ctx, secondBun, journalstore.DurableConfig{StoreID: "restart-store"})
	require.NoError(t, err)
	page, err := second.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: "restart-store", ProviderAccountKey: "acct-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Projections, 1)
	require.Equal(t, "4225/2", phase11ProjectionValue(t, page.Projections[0], "remaining_percent"))
}

func TestPhase11AccountWindowJournal_RequiresProviderAccountAndBoundsHistory(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	_, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{Limit: 10})
	require.ErrorIs(t, err, journalstore.ErrQueryTooBroad)
	_, err = store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", Limit: 501})
	require.ErrorIs(t, err, journalstore.ErrPageSizeExceeded)
	_, err = store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10, Cursor: "bad"})
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor)
}

func TestPhase11AccountWindowJournal_SearchProjectionDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("drift", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))

	_, err := store.DB().ExecContext(ctx, `UPDATE metering_facts SET observation_pool_id = ? WHERE observation_id = ?`, "pool-corrupt", observation.ID)
	require.NoError(t, err)
	_, err = store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-corrupt", Limit: 10})
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision)
}

func TestPhase11AccountWindowJournal_StreamIDSearchProjectionDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("stream-drift", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))

	_, err := store.DB().ExecContext(ctx, `UPDATE metering_facts SET stream_id = ? WHERE observation_id = ?`, "stream-corrupt", observation.ID)
	require.NoError(t, err)

	tests := []struct {
		name  string
		query func() error
	}{
		{
			name: "history",
			query: func() error {
				_, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", Limit: 10})
				return err
			},
		},
		{
			name: "history compatibility spelling",
			query: func() error {
				_, err := store.QueryAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", Limit: 10})
				return err
			},
		},
		{
			name: "current projection",
			query: func() error {
				_, err := store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", Limit: 10})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.query(), journalstore.ErrIdentityCollision)
		})
	}
}

func TestPhase11AccountWindowJournal_AllowsUnixEpochReset(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("epoch", "acct-a", "pool-a", "window-a", time.Unix(0, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))

	page, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, observation.ID, page.Observations[0].ID)
}

func TestPhase11AccountWindowJournal_PayloadDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation("payload-drift", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))

	mutated := observation.Clone()
	mutated.Measures[0].Value = phase11JournalDecimal(t, "11")
	payload, err := mutated.CanonicalJSON()
	require.NoError(t, err)
	_, err = store.DB().ExecContext(ctx, `UPDATE metering_facts SET payload_json = ? WHERE observation_id = ?`, string(payload), observation.ID)
	require.NoError(t, err)
	_, err = store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{ProviderAccountKey: "acct-a", PoolID: "pool-a", Limit: 10})
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision)
}

func TestPhase11AccountWindowJournal_ConcurrentOutOfOrderAppendsHaveOneDeterministicHead(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	reset := time.Unix(1_000, 0).UTC()
	older := phase11JournalAccountWindowObservation("concurrent-old", "acct-a", "pool-a", "window-a", reset, time.Unix(10, 0), phase11JournalMeasure("used_percent", "10"))
	newer := phase11JournalAccountWindowObservation("concurrent-new", "acct-a", "pool-a", "window-a", reset, time.Unix(20, 0), phase11JournalMeasure("used_percent", "20"))
	var group sync.WaitGroup
	errs := make(chan error, 2)
	group.Add(2)
	go func() {
		defer group.Done()
		errs <- store.AppendAccountWindowObservation(ctx, newer)
	}()
	go func() {
		defer group.Done()
		errs <- store.AppendAccountWindowObservation(ctx, older)
	}()
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	page, err := store.ProjectAccountWindows(ctx, journalstore.AccountWindowQuery{StoreID: "sqlite-test", ProviderAccountKey: "acct-a", PoolID: "pool-a", WindowID: "window-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Projections, 1)
	require.Equal(t, "20/0", phase11ProjectionValue(t, page.Projections[0], "used_percent"))
}

func phase11JournalAccountWindowObservation(id, account, pool, window string, reset, observed time.Time, measures ...aipsdkmetering.Measure) aipsdkmetering.Observation {
	return aipsdkmetering.Observation{
		Version: aipsdkmetering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "account-window", Sequence: uint64(observed.Unix()), Origin: aipsdkmetering.OriginProvider,
		Acquisition: aipsdkmetering.AcquisitionProviderHeader, Authority: aipsdkmetering.AuthorityObservedClaim,
		Perspective: aipsdkmetering.PerspectiveOperator, Boundary: aipsdkmetering.BoundaryBackendEgress,
		Lifecycle:   aipsdkmetering.LifecycleAuxiliaryRequest,
		Subject:     aipsdkmetering.SubjectRef{Kind: aipsdkmetering.SubjectAccountWindow, StoreID: "sqlite-test", ProviderAccountKey: account, PoolID: pool, WindowID: window, ResetAt: reset},
		Correlation: aipsdkmetering.CorrelationV2{StoreID: "sqlite-test", ProviderAccountKey: account, RequestID: "informational-" + id},
		Semantics:   aipsdkmetering.SemanticsGauge, ObservedAt: observed.UTC(), ReceivedAt: observed.Add(time.Second).UTC(),
		MappingRef: "account-window.v1", Measures: measures,
	}
}

func resetWindowID() string { return "window-a" }

func phase11JournalMeasure(component, value string) aipsdkmetering.Measure {
	parsed, err := aipsdkmetering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return aipsdkmetering.Measure{Key: aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionNone, Component: component, Unit: aipsdkmetering.UnitPercent, SchemaID: "account-window.v1"}, Value: &parsed, Quality: aipsdkmetering.QualityObserved}
}

func phase11JournalDecimal(t *testing.T, value string) *aipsdkmetering.Decimal {
	t.Helper()
	parsed, err := aipsdkmetering.ParseDecimal(value)
	require.NoError(t, err)
	return &parsed
}

func phase11ProjectionMeasure(projection coremetering.AccountWindowProjection, component string) *aipsdkmetering.Measure {
	for i := range projection.Measures {
		if projection.Measures[i].Key.Component == component {
			return &projection.Measures[i]
		}
	}
	return nil
}

func phase11ProjectionValue(t *testing.T, projection coremetering.AccountWindowProjection, component string) string {
	t.Helper()
	measure := phase11ProjectionMeasure(projection, component)
	if measure == nil || measure.Value == nil {
		return ""
	}
	return measure.Value.CanonicalString()
}
