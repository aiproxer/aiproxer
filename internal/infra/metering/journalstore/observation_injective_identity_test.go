package journalstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// strippedCollisionPair returns two distinct observations whose NUL-stripped
// legacy identities are byte-identical: source event key "evt-1" at revision 2
// and "evt-" at revision 12 both strip to "...evt-12". This reproduces the
// latent false identity collision documented in ADR 0009.
func strippedCollisionPair(store string) (metering.Observation, metering.Observation) {
	first := phase4Observation(store, "collide-a", 2)
	first.SourceEventKey = "evt-1"
	second := phase4Observation(store, "collide-b", 12)
	second.SourceEventKey = "evt-"
	return first, second
}

// seedLegacyObservationRow writes the row a pre-v2 binary stored for the
// observation: the NUL-stripped identity as source_event_key, the canonical
// payload, and the additive observation projection columns a replay resolution
// needs.
func seedLegacyObservationRow(t *testing.T, store *journalstore.DurableStore, factID string, observation metering.Observation) {
	t.Helper()
	ctx := context.Background()
	payload, err := observation.CanonicalJSON()
	require.NoError(t, err)
	_, err = store.DB().ExecContext(ctx, `
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json,
	payload_kind, observation_id, observation_revision, observation_fingerprint
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		observation.Subject.StoreID, factID, observation.StreamID, int64(observation.Sequence),
		dbinfra.LegacyPersistedText(observation.SourceEventIdentity()),
		string(observation.Semantics), string(observation.Perspective), string(observation.Boundary), string(observation.Lifecycle),
		observation.ReceivedAt.UnixNano(), string(payload),
		"observation", observation.ID, int64(observation.Revision), observation.Fingerprint())
	require.NoError(t, err)
}

// TestObservationAppendDistinctStrippedCollisionPersistsSeparately is the core
// slice proof: two distinct observations that collide under the legacy stripped
// encoding both persist, under separate injective v2 keys, instead of the
// second being dropped as a false identity collision.
func TestObservationAppendDistinctStrippedCollisionPersistsSeparately(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	first, second := strippedCollisionPair("sqlite-test")

	require.Equal(t,
		dbinfra.LegacyPersistedText(first.SourceEventIdentity()),
		dbinfra.LegacyPersistedText(second.SourceEventIdentity()),
		"precondition: the pair must collide under the legacy stripped encoding")
	require.NotEqual(t, first.SourceEventIdentity(), second.SourceEventIdentity())

	require.NoError(t, store.AppendObservation(ctx, first))
	require.NoError(t, store.AppendObservation(ctx, second),
		"a distinct observation may not be dropped as a legacy identity collision")

	for _, want := range []metering.Observation{first, second} {
		got, err := store.GetObservation(ctx, want.ID, want.Revision)
		require.NoError(t, err)
		require.Equal(t, want.Fingerprint(), got.Fingerprint())
	}

	keys := make([]string, 0, 2)
	for _, observation := range []metering.Observation{first, second} {
		var key string
		require.NoError(t, store.DB().NewRaw(
			`SELECT source_event_key FROM metering_facts WHERE store_id = ? AND observation_id = ?`,
			"sqlite-test", observation.ID).Scan(ctx, &key))
		require.True(t, strings.HasPrefix(key, "obs:v2:"), "key %q must carry the v2 prefix", key)
		keys = append(keys, key)
	}
	require.NotEqual(t, keys[0], keys[1])
}

// TestObservationAppendFalseStrippedMatchBesideLegacyRow proves the
// lookup-new-then-legacy rule for rows already in the wild: a legacy row that
// only matches after stripping keeps its bytes, while the distinct observation
// persists under the v2 key and both stay replayable.
func TestObservationAppendFalseStrippedMatchBesideLegacyRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	first, second := strippedCollisionPair("sqlite-test")
	seedLegacyObservationRow(t, store, "legacy-collide-a", first)

	require.NoError(t, store.AppendObservation(ctx, second),
		"a stripped false match must persist under the v2 key beside the legacy row")

	gotFirst, err := store.GetObservation(ctx, first.ID, first.Revision)
	require.NoError(t, err)
	require.Equal(t, first.Fingerprint(), gotFirst.Fingerprint())
	gotSecond, err := store.GetObservation(ctx, second.ID, second.Revision)
	require.NoError(t, err)
	require.Equal(t, second.Fingerprint(), gotSecond.Fingerprint())

	replayFirst := first.Clone()
	replayFirst.ReceivedAt = replayFirst.ReceivedAt.Add(time.Minute)
	require.NoError(t, store.AppendObservation(ctx, replayFirst), "legacy replay must stay idempotent")
	replaySecond := second.Clone()
	replaySecond.ReceivedAt = replaySecond.ReceivedAt.Add(time.Minute)
	require.NoError(t, store.AppendObservation(ctx, replaySecond), "v2 replay must stay idempotent")

	var facts int
	require.NoError(t, store.DB().NewRaw(
		`SELECT COUNT(1) FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation'`,
		"sqlite-test").Scan(ctx, &facts))
	require.Equal(t, 2, facts)

	var legacyKey string
	require.NoError(t, store.DB().NewRaw(
		`SELECT source_event_key FROM metering_facts WHERE store_id = ? AND observation_id = ?`,
		"sqlite-test", first.ID).Scan(ctx, &legacyKey))
	require.Equal(t, dbinfra.LegacyPersistedText(first.SourceEventIdentity()), legacyKey,
		"the legacy row must keep the bytes it was written with")
}

