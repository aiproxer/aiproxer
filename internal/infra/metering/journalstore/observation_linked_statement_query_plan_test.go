package journalstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// R10-A repair contract: the correlated B-leg lookup must be served by one
// ordered partial index. Seeding a realistic store (many unrelated
// provider-account observations that share the account but not the B-leg, plus
// matching B-leg statement rows) the production query must satisfy both its
// selective bound and its deterministic ORDER BY from idx_metering_facts_store_bleg
// with no SQLite temp B-tree sort.
func TestListObservationsCorrelationBLegOrderedIndexAvoidsTempBTree(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()

	// Unrelated provider-account history: same account, distinct B-legs.
	for i := range 200 {
		unrelated := r10StatementObservation("sqlite-test", fmt.Sprintf("unrelated-%03d", i), fmt.Sprintf("b-unrelated-%03d", i), 1)
		require.NoError(t, store.AppendObservation(ctx, unrelated))
	}
	// Matching B-leg rows across revisions and streams, to exercise every
	// ORDER BY tie-breaker column.
	for i := 1; i <= 4; i++ {
		target := r10StatementObservation("sqlite-test", fmt.Sprintf("stmt-target-%d", i), "b-target", uint64(i))
		require.NoError(t, store.AppendObservation(ctx, target))
	}

	recorder := &r10ListQueryRecorder{}
	store.DB().AddQueryHook(recorder)
	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-target", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Observations, 2)
	require.NotEmpty(t, page.NextCursor)
	require.NotEmpty(t, recorder.queries, "expected a ListObservations query to record")

	var plan strings.Builder
	for _, query := range recorder.queries {
		plan.WriteString(explainSQLiteQueryPlan(ctx, t, store.DB(), query))
	}
	t.Logf("EXPLAIN QUERY PLAN:\n%s", plan.String())
	require.Contains(t, plan.String(), "idx_metering_facts_store_bleg",
		"correlation B-leg query must use the ordered partial index; plan:\n%s", plan.String())
	require.NotContains(t, plan.String(), "TEMP B-TREE",
		"correlation B-leg query must satisfy ORDER BY from the index, not a temp B-tree; plan:\n%s", plan.String())
}

func explainSQLiteQueryPlan(ctx context.Context, t *testing.T, db *bun.DB, query string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query)
	require.NoError(t, err)
	columns, err := rows.Columns()
	require.NoError(t, err)
	var plan strings.Builder
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		require.NoError(t, rows.Scan(pointers...))
		for _, value := range values {
			fmt.Fprintf(&plan, "%v", value)
			plan.WriteString(" | ")
		}
		plan.WriteString("\n")
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return plan.String()
}
