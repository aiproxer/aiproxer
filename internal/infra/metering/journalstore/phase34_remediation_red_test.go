package journalstore_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
)

func TestPhase34_IdentityVersion_EffectiveV1_BackfillAndAppend(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "absent",
			payload: `{"fact_id":"iv-absent","stream_id":"s-iv","sequence":1,"kind":"cumulative","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z"}`,
		},
		{
			name:    "zero",
			payload: `{"fact_id":"iv-zero","stream_id":"s-iv","sequence":2,"identity_version":0,"kind":"cumulative","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z"}`,
		},
		{
			name:    "one",
			payload: `{"fact_id":"iv-one","stream_id":"s-iv","sequence":3,"identity_version":1,"kind":"cumulative","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z"}`,
		},
	}
	for i, tc := range cases {
		factID := "iv-" + tc.name
		_, err := bunDB.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json,
	identity_version, source_revision, source_event_kind, source_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, "p34-iv", factID, "s-iv", int64(i+1), "key-"+tc.name, "cumulative",
			"customer", "frontend", "request", 1, tc.payload,
			0, 0, "", "").Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := journalstore.BackfillSchemaV2IdentityForTest(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		factID := "iv-" + tc.name
		var ver int64
		if err := bunDB.NewRaw(
			`SELECT identity_version FROM metering_facts WHERE store_id = ? AND fact_id = ?`,
			"p34-iv", factID,
		).Scan(ctx, &ver); err != nil {
			t.Fatal(err)
		}
		if ver != int64(metering.IdentityVersionV1) {
			t.Fatalf("%s backfill identity_version=%d want %d", tc.name, ver, metering.IdentityVersionV1)
		}
	}

	store, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "p34-iv-append"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fresh := validFact("iv-fresh", "s-iv-fresh", 1)
	fresh.IdentityVersion = 0
	fresh.SourceID = "iv-fresh"
	if err := store.Append(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	var appendVer int64
	if err := bunDB.NewRaw(
		`SELECT identity_version FROM metering_facts WHERE store_id = ? AND fact_id = ?`,
		"p34-iv-append", "iv-fresh",
	).Scan(ctx, &appendVer); err != nil {
		t.Fatal(err)
	}
	if appendVer != int64(metering.IdentityVersionV1) {
		t.Fatalf("append identity_version=%d want %d", appendVer, metering.IdentityVersionV1)
	}
}

func TestPhase34_SQLite_VerifySchemaRequiresV2Indexes(t *testing.T) {
	t.Parallel()
	indexes := []string{
		"metering_facts_store_stream_fact_id_key",
		"idx_metering_facts_store_stream_seq",
		"idx_metering_facts_store_attempt",
		"idx_metering_facts_store_recorded",
		"idx_metering_facts_store_plane",
		"idx_metering_fact_supersessions_to",
	}
	for _, name := range indexes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bunDB := openPhase34SQLiteDB(t)
			ctx := context.Background()
			if err := journalstore.VerifySchema(ctx, bunDB); err != nil {
				t.Fatalf("baseline VerifySchema: %v", err)
			}
			if _, err := bunDB.ExecContext(ctx, `DROP INDEX IF EXISTS `+name); err != nil {
				t.Fatal(err)
			}
			err := journalstore.VerifySchema(ctx, bunDB)
			if err == nil {
				t.Fatalf("VerifySchema must fail after dropping %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("VerifySchema error %q must mention %s", err.Error(), name)
			}
		})
	}
}

