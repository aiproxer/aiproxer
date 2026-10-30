package outbound

// This file owns the two rows of the b-leg-path-virtualization FAILURE POLICY MATRIX that
// no runtime turn can reach, and it is the other half of
// internal/core/runtime/path_virtualization_failure_policy_matrix_test.go.
//
// Requirements 8.2 and 8.3. Design sections: "Error Handling" ("no pinned workspace view:
// nothing is published, the real path survives, and a bounded reason is recorded";
// "unexpected outbound transformation failure before alias exposure: fail open to the real
// path with bounded diagnostics") and "Testing Strategy / Runtime-integration".
//
// WHY THESE TWO ROWS LIVE HERE AND NOT ONLY IN THE RUNTIME TABLE
//
// The matrix is ONE table and it lives in the runtime file, because a policy a reader has
// to reconstruct from six files is not a policy. Two of its rows are driven here, and the
// reason is structural rather than a matter of convenience:
//
//   - an ABSENT pinned workspace view ([OutcomeWorkspaceUnresolved]). The runtime always
//     projects one: it deliberately projects an EMPTY view on the detached auxiliary path
//     precisely so both passes have an authority to read and refuse, and requirement 1.8
//     then decides that an empty root is unusable. There is no turn in which the late pass
//     finds no authority at all, so producing that condition means handing the shipped pass
//     a context that never went through the projection.
//   - an UNEXPECTED transformation error ([OutcomeTransformationFailed]). The shared
//     rewriter's error path is documented as unreachable from untrusted input - it can only
//     be a disagreement between two decoders over bytes that already decoded as one valid
//     JSON value - and the pass reaches it only through its declared rewriter port, which
//     is package private by design.
//
// Neither is worth a synthetic end-to-end harness, and a second end-to-end harness is
// exactly what this package's test discipline forbids. So both conditions are driven on
// the REAL shipped passes, and the runtime table's rows for them carry the same bounded
// reason read from the same production enum, which is what keeps the two halves from
// drifting.
//
// WHAT IS ADDED ON TOP OF THE EXISTING PER-PASS PROBES
//
// attempt_internal_test.go and part_internal_test.go already prove each pass's own
// fail-open branch. What a matrix ROW has to pin, and a single pass's probe cannot, is:
//
//   - both passes on the SAME input and BOTH canonical authorities, so the row says the
//     outbound DIRECTION fails open rather than one pass happening to;
//   - that no partially virtualized document escapes on either surface. Each pass's own
//     probe offers one partially rewritten publication; here the failure is offered on a
//     call carrying two surfaces in BOTH authorities at once, which is the shape that would
//     put a real path and a virtual alias on one backend-bound request;
//   - that the shared engine reports no statistics for work it never published, so a
//     failed turn can never read as a successful rewrite in requirements.md 7.6's counters;
//   - and, for the absent-authority row, that the two passes do not CONFLATE their two
//     different "no mapping" facts under one label.
//
// CONTENT FREEDOM
//
// No failure message here contains a path, an alias, a workspace tag, a tool-call ID, a
// tool name, or argument bytes. Documents are compared and never printed; messages carry
// requirement numbers, bounded reason labels, occurrence counts, and byte totals only.
//
// DETERMINISM
//
// One fixed project root, one content-defined mapping derivation, one fixed partially
// rewritten publication, no map iteration on any compared surface, and no clock or RNG.

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// matrixMarker is the frozen V1 reserved namespace marker, spelled as a LITERAL for the
// same reason the runtime table spells it: a leak detector that called the production
// recognizer could not detect a leak that recognizer fails to see.
const matrixMarker = ".__aip_v1__"

// matrixVirtualPath is one fully virtualized path, used to build the partial publication
// the injected failure offers. It is never formatted into a message.
const matrixVirtualPath = "/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"

// matrixCase is one row of this half of the matrix. It mirrors the runtime table's row
// shape deliberately - the same facts under the same names - so a reader moving between
// the two halves is reading one table.
type matrixCase struct {
	// label is a bounded, content-free failure-message token and the row's identity.
	label string
	// direction is the policy the row pins. Every row in this file is fail-open.
	direction string
	// reason is the BOUNDED label the row requires, spelled as the production outcome
	// enum publishes it.
	reason string
	// drive runs the row over the real shipped passes.
	drive func(t *testing.T, tc matrixCase)
}

