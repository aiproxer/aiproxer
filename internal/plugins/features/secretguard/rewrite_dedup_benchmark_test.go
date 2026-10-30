package secretguard

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkPrivateOccurrenceRewrite(b *testing.B) {
	for _, count := range []int{0, 16, 64, 256} {
		for _, repeatedReports := range []bool{false, true} {
			b.Run(fmt.Sprintf("hits=%d/repeated=%t", count, repeatedReports), func(b *testing.B) {
				const value = "private-credential"
				raw := bytes.Repeat([]byte(value+" "), count)
				fragment := LogicalFragment{Kind: FragmentText, Raw: raw, Location: "message", privateID: "field"}
				var occurrences []betterLeaksOccurrence
				for i := range count {
					start := i * (len(value) + 1)
					occurrence := betterLeaksOccurrence{value: []byte(value), fieldID: "field", span: betterLeaksSpan{1, 1, start + 1, start + len(value)}, start: start, end: start + len(value), offsetsValid: true, ruleID: "first-rule"}
					occurrences = append(occurrences, occurrence)
					if repeatedReports {
						occurrence.ruleID = "duplicate-rule"
						occurrences = append(occurrences, occurrence)
					}
				}
				findings := []betterLeaksFinding{{Location: fragment.Location, occurrences: occurrences}}
				b.ReportAllocs()
				for b.Loop() {
					candidates := betterLeaksLiteralCandidates(fragment, findings)
					if len(candidates) != count {
						b.Fatal("candidate identity changed")
					}
					matcher, err := newBetterLeaksRewriteMatcher(nil, fragment, candidates)
					if err != nil {
						b.Fatal(err)
					}
					if count == 0 {
						if matcher != nil {
							b.Fatal("no-hit path created a bridge")
						}
						continue
					}
					bridge, ok := matcher.(*betterLeaksRewriteMatcher)
					if !ok {
						b.Fatal("expected a rewrite bridge")
					}
					out, _, err := bridge.RedactBytes(b.Context(), raw)
					if err != nil || bridge.validateCoverage() != nil || bytes.Contains(out, []byte(value)) {
						b.Fatal("rewrite did not cover every credential")
					}
				}
			})
		}
	}
}
