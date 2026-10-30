//go:build integration

package journalstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// pgJSONBObservation builds an evidence-only observation (no measure/charge
// component projections) so the live-PostgreSQL test exercises the JSONB
// outbox envelope path without touching the independent value_present
// int4/boolean baseline issue owned by a separate work order.
func pgJSONBObservation(storeID, id string, revision uint64) metering.Observation {
	observation := phase4Observation(storeID, id, revision)
	observation.Measures = nil
	observation.Charges = nil
	observation.Evidence = []metering.SafeEvidenceField{
		{Path: "$.usage.audio_seconds", Lexeme: "12.5", Present: true, Acquisition: "provider_response"},
	}
	return observation
}

// TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent proves that
// PostgreSQL JSONB representation normalization of payload_json does not cause
// a false ErrIdentityCollision on the first or replay finalizer append. It
// exercises the production append/read path on the live dialect.
func TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-obs-jsonb")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	sink, ok := journalstore.NewObservationSinkWithOutbox(store).(interface {
		AppendEconomicObservationWithOutbox(context.Context, metering.Observation) error
	})
	require.True(t, ok, "outbox sink must expose the economic capability")

	observation := pgJSONBObservation(storeID, "obs-pg-jsonb-first", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation), "first PG JSONB finalizer append must not falsely collide")
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation), "exact PG JSONB replay must stay idempotent")

	receiptReplay := observation.Clone()
	receiptReplay.ReceivedAt = receiptReplay.ReceivedAt.Add(time.Minute)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, receiptReplay), "receipt-time PG JSONB replay must stay idempotent")

	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err, "PG JSONB stored envelope must decode through validation")
	require.Len(t, pending, 1)
	require.Equal(t, observation.ID, pending[0].Observation.ID)
	require.Equal(t, observation.Fingerprint(), pending[0].Observation.Fingerprint())

	mutated := observation.Clone()
	mutated.Evidence[0].Lexeme = "13.5"
	require.True(t, errors.Is(sink.AppendEconomicObservationWithOutbox(ctx, mutated), journalstore.ErrIdentityCollision), "semantic PG JSONB change must still collide")

	got, err := store.GetObservation(ctx, observation.ID, observation.Revision)
	require.NoError(t, err)
	require.Equal(t, observation.Fingerprint(), got.Fingerprint())
}