// matrixCases is this half of the failure-policy matrix.
//
// Every label here is ALSO a row label in the runtime table, whose driver names this file's
// test as the row's owner. That reciprocal naming is what keeps one policy from becoming
// two that drift.
func matrixCases() []matrixCase {
	return []matrixCase{
		{
			label:     "no_pinned_workspace_view_at_the_late_pass",
			direction: "fail-open",
			reason:    OutcomeWorkspaceUnresolved.String(),
			drive:     matrixDriveWorkspaceUnresolved,
		},
		{
			label:     "unexpected_outbound_transformation_error",
			direction: "fail-open",
			reason:    OutcomeTransformationFailed.String(),
			drive:     matrixDriveTransformError,
		},
	}
}

// matrixSurfaceCount is how many canonical surfaces every row in this file drives.
//
// It is a constant rather than a value read back from a fixture so that a row cannot pass by
// driving fewer surfaces than the claim needs: the count is what makes "the outbound
// DIRECTION fails open" a statement about both authorities.
const matrixSurfaceCount = 2

// matrixRecording binds one shipped pass to its own report sink.
//
// It exists because the two passes are separate types with separate fields and separate
// reporters BY DESIGN, so reading "the report" from a shared sink would answer a question
// about whichever pass happened to report last.
type matrixRecording struct {
	early *reports
	late  *reports
}

// reports returns the bounded reports one pass emitted over `invocations` surfaces.
//
// Every surface is driven separately, so a pass records once per surface. The count is
// therefore CHECKED rather than assumed, and every record must agree: a pass that answered
// differently for the two canonical authorities would satisfy a row that only read the first
// one, and requirements.md 8.2's claim is about the direction, not about one surface.
func (r matrixRecording) reports(t *testing.T, pass string, invocations int) Report {
	t.Helper()
	var sink *reports
	switch pass {
	case "early":
		sink = r.early
	case "late":
		sink = r.late
	default:
		t.Fatalf("fixture: unknown pass name %q", pass)
		return Report{}
	}
	seen := sink.all()
	if len(seen) != invocations {
		t.Fatalf("fixture: %s: %s must record exactly once per driven surface: reports=%d invocations=%d",
			"matrix", pass, len(seen), invocations)
	}
	for i, report := range seen {
		if report.Outcome != seen[0].Outcome || !reflect.DeepEqual(report.Stats, seen[0].Stats) {
			t.Fatalf("requirements.md 8.2 - %s: %s must answer the same way on every canonical authority: report=%d outcome=%q reports=%d",
				"matrix", pass, i, report.Outcome.String(), len(seen))
		}
	}
	return seen[0]
}

// matrixAssertRowOutcome is the row's bounded-reason assertion, shared by both rows.
func matrixAssertRowOutcome(t *testing.T, tc matrixCase, pass string, got Report) {
	t.Helper()
	if got.Outcome.String() != tc.reason {
		t.Fatalf("requirements.md 8.2 - %s: %s must record this row's bounded outcome: recorded=%q want=%q",
			tc.label, pass, got.Outcome.String(), tc.reason)
	}
	// The statistics describe work the pass never published, so reporting them would make
	// a failed turn read as a successful rewrite in requirements.md 7.6's counters.
	if !reflect.DeepEqual(got.Stats, rewrite.Stats{}) {
		t.Fatalf("requirements.md 8.2 - %s: %s must report no statistics it never earned: eligible=%d rewritten=%d bytes_before=%d bytes_after=%d",
			tc.label, pass, got.Stats.Eligible, got.Stats.Rewritten, got.Stats.BytesBefore, got.Stats.BytesAfter)
	}
}

