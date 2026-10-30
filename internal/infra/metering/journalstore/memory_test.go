package journalstore_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestMemoryStore_AppendIdempotentAndCollision(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := validFact("fact-1", "stream-1", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 42, Currency: "USD", Present: true, Source: metering.SourceProviderReported}

	if err := s.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, f); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	collide := f
	collide.Sequence = 2
	if err := s.Append(ctx, collide); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("collision got %v", err)
	}

	page, err := s.List(ctx, metering.Query{StreamID: "stream-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("facts=%d", len(page.Facts))
	}
	if page.Facts[0].Money == nil || page.Facts[0].Money.NanoUnits != 42 {
		t.Fatalf("money not preserved: %+v", page.Facts[0].Money)
	}
}

func TestMemoryStore_AppendRejectsSameIdentityDifferentContent(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-content-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := validFact("fact-content-1", "stream-content", 1)
	f.IdentityVersion = 1
	f.SourceEventKind = "usage-event"
	f.SourceID = f.FactID
	if err := s.Append(ctx, f); err != nil {
		t.Fatal(err)
	}

	diffKind := f
	diffKind.Kind = metering.FactKindDelta
	if err := s.Append(ctx, diffKind); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("same SourceEventKey different Kind collision got %v", err)
	}

	diffPayload := f
	diffPayload.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if err := s.Append(ctx, diffPayload); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Quantities collision got %v", err)
	}

	replay := f
	replay.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     3,
		Present:   true,
	}}
	if err := s.Append(ctx, replay); err != nil {
		t.Fatalf("identical content must stay idempotent: %v", err)
	}
}

func TestMemoryStore_AppendRejectsSameIdentityDifferentCorrelation(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-correlation-1"})
	if err != nil {
		t.Fatal(err)
	}
	f := validFact("fact-correlation-1", "stream-correlation", 1)
	f.Correlation = metering.Correlation{RequestID: "request-1", TraceID: "trace-1"}
	if err := s.Append(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	conflict := f
	conflict.Correlation.TraceID = "trace-2"
	if err := s.Append(context.Background(), conflict); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different correlation collision got %v", err)
	}
}

func TestMemoryStore_ListRequiresBoundAndPaginates(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{
		StoreID:         "mem-2",
		DefaultPageSize: 2,
		MaxPageSize:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.List(ctx, metering.Query{Limit: 10}); !errors.Is(err, journalstore.ErrQueryTooBroad) {
		t.Fatalf("got %v", err)
	}
	for i := int64(1); i <= 3; i++ {
		f := validFact("fact-"+itoa(i), "stream-a", i)
		f.Correlation.RequestID = "req-a"
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	page1, err := s.List(ctx, metering.Query{RequestID: "req-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Facts) != 2 || page1.NextCursor == "" {
		t.Fatalf("page1=%+v", page1)
	}
	page2, err := s.List(ctx, metering.Query{RequestID: "req-a", Limit: 2, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Facts) != 1 || page2.NextCursor != "" {
		t.Fatalf("page2=%+v", page2)
	}
}

func TestMemoryStore_AppendOnlyCorrectionHistory(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-3"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := validFact("base", "stream-c", 1)
	if err := s.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	corr := validFact("corr", "stream-c", 2)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{"base"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     5,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, metering.Query{StreamID: "stream-c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 2 {
		t.Fatalf("want append-only history len=2 got %d", len(page.Facts))
	}
}

func validFact(factID, streamID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    streamID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1", BLegID: "b-1"},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("prin-1"),
			TenantID:    scope.Known("ten-1"),
		},
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     3,
			Present:   true,
		}},
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
