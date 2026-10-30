package metering_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestValidateQueryRejectsTooBroadWithoutSelectiveBound(t *testing.T) {
	t.Parallel()
	err := metering.ValidateQuery(metering.Query{Limit: 10})
	if !errors.Is(err, metering.ErrQueryTooBroad) {
		t.Fatalf("got %v", err)
	}
	err = metering.ValidateQuery(metering.Query{
		Perspective: metering.PerspectiveCustomer,
		Limit:       10,
	})
	if !errors.Is(err, metering.ErrQueryTooBroad) {
		t.Fatalf("enum-only filter must be too broad: %v", err)
	}
	err = metering.ValidateQuery(metering.Query{
		TimeRange: metering.TimeRange{From: time.Unix(1, 0), To: time.Unix(2, 0)},
		Limit:     10,
	})
	if !errors.Is(err, metering.ErrQueryTooBroad) {
		t.Fatalf("time-only filter must be too broad: %v", err)
	}
}

func TestValidateQueryAcceptsIndexedSelectiveBounds(t *testing.T) {
	t.Parallel()
	cases := []metering.Query{
		{StreamID: "stream-1"},
		{RequestID: "req-1"},
		{Scope: metering.ScopeFilters{PrincipalID: scope.Known("prin-1")}},
		{BackendID: "backend-a"},
		{Model: "gpt-test"},
		{RuleID: "rule.quota"},
	}
	for i, q := range cases {
		if err := metering.ValidateQuery(q); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

func TestValidateQueryRejectsUnsupportedQueryClass(t *testing.T) {
	t.Parallel()
	err := metering.ValidateQuery(metering.Query{
		StreamID: "s",
		Class:    metering.QueryClassFinancialProjection,
	})
	if !errors.Is(err, metering.ErrQueryUnsupported) {
		t.Fatalf("got %v", err)
	}
	err = metering.ValidateQuery(metering.Query{
		StreamID: "s",
		Class:    metering.QueryClassActiveLease,
	})
	if !errors.Is(err, metering.ErrQueryUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestQueryUnsupportedReportsRouteAndWrongClassFields(t *testing.T) {
	t.Parallel()
	unsupported := metering.QueryUnsupported(metering.Query{
		StreamID: "s",
		RouteID:  "route-a",
		Class:    metering.QueryClassLiveReservation,
	})
	if len(unsupported) < 2 {
		t.Fatalf("unsupported=%#v", unsupported)
	}
	fields := map[string]bool{}
	for _, f := range unsupported {
		fields[f.Field] = true
	}
	if !fields["route_id"] || !fields["class"] {
		t.Fatalf("missing route/class: %#v", unsupported)
	}
}

func TestPageCarriesUnsupportedFilters(t *testing.T) {
	t.Parallel()
	page := metering.Page{
		Unsupported: []metering.UnsupportedFilter{{Field: "route_id", Reason: "not indexed"}},
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "route_id") {
		t.Fatalf("json=%s", raw)
	}
}