// matrixAssertUntouched is the row's whole payload-level claim over one surface: the real
// path survives byte for byte, no reserved namespace was minted anywhere, and the call is
// still canonical.
func matrixAssertUntouched(t *testing.T, tc matrixCase, surface string, before []byte, call *aipapi.Call) {
	t.Helper()
	after, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("fixture: %s: marshal the %s call after the pass: %v", tc.label, surface, err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("requirements.md 8.2 - %s: the real path must survive byte for byte on the %s surface; a partially virtualized document escaped: before_bytes=%d after_bytes=%d",
			tc.label, surface, len(before), len(after))
	}
	if bytes.Contains(after, []byte(matrixMarker)) {
		t.Fatalf("requirements.md 8.2 - %s: no reserved namespace may reach the backend on the %s surface when the pass published nothing: after_bytes=%d",
			tc.label, surface, len(after))
	}
	// requirements.md 8.5's floor: a pass that touched nothing must leave a canonical call
	// canonical, on whichever authority it carried.
	if err := call.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - %s: the %s surface must still be canonical: error_type=%T",
			tc.label, surface, err)
	}
}

// matrixSurfaces is one canonical call per authority, with its pre-pass rendering.
//
// Both authorities are driven because design.md "4. Canonical Outbound Rewriter"
// discriminates on them, and a fail-open claim that holds on one and not the other is not a
// claim about the direction.
func matrixSurfaces(t *testing.T) []struct {
	surface string
	call    *aipapi.Call
	before  []byte
} {
	t.Helper()
	built := []struct {
		surface string
		call    *aipapi.Call
	}{
		{"item_authority", matrixItemCall()},
		{"legacy_authority", matrixLegacyCall()},
	}
	out := make([]struct {
		surface string
		call    *aipapi.Call
		before  []byte
	}, 0, len(built))
	for _, surface := range built {
		if err := surface.call.Validate(); err != nil {
			t.Fatalf("fixture: %s: the %s fixture must be canonical before the pass: error_type=%T",
				"matrix", surface.surface, err)
		}
		raw, err := json.Marshal(surface.call)
		if err != nil {
			t.Fatalf("fixture: %s: marshal the %s fixture: %v", "matrix", surface.surface, err)
		}
		out = append(out, struct {
			surface string
			call    *aipapi.Call
			before  []byte
		}{surface.surface, surface.call, raw})
	}
	return out
}

// matrixDriveTransformError drives the unexpected-error row over both shipped outbound
// passes and both canonical authorities.
//
// The publication the failure is offered is PARTIAL ON PURPOSE: it rewrites every selected
// member it can reach and leaves the rest alone, so a pass that published it would put both
// spellings of the client's filesystem on one backend-bound request. That is a strictly
// stronger claim than either pass's own probe makes, and it is the claim a reader of the
// matrix actually needs: "the turn does not carry an alias" means no surface of it does.
func matrixDriveTransformError(t *testing.T, tc matrixCase) {
	t.Helper()
	rec := matrixRecording{early: &reports{}, late: &reports{}}
	transform := NewAttemptTransform(rewrite.ModeRewrite, nil, WithReporter(rec.early.record))
	transform.bind = func(pathvirtualization.Mapping) virtualizer {
		return virtualizerFunc(matrixPartialPublication)
	}
	hook := NewRequestPartHook(rewrite.ModeRewrite, nil, WithHookReporter(rec.late.record))
	hook.bind = func(pathvirtualization.Mapping) virtualizer {
		return virtualizerFunc(matrixPartialPublication)
	}
	ctx := internalPinnedView(context.Background())

	for _, surface := range matrixSurfaces(t) {
		decision, err := transform.HandleAttempt(ctx, surface.call, request.AttemptMeta{
			Workspace: aipworkspace.WorkspaceView{ProjectRoot: internalRoot},
		}, request.Services{})
		if err != nil {
			t.Fatalf("requirements.md 8.2 - %s: the early pass must fail open rather than surface an error: surface=%s error_type=%T",
				tc.label, surface.surface, err)
		}
		// Failing open means the candidate CONTINUES: an error or an exclusion here would
		// turn an internal error in an optional optimization into a failed client request.
		if decision.Kind != request.AttemptContinue || decision.ReasonCode != "" {
			t.Fatalf("requirements.md 8.2 - %s: the early pass must continue with no reason code: surface=%s reason_code_bytes=%d",
				tc.label, surface.surface, len(decision.ReasonCode))
		}
		if err := hook.HandleRequestParts(ctx, surface.call, sdkhooks.PartMeta{}); err != nil {
			t.Fatalf("requirements.md 8.2 - %s: the late pass must fail open rather than surface an error: surface=%s error_type=%T",
				tc.label, surface.surface, err)
		}
		matrixAssertUntouched(t, tc, surface.surface, surface.before, surface.call)
	}
	matrixAssertRowOutcome(t, tc, "the early pass", rec.reports(t, "early", matrixSurfaceCount))
	matrixAssertRowOutcome(t, tc, "the late pass", rec.reports(t, "late", matrixSurfaceCount))
}

