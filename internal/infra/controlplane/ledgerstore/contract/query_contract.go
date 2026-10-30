package contract

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func testEmptyResults(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	page, err := s.Events(c, cp.EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Events() empty error = %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("Events() empty items = %#v, want nil/empty", page.Items)
	}
	if !page.Next.IsZero() {
		t.Fatalf("Events() empty Next = %#v, want zero", page.Next)
	}
	if page.Visibility != cp.VisibilityDefault && page.Visibility != "" {
		t.Fatalf("Events() empty visibility = %q", page.Visibility)
	}
}

func testScopePresencePreserved(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	ev := authEvent(1, "auth:presence:1", "p1")
	// Mix unknown and known-empty for distinct dimensions.
	ev.Scope.OrganizationID = scope.Unknown()
	ev.Scope.ProjectID = scope.Known("")
	if _, err := s.Append(c, ev); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	page, err := s.Events(c, cp.EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Events() len = %d, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.Scope.OrganizationID.IsKnown() {
		t.Fatalf("unknown organization projected as known: %#v", got.Scope.OrganizationID)
	}
	if !got.Scope.ProjectID.IsKnown() || !got.Scope.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty project projected as %#v", got.Scope.ProjectID)
	}
	if !got.Scope.PrincipalID.IsKnown() || got.Scope.PrincipalID.String() != "p1" {
		t.Fatalf("principal projected as %#v", got.Scope.PrincipalID)
	}
}

