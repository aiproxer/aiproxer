package outbound_test

// This file pins requirements.md 7.7 at the SERIALIZATION boundary of the outbound
// report. A report is documented as safe to "log, export, or use as a metric
// dimension", and json.Marshal is the most likely exporter a deployment reaches for,
// so the rendered bytes are part of the feature's observable surface rather than an
// implementation detail of Go's encoder.
//
// The property is that EVERY enum dimension renders as a BOUNDED STRING. It is not
// enough that the in-process String() is bounded: an integer-coded enum serializes as
// a raw number, and a number is unbounded in the only sense that matters here - a
// future member, or a value this build does not define, exports as whatever the
// ordinal happens to be, and an ordinal carries no label an operator can act on. The
// test therefore pins the RENDERED form, not the String method, and it pins the
// out-of-vocabulary case separately because that is the one a healthy build never
// produces and the one that must still degrade.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// TestEveryReportEnumRendersAsABoundedString walks the whole closed outcome vocabulary
// plus the out-of-range value and asserts each enum field of a rendered report is the
// QUOTED label rather than a number.
//
// The rendered documents are decoded back into a map first so the assertion is about a
// FIELD's JSON type, not about a substring: an integer that happens to stringify to
// something label-shaped cannot pass, and a label that appears in a different field
// cannot stand in for the one under test.
func TestEveryReportEnumRendersAsABoundedString(t *testing.T) {
	t.Parallel()
	for _, outcome := range []outbound.Outcome{
		outbound.OutcomeRewriterRan,
		outbound.OutcomeProjectRootUnusable,
		outbound.OutcomeTransformationFailed,
		outbound.OutcomeWorkspaceUnresolved,
		// The value no pass produces: a build that later grows a fifth member, or a
		// caller that supplies an ordinal by hand. It must still render as a label.
		outbound.Outcome(0xFF),
	} {
		rendered := renderReport(t, outcome, pathvirtualization.SkipReasonEmptyRoot)
		assertEnumField(t, rendered, "Outcome", outcome.String())
	}
}

// TestTheRootReasonRendersAsABoundedString covers the sibling dimension, which is the
// one that was already a string and therefore the one a reader would assume is safe.
// It is only safe while the closed vocabulary is enforced, so the out-of-vocabulary
// value is asserted to degrade rather than to pass through verbatim.
func TestTheRootReasonRendersAsABoundedString(t *testing.T) {
	t.Parallel()
	for _, reason := range []pathvirtualization.SkipReason{
		pathvirtualization.SkipReasonNone,
		pathvirtualization.SkipReasonEmptyRoot,
		pathvirtualization.SkipReasonRelativeRoot,
		pathvirtualization.SkipReasonMalformedVolumeRoot,
		pathvirtualization.SkipReasonDeviceNamespace,
		pathvirtualization.SkipReasonReservedNamespaceCollision,
		// A value outside the closed vocabulary, which an exported named string type
		// permits any caller to construct. It must degrade rather than pass through.
		pathvirtualization.SkipReason("/home/dev/projects/aiproxer-example-project"),
	} {
		rendered := renderReport(t, outbound.OutcomeProjectRootUnusable, reason)
		assertEnumField(t, rendered, "RootReason", pathvirtualization.SkipReason(reason).String())
	}
}

// TestTheRewriteSkipReasonRendersAsABoundedString covers the third enum dimension of
// the report, the per-surface skip tally requirement 7.6 asks to be counted "by bounded
// reason".
func TestTheRewriteSkipReasonRendersAsABoundedString(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(rewrite.Stats{
		Skips: []rewrite.Skip{{Reason: rewrite.SkipReasonNoSelectors, Count: 2}},
	})
	if err != nil {
		t.Fatalf("marshal rewrite stats: %v", err)
	}
	var decoded struct {
		Skips []struct {
			Reason string `json:"Reason"`
			Count  int    `json:"Count"`
		} `json:"Skips"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode rendered stats %s: %v", encoded, err)
	}
	if len(decoded.Skips) != 1 {
		t.Fatalf("rendered stats %s lost its skip tally", encoded)
	}
	if got, want := decoded.Skips[0].Reason, rewrite.SkipReasonNoSelectors.String(); got != want {
		t.Fatalf("skip reason rendered as %q, want the bounded label %q", got, want)
	}
	if decoded.Skips[0].Count != 2 {
		t.Fatalf("a real count must stay a number; only ENUMS are bounded strings: %s", encoded)
	}
}

// TestTheReportNeverRendersAnEnumAsAnOrdinal is the negative form, stated separately
// because it is the property a reader of a metric export actually depends on.
func TestTheReportNeverRendersAnEnumAsAnOrdinal(t *testing.T) {
	t.Parallel()
	rendered := renderReport(t, outbound.Outcome(3), pathvirtualization.SkipReasonEmptyRoot)
	if strings.Contains(rendered, ":3") || strings.Contains(rendered, ": 3") {
		t.Fatalf("an enum rendered as an ordinal, which is unbounded (requirements.md 7.7): %s", rendered)
	}
	if !strings.Contains(rendered, strconv.Quote(outbound.Outcome(3).String())) {
		t.Fatalf("outcome must render as its bounded label, got %s", rendered)
	}
}

// renderReport renders one report carrying the given enum values and a non-zero
// statistics value, so every enum field of the value is present in the encoded document.
func renderReport(
	t *testing.T,
	outcome outbound.Outcome,
	rootReason pathvirtualization.SkipReason,
) string {
	t.Helper()
	encoded, err := json.Marshal(outbound.Report{
		Outcome:    outcome,
		RootReason: rootReason,
		Stats: rewrite.Stats{
			Eligible: 1, Rewritten: 1, BytesBefore: 40, BytesAfter: 10,
			Skips: []rewrite.Skip{{Reason: rewrite.SkipReasonNoSelectors, Count: 2}},
		},
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(encoded)
}

// assertEnumField asserts that one field of a rendered report is a quoted string equal
// to want. It decodes into map[string]any rather than substring matching so the check
// is about that field's JSON type and value.
func assertEnumField(t *testing.T, rendered, field, want string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatalf("decode rendered report %s: %v", rendered, err)
	}
	value, present := decoded[field]
	if !present {
		t.Fatalf("rendered report %s has no %s field", rendered, field)
	}
	text, isText := value.(string)
	if !isText {
		t.Fatalf("%s rendered as %T (%v), want the bounded string %q", field, value, value, want)
	}
	if text != want {
		t.Fatalf("%s rendered as %q, want %q", field, text, want)
	}
}
