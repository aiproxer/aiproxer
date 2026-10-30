package telemetry_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 7.6, 7.7, 9.1 and 9.5.
//
// This file pins the PASS BREAKDOWN of the outbound tally: the bounded per-pass
// measurement dimension that makes an audit deployment's per-candidate realized saving
// recoverable instead of merely summed.
//
// The problem it answers is arithmetic, not cosmetic. In production both outbound passes
// observe ONE candidate. A REWRITE publishes the alias, so the late pass finds no real-root
// prefix and measures nothing. An AUDIT publishes nothing, so the late pass finds exactly
// what the early pass found and measures the same figure again: the measured ratio was
// exactly 2.00 in every fixture, and an operator reading the generation total could not
// recover the number a rewrite deployment would have realized without knowing how many
// passes re-measured. Requirement 9.5 asks for that figure to be CALCULABLE.
//
// Three properties are pinned, and the third is what makes the first two usable:
//
//	THE ROWS ADD UP TO THE TOTAL. The breakdown is a partition of the same measurement,
//	    not a second accounting: the rows sum to [telemetry.TotalCounters.PassObservedOpportunityBytes]
//	    exactly, so the headline figure cannot disagree with its own breakdown.
//	CARDINALITY IS BOUNDED AND THE LABELS ARE CLOSED. The series is a fixed-size array
//	    indexed by the pass vocabulary, an UNSTATED pass lands in its own row rather than
//	    being attributed to a real pass, and a value outside the vocabulary folds into a
//	    bounded slot that renders the vocabulary's own "unknown". Nothing keys on an
//	    input, so no amount of traffic can grow the series.
//	NO ROW IS EVER NEGATIVE. Each row clamps its own saving per observation, for the same
//	    reason the generation total does.

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
)

// passBreakdownLabels is the closed label set a published pass row may carry.
var passBreakdownLabels = map[string]bool{
	"unattributed": true,
	"attempt":      true,
	"request_part": true,
	"unknown":      true,
}

// passBreakdownRecorder builds a recorder over the zero compiled shape. The shape is not
// what is under test here: these assertions are about the pass dimension, and every report
// is fed directly so the recorder's own configuration cannot influence a figure.
func passBreakdownRecorder(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	return telemetry.New(config.Shape{})
}

// findPassRow returns the published row for one pass label.
func findPassRow(rows []telemetry.PassCounters, label string) (telemetry.PassCounters, bool) {
	for _, row := range rows {
		if row.Pass == label {
			return row, true
		}
	}
	return telemetry.PassCounters{}, false
}

// TestTheOutboundTallyIsBrokenDownByPass is the partition property.
//
// Two reports from two different passes, then a third from the first: the rows accumulate
// per pass, the generation total is the sum of the rows, and the outbound headline figure
// still says the same thing it always did.
func TestTheOutboundTallyIsBrokenDownByPass(t *testing.T) {
	t.Parallel()
	tel := passBreakdownRecorder(t)

	tel.ObserveOutbound(outbound.Report{
		Pass:    outbound.PassAttempt,
		Outcome: outbound.OutcomeRewriterRan,
		Stats: rewrite.Stats{
			Eligible: 2, Rewritten: 2, BytesBefore: 200, BytesAfter: 100,
		},
	})
	tel.ObserveOutbound(outbound.Report{
		Pass:    outbound.PassRequestPart,
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 1, BytesBefore: 50, BytesAfter: 50},
	})
	tel.ObserveOutbound(outbound.Report{
		Pass:    outbound.PassAttempt,
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 3, BytesBefore: 90, BytesAfter: 60},
	})

	snapshot := tel.Snapshot()
	attempt, ok := findPassRow(snapshot.Outbound.ByPass, outbound.PassAttempt.String())
	if !ok {
		t.Fatalf("no row for the attempt pass: %s", mustRenderPass(t, snapshot.Outbound.ByPass))
	}
	if attempt.Eligible != 5 || attempt.Rewritten != 2 || attempt.BytesBefore != 290 ||
		attempt.BytesAfter != 160 || attempt.BytesSaved != 130 {
		t.Errorf("the attempt row = %+v, want eligible 5, rewritten 2, before 290, after 160, saved 130", attempt)
	}
	requestPart, ok := findPassRow(snapshot.Outbound.ByPass, outbound.PassRequestPart.String())
	if !ok {
		t.Fatalf("no row for the request-part pass: %s", mustRenderPass(t, snapshot.Outbound.ByPass))
	}
	if requestPart.Eligible != 1 || requestPart.BytesSaved != 0 || requestPart.Reports != 1 {
		t.Errorf("the request-part row = %+v, want eligible 1, saved 0, reports 1", requestPart)
	}

	// The partition: the rows sum to the headline figures exactly.
	var saved, eligible int64
	for _, row := range snapshot.Outbound.ByPass {
		saved += row.BytesSaved
		eligible += row.Eligible
	}
	if saved != snapshot.Total.PassObservedOpportunityBytes {
		t.Errorf("the rows' saving sums to %d but the generation total is %d",
			saved, snapshot.Total.PassObservedOpportunityBytes)
	}
	if eligible != snapshot.Outbound.Virtualized.Eligible {
		t.Errorf("the rows' eligible sums to %d but the outbound tally is %d",
			eligible, snapshot.Outbound.Virtualized.Eligible)
	}
	if got, want := snapshot.Total.PassObservedOpportunityBytes, int64(130); got != want {
		t.Errorf("generation saving = %d, want %d", got, want)
	}
}

