package journalstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// R10 repair contract: correlated B-leg evidence is selected by the indexed
// metering_facts.b_leg_id key, bounded to the trusted store, instead of a
// provider-account scan that pages unrelated observations.

func r10StatementObservation(store, id, blegID string, revision uint64) metering.Observation {
	now := time.Unix(1_700_010_000+int64(revision), 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:statement", Unit: metering.UnitToken, SchemaID: "vendor:statement:v1"}
	value := metering.Decimal{Coefficient: "7", Scale: 0}
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: "source-" + id, Revision: revision,
		StreamID: "statement-stream-" + id, Sequence: revision,
		Origin: metering.OriginStatement, Acquisition: metering.AcquisitionStatementImporter, Authority: metering.AuthorityVerifiedStatement,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectStatementLine, StoreID: store, ProviderAccountKey: "provider-1", StatementID: "statement-1", StatementLineID: id},
		Correlation: metering.CorrelationV2{StoreID: store, ProviderAccountKey: "provider-1", BLegID: blegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider:statement:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
}

func TestListObservationsCorrelationBLegIsSelective(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	target := r10StatementObservation("sqlite-test", "stmt-target", "b-target", 1)
	require.NoError(t, store.AppendObservation(ctx, target))
	for i := range 25 {
		other := r10StatementObservation("sqlite-test", fmt.Sprintf("stmt-other-%02d", i), fmt.Sprintf("b-other-%02d", i), 1)
		require.NoError(t, store.AppendObservation(ctx, other))
	}

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-target", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, target.ID, page.Observations[0].ID)
	require.Empty(t, page.NextCursor)
}

func TestListObservationsCorrelationBLegIsAValidSelectiveBound(t *testing.T) {
	store := newSQLiteJournal(t)
	_, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-target", Limit: 10})
	require.NoError(t, err, "a correlation B-leg bound must satisfy the query breadth guard")
	_, err = store.ListObservations(context.Background(), journalstore.ObservationQuery{StoreID: "sqlite-test", Limit: 10})
	require.ErrorIs(t, err, journalstore.ErrQueryTooBroad)
}

func TestListObservationsCorrelationBLegPaginates(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		obs := r10StatementObservation("sqlite-test", fmt.Sprintf("stmt-page-%d", i), "b-page", uint64(i))
		require.NoError(t, store.AppendObservation(ctx, obs))
	}
	query := journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-page", Limit: 1}
	seen := make(map[string]bool)
	pages := 0
	for {
		page, err := store.ListObservations(ctx, query)
		require.NoError(t, err)
		pages++
		for _, obs := range page.Observations {
			seen[obs.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	require.Len(t, seen, 3)
	require.Equal(t, 3, pages)
}

func TestListObservationsCorrelationBLegIsStoreScoped(t *testing.T) {
	ctx := context.Background()
	base := newSQLiteJournal(t)
	storeA, err := journalstore.OpenStore(ctx, base.DB(), journalstore.DurableConfig{StoreID: "store-a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := journalstore.OpenStore(ctx, base.DB(), journalstore.DurableConfig{StoreID: "store-b"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeB.Close() })

	mine := r10StatementObservation("store-a", "stmt-a", "b-shared", 1)
	theirs := r10StatementObservation("store-b", "stmt-b", "b-shared", 1)
	require.NoError(t, storeA.AppendObservation(ctx, mine))
	require.NoError(t, storeB.AppendObservation(ctx, theirs))

	page, err := storeA.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "store-a", CorrelationBLegID: "b-shared", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, mine.ID, page.Observations[0].ID)
}

// TestListObservationsCorrelationBLegUsesPartialIndex proves the production SQL
// carries an indexed store-scoped B-leg bound rather than scanning the store.
func TestListObservationsCorrelationBLegUsesPartialIndex(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	require.NoError(t, store.AppendObservation(ctx, r10StatementObservation("sqlite-test", "stmt-plan", "b-plan", 1)))

	recorder := &r10ListQueryRecorder{}
	store.DB().AddQueryHook(recorder)
	_, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-plan", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, recorder.queries, "expected a ListObservations query to record")

	var plan strings.Builder
	for _, query := range recorder.queries {
		plan.WriteString(explainSQLiteQueryPlan(ctx, t, store.DB(), query))
	}
	t.Logf("EXPLAIN QUERY PLAN:\n%s", plan.String())
	require.Contains(t, plan.String(), "idx_metering_facts_store_bleg", "correlation B-leg query must use the partial index; plan:\n%s", plan.String())
}

type r10ListQueryRecorder struct {
	queries []string
}

func (h *r10ListQueryRecorder) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *r10ListQueryRecorder) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if strings.Contains(event.Query, "FROM metering_facts f WHERE") {
		h.queries = append(h.queries, event.Query)
	}
}