func TestPhase34_SupersessionEdgeBackfill_FromPreV2Payload(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	ctx := context.Background()
	basePayload := `{"fact_id":"base-edge","stream_id":"stream-edge","sequence":1,"identity_version":1,"kind":"cumulative","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z","quantities":[{"component":"output_token","unit":"token","value":10,"present":true}]}`
	corrPayload := `{"fact_id":"corr-edge","stream_id":"stream-edge","sequence":2,"identity_version":1,"kind":"correction","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z","supersedes":["base-edge"],"quantities":[{"component":"output_token","unit":"token","value":-3,"present":true}]}`
	for _, row := range []struct {
		store, fact, stream, key, kind, payload string
		seq                                     int64
	}{
		{"p34-edge-a", "base-edge", "stream-edge", "k-base-a", "cumulative", basePayload, 1},
		{"p34-edge-a", "corr-edge", "stream-edge", "k-corr-a", "correction", corrPayload, 2},
		{"p34-edge-b", "base-edge", "stream-edge", "k-base-b", "cumulative", basePayload, 1},
		{"p34-edge-b", "corr-edge", "stream-edge", "k-corr-b", "correction", corrPayload, 2},
	} {
		_, err := bunDB.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json,
	identity_version, source_revision, source_event_kind, source_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, row.store, row.fact, row.stream, row.seq, row.key, row.kind,
			"customer", "frontend", "request", 1, row.payload,
			1, 0, row.kind, row.fact).Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bunDB.ExecContext(ctx, `DELETE FROM metering_fact_supersessions`); err != nil {
		t.Fatal(err)
	}
	if err := journalstore.BackfillSchemaV2SupersessionsForTest(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	assertEdgeCount(t, bunDB, "p34-edge-a", "stream-edge", "corr-edge", "base-edge", 1)
	assertEdgeCount(t, bunDB, "p34-edge-b", "stream-edge", "corr-edge", "base-edge", 1)
	if err := journalstore.BackfillSchemaV2SupersessionsForTest(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	var edgesAgain int
	if err := bunDB.NewRaw(`SELECT COUNT(1) FROM metering_fact_supersessions WHERE store_id = ?`, "p34-edge-a").Scan(ctx, &edgesAgain); err != nil {
		t.Fatal(err)
	}
	if edgesAgain != 1 {
		t.Fatalf("idempotent backfill edges=%d want 1", edgesAgain)
	}

	store, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "p34-edge-a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := bunDB.NewRaw(`
INSERT INTO metering_fact_supersessions(store_id, stream_id, from_fact_id, to_fact_id)
VALUES (?,?,?,?)
`, "p34-edge-a", "stream-edge", "base-edge", "pend-cycle").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	pend := validFact("pend-cycle", "stream-edge", 5)
	pend.Kind = metering.FactKindCorrection
	pend.IdentityVersion = metering.IdentityVersionV1
	pend.SourceID = "pend-cycle"
	pend.Supersedes = []string{"base-edge"}
	pend.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: -1, Present: true,
	}}
	err = store.Append(ctx, pend)
	if !errors.Is(err, journalstore.ErrSupersessionCycle) {
		t.Fatalf("cycle via table edges got %v want ErrSupersessionCycle", err)
	}
}

func TestPhase34_ConcurrentFactIdentityAppendRace(t *testing.T) {
	t.Parallel()
	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p34-race-mem"})
		if err != nil {
			t.Fatal(err)
		}
		runFactIdentityRace(t, store, "stream-race-mem", "same-fact-mem")
	})
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournalMultiConn(t)
		runFactIdentityRace(t, store, "stream-race-sqlite", "same-fact-sqlite")
	})
}

func TestPhase34_MigrationNameRegistered(t *testing.T) {
	t.Parallel()
	bunDB := openPhase34SQLiteDB(t)
	ctx := context.Background()
	var name string
	err := bunDB.NewRaw(
		`SELECT name FROM bun_metering_journal_migrations WHERE name = ? LIMIT 1`,
		journalstore.SchemaV2MigrationName,
	).Scan(ctx, &name)
	if err != nil {
		t.Fatalf("migration %s not registered: %v", journalstore.SchemaV2MigrationName, err)
	}
	if name != journalstore.SchemaV2MigrationName {
		t.Fatalf("got %q want %q", name, journalstore.SchemaV2MigrationName)
	}
}

func runFactIdentityRace(t *testing.T, store interface {
	Append(context.Context, metering.Fact) error
	List(context.Context, metering.Query) (metering.Page, error)
}, streamID, factID string,
) {
	t.Helper()
	ctx := context.Background()
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	start := make(chan struct{})
	var okCount atomic.Int64
	var collisionCount atomic.Int64
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			f := validFact(factID, streamID, 1)
			f.IdentityVersion = metering.IdentityVersionV1
			f.SourceID = "src-" + strconv.Itoa(i)
			f.SourceRevision = int64(i + 1)
			f.Quantities = []metering.Quantity{{
				Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: int64(10 + i), Present: true,
			}}
			err := store.Append(ctx, f)
			errs[i] = err
			switch {
			case err == nil:
				okCount.Add(1)
			case errors.Is(err, journalstore.ErrIdentityCollision):
				collisionCount.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if okCount.Load() != 1 {
		t.Fatalf("successes=%d want 1; errs=%v", okCount.Load(), errs)
	}
	if collisionCount.Load() != int64(workers-1) {
		t.Fatalf("collisions=%d want %d; errs=%v", collisionCount.Load(), workers-1, errs)
	}
	page, err := store.List(ctx, metering.Query{StreamID: streamID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("stored facts=%d want 1", len(page.Facts))
	}
	if page.Facts[0].FactID != factID {
		t.Fatalf("stored fact_id=%q want %q", page.Facts[0].FactID, factID)
	}
}

func assertEdgeCount(t *testing.T, bunDB *bun.DB, storeID, streamID, from, to string, want int) {
	t.Helper()
	var n int
	err := bunDB.NewRaw(`
SELECT COUNT(1) FROM metering_fact_supersessions
WHERE store_id = ? AND stream_id = ? AND from_fact_id = ? AND to_fact_id = ?
`, storeID, streamID, from, to).Scan(context.Background(), &n)
	if err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("edges %s/%s→%s = %d want %d", storeID, from, to, n, want)
	}
}
