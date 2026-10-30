package journalstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/uptrace/bun"
)

func TestPhase32_SQLite_LegacyNULSourceEventKey_ReplayNoDuplicate(t *testing.T) {
	t.Parallel()
	store, bunDB := openPhase32SQLite(t, "legacy-nul")
	ctx := context.Background()

	fact := phase32LegacyCustomerFact("req-nul", "fe-nul", 1)
	fact.IdentityVersion = 0
	legacyKey := db.LegacyPersistedText(fact.LegacySourceEventKeyPhase31())
	payload, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bunDB.NewRaw(
		`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		"legacy-nul",
		fact.FactID,
		fact.StreamID,
		fact.Sequence,
		legacyKey,
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
		string(payload),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("seed legacy NUL key: %v", err)
	}

	replay := fact
	replay.IdentityVersion = metering.IdentityVersionV1
	replay.SourceEventKind = string(fact.Kind)
	replay.SourceID = fact.FactID
	if err := store.Append(ctx, replay); err != nil {
		t.Fatalf("canonical/V1 alias replay against phase31 NUL key must be idempotent: %v", err)
	}

	page, err := store.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 fact after legacy replay (no double-count), got %d", len(page.Facts))
	}

	reopened, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "legacy-nul"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Append(ctx, replay); err != nil {
		t.Fatalf("restart replay must stay idempotent: %v", err)
	}
	page2, err := reopened.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Facts) != 1 {
		t.Fatalf("after restart want 1 fact, got %d", len(page2.Facts))
	}
}

func TestPhase32_Memory_PreloadedLegacyNULKey_ReplayNoDuplicate(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fact := phase32LegacyCustomerFact("req-mem", "fe-mem", 1)
	fact.IdentityVersion = 0
	legacyKey := fact.LegacySourceEventKeyPhase31()
	if err := s.SeedSourceKeyForTest(legacyKey, fact); err != nil {
		t.Fatal(err)
	}
	replay := fact
	replay.IdentityVersion = metering.IdentityVersionV1
	if err := s.Append(ctx, replay); err != nil {
		t.Fatalf("memory legacy NUL preload replay: %v", err)
	}
	page, err := s.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("memory want 1 fact, got %d", len(page.Facts))
	}
}

func TestPhase32_SQLite_Literal1NUL_ReplayRaw0_NoDuplicate(t *testing.T) {
	t.Parallel()
	store, bunDB := openPhase32SQLite(t, "legacy-nul-v1")
	ctx := context.Background()

	fact := phase32LegacyCustomerFact("req-nul1", "fe-nul1", 1)
	fact.IdentityVersion = metering.IdentityVersionV1
	legacyKey := fact.LegacySourceEventKeyPhase31()
	if len(legacyKey) < 2 || legacyKey[:2] != "1\x00" {
		t.Fatalf("seed key must be literal-1 NUL, got %q", legacyKey)
	}
	payload, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bunDB.NewRaw(
		`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		"legacy-nul-v1",
		fact.FactID,
		fact.StreamID,
		fact.Sequence,
		db.LegacyPersistedText(legacyKey),
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
		string(payload),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("seed literal-1 NUL key: %v", err)
	}

	replay := fact
	replay.IdentityVersion = 0
	if err := store.Append(ctx, replay); err != nil {
		t.Fatalf("raw-0 replay against literal-1 NUL must be idempotent: %v", err)
	}
	page, err := store.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("want 1 fact after reverse legacy replay, got %d", len(page.Facts))
	}

	reopened, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "legacy-nul-v1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Append(ctx, replay); err != nil {
		t.Fatalf("restart reverse replay must stay idempotent: %v", err)
	}
	page2, err := reopened.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Facts) != 1 {
		t.Fatalf("after restart want 1 fact, got %d", len(page2.Facts))
	}
}

func TestPhase32_Memory_Literal1NUL_ReplayRaw0_NoDuplicate(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-legacy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fact := phase32LegacyCustomerFact("req-mem1", "fe-mem1", 1)
	fact.IdentityVersion = metering.IdentityVersionV1
	legacyKey := fact.LegacySourceEventKeyPhase31()
	if err := s.SeedSourceKeyForTest(legacyKey, fact); err != nil {
		t.Fatal(err)
	}
	replay := fact
	replay.IdentityVersion = 0
	if err := s.Append(ctx, replay); err != nil {
		t.Fatalf("memory literal-1 NUL + raw-0 replay: %v", err)
	}
	page, err := s.List(ctx, metering.Query{StreamID: fact.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("memory reverse want 1 fact, got %d", len(page.Facts))
	}
}

func openPhase32SQLite(t *testing.T, storeID string) (*journalstore.DurableStore, *bun.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, bunDB
}

func phase32LegacyCustomerFact(requestID, factID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    "customer-request:" + requestID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: requestID, TraceID: "trace-" + requestID},
		Scope:       scope.PrincipalScopeView{PrincipalID: scope.Known("prin-" + requestID)},
		FrontendID:  "openai-responses",
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		RecordedAt:  time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     10,
			Present:   true,
		}},
	}
}