// matrixDriveWorkspaceUnresolved drives the absent-authority row.
//
// The context handed to the late pass carries NO workspace projection at all, which is what
// the runtime never does on any path and is therefore unreachable through a turn.
//
// It also pins the DISCRIMINATION the matrix's reason column depends on. The early pass
// reads the authority the runtime does project, finds a perfectly usable root, and records
// the routine outcome; reporting this row's absent-authority code there instead would be a
// false label, because the root was not unusable - the authority that would have supplied
// it was absent. Both facts are real, both are recorded, and they are different codes.
func matrixDriveWorkspaceUnresolved(t *testing.T, tc matrixCase) {
	t.Helper()
	rec := matrixRecording{early: &reports{}, late: &reports{}}
	transform := NewAttemptTransform(rewrite.ModeRewrite, nil, WithReporter(rec.early.record))
	hook := NewRequestPartHook(rewrite.ModeRewrite, nil, WithHookReporter(rec.late.record))
	// Deliberately NOT internalPinnedView: this context carries no projection at all.
	ctx := context.Background()

	for _, surface := range matrixSurfaces(t) {
		if _, err := transform.HandleAttempt(ctx, surface.call, request.AttemptMeta{
			Workspace: aipworkspace.WorkspaceView{ProjectRoot: internalRoot},
		}, request.Services{}); err != nil {
			t.Fatalf("requirements.md 8.2 - %s: the early pass must not surface an error: surface=%s error_type=%T",
				tc.label, surface.surface, err)
		}
		if err := hook.HandleRequestParts(ctx, surface.call, sdkhooks.PartMeta{}); err != nil {
			t.Fatalf("requirements.md 8.2 - %s: the late pass must fail open rather than surface an error: surface=%s error_type=%T",
				tc.label, surface.surface, err)
		}
		matrixAssertUntouched(t, tc, surface.surface, surface.before, surface.call)
	}
	matrixAssertRowOutcome(t, tc, "the late pass", rec.reports(t, "late", matrixSurfaceCount))

	// The early pass's answer is a DIFFERENT bounded label, and it must not be this row's.
	// A nil resolver is the shipped "no operator policy published" state: the pass runs,
	// selects nothing, publishes nothing, and records the routine outcome.
	if got := rec.reports(t, "early", matrixSurfaceCount); got.Outcome != OutcomeRewriterRan {
		t.Fatalf("requirements.md 8.2 - %s: the early pass reads a projected authority, so it must NOT report an absent-authority outcome: recorded=%q want=%q",
			tc.label, got.Outcome.String(), OutcomeRewriterRan.String())
	}
	if got := rec.reports(t, "early", matrixSurfaceCount); got.RootReason != pathvirtualization.SkipReasonNone {
		t.Fatalf("requirements.md 8.2 - %s: the early pass found a usable root, so it must report no root refusal: recorded=%q",
			tc.label, got.RootReason.String())
	}
}

