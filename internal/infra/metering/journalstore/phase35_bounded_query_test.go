package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_MemoryList_FiltersSourceAuthorityIdentityVersion(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-p35-filters"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	match := validFact("fact-match", "stream-p35", 1)
	match.Source = metering.SourceObserved
	match.Authority = metering.AuthorityAuthoritative
	match.IdentityVersion = 0
	otherSrc := validFact("fact-src", "stream-p35", 2)
	otherSrc.Source = metering.SourceDerived
	otherSrc.Authority = metering.AuthorityAuthoritative
	otherAuth := validFact("fact-auth", "stream-p35", 3)
	otherAuth.Source = metering.SourceObserved
	otherAuth.Authority = metering.AuthorityEstimated
	otherVer := validFact("fact-ver", "stream-p35", 4)
	otherVer.Source = metering.SourceObserved
	otherVer.Authority = metering.AuthorityAuthoritative
	otherVer.IdentityVersion = 2
	for _, f := range []metering.Fact{match, otherSrc, otherAuth, otherVer} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.List(ctx, metering.Query{
		StreamID:        "stream-p35",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].FactID != "fact-match" {
		t.Fatalf("facts=%#v want only fact-match (EffectiveV1)", page.Facts)
	}

	empty, err := s.List(ctx, metering.Query{
		StreamID:        "stream-p35",
		IdentityVersion: 2,
		Limit:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Facts) != 1 || empty.Facts[0].FactID != "fact-ver" {
		t.Fatalf("identity_version=2 facts=%#v", empty.Facts)
	}
}

func TestPhase35_MemoryList_StoreIsolationAndMaxLimit(t *testing.T) {
	t.Parallel()
	a, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{
		StoreID: "store-a", MaxPageSize: 2, DefaultPageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "store-b"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, id := range []string{"a1", "a2", "a3"} {
		f := validFact(id, "stream-iso", int64(i+1))
		f.Source = metering.SourceObserved
		if err := a.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	fB := validFact("b1", "stream-iso", 1)
	if err := b.Append(ctx, fB); err != nil {
		t.Fatal(err)
	}

	page, err := a.List(ctx, metering.Query{StreamID: "stream-iso", Source: metering.SourceObserved, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 2 {
		t.Fatalf("max page size not applied: got %d facts", len(page.Facts))
	}
	if page.NextCursor == "" {
		t.Fatal("expected deterministic pagination cursor")
	}
	page2, err := a.List(ctx, metering.Query{StreamID: "stream-iso", Source: metering.SourceObserved, Limit: 100, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Facts) != 1 || page2.Facts[0].FactID != "a3" {
		t.Fatalf("page2=%#v", page2.Facts)
	}
	for _, f := range append(page.Facts, page2.Facts...) {
		if f.FactID == "b1" {
			t.Fatal("store isolation broken: saw store-b fact")
		}
	}
}

func TestPhase35_MemoryList_ComposesPerspectiveBoundarySourceAuthority(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-p35-compose"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	match := validFact("fact-match", "stream-compose", 1)
	match.Perspective = metering.PerspectiveCustomer
	match.Boundary = metering.BoundaryFrontendIngress
	match.Lifecycle = metering.LifecycleLogicalRequest
	match.Source = metering.SourceObserved
	match.Authority = metering.AuthorityAuthoritative
	match.IdentityVersion = 0
	other := validFact("fact-other", "stream-compose", 2)
	other.Perspective = metering.PerspectiveOperator
	other.Boundary = metering.BoundaryBackendEgress
	other.Source = metering.SourceObserved
	other.Authority = metering.AuthorityAuthoritative
	for _, f := range []metering.Fact{match, other} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(ctx, metering.Query{
		StreamID:        "stream-compose",
		Perspective:     metering.PerspectiveCustomer,
		Boundary:        metering.BoundaryFrontendIngress,
		Lifecycle:       metering.LifecycleLogicalRequest,
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].FactID != "fact-match" {
		t.Fatalf("compose facts=%#v", page.Facts)
	}
}

func TestPhase35_SQLite_IndexedFilterRowsAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "sqlite-p35-idx"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	f := validFact("fact-idx", "stream-idx", 1)
	f.Source = metering.SourceObserved
	f.Authority = metering.AuthorityAuthoritative
	f.IdentityVersion = 0
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ name, value string }{
		{"source", string(metering.SourceObserved)},
		{"authority", string(metering.AuthorityAuthoritative)},
		{"identity_version", "1"},
	} {
		var n int
		if err := bunDB.NewRaw(
			`
SELECT COUNT(1) FROM metering_fact_filters
WHERE store_id = ? AND fact_id = ? AND field_name = ? AND field_value = ?`,
			"sqlite-p35-idx", "fact-idx", field.name, field.value,
		).Scan(ctx, &n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("filter row %s=%s count=%d", field.name, field.value, n)
		}
	}
	var idx int
	if err := bunDB.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_metering_fact_filters_field'`).Scan(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatal("missing idx_metering_fact_filters_field")
	}
	page, err := store.List(ctx, metering.Query{
		StreamID:        "stream-idx",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("list via indexed filters got %d", len(page.Facts))
	}
}

func TestPhase35_SQLiteList_FiltersSourceAuthorityIdentityVersion(t *testing.T) {
	t.Parallel()
	store := newSQLiteJournal(t)
	ctx := context.Background()

	match := validFact("fact-match", "stream-p35-sql", 1)
	match.Source = metering.SourceObserved
	match.Authority = metering.AuthorityAuthoritative
	match.IdentityVersion = 0
	match.RecordedAt = time.Unix(1, 0).UTC()
	other := validFact("fact-other", "stream-p35-sql", 2)
	other.Source = metering.SourceDerived
	other.Authority = metering.AuthorityAuthoritative
	other.RecordedAt = time.Unix(2, 0).UTC()
	for _, f := range []metering.Fact{match, other} {
		if err := store.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List(ctx, metering.Query{
		StreamID:        "stream-p35-sql",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].FactID != "fact-match" {
		t.Fatalf("sqlite facts=%#v", page.Facts)
	}
	_, err = store.List(ctx, metering.Query{Source: metering.SourceObserved, Limit: 10})
	if !errors.Is(err, journalstore.ErrQueryTooBroad) {
		t.Fatalf("source-only got %v want ErrQueryTooBroad", err)
	}
}
