package journalstore_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestRefinement41AppendObservationsBatchIsQueryableAndAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)

	first := phase4Observation("sqlite-test", "refinement41-first", 1)
	second := phase4Observation("sqlite-test", "refinement41-second", 2)
	sink := journalstore.NewObservationSink(store)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok, "journalstore observation sink must expose atomic batch capability")
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{first, second}))

	page, err := store.ListObservations(ctx, ObservationQueryForPhase4(first, 20))
	require.NoError(t, err)
	require.Len(t, page.Observations, 2)

	// A replay of a complete batch remains idempotent.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{first, second}))

	third := phase4Observation("sqlite-test", "refinement41-third", 3)
	collision := second.Clone()
	collision.Measures[0].Value = &metering.Decimal{Coefficient: "999", Scale: 1}
	err = atomicSink.AppendObservations(ctx, []metering.Observation{third, collision})
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision)

	// The collision rolls back the whole batch, so the otherwise valid third
	// observation is not visible either.
	page, err = store.ListObservations(ctx, ObservationQueryForPhase4(first, 20))
	require.NoError(t, err)
	require.Len(t, page.Observations, 2)
}
