package bundle_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 7.3, 7.6, 7.7, 9.1 and 9.5.
//
// Pass rows describe observations, not distinct candidates. Audit sees unchanged
// paths in both passes; rewrite's second pass sees aliases and has no new saving.
// The total therefore names pass opportunities explicitly. Late-shaping coverage
// in opportunity_test.go prevents treating the early row as candidate savings.
// Rows remain bounded and content-free through the reader-enabled bundle seam.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
)

// savingsPassLabels is the closed label set a published pass row may carry.
var savingsPassLabels = map[string]bool{
	"unattributed": true,
	"attempt":      true,
	"request_part": true,
	"unknown":      true,
}

// savingsPassRow returns the published row for one pass label.
func savingsPassRow(s telemetry.Snapshot, label string) (telemetry.PassCounters, bool) {
	for _, row := range s.Outbound.ByPass {
		if row.Pass == label {
			return row, true
		}
	}
	return telemetry.PassCounters{}, false
}

// savingsPassRowsSum returns the saving and eligible totals the rows account for.
func savingsPassRowsSum(s telemetry.Snapshot) (saved, eligible int64) {
	for _, row := range s.Outbound.ByPass {
		saved += row.BytesSaved
		eligible += row.Eligible
	}
	return saved, eligible
}

// TestPassRowsExposeRepeatedAuditOpportunities drives one unchanged candidate
// through the two passes across the supported fixture flavors and sizes.
func TestPassRowsExposeRepeatedAuditOpportunities(t *testing.T) {
	t.Parallel()
	for _, flavor := range auditSavingsFlavors {
		for _, occurrences := range auditSavingsOccurrenceTiers {
			name := flavor.name + "_occurrences_" + itoa(occurrences) + "_shared_candidate"
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				tc := auditSavingsCase{
					name: name, flavor: flavor, occurrences: occurrences, eligible: true,
				}
				before, after := tc.auditSavingsExpectation(t)
				perCandidate := (before - after) * int64(occurrences)
				if perCandidate <= 0 {
					t.Fatal("the fixture measured no saving; the assertions below would be vacuous")
				}

				audit := tc.run(t, rewrite.ModeAudit, true)
				mutated := tc.run(t, rewrite.ModeRewrite, true)

				// A REWRITE realizes one pass's worth: the late pass finds the alias the
				// early pass published and measures nothing (requirement 2.9).
				mutatedEarly, ok := savingsPassRow(mutated.final, outbound.PassAttempt.String())
				if !ok {
					t.Fatalf("%s: the rewrite deployment published no attempt row", name)
				}
				if mutatedEarly.BytesSaved != perCandidate {
					t.Errorf("%s: the rewrite attempt row saved %d, want %d",
						name, mutatedEarly.BytesSaved, perCandidate)
				}
				if mutatedEarly.Eligible != int64(occurrences) {
					t.Errorf("%s: the rewrite attempt row measured %d eligible occurrences, want %d",
						name, mutatedEarly.Eligible, occurrences)
				}
				// The late pass ran and found nothing. It keeps its row - a non-zero report
				// count with zero figures - because "it ran and found nothing" and "it did
				// not run" are different operational facts, and only the second is an absent
				// row.
				late, present := savingsPassRow(mutated.final, outbound.PassRequestPart.String())
				if !present {
					t.Errorf("%s: the rewrite deployment published no late-pass row, so a pass that "+
						"never ran is indistinguishable from one that found nothing", name)
				} else if late.Reports != 1 || late.Eligible != 0 || late.Rewritten != 0 ||
					late.BytesBefore != 0 || late.BytesAfter != 0 || late.BytesSaved != 0 {
					t.Errorf("%s: the rewrite late-pass row = %+v, want one report and no measurement",
						name, late)
				}
				if got := mutated.final.Total.PassObservedOpportunityBytes; got != perCandidate {
					t.Errorf("%s: rewrite pass opportunity sum = %d, want %d", name, got, perCandidate)
				}

				// An AUDIT measures the same unmutated candidate twice, and now says so:
				// two rows, each one pass's worth.
				auditEarly, ok := savingsPassRow(audit.final, outbound.PassAttempt.String())
				if !ok {
					t.Fatalf("%s: the audit deployment published no attempt row", name)
				}
				auditLate, ok := savingsPassRow(audit.final, outbound.PassRequestPart.String())
				if !ok {
					t.Fatalf("%s: the audit deployment published no request-part row", name)
				}
				if auditEarly.BytesSaved != perCandidate || auditLate.BytesSaved != perCandidate {
					t.Errorf("%s: the audit rows saved %d and %d, want %d each",
						name, auditEarly.BytesSaved, auditLate.BytesSaved, perCandidate)
				}
				if got, want := audit.final.Total.PassObservedOpportunityBytes, 2*perCandidate; got != want {
					t.Errorf("%s: audit pass opportunity sum = %d, want %d: the late pass "+
						"re-measured the same unmutated candidate", name, got, want)
				}

				// The early passes see identical input and measure the same opportunity.
				if auditEarly.BytesSaved != mutatedEarly.BytesSaved {
					t.Errorf("%s: early opportunity differs by mode: audit %d, rewrite %d",
						name, auditEarly.BytesSaved, mutatedEarly.BytesSaved)
				}
				// The breakdown partitions the same measurement rather than restating it.
				for label, snapshot := range map[string]telemetry.Snapshot{"audit": audit.final, "rewrite": mutated.final} {
					saved, eligible := savingsPassRowsSum(snapshot)
					if saved != snapshot.Total.PassObservedOpportunityBytes {
						t.Errorf("%s/%s: the rows sum to %d but the total is %d",
							name, label, saved, snapshot.Total.PassObservedOpportunityBytes)
					}
					if eligible != snapshot.Outbound.Virtualized.Eligible {
						t.Errorf("%s/%s: the rows account for %d eligible occurrences but the tally is %d",
							name, label, eligible, snapshot.Outbound.Virtualized.Eligible)
					}
				}
			})
		}
	}
}

