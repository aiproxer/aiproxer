package journalstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// The fact-row paths (post-conflict replay, GetObservation, ListObservations,
// RebuildObservationProjections) must apply the same canonical
// identity/fingerprint validation as the outbox: representation differences
// never collide, receipt/lineage replay stays idempotent, and corrupt rows
// fail closed with classified errors.

// seedFactWinner rewrites the durable fact envelope to simulate a concurrent
// winner (or durable corruption) with a different representation.
//
//nolint:revive // test helper keeps t first per Go testing convention
func seedFactWinner(t *testing.T, ctx context.Context, store *journalstore.DurableStore, observation metering.Observation, payload, fingerprint string) {
	t.Helper()
	_, err := store.DB().NewRaw(`UPDATE metering_facts SET payload_json = ?, observation_fingerprint = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, payload, fingerprint, observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
	require.NoError(t, err)
}

//nolint:revive // test helper keeps t first per Go testing convention
func storedFactFingerprint(t *testing.T, ctx context.Context, store *journalstore.DurableStore, observation metering.Observation) string {
	t.Helper()
	var fingerprint string
	require.NoError(t, store.DB().NewRaw(`SELECT observation_fingerprint FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ? LIMIT 1`, observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Scan(ctx, &fingerprint))
	return fingerprint
}

func TestObservationFactJSONB_ConcurrentWinnerReplaySucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-fact-winner", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))

	// Concurrent winner stored a different representation of the same fact.
	variant := jsonbKeyOrderVariant(t, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision))
	seedFactWinner(t, ctx, store, observation, variant, storedFactFingerprint(t, ctx, store, observation))

	require.NoError(t, store.AppendObservation(ctx, observation), "equivalent winner representation must not collide")

	receiptReplay := observation.Clone()
	receiptReplay.ReceivedAt = receiptReplay.ReceivedAt.Add(time.Minute)
	require.NoError(t, store.AppendObservation(ctx, receiptReplay), "receipt-time winner replay must stay idempotent")

	lineageReplay := observation.Clone()
	lineageReplay.Subject.ALegID = ""
	require.Equal(t, observation.Correlation.ALegID, lineageReplay.Correlation.ALegID)
	require.NoError(t, store.AppendObservation(ctx, lineageReplay), "lineage-placement winner replay must stay idempotent")

	got, err := store.GetObservation(ctx, observation.ID, observation.Revision)
	require.NoError(t, err)
	require.Equal(t, observation.Fingerprint(), got.Fingerprint())
}

func TestObservationFactJSONB_ConcurrentWinnerReceiptEnvelopeReplaySucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-fact-winner-receipt", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))

	// Winner committed the same semantic fact with different receipt metadata.
	winner := observation.Clone()
	winner.ReceivedAt = winner.ReceivedAt.Add(17 * time.Minute)
	winnerCanonical, err := winner.Canonical()
	require.NoError(t, err)
	winnerPayload, err := winnerCanonical.CanonicalJSON()
	require.NoError(t, err)
	seedFactWinner(t, ctx, store, observation, string(winnerPayload), winnerCanonical.Fingerprint())

	require.NoError(t, store.AppendObservation(ctx, observation), "receipt-drifted winner must replay idempotently")
}

func TestObservationFactJSONB_WinnerFingerprintMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-fact-winner-fp", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))

	variant := jsonbKeyOrderVariant(t, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision))
	seedFactWinner(t, ctx, store, observation, variant, "deadbeef")

	err := store.AppendObservation(ctx, observation)
	require.Error(t, err, "winner fingerprint mismatch must fail closed")
	require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "fingerprint mismatch got %v, want ErrIdentityCollision", err)
}

func TestObservationFactJSONB_WinnerMissingFingerprintFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-fact-winner-nofp", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))

	variant := jsonbWhitespaceVariant(t, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision))
	seedFactWinner(t, ctx, store, observation, variant, "")

	err := store.AppendObservation(ctx, observation)
	require.Error(t, err, "missing winner fingerprint must fail closed")
	require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "missing fingerprint got %v, want ErrIdentityCollision", err)
}

func TestObservationFactJSONB_IdentityColumnDriftFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("observation id drift", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", "obs-fact-iddrift", 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_id = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, "obs-fact-impersonator", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		replayErr := store.AppendObservation(ctx, observation)
		require.Error(t, replayErr, "fact identity column drift must fail closed on replay")
		require.True(t, errors.Is(replayErr, journalstore.ErrIdentityCollision), "identity drift got %v, want ErrIdentityCollision", replayErr)
	})

	t.Run("revision drift", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", "obs-fact-revdrift", 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_revision = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, 99, observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		replayErr := store.AppendObservation(ctx, observation)
		require.Error(t, replayErr, "fact revision column drift must fail closed on replay")
		require.True(t, errors.Is(replayErr, journalstore.ErrIdentityCollision), "revision drift got %v, want ErrIdentityCollision", replayErr)
	})
}

func TestObservationFactJSONB_InvalidStoredObservationFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newInvalidStore := func(t *testing.T, id string) (*journalstore.DurableStore, metering.Observation) {
		t.Helper()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", id, 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		updateFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, `{}`)
		return store, observation
	}

	t.Run("replay", func(t *testing.T) {
		t.Parallel()
		store, observation := newInvalidStore(t, "obs-fact-invalid-replay")
		err := store.AppendObservation(ctx, observation)
		require.Error(t, err, "semantically invalid stored observation must fail closed")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "invalid stored observation got %v, want invalid-record classification", err)
	})

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		store, observation := newInvalidStore(t, "obs-fact-invalid-get")
		_, err := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.Error(t, err, "GetObservation must not return an unvalidated observation")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "invalid stored observation got %v, want invalid-record classification", err)
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		store, observation := newInvalidStore(t, "obs-fact-invalid-list")
		_, err := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 10))
		require.Error(t, err, "ListObservations must not return an unvalidated observation")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "invalid stored observation got %v, want invalid-record classification", err)
	})

	t.Run("rebuild", func(t *testing.T) {
		t.Parallel()
		store, _ := newInvalidStore(t, "obs-fact-invalid-rebuild")
		err := store.RebuildObservationProjections(ctx)
		require.Error(t, err, "rebuild must fail closed on invalid stored observation")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "invalid stored observation got %v, want invalid-record classification", err)
	})
}

func TestObservationFactJSONB_GetObservationCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newSeededStore := func(t *testing.T, id string) (*journalstore.DurableStore, metering.Observation) {
		t.Helper()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", id, 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		return store, observation
	}

	t.Run("malformed payload", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-get-malformed")
		updateFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, "{broken-json")
		_, err := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.Error(t, err, "malformed stored payload must fail closed")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "malformed payload got %v, want invalid-record classification", err)
	})

	t.Run("missing fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-get-nofp")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "")
		_, err := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.Error(t, err, "missing stored fingerprint must fail closed")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "missing fingerprint got %v, want ErrIdentityCollision", err)
	})

	t.Run("mismatched fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-get-fpmismatch")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "deadbeef")
		_, err := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.Error(t, err, "mismatched stored fingerprint must fail closed")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "fingerprint mismatch got %v, want ErrIdentityCollision", err)
	})

	t.Run("identity drift is not returned", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-get-iddrift")
		_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_id = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, "obs-fact-impersonator", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		_, getErr := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.Error(t, getErr, "drifted identity columns must not return the row as the requested observation")
	})
}

func TestObservationFactJSONB_ListObservationsCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newSeededStore := func(t *testing.T, id string) (*journalstore.DurableStore, metering.Observation) {
		t.Helper()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", id, 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		return store, observation
	}

	t.Run("malformed payload", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-list-malformed")
		updateFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, "{broken-json")
		_, err := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 10))
		require.Error(t, err, "malformed stored payload must fail closed")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "malformed payload got %v, want invalid-record classification", err)
	})

	t.Run("missing fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-list-nofp")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "")
		_, err := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 10))
		require.Error(t, err, "missing stored fingerprint must fail closed")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "missing fingerprint got %v, want ErrIdentityCollision", err)
	})

	t.Run("mismatched fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-list-fpmismatch")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "deadbeef")
		_, err := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 10))
		require.Error(t, err, "mismatched stored fingerprint must fail closed")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "fingerprint mismatch got %v, want ErrIdentityCollision", err)
	})

	t.Run("identity drift", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-list-iddrift")
		_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_id = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, "obs-fact-impersonator", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		_, listErr := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 10))
		require.Error(t, listErr, "identity column drift must fail closed")
		require.True(t, errors.Is(listErr, journalstore.ErrIdentityCollision), "identity drift got %v, want ErrIdentityCollision", listErr)
	})
}

func TestObservationFactJSONB_RebuildCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newSeededStore := func(t *testing.T, id string) (*journalstore.DurableStore, metering.Observation) {
		t.Helper()
		store := newSQLiteJournal(t)
		observation := phase4Observation("sqlite-test", id, 1)
		require.NoError(t, store.AppendObservation(ctx, observation))
		return store, observation
	}

	t.Run("malformed payload", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-rebuild-malformed")
		updateFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision, "{broken-json")
		err := store.RebuildObservationProjections(ctx)
		require.Error(t, err, "rebuild must fail closed on malformed payload")
		require.True(t, errors.Is(err, metering.ErrInvalidObservation), "malformed payload got %v, want invalid-record classification", err)
	})

	t.Run("missing fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-rebuild-nofp")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "")
		err := store.RebuildObservationProjections(ctx)
		require.Error(t, err, "rebuild must fail closed on missing fingerprint")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "missing fingerprint got %v, want ErrIdentityCollision", err)
	})

	t.Run("mismatched fingerprint", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-rebuild-fpmismatch")
		seedFactWinner(t, ctx, store, observation, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision), "deadbeef")
		err := store.RebuildObservationProjections(ctx)
		require.Error(t, err, "rebuild must fail closed on fingerprint mismatch")
		require.True(t, errors.Is(err, journalstore.ErrIdentityCollision), "fingerprint mismatch got %v, want ErrIdentityCollision", err)
	})

	t.Run("identity drift", func(t *testing.T) {
		t.Parallel()
		store, observation := newSeededStore(t, "obs-fact-rebuild-iddrift")
		_, err := store.DB().NewRaw(`UPDATE metering_facts SET observation_id = ? WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`, "obs-fact-impersonator", observation.Subject.StoreID, observation.ID, int64(observation.Revision)).Exec(ctx)
		require.NoError(t, err)
		rebuildErr := store.RebuildObservationProjections(ctx)
		require.Error(t, rebuildErr, "rebuild must fail closed on identity drift")
		require.True(t, errors.Is(rebuildErr, journalstore.ErrIdentityCollision), "identity drift got %v, want ErrIdentityCollision", rebuildErr)
	})
}
