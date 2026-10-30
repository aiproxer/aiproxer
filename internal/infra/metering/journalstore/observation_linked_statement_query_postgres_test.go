//go:build integration

package journalstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
)

func TestListObservationsCorrelationBLegPostgres(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("linked-statement")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)

	target := r10StatementObservation(storeID, "stmt-target", "b-target", 1)
	other := r10StatementObservation(storeID, "stmt-other", "b-other", 1)
	require.NoError(t, store.AppendObservation(ctx, target))
	require.NoError(t, store.AppendObservation(ctx, other))

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, CorrelationBLegID: "b-target", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, target.ID, page.Observations[0].ID)
}

// TestListObservationsCorrelationBLegPostgresUsesOrderedIndex proves the
// production predicate and ORDER BY shape are served by the ordered partial
// index on PostgreSQL without a separate Sort node.
func TestListObservationsCorrelationBLegPostgresUsesOrderedIndex(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("linked-statement-plan")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)

	for i := range 50 {
		other := r10StatementObservation(storeID, fmt.Sprintf("stmt-plan-other-%03d", i), fmt.Sprintf("b-plan-other-%03d", i), 1)
		require.NoError(t, store.AppendObservation(ctx, other))
	}
	require.NoError(t, store.AppendObservation(ctx, r10StatementObservation(storeID, "stmt-plan-target", "b-plan-target", 1)))

	// Prove the index can satisfy the production ordering, not that a tiny
	// fixture wins the optimizer's cost comparison against a scan and sort.
	// Keep the setting on this transaction's connection and restore it on exit.
	tx, err := store.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SET LOCAL enable_sort = off")
	require.NoError(t, err)
	// EXPLAIN a literal expansion of the production predicate and ORDER BY.
	explain := fmt.Sprintf(`EXPLAIN SELECT f.id FROM metering_facts f
		WHERE f.store_id = '%s' AND f.payload_kind = 'observation' AND f.b_leg_id = 'b-plan-target'
		ORDER BY f.stream_id ASC, f.sequence ASC, f.observation_id ASC, f.observation_revision ASC, f.id ASC
		LIMIT 11`, storeID)
	rows, err := tx.QueryContext(ctx, explain)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	require.NoError(t, rows.Err())
	t.Logf("EXPLAIN:\n%s", plan.String())
	require.Contains(t, plan.String(), "idx_metering_facts_store_bleg",
		"correlation B-leg query must use the ordered partial index; plan:\n%s", plan.String())
	require.NotContains(t, plan.String(), "Sort",
		"correlation B-leg query must satisfy ORDER BY from the index, not a Sort node; plan:\n%s", plan.String())
}
