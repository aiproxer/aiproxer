package journalstore_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/stretchr/testify/require"
)

// legacyObservationSourceKeyGolden is the source_event_key Bun 1.2.18 stored
// for phase4Observation("sqlite-test", "golden-obs", 3): Bun's formatter
// silently dropped the NUL delimiters of Observation.IdentityKey. Rows written
// by earlier releases carry exactly these bytes, so the legacy lookup and
// replay path must keep matching them.
const legacyObservationSourceKeyGolden = `v2sqlite-testproviderprovider_response{"subject":{"kind":"b_leg","store_id":"sqlite-test","a_leg_id":"a-sqlite-test","billing_call_id":"call-sqlite-test","call_id":"call-sqlite-test","b_leg_id":"b-sqlite-test"}}stream-sqlite-testsource-event3`

// TestObservationSourceKeyUsesV2LengthFramedIdentity pins the persisted bytes
// of a new write: the injective v2 key frames the full unstripped identity with
// its length, so stripping can never merge two observations.
func TestObservationSourceKeyUsesV2LengthFramedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "golden-obs", 3)
	require.NoError(t, store.AppendObservation(ctx, observation))

	var key string
	require.NoError(t, store.DB().NewRaw(
		`SELECT source_event_key FROM metering_facts WHERE observation_id = ?`, "golden-obs",
	).Scan(ctx, &key))

	identity := observation.SourceEventIdentity()
	require.Contains(t, identity, "\x00", "the persisted identity must not strip NUL delimiters")
	var wantKey strings.Builder
	wantKey.WriteString("obs:v2:")
	for field := range strings.SplitSeq(identity, "\x00") {
		wantKey.WriteString(strconv.Itoa(len(field)) + ":" + field)
	}
	require.Equal(t, wantKey.String(), key,
		"new writes must persist the injective v2 key, not the legacy stripped bytes")
	require.NotContains(t, key, "\x00", "the persisted key must stay NUL-free for Bun's formatter")
}

// TestObservationLegacyGoldenBytesStayReadableAndStable keeps the legacy golden
// bytes as the compatibility fixture: a row carrying exactly the bytes earlier
// releases wrote must remain readable, replayable, and byte-stable.
func TestObservationLegacyGoldenBytesStayReadableAndStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "golden-obs", 3)
	require.Equal(t, legacyObservationSourceKeyGolden,
		dbinfra.LegacyPersistedText(observation.SourceEventIdentity()),
		"the legacy lookup alias must keep matching rows written by earlier releases")

	seedLegacyObservationRow(t, store, "legacy-golden-obs", observation)

	replay := observation.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NoError(t, store.AppendObservation(ctx, replay), "a legacy row replay must stay idempotent")

	var count int
	require.NoError(t, store.DB().NewRaw(
		`SELECT COUNT(1) FROM metering_facts WHERE store_id = ? AND fact_id = ?`,
		"sqlite-test", "legacy-golden-obs").Scan(ctx, &count))
	require.Equal(t, 1, count)

	var key string
	require.NoError(t, store.DB().NewRaw(
		`SELECT source_event_key FROM metering_facts WHERE store_id = ? AND fact_id = ?`,
		"sqlite-test", "legacy-golden-obs").Scan(ctx, &key))
	require.Equal(t, legacyObservationSourceKeyGolden, key, "a replay must not rewrite the legacy row key")
}
