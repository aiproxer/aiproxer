package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
)

// Phase 3.4 RED: physical Metering V2 schema, supersession relation, store-scoped
// fact identity, bounded indexes, and restart-deterministic aggregates
// (requirements 6.2–6.9, 11.3–11.5; design Metering V2, D6, D7, D12).

func TestPhase34_SchemaV2_HasIdentityRevisionColumns(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	ctx := context.Background()
	var n int
	err := bunDB.NewRaw(`
SELECT COUNT(1) FROM pragma_table_info('metering_facts')
WHERE name IN ('identity_version','source_revision','source_event_kind','source_id')
`).Scan(ctx, &n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("metering_facts V2 identity columns present=%d want 4 (task 3.4)", n)
	}
}

func TestPhase34_SchemaV2_UniqueStoreStreamFactID(t *testing.T) {
	t.Parallel()
	store := openPhase34SQLiteStore(t, "p34-fact-id")
	ctx := context.Background()
	a := validFact("same-fact", "stream-a", 1)
	a.SourceID = "src-a"
	a.IdentityVersion = metering.IdentityVersionV1
	a.SourceRevision = 1
	if err := store.Append(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.SourceID = "src-b"
	b.SourceRevision = 2
	b.Sequence = 2
	err := store.Append(ctx, b)
	if !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("conflicting (store_id,stream_id,fact_id) got %v want ErrIdentityCollision", err)
	}
}

func TestPhase34_SchemaV2_SupersessionTable_AppendWritesEdge(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	store, err := journalstore.OpenStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "p34-edge"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := validFact("base-1", "stream-ss", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "base-1"
	if err := store.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	corr := validFact("corr-1", "stream-ss", 2)
	corr.Kind = metering.FactKindCorrection
	corr.IdentityVersion = metering.IdentityVersionV1
	corr.SourceID = "corr-1"
	corr.Supersedes = []string{"base-1"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: -1, Present: true,
	}}
	if err := store.Append(ctx, corr); err != nil {
		t.Fatal(err)
	}
	var edges int
	err = bunDB.NewRaw(`
SELECT COUNT(1) FROM metering_fact_supersessions
WHERE store_id = ? AND stream_id = ? AND from_fact_id = ? AND to_fact_id = ?
`, "p34-edge", "stream-ss", "corr-1", "base-1").Scan(ctx, &edges)
	if err != nil {
		t.Fatalf("supersession table missing or unreadable: %v", err)
	}
	if edges != 1 {
		t.Fatalf("supersession edges=%d want 1", edges)
	}
}

func TestPhase34_SchemaV2_BoundedIndexes(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	ctx := context.Background()
	want := []string{
		"idx_metering_facts_store_stream_seq",
		"idx_metering_facts_store_attempt",
		"idx_metering_facts_store_recorded",
		"idx_metering_facts_store_plane",
		"idx_metering_fact_supersessions_to",
	}
	for _, name := range want {
		var n int
		err := bunDB.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name = ?`, name).Scan(ctx, &n)
		if err != nil || n != 1 {
			t.Fatalf("index %s present=%d err=%v", name, n, err)
		}
	}
}

func TestPhase34_SchemaV2_PersistsIdentityColumnsOnAppend(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	store, err := journalstore.OpenStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "p34-bf"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	f := validFact("bf-1", "stream-bf", 1)
	f.IdentityVersion = metering.IdentityVersionV1
	f.SourceRevision = 3
	f.SourceEventKind = string(metering.FactKindCumulative)
	f.SourceID = "src-bf-1"
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	var ver, rev int64
	var kind, src string
	err = bunDB.NewRaw(`
SELECT identity_version, source_revision, source_event_kind, source_id
FROM metering_facts WHERE store_id = ? AND fact_id = ?
`, "p34-bf", "bf-1").Scan(ctx, &ver, &rev, &kind, &src)
	if err != nil {
		t.Fatal(err)
	}
	if ver != metering.IdentityVersionV1 || rev != 3 || kind != string(metering.FactKindCumulative) || src != "src-bf-1" {
		t.Fatalf("columns ver=%d rev=%d kind=%q src=%q", ver, rev, kind, src)
	}
}

func TestPhase34_Memory_UniqueStoreStreamFactID(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p34-mem"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := validFact("same-mem", "stream-m", 1)
	a.SourceID = "a"
	if err := store.Append(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.SourceID = "b"
	b.Sequence = 2
	err = store.Append(ctx, b)
	if !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("memory fact identity collision got %v", err)
	}
}

func TestPhase34_SQLite_CorrectionAggregateRestartDeterministic(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "p34-agg.db")
	ctx := context.Background()
	store1 := openPhase34SQLiteStoreAt(t, path, "p34-agg")
	base := validFact("base-agg", "stream-agg", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "base-agg"
	base.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 10, Present: true,
	}}
	corr := validFact("corr-agg", "stream-agg", 2)
	corr.Kind = metering.FactKindCorrection
	corr.IdentityVersion = metering.IdentityVersionV1
	corr.SourceID = "corr-agg"
	corr.Supersedes = []string{"base-agg"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: -3, Present: true,
	}}
	if err := store1.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := store1.Append(ctx, corr); err != nil {
		t.Fatal(err)
	}
	page1, err := store1.List(ctx, metering.Query{StreamID: "stream-agg", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	snap1, err := aggregate.Apply(page1.Facts)
	if err != nil {
		t.Fatal(err)
	}
	_ = store1.Close()

	store2 := openPhase34SQLiteStoreAt(t, path, "p34-agg")
	page2, err := store2.List(ctx, metering.Query{StreamID: "stream-agg", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := aggregate.Apply(page2.Facts)
	if err != nil {
		t.Fatal(err)
	}
	if snap1.Quantities[metering.ComponentOutputToken] != 7 || snap2.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("restart aggregate want 7; before=%v after=%v", snap1.Quantities, snap2.Quantities)
	}
	// Insertion-order independence.
	rev := []metering.Fact{page2.Facts[1], page2.Facts[0]}
	snap3, err := aggregate.Apply(rev)
	if err != nil {
		t.Fatal(err)
	}
	if snap3.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("order-independent aggregate got %v", snap3.Quantities)
	}
}

func openPhase34SQLiteDB(t *testing.T) *bun.DB {
	t.Helper()
	return openPhase34SQLiteDBAt(t, memorySQLiteDSN())
}

func openPhase34SQLiteDBAt(t *testing.T, path string) *bun.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := journalstore.Migrate(context.Background(), bunDB); err != nil {
		t.Fatal(err)
	}
	return bunDB
}

func openPhase34SQLiteStore(t *testing.T, storeID string) *journalstore.DurableStore {
	t.Helper()
	bunDB := openPhase34SQLiteDB(t)
	store, err := journalstore.OpenStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openPhase34SQLiteStoreAt(t *testing.T, path, storeID string) *journalstore.DurableStore {
	t.Helper()
	bunDB := openPhase34SQLiteDBAt(t, path)
	store, err := journalstore.OpenStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