func testScopeFiltersKnownValueAndEmpty(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	evA := authEvent(1, "auth:scope:a", "pA")
	evA.Scope.ProjectID = scope.Known("proj-1")
	if _, err := s.Append(c, evA); err != nil {
		t.Fatalf("Append() a error = %v", err)
	}
	evB := authEvent(2, "auth:scope:b", "pB")
	evB.Scope.ProjectID = scope.Known("proj-2")
	if _, err := s.Append(c, evB); err != nil {
		t.Fatalf("Append() b error = %v", err)
	}
	evEmpty := authEvent(3, "auth:scope:empty", "pE")
	evEmpty.Scope.ProjectID = scope.Known("")
	if _, err := s.Append(c, evEmpty); err != nil {
		t.Fatalf("Append() empty error = %v", err)
	}

	page, err := s.Events(c, cp.EventQuery{Limit: 10, Common: cp.CommonFilters{Scope: cp.ScopeFilters{ProjectID: scope.Known("proj-1")}}})
	if err != nil {
		t.Fatalf("Events(proj-1) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceEventKey != "auth:scope:a" {
		t.Fatalf("Events(proj-1) = %d items, want only a", len(page.Items))
	}

	page, err = s.Events(c, cp.EventQuery{Limit: 10, Common: cp.CommonFilters{Scope: cp.ScopeFilters{ProjectID: scope.Known("")}}})
	if err != nil {
		t.Fatalf("Events(empty) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceEventKey != "auth:scope:empty" {
		t.Fatalf("Events(empty) = %d items, want only empty", len(page.Items))
	}
}

func testEventsQueryFilters(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	cfg := unsupportedConfigOf(f)
	appendAll(
		t, s,
		authEvent(1, "auth:p1:1", "p1"),
		attemptEvent(2, "attempt:p1:2", "p1", "openai", "gpt-4.1-mini", "routed", cp.AttemptSurfacedSurfaced),
		usageEvent(3, "usage:p1:3", "p1", "openai", "gpt-4.1-mini", 10, 5),
		attemptEvent(4, "attempt:p2:4", "p2", "anthropic", "claude-haiku", "routed", cp.AttemptSurfacedSurfaced),
	)

	// Filter by backend. Skip the assertion when backend_id is unsupported for
	// this store config (the filter is reported via Page.Unsupported instead).
	if !cfg.IsUnsupported(FieldBackendID) {
		page, err := s.Events(c, cp.EventQuery{
			Limit:  10,
			Common: cp.CommonFilters{BackendID: "anthropic"},
		})
		if err != nil {
			t.Fatalf("Events(backend) error = %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("Events(backend) len = %d, want 1", len(page.Items))
		}
		if page.Items[0].Correlation.BackendID != "anthropic" {
			t.Fatalf("Events(backend) returned wrong row: %#v", page.Items[0])
		}
	}

	// Filter by category.
	page, err := s.Events(c, cp.EventQuery{Limit: 10, Category: cp.CategoryUsage})
	if err != nil {
		t.Fatalf("Events(usage) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Category != cp.CategoryUsage {
		t.Fatalf("Events(usage) len = %d", len(page.Items))
	}

	// Filter by time range. Skip when time_range is unsupported.
	if !cfg.IsUnsupported(FieldTimeRange) {
		page, err = s.Events(c, cp.EventQuery{
			Limit:  10,
			Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: FixedTime.Add(3 * time.Second), To: FixedTime.Add(4 * time.Second)}},
		})
		if err != nil {
			t.Fatalf("Events(time) error = %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("Events(time) len = %d, want 1", len(page.Items))
		}
		if page.Items[0].SourceEventKey != "usage:p1:3" {
			t.Fatalf("Events(time) returned wrong row: %q", page.Items[0].SourceEventKey)
		}
	}
}

func testSessionsProjection(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		authEvent(1, "auth:p1:1", "p1"),
		attemptEvent(2, "attempt:p1:2", "p1", "openai", "gpt-4.1-mini", "routed", cp.AttemptSurfacedSurfaced),
		usageEvent(3, "usage:p1:3", "p1", "openai", "gpt-4.1-mini", 10, 5),
	)
	page, err := s.Sessions(c, cp.SessionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Sessions() len = %d, want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.SessionID != "sess-p1" {
		t.Fatalf("Sessions() session_id = %q, want sess-p1", row.SessionID)
	}
	if row.AttemptCount != 1 {
		t.Fatalf("Sessions() attempt_count = %d, want 1", row.AttemptCount)
	}
	if row.UsageTotals == nil || row.UsageTotals.TotalTokens != 15 {
		t.Fatalf("Sessions() usage_totals = %#v, want 15 total tokens", row.UsageTotals)
	}
	if row.EvidenceState != cp.EvidenceRecorded {
		t.Fatalf("Sessions() evidence_state = %q, want recorded", row.EvidenceState)
	}
}

func testAttemptsProjection(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		attemptEvent(1, "attempt:p1:1", "p1", "openai", "gpt-4.1-mini", "routed", cp.AttemptSurfacedSurfaced),
		attemptEvent(2, "attempt:p1:2", "p1", "anthropic", "claude-haiku", "replaced", cp.AttemptSurfacedSwallowed),
	)
	page, err := s.Attempts(c, cp.AttemptQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Attempts() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Attempts() len = %d, want 2", len(page.Items))
	}
	if page.Items[0].Surfaced != cp.AttemptSurfacedSurfaced {
		t.Fatalf("Attempts() first surfaced = %q, want surfaced", page.Items[0].Surfaced)
	}
	// Filter by surfaced.
	page, err = s.Attempts(c, cp.AttemptQuery{Limit: 10, Surfaced: string(cp.AttemptSurfacedSwallowed)})
	if err != nil {
		t.Fatalf("Attempts(swallowed) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Surfaced != cp.AttemptSurfacedSwallowed {
		t.Fatalf("Attempts(swallowed) len = %d", len(page.Items))
	}
}

func testUsageProjection(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		usageEvent(1, "usage:p1:1", "p1", "openai", "gpt-4.1-mini", 10, 5),
		usageEvent(2, "usage:p1:2", "p1", "openai", "gpt-4.1-mini", 20, 7),
	)
	page, err := s.Usage(c, cp.UsageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Usage() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Usage() len = %d, want 2", len(page.Items))
	}
	if page.Items[0].TotalTokens != 15 {
		t.Fatalf("Usage() first total = %d, want 15", page.Items[0].TotalTokens)
	}
	agg, err := s.UsageAggregate(c, cp.UsageAggregateQuery{
		Limit:   10,
		GroupBy: []string{"backend", "model"},
	})
	if err != nil {
		t.Fatalf("UsageAggregate() error = %v", err)
	}
	if len(agg.Items) != 1 {
		t.Fatalf("UsageAggregate() len = %d, want 1", len(agg.Items))
	}
	if agg.Items[0].TotalTokens != 42 {
		t.Fatalf("UsageAggregate() total = %d, want 42", agg.Items[0].TotalTokens)
	}
}

func testPolicyAuditProjection(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		policyEvent(1, "policy:p1:1", "p1", "allow", "ok"),
		auditEvent(2, "audit:p1:2", "p1", "session_started"),
	)
	page, err := s.PolicyAudit(c, cp.EvidenceQuery{Limit: 10})
	if err != nil {
		t.Fatalf("PolicyAudit() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("PolicyAudit() len = %d, want 2", len(page.Items))
	}
	// Filter by effect.
	page, err = s.PolicyAudit(c, cp.EvidenceQuery{Limit: 10, Effect: "allow"})
	if err != nil {
		t.Fatalf("PolicyAudit(allow) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Effect != "allow" {
		t.Fatalf("PolicyAudit(allow) len = %d", len(page.Items))
	}
}

func testPaginationContinuation(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	var evs []cp.Event
	for i := 1; i <= 5; i++ {
		ev := authEvent(i, "auth:page:"+strconv.Itoa(i), "p"+strconv.Itoa(i))
		evs = append(evs, ev)
	}
	appendAll(t, s, evs...)

	var collected []cp.Event
	cursor := cp.Cursor{}
	const maxPaginationIter = 100
	var iter int
	for ; iter < maxPaginationIter; iter++ {
		page, err := s.Events(c, cp.EventQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("Events() page error = %v", err)
		}
		collected = append(collected, page.Items...)
		if page.Next.IsZero() {
			break
		}
		cursor = page.Next
	}
	if iter == maxPaginationIter {
		t.Fatalf("pagination did not terminate after %d iterations (store adapter may regress on cursor advancement)", maxPaginationIter)
	}
	if len(collected) != 5 {
		t.Fatalf("collected = %d, want 5", len(collected))
	}
	// No duplicates by sequence and no skips: sequences must be 1..5.
	seen := map[int64]bool{}
	for _, ev := range collected {
		if seen[ev.ID.Sequence] {
			t.Fatalf("duplicate sequence %d in continuation walk", ev.ID.Sequence)
		}
		seen[ev.ID.Sequence] = true
	}
	for i := int64(1); i <= 5; i++ {
		if !seen[i] {
			t.Fatalf("missing sequence %d in continuation walk", i)
		}
	}
}

func testContinuationShapeBound(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		authEvent(1, "auth:cb:1", "p1"),
		authEvent(2, "auth:cb:2", "p2"),
	)
	page, err := s.Events(c, cp.EventQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if page.Next.IsZero() {
		t.Fatalf("expected continuation token, got none")
	}
	// Reusing the cursor with a different query shape must fail with a stable
	// invalid-query classification rather than silently widening the result.
	_, err = s.Events(c, cp.EventQuery{
		Limit:  1,
		Cursor: page.Next,
		Common: cp.CommonFilters{BackendID: "openai"},
	})
	if err == nil {
		t.Fatalf("cursor reuse across query shape must fail; got nil error")
	}
	if !errors.Is(err, controlplane.ErrInvalidQuery) {
		t.Fatalf("cursor reuse error = %v, want ErrInvalidQuery", err)
	}
}

// testContinuationTimeRangeShape asserts that a cursor obtained with one
// TimeRange.From value cannot be reused with a different From value. The
// time-range filter participates in the query shape hash, so changing the
// actual time value (not just its presence) must invalidate the cursor
// (requirement 2.7, 7.4).
func testContinuationTimeRangeShape(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	cfg := unsupportedConfigOf(f)
	if cfg.IsUnsupported(FieldTimeRange) {
		t.Skip("time_range unsupported for this store config; cursor time-range shape not exercised")
	}
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		authEvent(1, "auth:trs:1", "p1"),
		authEvent(2, "auth:trs:2", "p2"),
	)
	t1 := FixedTime.Add(1 * time.Second)
	t2 := FixedTime.Add(2 * time.Second)
	if t1.Equal(t2) {
		t.Fatalf("fixtures: t1 == t2, expected distinct times")
	}
	page, err := s.Events(c, cp.EventQuery{
		Limit:  1,
		Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: t1}},
	})
	if err != nil {
		t.Fatalf("Events(from=t1) error = %v", err)
	}
	if page.Next.IsZero() {
		t.Fatalf("expected continuation token, got none")
	}
	// Reusing the cursor with a different TimeRange.From value must fail with
	// ErrInvalidQuery rather than silently widening or narrowing the result.
	_, err = s.Events(c, cp.EventQuery{
		Limit:  1,
		Cursor: page.Next,
		Common: cp.CommonFilters{TimeRange: cp.TimeRange{From: t2}},
	})
	if err == nil {
		t.Fatalf("cursor reuse across TimeRange.From value must fail; got nil error")
	}
	if !errors.Is(err, controlplane.ErrInvalidQuery) {
		t.Fatalf("cursor reuse error = %v, want ErrInvalidQuery", err)
	}
}

