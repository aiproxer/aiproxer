package expansion

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// The canonical-validity check on a published document is a DEFENSIVE invariant:
// a splice of a decoded document cannot break the grammar, so no input reaches the
// branch that applies it. That is exactly why its ordering has to be pinned here
// rather than left to the reachable paths.
//
// The defect this pins is an accounting one. Finalize recorded the decision and
// only THEN re-labelled it for an invalid document, so one decision produced two
// reports: the first published the refused call as a successful expansion -
// decision.outcome derives OutcomeExpanded from `published != nil`, so the byte
// counters and the eligible/rewritten tallies were recorded for a call that was
// released as nothing - and the second published the refusal it actually was. The
// one-report-per-decision contract is stated on Reporter, and the fail-closed cases
// are precisely the ones whose accounting an operator has to be able to trust.
//
// The test is in-package because the branch is unreachable through Finalize, which
// is the whole point: there is no input that makes decide return an invalid
// document. The ordering is therefore only observable as the property of the step
// that performs it.

func TestAnInvalidPublishedDocumentIsReLabelledBeforeItIsReported(t *testing.T) {
	t.Parallel()

	t.Run("an_invalid_document_is_a_rejection", func(t *testing.T) {
		t.Parallel()

		got := decision{
			reason:    ReasonExpanded,
			published: []byte(`{"path":`),
		}.withValidatedPublication()

		if got.reason != ReasonInvalidRewrite {
			t.Fatalf("reason = %q, want %q", got.reason, ReasonInvalidRewrite)
		}
		if got.outcome() != OutcomeRejected {
			t.Fatalf("outcome = %q, want %q; a refused call must never be reported as an "+
				"expansion, because that is the figure an operator reads as work done",
				got.outcome(), OutcomeRejected)
		}
		if !got.reason.rejects() {
			t.Fatal("the relabelled reason must classify as a refusal")
		}
	})

	t.Run("a_valid_document_is_untouched", func(t *testing.T) {
		t.Parallel()

		for _, published := range [][]byte{
			[]byte(`{"path":"/real/root"}`),
			[]byte(`[1,2,3]`),
			[]byte(`"a string"`),
		} {
			if !rewrite.PublishedJSONValid(published) {
				t.Fatalf("fixture %q is not valid JSON", published)
			}
			got := decision{reason: ReasonExpanded, published: published}.withValidatedPublication()
			if got.reason != ReasonExpanded {
				t.Fatalf("a valid document was relabelled: reason = %q", got.reason)
			}
			if string(got.published) != string(published) {
				t.Fatalf("published document changed: %q", got.published)
			}
		}
	})

	t.Run("a_pass_through_publishes_nothing_and_is_untouched", func(t *testing.T) {
		t.Parallel()

		got := decision{reason: ReasonNoSelectors}.withValidatedPublication()
		if got.reason != ReasonNoSelectors {
			t.Fatalf("reason = %q, want %q", got.reason, ReasonNoSelectors)
		}
		if got.outcome() != OutcomeNoop {
			t.Fatalf("outcome = %q, want %q", got.outcome(), OutcomeNoop)
		}
	})

	t.Run("an_existing_refusal_is_not_relabelled_away", func(t *testing.T) {
		t.Parallel()

		// The check must not overwrite a reason that already refuses: the assembler
		// publishes that reason to the client, so replacing it would change what a
		// client is told. The published document is absent on this path anyway, which
		// is what makes the case worth stating.
		got := decision{reason: ReasonWorkspaceMismatch}.withValidatedPublication()
		if got.reason != ReasonWorkspaceMismatch {
			t.Fatalf("reason = %q, want %q", got.reason, ReasonWorkspaceMismatch)
		}
	})
}
