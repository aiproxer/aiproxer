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

func TestListObservationsStatementEvidenceOnlyPostgres(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("linked-statement-candidate")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)

	linked := r10StatementObservation(storeID, "stmt-linked", "b-target", 1)
	unrelated := r10UnrelatedBLegObservation(t, storeID, "usage-target", "b-target")
	unverified := r10StatementObservation(storeID, "stmt-unverified", "b-target", 1)
	unverified.Authority = "observed_claim"
	require.NoError(t, unverified.Validate())

	require.NoError(t, store.AppendObservation(ctx, linked))
	require.NoError(t, store.AppendObservation(ctx, unrelated))
	require.NoError(t, store.AppendObservation(ctx, unverified))

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, CorrelationBLegID: "b-target", StatementEvidenceOnly: true, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, linked.ID, page.Observations[0].ID)
}

// TestListObservationsStatementEvidenceOnlyPostgresUsesCandidateIndex proves the
// production predicate and ORDER BY shape are served by the dedicated statement
// partial index on PostgreSQL without a separate Sort node.
func TestListObservationsStatementEvidenceOnlyPostgresUsesCandidateIndex(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("linked-statement-candidate-plan")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)

	for i := range 50 {
		other := r10UnrelatedBLegObservation(t, storeID, fmt.Sprintf("usage-plan-%03d", i), "b-plan-target")
		require.NoError(t, store.AppendObservation(ctx, other))
	}
	require.NoError(t, store.AppendObservation(ctx, r10StatementObservation(storeID, "stmt-plan-target", "b-plan-target", 1)))

	explain := fmt.Sprintf(`EXPLAIN SELECT f.id FROM metering_facts f
		WHERE f.store_id = '%s' AND f.payload_kind = 'observation' AND f.b_leg_id = 'b-plan-target'
		  AND f.observation_subject_kind = 'statement_line'
		  AND f.observation_origin = 'statement'
		  AND f.observation_acquisition = 'statement_importer'
		  AND f.authority = 'verified_statement'
		ORDER BY f.stream_id ASC, f.sequence ASC, f.observation_id ASC, f.observation_revision ASC, f.id ASC
		LIMIT 11`, storeID)
	rows, err := store.DB().QueryContext(ctx, explain)
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
	require.Contains(t, plan.String(), "idx_metering_facts_store_bleg_statement",
		"statement evidence query must use the dedicated partial index; plan:\n%s", plan.String())
	require.NotContains(t, plan.String(), "Sort",
		"statement evidence query must satisfy ORDER BY from the index, not a Sort node; plan:\n%s", plan.String())
}