func testUnsupportedFiltersReported(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	cfg := unsupportedConfigOf(f)
	if len(cfg.Fields) == 0 {
		t.Skip("store supports all documented filters; unsupported-filter reporting not exercised for this adapter")
	}
	// The contract exercises a common filter field that the configured store
	// cannot apply. BackendID is unsupported by every UnsupportedConfig used in
	// this package, so the assertion stays stable across memory and durable
	// factories.
	if !cfg.IsUnsupported(FieldBackendID) {
		t.Skipf("unsupported config does not include %q; got %v", FieldBackendID, cfg.Fields)
	}
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		authEvent(1, "auth:unsup:1", "p1"),
		attemptEvent(2, "attempt:unsup:2", "p1", "openai", "gpt-4.1-mini", "routed", cp.AttemptSurfacedSurfaced),
		usageEvent(3, "usage:unsup:3", "p1", "openai", "gpt-4.1-mini", 10, 5),
		policyEvent(4, "policy:unsup:4", "p1", "allow", "ok"),
		auditEvent(5, "audit:unsup:5", "p1", "session_started"),
	)

	// A common backend_id filter set to a value that matches none of the
	// appended events. If the store (incorrectly) applied this unsupported
	// filter, every view below would return zero rows. The contract requires
	// the filter be reported via Page.Unsupported and NOT applied, so each
	// view must return its full projected set (requirement 2.5, 8.6, 9.4).
	common := cp.CommonFilters{BackendID: "nonexistent-backend"}

	assertReportedAndNotApplied := func(t *testing.T, view string, unsupported []cp.UnsupportedFilter, gotCount, wantCount int) {
		t.Helper()
		found := false
		for _, u := range unsupported {
			if u.Field == FieldBackendID {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: unsupported filters = %#v, want %q reported", view, unsupported, FieldBackendID)
		}
		if gotCount != wantCount {
			t.Fatalf("%s: unsupported filter was applied: got %d items, want %d (filter must be reported, not applied)", view, gotCount, wantCount)
		}
	}

	evPage, err := s.Events(c, cp.EventQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("Events(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "Events", evPage.Unsupported, len(evPage.Items), 5)

	sessPage, err := s.Sessions(c, cp.SessionQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("Sessions(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "Sessions", sessPage.Unsupported, len(sessPage.Items), 1)

	attPage, err := s.Attempts(c, cp.AttemptQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("Attempts(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "Attempts", attPage.Unsupported, len(attPage.Items), 1)

	usePage, err := s.Usage(c, cp.UsageQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("Usage(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "Usage", usePage.Unsupported, len(usePage.Items), 1)

	aggPage, err := s.UsageAggregate(c, cp.UsageAggregateQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("UsageAggregate(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "UsageAggregate", aggPage.Unsupported, len(aggPage.Items), 1)

	polPage, err := s.PolicyAudit(c, cp.EvidenceQuery{Limit: 100, Common: common})
	if err != nil {
		t.Fatalf("PolicyAudit(unsupported) error = %v", err)
	}
	assertReportedAndNotApplied(t, "PolicyAudit", polPage.Unsupported, len(polPage.Items), 2)
}
