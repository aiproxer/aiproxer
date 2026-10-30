package rewrite_test

// This file pins requirements.md 7.7 at the serialization boundary of the shared
// engine's own vocabulary: the per-surface skip tally requirement 7.6 asks to be
// counted "by bounded reason" must reach any exporter as the closed-vocabulary label,
// never as the ordinal an integer-coded enum otherwise renders as.
//
// The out-of-vocabulary value is included because it is the case a healthy build never
// produces and the one that must still degrade: an integer that escapes the process
// carries no label a reader can act on, whereas "unknown" is a bounded, actionable
// answer and keeps the exporter's cardinality at the size of the vocabulary plus one.

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

func TestEverySkipReasonRendersAsABoundedString(t *testing.T) {
	t.Parallel()
	for _, reason := range []rewrite.SkipReason{
		rewrite.SkipReasonNone,
		rewrite.SkipReasonMappingInactive,
		rewrite.SkipReasonNoSelectors,
		rewrite.SkipReasonPayloadAbsent,
		rewrite.SkipReasonPayloadInvalid,
		rewrite.SkipReasonPayloadNotObject,
		rewrite.SkipReasonSelectorUnresolved,
		rewrite.SkipReasonSelectorNotString,
		rewrite.SkipReasonSelectorObject,
		rewrite.SkipReasonSelectorNotStringArray,
		rewrite.SkipReasonOpaqueResultUnchanged,
		rewrite.SkipReasonOpaqueResultBounded,
		rewrite.SkipReason(0xFF),
	} {
		encoded, err := json.Marshal(rewrite.Stats{
			Skips: []rewrite.Skip{{Reason: reason, Count: 3}},
		})
		if err != nil {
			t.Fatalf("marshal stats: %v", err)
		}
		var decoded struct {
			Skips []struct {
				Reason string `json:"Reason"`
				Count  int    `json:"Count"`
			} `json:"Skips"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("reason %v rendered as %s, which is not a bounded label: %v", reason, encoded, err)
		}
		if len(decoded.Skips) != 1 {
			t.Fatalf("reason %v: rendered stats %s lost its skip tally", reason, encoded)
		}
		if got, want := decoded.Skips[0].Reason, reason.String(); got != want {
			t.Fatalf("reason %v rendered as %q, want the bounded label %q", reason, got, want)
		}
		if decoded.Skips[0].Count != 3 {
			t.Fatalf("a real count must stay a number; only ENUMS are bounded strings: %s", encoded)
		}
	}
}
