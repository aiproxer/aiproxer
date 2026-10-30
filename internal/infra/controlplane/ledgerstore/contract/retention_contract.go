package contract

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func testRetentionIdempotent(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	appendAll(
		t, s,
		authEvent(1, "auth:ret:1", "p1"),
		usageEvent(2, "usage:ret:2", "p1", "openai", "gpt-4.1-mini", 10, 5),
	)
	cutoff := FixedTime.Add(2 * time.Second)
	cmd := controlplane.RetentionCommand{
		Cutoff:     cutoff,
		Profile:    controlplane.RetentionProfileStandard,
		Visibility: cp.VisibilityDefault,
	}
	first, err := s.ApplyRetention(c, cmd)
	if err != nil {
		t.Fatalf("ApplyRetention() first error = %v", err)
	}
	second, err := s.ApplyRetention(c, cmd)
	if err != nil {
		t.Fatalf("ApplyRetention() second error = %v", err)
	}
	// Idempotence: a second run after the same cutoff must not mark or prune
	// additional records (requirement 6.1, design "Idempotency").
	if second.Marked+second.Pruned != 0 {
		t.Fatalf("ApplyRetention() second = +%d/+%d, want 0/0 (idempotent)", second.Marked, second.Pruned)
	}
	if first.Marked+first.Pruned == 0 {
		t.Fatalf("ApplyRetention() first = +0/+0, want at least one mark/prune (store performed work before idempotent re-run)")
	}
	// Records older than the cutoff must not appear in default query results.
	page, err := s.Events(c, cp.EventQuery{Limit: 100})
	if err != nil {
		t.Fatalf("Events() post-retention error = %v", err)
	}
	for _, ev := range page.Items {
		if !ev.OccurredAt.After(cutoff) && ev.EvidenceState != cp.EvidenceExpired && ev.EvidenceState != cp.EvidenceUnavailable {
			t.Fatalf("retention did not expire pre-cutoff record %q: state=%q", ev.SourceEventKey, ev.EvidenceState)
		}
	}
}

func testRedactionDefaultVisibility(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	ev := authEvent(1, "auth:redact:1", "p1")
	ev.Visibility = cp.VisibilityPrivileged
	ev.RedactionState = cp.RedactionPrivileged
	if _, err := s.Append(c, ev); err != nil {
		t.Fatalf("Append() privileged error = %v", err)
	}
	// Default-visibility query must not surface privileged raw evidence.
	page, err := s.Events(c, cp.EventQuery{Limit: 10, Visibility: cp.VisibilityDefault})
	if err != nil {
		t.Fatalf("Events(default) error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Events(default) len = %d, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.Visibility == cp.VisibilityPrivileged {
		t.Fatalf("privileged visibility leaked into default query: %#v", got)
	}
	if got.RedactionState == cp.RedactionPrivileged {
		t.Fatalf("privileged redaction state leaked into default query: %#v", got.RedactionState)
	}
}

// testSessionsDefaultVisibility asserts that a session summary built from a
// privileged event, queried with default visibility, does not expose privileged
// visibility/redaction state and that the page visibility remains default.
// SessionSummary carries no privileged raw detail fields today; this guard
// asserts the page-level visibility contract still mirrors the events view
// (requirement 4.6, 6.5).
func testSessionsDefaultVisibility(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	ev := authEvent(1, "auth:sessvis:1", "p1")
	ev.Visibility = cp.VisibilityPrivileged
	ev.RedactionState = cp.RedactionPrivileged
	if _, err := s.Append(c, ev); err != nil {
		t.Fatalf("Append(privileged) error = %v", err)
	}
	page, err := s.Sessions(c, cp.SessionQuery{Limit: 10, Visibility: cp.VisibilityDefault})
	if err != nil {
		t.Fatalf("Sessions(default) error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Sessions(default) len = %d, want 1", len(page.Items))
	}
	if page.Visibility == cp.VisibilityPrivileged {
		t.Fatalf("Sessions(default) page visibility leaked privileged: %q", page.Visibility)
	}
	row := page.Items[0]
	if row.EvidenceState == cp.EvidenceExpired || row.EvidenceState == cp.EvidenceUnavailable {
		t.Fatalf("Sessions(default) evidence_state = %q, want a non-degraded state", row.EvidenceState)
	}
}
