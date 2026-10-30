package journalstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// jsonbKeyOrderVariant re-serializes a canonical observation payload through a
// generic map so object keys are reordered, simulating PostgreSQL JSONB
// textual normalization. Semantics and canonical fingerprint are unchanged;
// only the raw representation differs.
func jsonbKeyOrderVariant(t *testing.T, payload string) string {
	t.Helper()
	var generic map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &generic))
	variant, err := json.Marshal(generic)
	require.NoError(t, err)
	require.NotEqual(t, payload, string(variant), "variant must differ textually to simulate JSONB normalization")
	assertSameCanonicalFingerprint(t, payload, string(variant))
	return string(variant)
}

// jsonbWhitespaceVariant re-serializes a canonical observation payload with
// insignificant whitespace, simulating another PostgreSQL JSONB textual
// representation difference with identical canonical semantics.
func jsonbWhitespaceVariant(t *testing.T, payload string) string {
	t.Helper()
	var generic map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &generic))
	variant, err := json.MarshalIndent(generic, "", " ")
	require.NoError(t, err)
	require.NotEqual(t, payload, string(variant), "variant must differ textually to simulate JSONB normalization")
	assertSameCanonicalFingerprint(t, payload, string(variant))
	return string(variant)
}

func assertSameCanonicalFingerprint(t *testing.T, original, variant string) {
	t.Helper()
	var originalObs, variantObs metering.Observation
	require.NoError(t, json.Unmarshal([]byte(original), &originalObs))
	require.NoError(t, json.Unmarshal([]byte(variant), &variantObs))
	originalCanonical, err := originalObs.Canonical()
	require.NoError(t, err)
	variantCanonical, err := variantObs.Canonical()
	require.NoError(t, err)
	require.Equal(t, originalCanonical.Fingerprint(), variantCanonical.Fingerprint(), "variant must preserve canonical semantics")
}

func economicSink(t *testing.T, store *journalstore.DurableStore) coremetering.EconomicObservationSink {
	t.Helper()
	sink := journalstore.NewObservationSinkWithOutbox(store)
	require.NotNil(t, sink, "outbox sink must expose the economic capability")
	return sink
}

//nolint:revive // test helper keeps t first per Go testing convention
func selectOutboxPayload(t *testing.T, ctx context.Context, store *journalstore.DurableStore, storeID, observationID string, revision uint64) string {
	t.Helper()
	var payload string
	require.NoError(t, store.DB().NewRaw(`SELECT payload_json FROM metering_observation_economic_outbox WHERE store_id = ? AND observation_id = ? AND observation_revision = ? LIMIT 1`, storeID, observationID, int64(revision)).Scan(ctx, &payload))
	return payload
}

//nolint:revive // test helper keeps t first per Go testing convention
func updateOutboxPayload(t *testing.T, ctx context.Context, store *journalstore.DurableStore, storeID, observationID string, revision uint64, payload string) {
	t.Helper()
	_, err := store.DB().NewRaw(`UPDATE metering_observation_economic_outbox SET payload_json = ? WHERE store_id = ? AND observation_id = ? AND observation_revision = ?`, payload, storeID, observationID, int64(revision)).Exec(ctx)
	require.NoError(t, err)
}