// TestThePassRowsAreBoundedAndCarryNoContent is requirements 7.7 and 9.3 on the new
// dimension, driven by the hostile private fixture so the scan is over rows a
// fully-configured deployment really publishes.
func TestThePassRowsAreBoundedAndCarryNoContent(t *testing.T) {
	t.Parallel()
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			tel, b := auditSavingsPrivateBundle(t, mode)
			drivePrivateSavings(t, tel, b)

			snapshot := tel.Snapshot()
			// Non-vacuity: both passes ran and measured something, so the rows below are
			// published figures rather than an empty series.
			if len(snapshot.Outbound.ByPass) < 2 {
				t.Fatalf("the hostile fixture published %d pass rows, want at least 2: %s",
					len(snapshot.Outbound.ByPass), mustMarshal(t, snapshot.Outbound.ByPass))
			}
			if snapshot.Outbound.Virtualized.BytesSaved <= 0 {
				t.Fatalf("the hostile fixture recorded no outbound saving, so the rows prove nothing")
			}
			if len(snapshot.Outbound.ByPass) > len(savingsPassLabels) {
				t.Errorf("the series holds %d rows, over the closed set's %d: cardinality is not bounded",
					len(snapshot.Outbound.ByPass), len(savingsPassLabels))
			}
			for _, row := range snapshot.Outbound.ByPass {
				if !savingsPassLabels[row.Pass] {
					t.Errorf("row label %q is outside the closed set", row.Pass)
				}
			}

			rendered := mustMarshal(t, snapshot.Outbound.ByPass)
			for _, forbidden := range []string{
				auditSavingsPrivateTool,
				auditSavingsPrivatePointer,
				auditSavingsPrivateRoot,
				auditSavingsStaleAlias,
				auditSavingsPrivateCallID,
				".__aip_v1__",
				"w_",
				"pack/aipapi",
			} {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("the rendered pass rows carry %q: %s", forbidden, rendered)
				}
			}
		})
	}
}

// itoa is the bounded integer spelling the subtest names use, so no case name carries a
// payload byte.
func itoa(value int) string { return strconv.Itoa(value) }