// matrixPartialPublication is the failure both passes are offered: a partially virtualized
// call alongside an error, plus statistics describing work that was never published.
//
// The statistics matter as much as the partial call. A pass that reported them would make a
// failed turn look like a successful rewrite in requirements.md 7.6's counters, which is how
// an operator concludes the optimization is working when it did nothing.
func matrixPartialPublication(call *aipapi.Call) (*aipapi.Call, rewrite.Stats, error) {
	partial := aipapi.CloneCall(*call)
	for i := range partial.Items {
		if partial.Items[i].ToolCall != nil && len(partial.Items[i].ToolCall.Arguments) > 0 {
			partial.Items[i].ToolCall.Arguments = json.RawMessage(`{"file_path":"` + matrixVirtualPath + `"}`)
		}
	}
	for mi := range partial.Messages {
		for pi := range partial.Messages[mi].Parts {
			part := &partial.Messages[mi].Parts[pi]
			if part.Kind == aipapi.PartJSON && len(part.Content) > 0 {
				part.Content = json.RawMessage(`{"file_path":"` + matrixVirtualPath + `"}`)
			}
		}
	}
	return &partial, rewrite.Stats{Eligible: 9, Rewritten: 9, BytesBefore: 999, BytesAfter: 1}, errUnexpected
}

// matrixItemCall is one item-authoritative candidate carrying two path-bearing surfaces.
func matrixItemCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "matrix:m"},
		Tools: []aipapi.ToolDef{{Name: "read_file", Parameters: []byte(`{"type":"object"}`)}},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindToolCall,
				ID:     "matrix_item_one",
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "matrix_item_one_call",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":"` + internalTarget + `"}`),
				},
			},
			{
				Kind:   aipapi.ItemKindToolCall,
				ID:     "matrix_item_two",
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "matrix_item_two_call",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":"` + internalTarget + `.bak"}`),
				},
			},
		},
	}
}

// matrixLegacyCall is the same two path-bearing surfaces in legacy message authority.
func matrixLegacyCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "matrix:m"},
		Tools: []aipapi.ToolDef{{Name: "read_file", Parameters: []byte(`{"type":"object"}`)}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "matrix_legacy_one",
				ToolName:   "read_file",
				Content:    json.RawMessage(`{"file_path":"` + internalTarget + `"}`),
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("matrix-legacy-answer")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "matrix_legacy_two",
				ToolName:   "read_file",
				Content:    json.RawMessage(`{"file_path":"` + internalTarget + `.bak"}`),
			}}},
		},
	}
}

// TestOutboundPassFailureMatrixRowsUnreachableFromATurn drives this half of the
// requirements.md 8 failure-policy matrix.
//
// Each row asserts its whole claim over the real shipped passes: nothing published, the real
// path preserved byte for byte, no reserved namespace anywhere on the request, the call
// still canonical, and exactly one bounded reason recorded with no statistics attached.
//
// The table's shape is asserted before any row runs - both rows fail-open, both reasons are
// labels this package's own outcome enum publishes, every row is driven here, and no label
// repeats - so a row cannot be added, dropped, or silently re-pointed without the table
// saying so.
func TestOutboundPassFailureMatrixRowsUnreachableFromATurn(t *testing.T) {
	t.Parallel()

	cases := matrixCases()
	if len(cases) == 0 {
		t.Fatal("fixture: this half of the matrix must not be empty")
	}
	published := map[string]struct{}{
		OutcomeRewriterRan.String():          {},
		OutcomeProjectRootUnusable.String():  {},
		OutcomeTransformationFailed.String(): {},
		OutcomeWorkspaceUnresolved.String():  {},
		Outcome(255).String():                {},
	}
	seen := map[string]int{}
	for _, tc := range cases {
		seen[tc.label]++
		if tc.direction != "fail-open" {
			t.Fatalf("fixture: %s: every row in this file is an OUTBOUND fail-open row: direction=%q", tc.label, tc.direction)
		}
		if _, ok := published[tc.reason]; !ok {
			t.Fatalf("fixture: %s: the row must name a bounded label this package's outcome enum publishes: reason=%q", tc.label, tc.reason)
		}
		if tc.drive == nil {
			t.Fatalf("fixture: %s: every row in this file must be driven here", tc.label)
		}
	}
	for label, count := range seen {
		if count != 1 {
			t.Fatalf("fixture: a matrix row must appear exactly once: label=%q occurrences=%d", label, count)
		}
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			tc.drive(t, tc)
		})
	}
}