// TestObservationAppendPreservesLegacyIdentityDriftCollision keeps the
// fail-closed edge of the legacy confirmation: the same unstripped identity
// under a different observation id is a typed drift collision, not a second
// durable observation.
func TestObservationAppendPreservesLegacyIdentityDriftCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	first, _ := strippedCollisionPair("sqlite-test")
	seedLegacyObservationRow(t, store, "legacy-drift-a", first)

	drift := first.Clone()
	drift.ID = "collide-a-other-id"
	err := store.AppendObservation(ctx, drift)
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision,
		"the same unstripped identity under a different observation id stays a typed collision")
	require.Contains(t, err.Error(), "stored observation identity drift")
}

// TestObservationAppendLegacyIdentityLookupIsGatedByStoreContents pins the read
// cost model of the dual-identity resolution. A store that may hold legacy keys
// pays one legacy read plus one v2 read per fresh append; a legacy replay is
// resolved by the single legacy read. A store that never saw legacy keys pays
// only the v2 read (pinned by
// TestObservationAppendResolvesDurableIdentityOnceAfterInsert).
func TestObservationAppendLegacyIdentityLookupIsGatedByStoreContents(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	first, _ := strippedCollisionPair("sqlite-test")
	seedLegacyObservationRow(t, store, "legacy-gate-a", first)

	const total = 4
	batch := make([]metering.Observation, 0, total)
	for i := 1; i <= total; i++ {
		batch = append(batch, phase4Observation("sqlite-test", "gate-"+string(rune('0'+i)), uint64(i)))
	}

	recorder := &identityWorkRecorder{}
	store.DB().AddQueryHook(recorder)
	require.NoError(t, store.AppendObservations(ctx, batch))

	reads := 0
	for _, query := range recorder.take() {
		if isDurableIdentityRead(query) {
			reads++
		}
	}
	require.Equalf(t, 2*total, reads,
		"a store holding legacy keys must read the legacy identity and the v2 identity once per fresh append; got %d reads", reads)

	replay := first.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NoError(t, store.AppendObservation(ctx, replay))
	reads = 0
	for _, query := range recorder.take() {
		if isDurableIdentityRead(query) {
			reads++
		}
	}
	require.Equal(t, 1, reads, "a legacy replay resolves from the single legacy read")
}

// TestRollbackBoundaryLegacyReplayCannotDuplicateV2Row pins the documented
// rollback boundary: a pre-v2 binary replaying an observation that this binary
// stored under the v2 key cannot insert a duplicate row, because the inherited
// (store, stream, fact_id) fence and the observation id/revision columns do not
// depend on the key encoding. Rolling back therefore degrades to the pre-fix
// stripped-collision behavior; it cannot double-count.
func TestRollbackBoundaryLegacyReplayCannotDuplicateV2Row(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "rollback-b", 12)
	observation.SourceEventKey = "evt-"
	require.NoError(t, store.AppendObservation(ctx, observation))
	legacyKey := dbinfra.LegacyPersistedText(observation.SourceEventIdentity())

	// The v2 row does not answer a legacy source-key lookup...
	var byLegacyKey int
	require.NoError(t, store.DB().NewRaw(
		`SELECT COUNT(1) FROM metering_facts WHERE store_id = ? AND source_event_key = ?`,
		"sqlite-test", legacyKey).Scan(ctx, &byLegacyKey))
	require.Zero(t, byLegacyKey)

	// ...so a pre-v2 binary re-inserts under the legacy key; the encoding-
	// independent fact id fence suppresses it.
	payload, err := observation.CanonicalJSON()
	require.NoError(t, err)
	result, err := store.DB().ExecContext(ctx, `
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json,
	payload_kind, observation_id, observation_revision, observation_fingerprint
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		"sqlite-test", "observation:"+observation.ID+":12", observation.StreamID, int64(observation.Sequence),
		legacyKey, string(observation.Semantics), string(observation.Perspective), string(observation.Boundary), string(observation.Lifecycle),
		observation.ReceivedAt.UnixNano(), string(payload),
		"observation", observation.ID, int64(observation.Revision), observation.Fingerprint())
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, affected, "a legacy replay may not insert a second row")

	// The pre-v2 fallback still finds the single canonical row.
	var found int
	require.NoError(t, store.DB().NewRaw(
		`SELECT COUNT(1) FROM metering_facts WHERE store_id = ? AND payload_kind = 'observation' AND observation_id = ? AND observation_revision = ?`,
		"sqlite-test", observation.ID, int64(observation.Revision)).Scan(ctx, &found))
	require.Equal(t, 1, found)
}
