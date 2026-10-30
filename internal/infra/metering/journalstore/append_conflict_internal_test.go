package journalstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var memSQLiteDSNInternal atomic.Int64

// memorySQLiteDSNInternal returns a uniquely named shared-cache in-memory
// SQLite DSN for tests that never close and reopen the database file;
// restart-persistence tests must stay file-backed. The "jsi-" prefix keeps
// names disjoint from the external test package within the shared binary.
func memorySQLiteDSNInternal() string {
	return fmt.Sprintf("file:jsi-mem-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", memSQLiteDSNInternal.Add(1))
}

func TestResolveAppendConflictMissingWinner(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "missing-winner")

	fact := metering.Fact{
		StreamID:    "stream-missing",
		FactID:      "fact-missing",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
	}
	err := store.resolveAppendConflict(context.Background(), fact)
	if !errors.Is(err, ErrUniqueRaceMissingRow) {
		t.Fatalf("error=%v want ErrUniqueRaceMissingRow", err)
	}
}

func TestResolveAppendConflictIdenticalWinnerIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "identical-winner")
	ctx := context.Background()
	fact := conflictTestFact("fact-identical", "stream-identical", 1)
	if err := store.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if err := store.resolveAppendConflict(ctx, fact); err != nil {
		t.Fatalf("identical winner must be idempotent: %v", err)
	}
}

func TestResolveAppendConflictDifferentPayloadIsCollision(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "collision-winner")
	ctx := context.Background()
	fact := conflictTestFact("fact-collision", "stream-collision", 1)
	if err := store.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diff := fact
	diff.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	err := store.resolveAppendConflict(ctx, diff)
	if !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("error=%v want ErrIdentityCollision", err)
	}
}

func TestLookupDurableSourcePayloadLegacyIdempotencyKey(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "legacy-key")
	ctx := context.Background()
	fact := conflictTestFact("fact-legacy", "stream-legacy", 1)
	legacy := db.LegacyPersistedText(fact.IdempotencyKey())
	payload := mustMarshalFact(t, fact)
	_, err := store.db.NewRaw(
		`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		store.cfg.StoreID,
		fact.FactID,
		fact.StreamID,
		fact.Sequence,
		legacy,
		string(fact.Kind),
		string(fact.Perspective),
		string(fact.Boundary),
		string(fact.Lifecycle),
		fact.Correlation.RequestID,
		fact.Correlation.ALegID,
		fact.Correlation.BLegID,
		fact.Correlation.AttemptID,
		fact.FrontendID,
		fact.BackendID,
		fact.Model,
		string(fact.Presence),
		string(fact.Source),
		string(fact.Authority),
		fact.RecordedAt.UnixNano(),
		payload,
	).Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, fact); err != nil {
		t.Fatalf("legacy IdempotencyKey row must replay via SourceEventKey lookup: %v", err)
	}
}

func newSQLiteConflictStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSNInternal())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func conflictTestFact(factID, streamID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    streamID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1", BLegID: "b-1"},
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		RecordedAt:  time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     1,
			Present:   true,
		}},
	}
}

func mustMarshalFact(t *testing.T, fact metering.Fact) string {
	t.Helper()
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