// TestThePassRowsStayBoundedWhateverIsFed pins the cardinality and the closed labels.
//
// The three cases are the three ways a pass value can be wrong or absent, and all three
// have to stay countable: an UNSTATED pass (the zero value), a value this build does not
// define, and the two real passes. None of them may be dropped, and none of them may grow
// the series.
func TestThePassRowsStayBoundedWhateverIsFed(t *testing.T) {
	t.Parallel()
	tel := passBreakdownRecorder(t)

	fed := []outbound.Pass{
		outbound.PassUnattributed,
		outbound.PassAttempt,
		outbound.PassRequestPart,
		outbound.Pass(0x7F),
		outbound.Pass(0xFF),
	}
	for i, pass := range fed {
		tel.ObserveOutbound(outbound.Report{
			Pass:    pass,
			Outcome: outbound.OutcomeRewriterRan,
			Stats:   rewrite.Stats{Eligible: 1, BytesBefore: 10, BytesAfter: 5},
		})
		// One row per distinct slot at most, so the series never grows with traffic.
		if got := len(tel.Snapshot().Outbound.ByPass); got > i+1 {
			t.Fatalf("after %d distinct pass values the series holds %d rows", i+1, got)
		}
	}

	rows := tel.Snapshot().Outbound.ByPass
	// Three vocabulary members plus the ONE out-of-vocabulary slot both unknown values
	// share. The bound is what keeps the export's shape fixed whatever is fed, and the
	// closed label set is this file's own statement of how many rows that allows.
	if len(rows) != len(passBreakdownLabels) {
		t.Fatalf("the series holds %d rows after five distinct pass values, want %d: %s",
			len(rows), len(passBreakdownLabels), mustRenderPass(t, rows))
	}
	for _, row := range rows {
		if !passBreakdownLabels[row.Pass] {
			t.Errorf("row label %q is outside the closed set", row.Pass)
		}
	}
	// Each vocabulary member saw exactly one report, and the ONE bounded out-of-vocabulary
	// slot absorbed both unknown values. That is what "cardinality stays bounded" has to
	// mean in practice: an unknown pass is countable without being able to grow the series.
	for _, row := range rows {
		want := int64(1)
		if row.Pass == outbound.Pass(0xFF).String() {
			want = 2
		}
		if row.Reports != want || row.BytesSaved != 5*want || row.Eligible != want {
			t.Errorf("row %q = %+v, want %d reports and %d saved bytes", row.Pass, row, want, 5*want)
		}
	}
	// An unstated pass must NOT be attributed to a real pass. Were it merged into the
	// attempt row, the per-candidate figure an operator reads would be quietly wrong.
	unattributed, ok := findPassRow(rows, outbound.PassUnattributed.String())
	if !ok {
		t.Fatalf("an unstated pass was dropped rather than published under its own label: %s",
			mustRenderPass(t, rows))
	}
	if unattributed.BytesSaved != 5 {
		t.Errorf("the unstated row = %+v, want its own five saved bytes", unattributed)
	}
}

// TestAPassRowNeverPublishesANegativeSaving keeps the clamp per row.
//
// Requirement 9.1 makes the outbound case unreachable through the rewriter, so this is the
// defensive half: a row that could read negative would be the most misleading number the
// projection could publish, and it is published per pass now.
func TestAPassRowNeverPublishesANegativeSaving(t *testing.T) {
	t.Parallel()
	tel := passBreakdownRecorder(t)

	tel.ObserveOutbound(outbound.Report{
		Pass:    outbound.PassAttempt,
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 10, BytesAfter: 99},
	})
	// A later genuine saving must not be reduced by the clamped one, which is what a sum
	// of signed deltas would do.
	tel.ObserveOutbound(outbound.Report{
		Pass:    outbound.PassAttempt,
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 100, BytesAfter: 40},
	})

	row, ok := findPassRow(tel.Snapshot().Outbound.ByPass, outbound.PassAttempt.String())
	if !ok {
		t.Fatal("the attempt pass published no row")
	}
	if row.BytesSaved != 60 {
		t.Errorf("the attempt row saved %d, want the genuine 60", row.BytesSaved)
	}
	// The raw totals are the measurement requirement 7.6 asks for, so clamping the saving
	// must not clamp them.
	if row.BytesBefore != 110 || row.BytesAfter != 139 {
		t.Errorf("the attempt row's raw totals = %d before / %d after, want 110 / 139",
			row.BytesBefore, row.BytesAfter)
	}
	for _, candidate := range tel.Snapshot().Outbound.ByPass {
		if candidate.BytesSaved < 0 || candidate.Eligible < 0 || candidate.Reports < 0 {
			t.Errorf("row %q publishes a negative figure: %+v", candidate.Pass, candidate)
		}
	}
}

// mustRenderPass renders published rows as JSON so a change to any nested value is visible
// where a Go comparison would not catch it.
func mustRenderPass(t *testing.T, rows []telemetry.PassCounters) string {
	t.Helper()
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("the pass rows are not encodable: %v", err)
	}
	return string(encoded)
}
