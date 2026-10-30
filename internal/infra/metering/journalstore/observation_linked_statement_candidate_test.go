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
)

// R10-B repair contract: the economic relay's linked-statement lookup is a
// statement-line-only, verified-provenance indexed bound. Without it a single
// B-leg's unrelated usage observations are enumerated as candidates.

func TestListObservationsStatementEvidenceOnlyIsSelective(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()

	linked := r10StatementObservation("sqlite-test", "stmt-linked", "b-target", 1)
	unrelatedBLeg := r10UnrelatedBLegObservation(t, "sqlite-test", "usage-target", "b-target")
	unverified := r10StatementObservation("sqlite-test", "stmt-unverified", "b-target", 1)
	unverified.Authority = metering.AuthorityObservedClaim
	require.NoError(t, unverified.Validate())
	otherBLeg := r10StatementObservation("sqlite-test", "stmt-other", "b-other", 1)
	for _, observation := range []metering.Observation{linked, unrelatedBLeg, unverified, otherBLeg} {
		require.NoError(t, store.AppendObservation(ctx, observation))
	}

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "sqlite-test", CorrelationBLegID: "b-target", StatementEvidenceOnly: true, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1, "only verified statement lines for the B-leg are candidates")
	require.Equal(t, linked.ID, page.Observations[0].ID)
	require.Empty(t, page.NextCursor)
}

func TestListObservationsStatementEvidenceOnlyRequiresCorrelationBLeg(t *testing.T) {
	store := newSQLiteJournal(t)
	_, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{
		StoreID: "sqlite-test", StatementEvidenceOnly: true, Limit: 10,
	})
	require.ErrorIs(t, err, journalstore.ErrQueryOutOfScope)
}

// TestListObservationsStatementEvidenceOnlyUsesCandidateIndex proves the
// production identifier predicate and ORDER BY are served by the dedicated
// statement partial index rather than the generic B-leg index.
func TestListObservationsStatementEvidenceOnlyUsesCandidateIndex(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	for i := range 50 {
		unrelated := r10UnrelatedBLegObservation(t, "sqlite-test", fmt.Sprintf("usage-plan-%03d", i), "b-plan")
		require.NoError(t, store.AppendObservation(ctx, unrelated))
	}
	require.NoError(t, store.AppendObservation(ctx, r10StatementObservation("sqlite-test", "stmt-plan", "b-plan", 1)))

	recorder := &r10ListQueryRecorder{}
	store.DB().AddQueryHook(recorder)
	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "sqlite-test", CorrelationBLegID: "b-plan", StatementEvidenceOnly: true, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.NotEmpty(t, recorder.queries, "expected a ListObservations query to record")

	var plan strings.Builder
	for _, query := range recorder.queries {
		plan.WriteString(explainSQLiteQueryPlan(ctx, t, store.DB(), query))
	}
	explainPlan := plan.String()
	t.Logf("EXPLAIN QUERY PLAN:\n%s", explainPlan)
	require.Contains(t, explainPlan, "idx_metering_facts_store_bleg_statement",
		"statement evidence lookup must use the dedicated partial index; plan:\n%s", explainPlan)
	require.NotContains(t, explainPlan, "TEMP B-TREE",
		"statement evidence lookup must satisfy ORDER BY from the index; plan:\n%s", explainPlan)
}

func r10UnrelatedBLegObservation(t *testing.T, store, id, blegID string) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_020_000, 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "vendor:tokens:v1"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	observation := metering.Observation{
		Version: 2, ID: id, SourceEventKey: "source-" + id, Revision: 1,
		StreamID: "usage-stream-" + id, Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store, ProviderAccountKey: "provider-1", BLegID: blegID},
		Correlation: metering.CorrelationV2{StoreID: store, ProviderAccountKey: "provider-1", BLegID: blegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider:tokens:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
	require.NoError(t, observation.Validate())
	return observation
}
