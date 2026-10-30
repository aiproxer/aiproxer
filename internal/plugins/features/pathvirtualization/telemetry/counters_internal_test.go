package telemetry

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
)

// A tally array is declared to be one slot LARGER than the closed vocabulary it
// indexes, so that a value outside that vocabulary lands in a bounded slot whose
// label says the recorder did not recognise it. Sharing the last slot with the
// final genuine member is not a bounded fold: it publishes an unrecognised
// condition AS a specific one, which is the reading the array's own header calls
// out as unacceptable.
//
// These are in-package tests because the fold and the label projection are the
// unit under test: the published symptom is only visible through a snapshot, and a
// snapshot cannot distinguish "this build has a fifth outcome" from "this build
// mislabelled a hostile value as workspace_unresolved".

func TestAnOutOfVocabularyOutboundOutcomeIsNotPublishedAsAGenuineOne(t *testing.T) {
	t.Parallel()

	const hostile = outbound.Outcome(outbound.OutcomeWorkspaceUnresolved + 1)

	slot := outcomeSlot(hostile)
	if slot == int(outbound.OutcomeWorkspaceUnresolved) {
		t.Fatalf("an outcome outside the closed vocabulary folds into slot %d, which IS "+
			"OutcomeWorkspaceUnresolved; an unrecognised condition is being published as a "+
			"genuine workspace failure", slot)
	}
	if slot >= outboundOutcomeSlots {
		t.Fatalf("fold target %d is outside the %d-slot array, so the tally would index out of range",
			slot, outboundOutcomeSlots)
	}
	if got, want := labelOutboundOutcome(slot), "unknown"; got != want {
		t.Fatalf("folded outcome label = %q, want %q", got, want)
	}
}

func TestAnOutOfVocabularyExpansionOutcomeIsNotPublishedAsAClientRefusal(t *testing.T) {
	t.Parallel()

	// The expansion direction is the security-relevant one: OutcomeRejected is a
	// client-facing refusal. Publishing an unrecognised decision AS a refusal is the
	// opposite failure direction from publishing it as a noop, so it is pinned
	// separately.
	const hostile = expansion.Outcome(expansion.OutcomeRejected + 1)

	slot := expansionOutcomeSlot(hostile)
	if slot == int(expansion.OutcomeRejected) {
		t.Fatalf("an outcome outside the closed vocabulary folds into slot %d, which IS "+
			"OutcomeRejected; a snapshot would assert a client refusal that never happened", slot)
	}
	if slot >= expansionOutcomeSlots {
		t.Fatalf("fold target %d is outside the %d-slot array, so the tally would index out of range",
			slot, expansionOutcomeSlots)
	}
	if got, want := labelExpansionOutcome(slot), "unknown"; got != want {
		t.Fatalf("folded outcome label = %q, want %q", got, want)
	}
}

// The fold must stay a fold rather than becoming a drop or a panic: a hostile
// value still has to be countable, and the whole array still has to be writable
// at every index the fold can produce.
func TestEveryOutcomeSlotIsWritableAndBounded(t *testing.T) {
	t.Parallel()

	for value := -4; value <= 12; value++ {
		outSlot := outcomeSlot(outbound.Outcome(value))
		if outSlot < 0 || outSlot >= outboundOutcomeSlots {
			t.Fatalf("outcomeSlot(%d) = %d, outside [0, %d)", value, outSlot, outboundOutcomeSlots)
		}
		if got := labelOutboundOutcome(outSlot); got == "" {
			t.Fatalf("labelOutboundOutcome(%d) is empty", outSlot)
		}

		expSlot := expansionOutcomeSlot(expansion.Outcome(value))
		if expSlot < 0 || expSlot >= expansionOutcomeSlots {
			t.Fatalf("expansionOutcomeSlot(%d) = %d, outside [0, %d)", value, expSlot, expansionOutcomeSlots)
		}
		if got := labelExpansionOutcome(expSlot); got == "" {
			t.Fatalf("labelExpansionOutcome(%d) is empty", expSlot)
		}
	}
}
