package journalstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/stretchr/testify/require"
)

// R10-A repair contract: adding the optional CorrelationBLegID filter must not
// change the durable cursor contract of any query that leaves it empty. A
// cursor minted before the field existed must keep decoding, and a non-empty
// B-leg (or tenant) filter must remain a distinct, bound cursor contract.

// legacyObservationFilter is the pre-CorrelationBLegID selective-filter shape.
// Its JSON bytes, and therefore its SHA-256 cursor FilterHash, are the durable
// cursor contract for queries without a B-leg bound. Field order is significant.
type legacyObservationFilter struct {
	StoreID, SubjectKind, SubjectID, TenantID, ProviderAccountKey, StreamID, ComponentKey, ComponentKeyHash, ItemKind string
}

func legacyObservationFilterHash(t *testing.T, f legacyObservationFilter) string {
	t.Helper()
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// legacyObservationCursor mirrors the durable v2 cursor JSON contract.
type legacyObservationCursor struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	StoreID       string `json:"store_id"`
	FilterHash    string `json:"filter_hash"`
	StreamID      string `json:"stream_id"`
	Sequence      int64  `json:"sequence"`
	ObservationID string `json:"observation_id"`
	Revision      int64  `json:"revision"`
	RowID         int64  `json:"row_id"`
}

func legacyObservationCursorToken(t *testing.T, cursor legacyObservationCursor) string {
	t.Helper()
	raw, err := json.Marshal(cursor)
	require.NoError(t, err)
	return "v2." + base64.RawURLEncoding.EncodeToString(raw)
}

func TestListObservationsLegacyEmptyFilterCursorStillRoundTrips(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	require.NoError(t, store.AppendObservation(ctx, r10StatementObservation("sqlite-test", "legacy-1", "b-legacy-1", 1)))

	// A cursor issued before the optional CorrelationBLegID filter existed. Its
	// filter hash is the legacy 9-field struct hash carrying no B-leg field.
	streamID := "statement-stream-legacy-1"
	legacyHash := legacyObservationFilterHash(t, legacyObservationFilter{
		StoreID: "sqlite-test", StreamID: streamID,
	})
	token := legacyObservationCursorToken(t, legacyObservationCursor{
		Version: 1, Kind: "observations", StoreID: "sqlite-test", FilterHash: legacyHash,
		StreamID: streamID, Sequence: 1, ObservationID: "legacy-1", Revision: 1, RowID: 1,
	})

	// The same empty-B-leg query that issued the cursor must keep accepting it
	// unchanged instead of rejecting it as a stale filter contract.
	_, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", StreamID: streamID, Cursor: token})
	require.NoError(t, err, "legacy empty-B-leg cursor must keep round-tripping after the optional filter was added")

	// A B-leg-bound query is a different filter contract; the legacy cursor must
	// not silently cross into it.
	_, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", StreamID: streamID, CorrelationBLegID: "b-legacy-1", Cursor: token})
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "legacy cursor must not bind to a B-leg filter")

	// A tenant filter is likewise a distinct contract and must reject the cursor.
	_, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", StreamID: streamID, TenantID: "tenant-other", Cursor: token})
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "cursor must not cross a tenant filter")
}

func TestListObservationsCorrelationBLegCursorIsFilterBound(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		require.NoError(t, store.AppendObservation(ctx, r10StatementObservation("sqlite-test", "bound-"+string(rune('0'+i)), "b-bound", uint64(i))))
	}

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-bound", Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	// Same B-leg, same store: the cursor round-trips.
	_, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-bound", Limit: 1, Cursor: page.NextCursor})
	require.NoError(t, err)

	// A different B-leg must reject the cursor as out of contract.
	_, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-other", Limit: 1, Cursor: page.NextCursor})
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "a different B-leg filter must not reuse the cursor")

	// Dropping the B-leg bound must also reject it.
	_, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", StreamID: "statement-stream-bound-1", Limit: 1, Cursor: page.NextCursor})
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor, "an unbound filter must not reuse a B-leg cursor")
}

func TestListObservationsCorrelationBLegIsTenantScoped(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()

	mine := r10StatementObservation("sqlite-test", "stmt-tenant-a", "b-shared-tenant", 1)
	mine.Subject.TenantID = "tenant-a"
	mine.Correlation.TenantID = "tenant-a"
	require.NoError(t, store.AppendObservation(ctx, mine))

	theirs := r10StatementObservation("sqlite-test", "stmt-tenant-b", "b-shared-tenant", 1)
	theirs.Subject.TenantID = "tenant-b"
	theirs.Correlation.TenantID = "tenant-b"
	require.NoError(t, store.AppendObservation(ctx, theirs))

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", CorrelationBLegID: "b-shared-tenant", TenantID: "tenant-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, mine.ID, page.Observations[0].ID)
}
