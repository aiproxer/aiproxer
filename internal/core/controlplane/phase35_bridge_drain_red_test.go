package controlplane_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_BridgeDrain_PagedIngressEgressStaysRecordedAndComplete(t *testing.T) {
	t.Parallel()
	for _, pageSize := range []int{1, 2} {
		t.Run("limit_"+drainItoa(pageSize), func(t *testing.T) {
			t.Parallel()
			s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{
				StoreID: "drain-" + drainItoa(pageSize), MaxPageSize: pageSize, DefaultPageSize: pageSize,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			facts := []metering.Fact{
				phase35Fact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 10, 0),
				phase35Fact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 3),
			}
			for _, f := range facts {
				if err := s.Append(ctx, f); err != nil {
					t.Fatal(err)
				}
			}
			q := metering.Query{StreamID: "cust-req", Limit: pageSize}
			rows, page, err := controlplane.ListUsageRowsFromMetering(ctx, s, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || len(page.Facts) != 2 {
				t.Fatalf("pageSize=%d rows=%d facts=%d next=%q", pageSize, len(rows), len(page.Facts), page.NextCursor)
			}
			if page.NextCursor != "" {
				t.Fatalf("drained page must not expose NextCursor: %q", page.NextCursor)
			}
			for _, r := range rows {
				if r.EvidenceState != cp.EvidenceRecorded {
					t.Fatalf("evidence=%q want recorded across pages", r.EvidenceState)
				}
			}
			in, _, err := controlplane.DualPlaneReportInputsFromMetering(ctx, s, q)
			if err != nil {
				t.Fatal(err)
			}
			if in.Completeness != cp.ReportCompletenessComplete {
				t.Fatalf("completeness=%q", in.Completeness)
			}
			if in.Customer.FrontendIngressTokens.Value != 10 || in.Customer.FrontendEgressTokens.Value != 3 {
				t.Fatalf("tokens ingress=%#v egress=%#v", in.Customer.FrontendIngressTokens, in.Customer.FrontendEgressTokens)
			}
		})
	}
}

func TestPhase35_BridgeDrain_PreservesFiltersIdentityAndStoreScope(t *testing.T) {
	t.Parallel()
	a, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "scope-a", MaxPageSize: 1, DefaultPageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "scope-b", MaxPageSize: 1, DefaultPageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	match := phase35Fact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 5, 0)
	match.Source = metering.SourceObserved
	match.Authority = metering.AuthorityAuthoritative
	match.IdentityVersion = 0
	egress := phase35Fact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 1)
	egress.Source = metering.SourceObserved
	egress.Authority = metering.AuthorityAuthoritative
	other := phase35Fact("fe-in-der", "cust-req", 3, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 99, 0)
	other.Source = metering.SourceDerived
	other.Authority = metering.AuthorityAuthoritative
	for _, f := range []metering.Fact{match, egress, other} {
		if err := a.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	leak := phase35Fact("leak", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 7, 0)
	if err := b.Append(ctx, leak); err != nil {
		t.Fatal(err)
	}
	rows, page, err := controlplane.ListUsageRowsFromMetering(ctx, a, metering.Query{
		StreamID:        "cust-req",
		Source:          metering.SourceObserved,
		Authority:       metering.AuthorityAuthoritative,
		IdentityVersion: metering.IdentityVersionV1,
		Limit:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(page.Facts) != 2 {
		t.Fatalf("filtered drain rows=%d facts=%d", len(rows), len(page.Facts))
	}
	for _, f := range page.Facts {
		if f.FactID == "fe-in-der" || f.FactID == "leak" {
			t.Fatalf("filter/store isolation broken: %q", f.FactID)
		}
		if f.Source != metering.SourceObserved {
			t.Fatalf("source filter lost: %q", f.Source)
		}
	}
}

func TestPhase35_BridgeDrain_CursorFaultSafeError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, _, err := controlplane.ListUsageRowsFromMetering(ctx, &stuckCursorQuerier{}, metering.Query{StreamID: "s1", Limit: 1})
	if !errors.Is(err, controlplane.ErrMeteringCursorFault) {
		t.Fatalf("got %v want ErrMeteringCursorFault", err)
	}
	_, _, err = controlplane.DualPlaneReportInputsFromMetering(ctx, &cyclicCursorQuerier{}, metering.Query{StreamID: "s1", Limit: 1})
	if !errors.Is(err, controlplane.ErrMeteringCursorFault) {
		t.Fatalf("got %v want ErrMeteringCursorFault", err)
	}
}

func TestPhase35_BridgeDrain_MaxFactsBoundSafeError(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "bound", MaxPageSize: 1, DefaultPageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := int64(1); i <= 3; i++ {
		f := phase35Fact("f"+drainItoa(int(i)), "cust-req", i, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 1, 0)
		if err := s.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = controlplane.ListUsageRowsFromMeteringBounded(ctx, s, metering.Query{StreamID: "cust-req", Limit: 1}, controlplane.MeteringDrainBound{MaxFacts: 2})
	if !errors.Is(err, controlplane.ErrMeteringDrainBoundExceeded) {
		t.Fatalf("got %v want ErrMeteringDrainBoundExceeded", err)
	}
}

func TestPhase35_DualPlaneReportInputsFromFacts_EmptyIsIncomplete(t *testing.T) {
	t.Parallel()
	in, err := cp.DualPlaneReportInputsFromFacts(nil)
	if err != nil {
		t.Fatal(err)
	}
	if in.Completeness != cp.ReportCompletenessIncomplete {
		t.Fatalf("empty completeness=%q want incomplete", in.Completeness)
	}
}

type stuckCursorQuerier struct{}

func (stuckCursorQuerier) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	return metering.Page{
		Facts: []metering.Fact{{
			FactID: "f1", StreamID: "s1", Sequence: 1,
			Kind: metering.FactKindCumulative, Perspective: metering.PerspectiveCustomer,
			Boundary: metering.BoundaryFrontendEgress, Lifecycle: metering.LifecycleLogicalRequest,
			Source: metering.SourceObserved, Authority: metering.AuthorityAuthoritative,
			Presence: metering.PresencePresent, RecordedAt: time.Unix(1, 0).UTC(),
		}},
		NextCursor: "stuck",
	}, nil
}

type cyclicCursorQuerier struct{ n int }

func (c *cyclicCursorQuerier) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	c.n++
	switch q.Cursor {
	case "":
		return metering.Page{Facts: []metering.Fact{phase35Fact("a", "s1", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 1, 0)}, NextCursor: "1"}, nil
	case "1":
		return metering.Page{Facts: []metering.Fact{phase35Fact("b", "s1", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 1)}, NextCursor: "2"}, nil
	default:
		return metering.Page{Facts: []metering.Fact{phase35Fact("c", "s1", 3, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 1)}, NextCursor: "1"}, nil
	}
}

func drainItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
