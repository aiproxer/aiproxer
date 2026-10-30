package contract

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func testAppendDedupeOrdering(t *testing.T, f Factory) {
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

	// Dedupe: same source key returns duplicate outcome and does not create a new row.
	dup, err := s.Append(c, authEvent(1, "auth:p1:1", "p1"))
	if err != nil {
		t.Fatalf("Append(dup) error = %v", err)
	}
	if dup.Dedupe != cp.DedupeDuplicate {
		t.Fatalf("Append(dup) dedupe = %q, want duplicate", dup.Dedupe)
	}

	page, err := s.Events(c, cp.EventQuery{Limit: 100})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("Events() len = %d, want 3 (dedupe must not add a row)", len(page.Items))
	}
	// Deterministic ordering by store sequence: occurred_at ascends with seq.
	for i := 1; i < len(page.Items); i++ {
		prev := page.Items[i-1]
		cur := page.Items[i]
		if cur.ID.Sequence <= prev.ID.Sequence {
			t.Fatalf("Events() not ordered by sequence at %d: prev=%d cur=%d", i, prev.ID.Sequence, cur.ID.Sequence)
		}
	}
}

func testReadiness(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	if err := s.CheckReadiness(ctx(t)); err != nil {
		t.Fatalf("CheckReadiness on empty store = %v, want nil", err)
	}
}

func testUnsafeEvidenceRejected(t *testing.T, f Factory) {
	t.Helper()
	maybeParallel(t, f)
	s := f.Build(t)
	c := ctx(t)
	ev := authEvent(1, "auth:unsafe:1", "p1")
	ev.Summary = "bearer token leak"
	_, err := s.Append(c, ev)
	if err == nil {
		t.Fatalf("Append(unsafe summary) must fail; got nil")
	}
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("Append(unsafe summary) error = %v, want ErrUnsafeEvidence", err)
	}
}
