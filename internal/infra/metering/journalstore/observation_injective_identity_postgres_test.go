//go:build integration

package journalstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestPostgresObservationStrippedCollisionPersistsSeparately is the live-
// PostgreSQL parity proof for the injective persisted identity: distinct
// observations whose legacy stripped keys are byte-identical persist under
// separate v2 keys.
func TestPostgresObservationStrippedCollisionPersistsSeparately(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-obs-injective")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	first, second := strippedCollisionPair(storeID)
	require.NoError(t, store.AppendObservation(ctx, first))
	require.NoError(t, store.AppendObservation(ctx, second),
		"a distinct observation may not be dropped as a legacy identity collision")

	keys := make([]string, 0, 2)
	for _, observation := range []metering.Observation{first, second} {
		got, err := store.GetObservation(ctx, observation.ID, observation.Revision)
		require.NoError(t, err)
		require.Equal(t, observation.Fingerprint(), got.Fingerprint())

		var key string
		require.NoError(t, store.DB().NewRaw(
			`SELECT source_event_key FROM metering_facts WHERE store_id = ? AND observation_id = ?`,
			storeID, observation.ID).Scan(ctx, &key))
		require.True(t, strings.HasPrefix(key, "obs:v2:"), "key %q must carry the v2 prefix", key)
		keys = append(keys, key)
	}
	require.NotEqual(t, keys[0], keys[1])
}