//nolint:revive // test helper keeps t first per Go testing convention
func updateFactsPayload(t *testing.T, ctx context.Context, store *journalstore.DurableStore, storeID, observationID string, revision uint64, payload string) {
	t.Helper()
	_, err := store.DB().NewRaw(`UPDATE metering_facts SET payload_json = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, payload, storeID, observationID, int64(revision)).Exec(ctx)
	require.NoError(t, err)
}

//nolint:revive // test helper keeps t first per Go testing convention
func selectFactsPayload(t *testing.T, ctx context.Context, store *journalstore.DurableStore, storeID, observationID string, revision uint64) string {
	t.Helper()
	var payload string
	require.NoError(t, store.DB().NewRaw(`SELECT payload_json FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ? LIMIT 1`, storeID, observationID, int64(revision)).Scan(ctx, &payload))
	return payload
}

func TestObservationOutboxJSONB_NormalizedRepresentationReplaySucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := economicSink(t, store)
	observation := phase4Observation("sqlite-test", "obs-jsonb-replay", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))

	// Simulate PostgreSQL JSONB normalization on both durable envelopes: key
	// ordering differs but canonical semantics and fingerprint are identical.
	outboxPayload := selectOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
	updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, jsonbKeyOrderVariant(t, outboxPayload))
	factsPayload := selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
	updateFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, jsonbWhitespaceVariant(t, factsPayload))

	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation), "semantically identical JSONB representation must not collide")

	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err, "normalized stored envelope must still decode through validation")
	require.Len(t, pending, 1)
	require.Equal(t, observation.ID, pending[0].Observation.ID)
	require.Equal(t, observation.Revision, pending[0].Observation.Revision)
}

func TestObservationOutboxJSONB_ReceiptAndLineageReplaySucceedsAfterNormalization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := economicSink(t, store)
	observation := phase4Observation("sqlite-test", "obs-jsonb-lineage", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))

	outboxPayload := selectOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
	updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, jsonbKeyOrderVariant(t, outboxPayload))

	replay := observation.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, replay), "receipt-time replay must stay idempotent after normalization")

	lineageReplay := observation.Clone()
	lineageReplay.Subject.ALegID = ""
	require.Equal(t, observation.Correlation.ALegID, lineageReplay.Correlation.ALegID)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, lineageReplay), "approved lineage-carrier placement replay must stay idempotent after normalization")
}

func TestObservationOutboxJSONB_SemanticChangesCollide(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := economicSink(t, store)
	observation := phase4Observation("sqlite-test", "obs-jsonb-collision", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))

	outboxPayload := selectOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
	updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, jsonbKeyOrderVariant(t, outboxPayload))

	changedValue := observation.Clone()
	changedValue.Measures[0].Value = &metering.Decimal{Coefficient: "999", Scale: 1}
	changedComponent := observation.Clone()
	changedComponent.Measures[0].Key.Component = "vendor:other_frames"
	changedSubject := observation.Clone()
	changedSubject.Subject.BLegID = "b-other"
	changedSubject.Correlation.BLegID = "b-other"
	changedCharge := observation.Clone()
	changedCharge.Charges[0].Amount = &metering.Decimal{Coefficient: "999", Scale: 1}

	for _, tc := range []struct {
		name        string
		observation metering.Observation
	}{
		{"measure quantity", changedValue},
		{"measure component", changedComponent},
		{"subject identity", changedSubject},
		{"charge amount", changedCharge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := sink.AppendEconomicObservationWithOutbox(ctx, tc.observation)
			require.Error(t, err, "semantically different payload must fail closed")
			require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "semantic change got %v, want ErrIdentityCollision", err)
		})
	}
}

func TestObservationOutboxJSONB_CorruptStoredRowsFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newNormalizedStore := func(t *testing.T, id string) (*journalstore.DurableStore, metering.Observation) {
		t.Helper()
		store := newSQLiteJournal(t)
		sink := economicSink(t, store)
		observation := phase4Observation("sqlite-test", id, 1)
		require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))
		payload := selectOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
		updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, jsonbKeyOrderVariant(t, payload))
		return store, observation
	}

	t.Run("malformed payload", func(t *testing.T) {
		t.Parallel()
		store, observation := newNormalizedStore(t, "obs-jsonb-malformed")
		updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, "{broken-json")
		_, err := store.ListPendingObservationOutbox(ctx, 10)
		require.Error(t, err, "malformed stored JSON must fail closed")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "malformed payload got %v, want invalid-record classification", err)
	})

	t.Run("missing fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newNormalizedStore(t, "obs-jsonb-nofp")
		_, err := store.DB().NewRaw(`UPDATE metering_observation_economic_outbox SET observation_fingerprint = '' WHERE store_id = ? AND observation_id = ? AND observation_revision = ?`, observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		_, listErr := store.ListPendingObservationOutbox(ctx, 10)
		require.Error(t, listErr, "missing fingerprint evidence must fail closed")
		require.True(t, errors.Is(listErr, journalstore.ErrIdentityCollision), "missing fingerprint got %v, want identity classification", listErr)
	})

	t.Run("stored fingerprint mismatch", func(t *testing.T) {
		t.Parallel()
		store, observation := newNormalizedStore(t, "obs-jsonb-fpmismatch")
		_, err := store.DB().NewRaw(`UPDATE metering_observation_economic_outbox SET observation_fingerprint = ? WHERE store_id = ? AND observation_id = ? AND observation_revision = ?`, "deadbeef", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		appendErr := economicSink(t, store).AppendEconomicObservationWithOutbox(ctx, observation)
		require.Error(t, appendErr, "stored fingerprint mismatch must fail closed on replay")
		require.True(t, errors.Is(appendErr, journalstore.ErrIdentityCollision), "fingerprint mismatch got %v, want ErrIdentityCollision", appendErr)
		_, listErr := store.ListPendingObservationOutbox(ctx, 10)
		require.Error(t, listErr, "stored fingerprint mismatch must fail closed on read")
		require.True(t, errors.Is(listErr, journalstore.ErrIdentityCollision), "fingerprint mismatch read got %v, want ErrIdentityCollision", listErr)
	})

	t.Run("recomputed fingerprint mismatch", func(t *testing.T) {
		t.Parallel()
		store, observation := newNormalizedStore(t, "obs-jsonb-redrift")
		diverged := observation.Clone()
		diverged.Measures[0].Value = &metering.Decimal{Coefficient: "777", Scale: 1}
		divergedCanonical, err := diverged.Canonical()
		require.NoError(t, err)
		divergedPayload, err := divergedCanonical.CanonicalJSON()
		require.NoError(t, err)
		// Store semantically different content under the original fingerprint.
		updateOutboxPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, string(divergedPayload))
		_, listErr := store.ListPendingObservationOutbox(ctx, 10)
		require.Error(t, listErr, "recomputed fingerprint drift must fail closed")
		require.True(t, errors.Is(listErr, journalstore.ErrIdentityCollision), "recomputed drift got %v, want ErrIdentityCollision", listErr)
	})

	t.Run("row identity column drift", func(t *testing.T) {
		t.Parallel()
		store, observation := newNormalizedStore(t, "obs-jsonb-iddrift")
		_, err := store.DB().NewRaw(`UPDATE metering_observation_economic_outbox SET observation_id = ? WHERE store_id = ? AND observation_id = ? AND observation_revision = ?`, "obs-jsonb-impersonator", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		_, listErr := store.ListPendingObservationOutbox(ctx, 10)
		require.Error(t, listErr, "duplicated identity column drift must fail closed")
		require.True(t, errors.Is(listErr, journalstore.ErrIdentityCollision), "identity drift got %v, want ErrIdentityCollision", listErr)
	})
}

func TestObservationJSONB_EmptyStoredFingerprintReplayFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-jsonb-emptyfp", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))
	_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_fingerprint = '' WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
	require.NoError(t, err)
	replayErr := store.AppendObservation(ctx, observation)
	require.Error(t, replayErr, "incomplete legacy fingerprint evidence must fail closed")
	require.True(t, errors.Is(replayErr, journalstore.ErrIdentityCollision), "empty fingerprint got %v, want ErrIdentityCollision", replayErr)
}

func TestObservationOutboxJSONB_FirstFinalizerAppendIdempotentSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := economicSink(t, store)
	observation := phase4Observation("sqlite-test", "obs-jsonb-first", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation), "exact replay must stay idempotent")
	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, observation.Fingerprint(), pending[0].Observation.Fingerprint())
}
