package expansion_test

// This file pins requirements.md 7.7 at the SERIALIZATION boundary of the expansion
// report: every enum dimension of the value must reach any exporter as a bounded
// string, never as the ordinal an integer-coded enum otherwise renders as.
//
// The expansion report carries THREE enum dimensions - the pass outcome, the per-call
// decision reason, and the lexical core's own root refusal - plus the shared engine's
// per-surface skip tally. A reader who found one of them rendering as a number would
// reasonably assume the rest do too, so all of them are pinned here rather than only
// the one the obligation happened to name. The out-of-vocabulary value is included for
// each, because it is the case a healthy build never produces and the one that must
// still degrade.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
)

// TestEveryReportEnumRendersAsABoundedString pins requirements.md 7.7 at the
// serialization boundary: every enum dimension of the report reaches an exporter as
// the fixed, closed label, never as the ordinal an integer-coded enum otherwise
// renders as, and a value outside the vocabulary still degrades to a bounded label.
//
// The expected labels are hard-coded rather than read back from the code under test,
// so a String method that started echoing its input - the risk the string-typed root
// refusal carries - fails here instead of agreeing with itself.
func TestEveryReportEnumRendersAsABoundedString(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome expansion.Outcome
		label   string
	}{
		{expansion.OutcomeExpanded, "expanded"},
		{expansion.OutcomeNoop, "noop"},
		{expansion.OutcomeRejected, "rejected"},
		{expansion.Outcome(0xFF), "unknown"},
	} {
		assertEnumField(t, renderReport(t, tc.outcome, expansion.ReasonExpanded), "Outcome", tc.label)
	}
	for _, tc := range []struct {
		reason expansion.Reason
		label  string
	}{
		{expansion.ReasonNone, ""},
		{expansion.ReasonExpanded, "expanded"},
		{expansion.ReasonAuditMode, "audit_mode"},
		{expansion.ReasonNoAlias, "no_alias"},
		{expansion.ReasonNoSelectors, "no_selectors"},
		{expansion.ReasonArgsAbsent, "args_absent"},
		{expansion.ReasonPayloadNotObject, "payload_not_object"},
		{expansion.ReasonRootUnusable, "root_unusable"},
		{expansion.ReasonMappingInactive, "mapping_inactive"},
		{expansion.ReasonArgsUnparseable, "args_unparseable"},
		{expansion.ReasonMalformedReservedAlias, "malformed_reserved_alias"},
		{expansion.ReasonWorkspaceMismatch, "workspace_mismatch"},
		{expansion.ReasonExpandedTooLarge, "expanded_too_large"},
		{expansion.ReasonInvalidRewrite, "invalid_rewrite"},
		{expansion.Reason(0xFF), "unknown"},
	} {
		assertEnumField(t, renderReport(t, expansion.OutcomeNoop, tc.reason), "Reason", tc.label)
	}
	for _, tc := range []struct {
		root  pathvirtualization.SkipReason
		label string
	}{
		{pathvirtualization.SkipReasonNone, ""},
		{pathvirtualization.SkipReasonEmptyRoot, "empty_root"},
		{pathvirtualization.SkipReasonRelativeRoot, "relative_root"},
		{pathvirtualization.SkipReasonMalformedVolumeRoot, "malformed_volume_root"},
		{pathvirtualization.SkipReasonDeviceNamespace, "device_namespace"},
		{pathvirtualization.SkipReasonReservedNamespaceCollision, "reserved_namespace_collision"},
		// A real root handed to the string-typed enum must not survive into the export.
		{pathvirtualization.SkipReason("/home/dev/projects/aiproxer-example-project"), "unknown"},
	} {
		assertEnumField(t, renderReportWithRoot(t, expansion.OutcomeNoop,
			expansion.ReasonRootUnusable, tc.root), "RootReason", tc.label)
	}
}

