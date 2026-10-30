package observability

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecorderSnapshotSortsEvidenceAndPhaseTieBreakers(t *testing.T) {
	t.Parallel()
	recorder := NewRecorder(8)
	for _, sample := range []Observation{
		{Stage: StagePreview, Outcome: OutcomeCandidate, Evidence: "z", RuleID: "same", Phase: "a"},
		{Stage: StagePreview, Outcome: OutcomeCandidate, Evidence: "a", RuleID: "same", Phase: "z"},
		{Stage: StagePreview, Outcome: OutcomeCandidate, Evidence: "a", RuleID: "same", Phase: "a"},
	} {
		recorder.Observe(sample)
	}

	got := recorder.Snapshot()
	if len(got) != 3 {
		t.Fatalf("snapshot length=%d, want 3: %#v", len(got), got)
	}
	for i, want := range []struct{ evidence, phase string }{{"a", "a"}, {"a", "z"}, {"z", "a"}} {
		if got[i].Evidence != want.evidence || got[i].Phase != want.phase {
			t.Fatalf("snapshot[%d]=%#v, want evidence=%q phase=%q", i, got[i], want.evidence, want.phase)
		}
	}
}

func TestBoundedIDTruncatesOnRuneBoundary(t *testing.T) {
	t.Parallel()
	got := BoundedID(strings.Repeat("界", 30))
	if !utf8.ValidString(got) {
		t.Fatalf("bounded label is invalid UTF-8: %q", got)
	}
	if len(got) > 64 {
		t.Fatalf("bounded label bytes=%d, want <=64", len(got))
	}
	if got != strings.Repeat("界", 21) {
		t.Fatalf("bounded label=%q, want 21 complete runes", got)
	}
}

func TestRecorderNormalizesNonHexHash(t *testing.T) {
	t.Parallel()
	malformed := "sha256:" + strings.Repeat("z", 64)
	recorder := NewRecorder(1)
	recorder.Observe(Observation{Stage: StageEvent, Outcome: OutcomeObserved, CorrelationHash: malformed})
	got := recorder.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot=%#v, want one observation", got)
	}
	if got[0].CorrelationHash != HashID(malformed) {
		t.Fatalf("correlation hash=%q, want normalized hash %q", got[0].CorrelationHash, HashID(malformed))
	}
}

func TestRecorderClearsCorrelationHashAfterSeriesAggregates(t *testing.T) {
	t.Parallel()
	recorder := NewRecorder(1)
	recorder.Observe(Observation{
		Stage: StageEvent, Outcome: OutcomeObserved, CorrelationHash: HashID("first"),
	})
	recorder.Observe(Observation{
		Stage: StageEvent, Outcome: OutcomeObserved, CorrelationHash: HashID("second"),
	})
	got := recorder.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot=%#v, want one aggregate", got)
	}
	if got[0].Count != 2 {
		t.Fatalf("aggregate count=%d, want 2", got[0].Count)
	}
	if got[0].CorrelationHash != "" {
		t.Fatalf("aggregate correlation hash=%q, want cleared after merge", got[0].CorrelationHash)
	}
}
