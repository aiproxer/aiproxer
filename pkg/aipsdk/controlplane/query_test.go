package controlplane_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCursorIsOpaque(t *testing.T) {
	t.Parallel()
	if !(controlplane.Cursor{}).IsZero() {
		t.Fatalf("zero cursor must report zero")
	}
	c := controlplane.Cursor{Token: "opaque-bytes"}
	if c.IsZero() {
		t.Fatalf("non-zero cursor reported zero")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !contains(string(raw), `"token":"opaque-bytes"`) {
		t.Fatalf("cursor token must round-trip opaquely: %s", raw)
	}
}

func TestPageReportsItemsNextAndUnsupported(t *testing.T) {
	t.Parallel()
	p := controlplane.Page[controlplane.SessionSummary]{
		Items: []controlplane.SessionSummary{{SessionID: "s1"}},
		Next:  controlplane.Cursor{Token: "next"},
		Unsupported: []controlplane.UnsupportedFilter{
			{Field: "cost_center", Reason: "not recorded"},
		},
		Visibility: controlplane.VisibilityDefault,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.Page[controlplane.SessionSummary]
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Items) != 1 || back.Items[0].SessionID != "s1" {
		t.Fatalf("items lost: %#v", back.Items)
	}
	if back.Next.Token != "next" {
		t.Fatalf("next cursor lost: %#v", back.Next)
	}
	if len(back.Unsupported) != 1 || back.Unsupported[0].Field != "cost_center" {
		t.Fatalf("unsupported filters lost: %#v", back.Unsupported)
	}
	if back.Visibility != controlplane.VisibilityDefault {
		t.Fatalf("visibility lost: %q", back.Visibility)
	}
}

func TestPageEmptyIsSerializable(t *testing.T) {
	t.Parallel()
	p := controlplane.Page[controlplane.Event]{}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal empty page: %v", err)
	}
	if !contains(string(raw), `"items":null`) {
		t.Fatalf("empty page items must serialize as null/empty, got %s", raw)
	}
}

func TestSessionQueryPreservesUnknownVsKnownEmptyFilters(t *testing.T) {
	t.Parallel()
	q := controlplane.SessionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("u1"),
				TenantID:    scope.Unknown(),
				ProjectID:   scope.Known(""),
			},
		},
		Limit: 50,
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.SessionQuery
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Common.Scope.PrincipalID.IsKnown() || back.Common.Scope.PrincipalID.String() != "u1" {
		t.Fatalf("principal filter lost: %#v", back.Common.Scope.PrincipalID)
	}
	if !back.Common.Scope.TenantID.IsUnknown() {
		t.Fatalf("unknown tenant filter must round-trip unknown")
	}
	if !back.Common.Scope.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty project filter must round-trip known-empty")
	}
	if back.Limit != 50 {
		t.Fatalf("limit lost: %d", back.Limit)
	}
}

func TestQueryStructsAreConstructible(t *testing.T) {
	t.Parallel()
	var (
		_ controlplane.SessionQuery
		_ controlplane.AttemptQuery
		_ controlplane.UsageQuery
		_ controlplane.UsageAggregateQuery
		_ controlplane.EvidenceQuery
		_ controlplane.EventQuery
	)
	qs := []struct {
		name string
		has  func(q any) bool
	}{
		{"session", func(q any) bool { _, ok := q.(controlplane.SessionQuery); return ok }},
		{"attempt", func(q any) bool { _, ok := q.(controlplane.AttemptQuery); return ok }},
		{"usage", func(q any) bool { _, ok := q.(controlplane.UsageQuery); return ok }},
		{"usage_aggregate", func(q any) bool { _, ok := q.(controlplane.UsageAggregateQuery); return ok }},
		{"evidence", func(q any) bool { _, ok := q.(controlplane.EvidenceQuery); return ok }},
		{"event", func(q any) bool { _, ok := q.(controlplane.EventQuery); return ok }},
	}
	for _, c := range qs {
		if !c.has(nil) {
			// just ensuring compile; the type assertions above already prove constructibility.
			_ = c
		}
	}
}

func TestResultRowTypesAreConstructible(t *testing.T) {
	t.Parallel()
	var (
		_ controlplane.SessionSummary
		_ controlplane.AttemptRow
		_ controlplane.UsageRow
		_ controlplane.UsageAggregate
		_ controlplane.PolicyAuditRow
	)
}

func contains(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