// TestTheRenderedReasonIsThePublishedReasonCode ties the two renderings together: the
// same label travels as the toolcall.Result.ReasonCode a client-facing refusal is built
// from, so a report and the refusal that names the same condition must not disagree
// about how to spell it.
func TestTheRenderedReasonIsThePublishedReasonCode(t *testing.T) {
	t.Parallel()
	rendered := renderReport(t, expansion.OutcomeRejected, expansion.ReasonWorkspaceMismatch)
	assertEnumField(t, rendered, "Reason", expansion.ReasonWorkspaceMismatch.String())
	if !strings.Contains(rendered, `"workspace_mismatch"`) {
		t.Fatalf("the stale-workspace label must reach the export verbatim: %s", rendered)
	}
}

// TestParseReasonRejectsAnythingOutsideTheClosedVocabulary pins the decode side of
// the same wire contract: every label the pass can publish parses back to its own
// reason, while the empty label and any other text are rejected rather than mistaken
// for ReasonNone. A consumer acting on a toolcall.Result.ReasonCode depends on
// exactly that distinction.
func TestParseReasonRejectsAnythingOutsideTheClosedVocabulary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		reason expansion.Reason
		label  string
	}{
		{expansion.ReasonExpanded, "expanded"},
		{expansion.ReasonAuditMode, "audit_mode"},
		{expansion.ReasonNoAlias, "no_alias"},
		{expansion.ReasonNoSelectors, "no_selectors"},
		{expansion.ReasonArgsAbsent, "args_absent"},
		{expansion.ReasonPayloadNotObject, "payload_not_object"},
		{expansion.ReasonRootUnusable, "root_unusable"},
		{expansion.ReasonMappingInactive, "mapping_inactive"},
		{expansion.ReasonArgsUnparseable, "args_unparseable"},
		{expansion.ReasonMalformedReservedAlias, "malformed_reserved_alias"},
		{expansion.ReasonWorkspaceMismatch, "workspace_mismatch"},
		{expansion.ReasonExpandedTooLarge, "expanded_too_large"},
		{expansion.ReasonInvalidRewrite, "invalid_rewrite"},
	} {
		got, ok := expansion.ParseReason(tc.label)
		if !ok || got != tc.reason {
			t.Errorf("ParseReason(%q) = (%v, %v), want (%v, true)", tc.label, got, ok, tc.reason)
		}
	}
	for _, label := range []string{
		"",
		"unknown",
		"expanded ",
		"reason_expanded",
		"/home/dev/projects/aiproxer-example-project",
		".__aip_v1__/w_AAAAAAAAAAAAAAAAAAAA",
	} {
		if got, ok := expansion.ParseReason(label); ok || got != expansion.ReasonNone {
			t.Errorf("ParseReason(%q) = (%v, %v), want (ReasonNone, false)", label, got, ok)
		}
	}
}

func TestTheReportNeverRendersAnEnumAsAnOrdinal(t *testing.T) {
	t.Parallel()
	rendered := renderReport(t, expansion.Outcome(2), expansion.Reason(11))
	for _, ordinal := range []string{`"Outcome":2`, `"Outcome": 2`, `"Reason":11`, `"Reason": 11`} {
		if strings.Contains(rendered, ordinal) {
			t.Fatalf("an enum rendered as an ordinal, which is unbounded (requirements.md 7.7): %s", rendered)
		}
	}
}

func renderReport(t *testing.T, outcome expansion.Outcome, reason expansion.Reason) string {
	t.Helper()
	return renderReportWithRoot(t, outcome, reason, pathvirtualization.SkipReasonEmptyRoot)
}

func renderReportWithRoot(
	t *testing.T,
	outcome expansion.Outcome,
	reason expansion.Reason,
	root pathvirtualization.SkipReason,
) string {
	t.Helper()
	encoded, err := json.Marshal(expansion.Report{
		Outcome:    outcome,
		Reason:     reason,
		RootReason: root,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(encoded)
}

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
