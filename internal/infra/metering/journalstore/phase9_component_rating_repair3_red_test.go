package journalstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	require "github.com/stretchr/testify/require"
)

func TestPhase9Repair3_DurableObservationReplayIgnoresReceiptDrift(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	first := phase4Observation("sqlite-test", "obs-receipt-drift", 1)
	require.NoError(t, store.AppendObservation(ctx, first))

	var originalPayload, originalFingerprint string
	require.NoError(t, store.DB().NewRaw(`
SELECT payload_json, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND observation_id = ? AND observation_revision = ?
`, first.Subject.StoreID, first.ID, first.Revision).Scan(ctx, &originalPayload, &originalFingerprint))

	driftedReceipt := first.Clone()
	driftedReceipt.ReceivedAt = first.ReceivedAt.Add(17 * time.Minute)
	require.NoError(t, store.AppendObservation(ctx, driftedReceipt), "receipt transport metadata must not collide")

	var retainedPayload, retainedFingerprint string
	require.NoError(t, store.DB().NewRaw(`
SELECT payload_json, observation_fingerprint
FROM metering_facts
WHERE store_id = ? AND observation_id = ? AND observation_revision = ?
`, first.Subject.StoreID, first.ID, first.Revision).Scan(ctx, &retainedPayload, &retainedFingerprint))
	require.Equal(t, originalPayload, retainedPayload, "first full envelope remains the audit record")
	require.Equal(t, originalFingerprint, retainedFingerprint, "first full envelope hash remains the audit record")

	semanticMutation := first.Clone()
	semanticMutation.Measures[0].Value.Coefficient = "126"
	err := store.AppendObservation(ctx, semanticMutation)
	require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "semantic mutation must remain a collision: %v", err)
}
