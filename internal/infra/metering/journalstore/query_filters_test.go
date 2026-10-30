package journalstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestMemoryStore_ListFiltersByPrincipalAndReportsUnsupportedRoute(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-filters"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	match := validFact("fact-match", "stream-match", 1)
	match.Scope.PrincipalID = scope.Known("prin-a")
	other := validFact("fact-other", "stream-other", 1)
	other.Scope.PrincipalID = scope.Known("prin-b")
	for _, f := range []metering.Fact{match, other} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.List(ctx, metering.Query{
		Scope:   metering.ScopeFilters{PrincipalID: scope.Known("prin-a")},
		RouteID: "route-1",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].FactID != "fact-match" {
		t.Fatalf("facts=%#v", page.Facts)
	}
	if len(page.Unsupported) != 1 || page.Unsupported[0].Field != "route_id" {
		t.Fatalf("unsupported=%#v", page.Unsupported)
	}
}

func TestMemoryStore_ListRejectsTooBroadPrincipalScan(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-broad"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := validFact("fact-1", "stream-1", 1)
	f.Scope.TenantID = scope.Known("ten-1")
	if err := s.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	_, err = s.List(ctx, metering.Query{Boundary: metering.BoundaryFrontendIngress, Limit: 10})
	if !errors.Is(err, journalstore.ErrQueryTooBroad) {
		t.Fatalf("got %v", err)
	}
}

func TestMemoryStore_ListFiltersByTimeRangeWithSelectiveBound(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-time"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	early := validFact("early", "stream-t", 1)
	early.RecordedAt = time.Unix(10, 0).UTC()
	late := validFact("late", "stream-t", 2)
	late.RecordedAt = time.Unix(100, 0).UTC()
	for _, f := range []metering.Fact{early, late} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(ctx, metering.Query{
		StreamID: "stream-t",
		TimeRange: metering.TimeRange{
			From: time.Unix(50, 0).UTC(),
			To:   time.Unix(200, 0).UTC(),
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].FactID != "late" {
		t.Fatalf("facts=%#v", page.Facts)
	}
}
