package secretguard

import (
	"bytes"
	"testing"
)

func TestPrivateCandidates_PreserveFirstIdentityAndOwnBytes(t *testing.T) {
	raw := []byte("credential credential")
	first := betterLeaksOccurrence{value: []byte("credential"), fieldID: "field", span: betterLeaksSpan{1, 1, 1, 10}, start: 0, end: 10, offsetsValid: true, ruleID: "first"}
	duplicate := first
	duplicate.ruleID, duplicate.role = "later", betterLeaksOccurrenceComponent
	second := first
	second.start, second.end, second.span = 11, 21, betterLeaksSpan{1, 1, 12, 21}
	otherField := first
	otherField.fieldID = "other"
	fragment := LogicalFragment{Kind: FragmentText, Raw: raw, Location: "message", privateID: "field"}
	findings := []betterLeaksFinding{{Location: fragment.Location, occurrences: []betterLeaksOccurrence{first, duplicate, otherField, second}}}
	candidates := betterLeaksLiteralCandidates(fragment, findings)
	if len(candidates) != 2 || candidates[0].ruleID != "first" || candidates[0].role != first.role || candidates[1].start != 11 {
		t.Fatal("dedup changed private identity, ordering or first attribution")
	}
	matcher, err := newBetterLeaksRewriteMatcher(nil, fragment, append(candidates, candidates...))
	if err != nil {
		t.Fatal(err)
	}
	bridge, ok := matcher.(*betterLeaksRewriteMatcher)
	if !ok {
		t.Fatal("expected a rewrite bridge")
	}
	candidates[0].value[0] = 'X'
	if !bytes.Equal(first.value, []byte("credential")) || !bytes.Equal(bridge.occurrences[0].value, first.value) {
		t.Fatal("candidate or bridge storage aliases its input")
	}
	out, _, err := bridge.RedactBytes(t.Context(), raw)
	if err != nil || bridge.validateCoverage() != nil || string(out) != "********** **********" || len(bridge.covered) != 2 {
		t.Fatal("deduplicated rewrite lost complete coverage")
	}
}
