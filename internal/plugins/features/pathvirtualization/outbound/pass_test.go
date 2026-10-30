package outbound_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 7.6, 7.7 and 9.5.
//
// This file pins the PASS dimension of one outbound report: which of the feature's two
// outbound passes produced it.
//
// The dimension exists because a generation-wide total cannot answer requirement 9.5 on its
// own. In production both outbound passes observe ONE candidate: a REWRITE publishes the
// alias, so the late pass finds no real-root prefix and measures nothing, while an AUDIT
// publishes nothing, so the late pass finds exactly what the early pass found and measures
// the same figure again. The measured consequence was that an audit deployment's
// generation total was exactly double the rewrite deployment's, with nothing in the
// projection saying so. Stamping each report with its own pass makes the per-candidate
// figure recoverable instead of merely summed.
//
// Three properties are pinned here, at the boundary the two passes share:
//
//	EACH SHIPPED PASS STAMPS ITSELF. The dimension is only useful if it cannot be wrong by
//	    omission, so every outcome the attempt transform and the request-part hook can
//	    report - including the three that carry no statistics at all - carries its own
//	    pass. The unattributed value exists to make an omission VISIBLE rather than to be
//	    a convenient default.
//	THE ENUM IS TOTAL AND CONTENT-FREE. Every ordinal renders a bounded label from a
//	    closed set, including one this build does not define, so a pass added later
//	    renders a label rather than a number or a name.
//	THE REPORT NEVER RENDERS AN ENUM AS AN ORDINAL. That is the sibling render
//	    guarantee, re-asserted for the new field because a new enum field is exactly
//	    where a missing text marshaler hides.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// passRoot is a synthetic deep POSIX root whose derived alias is strictly shorter, so the
// mapping is ACTIVE and every pass reaches the rewriter.
const passRoot = "/synthetic/build-agent/workspaces/aiproxer-example-project/monorepo" +
	"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-pass-dimension-fixture"

// passLabels is the closed set the pass dimension may render. Every label is asserted to
// be one of these, so a label assembled from anything else fails.
var passLabels = map[string]bool{
	"unattributed": true,
	"attempt":      true,
	"request_part": true,
	"unknown":      true,
}

// captureReports returns a reporter and the slice it appends to.
func captureReports() (outbound.Reporter, *[]outbound.Report) {
	reports := &[]outbound.Report{}
	return func(report outbound.Report) { *reports = append(*reports, report) }, reports
}

// passCall builds one canonical item-authoritative call carrying a single eligible
// tool-call occurrence, so a pass that runs the rewriter has something to report.
func passCall(t *testing.T) *aipapi.Call {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(passRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("the fixture root must derive an active alias: reason %v", reason)
	}
	call := &aipapi.Call{
		ID: "call_pass_dimension",
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindToolCall,
			ID:     "item_call",
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call_pass_dimension",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":"` + passRoot + `/pkg/file.go"}`),
			},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the fixture call must be canonical: %v", err)
	}
	return call
}

// TestEachShippedOutboundPassStampsItsOwnPass is the substantive property: the dimension
// is set by the pass that produced the report, on every outcome it can report.
//
// The fail-open transformation-failure outcome is NOT here: that branch is reachable only
// through the injection point the package keeps unexported, so the sibling white-box suites
// cover it and a case here would either assert nothing or need an export.
func TestEachShippedOutboundPassStampsItsOwnPass(t *testing.T) {
	t.Parallel()

	t.Run("the_attempt_transform", func(t *testing.T) {
		t.Parallel()
		for _, fixture := range []struct {
			label string
			root  string
		}{
			{label: "rewriter_ran", root: passRoot},
			{label: "project_root_unusable", root: ""},
		} {
			reporter, reports := captureReports()
			transform := outbound.NewAttemptTransform(rewrite.ModeRewrite, nil, outbound.WithReporter(reporter))
			decision, err := transform.HandleAttempt(context.Background(), passCall(t),
				request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ProjectRoot: fixture.root}},
				request.Services{})
			if err != nil {
				t.Fatalf("%s: the early pass must never return an error", fixture.label)
			}
			if decision.Kind != request.AttemptContinue {
				t.Fatalf("%s: the early pass must continue the candidate", fixture.label)
			}
			if len(*reports) != 1 {
				t.Fatalf("%s: the pass recorded %d reports, want 1", fixture.label, len(*reports))
			}
			if got := (*reports)[0].Pass; got != outbound.PassAttempt {
				t.Errorf("%s: pass = %v, want PassAttempt", fixture.label, got)
			}
		}
	})

	t.Run("the_request_part_hook", func(t *testing.T) {
		t.Parallel()
		for _, fixture := range []struct {
			label  string
			pinned bool
			root   string
		}{
			{label: "rewriter_ran", pinned: true, root: passRoot},
			{label: "workspace_unresolved"},
			{label: "root_unusable", pinned: true},
		} {
			reporter, reports := captureReports()
			hook := outbound.NewRequestPartHook(rewrite.ModeRewrite, nil, outbound.WithHookReporter(reporter))
			ctx := context.Background()
			if fixture.pinned {
				ctx = aipworkspace.WithWorkspaceView(ctx, aipworkspace.WorkspaceView{ProjectRoot: fixture.root})
			}
			if err := hook.HandleRequestParts(ctx, passCall(t), sdkhooks.PartMeta{}); err != nil {
				t.Fatalf("%s: the late pass must never return an error", fixture.label)
			}
			if len(*reports) != 1 {
				t.Fatalf("%s: the pass recorded %d reports, want 1", fixture.label, len(*reports))
			}
			if got := (*reports)[0].Pass; got != outbound.PassRequestPart {
				t.Errorf("%s: pass = %v, want PassRequestPart", fixture.label, got)
			}
		}
	})
}

// TestThePassEnumIsTotalAndBounded pins the vocabulary rather than the passes that use it.
//
// The series has to stay countable for a value this build does not define, which is what
// makes the dimension safe to add a member to.
func TestThePassEnumIsTotalAndBounded(t *testing.T) {
	t.Parallel()
	// Four ordinals: the unattributed zero value, the two shipped passes, and one past the
	// end of the vocabulary.
	for ordinal := range 4 {
		pass := outbound.Pass(ordinal)
		label := pass.String()
		if !passLabels[label] {
			t.Errorf("Pass(%d) rendered %q, which is outside the closed label set", ordinal, label)
		}
		text, err := pass.MarshalText()
		if err != nil {
			t.Fatalf("Pass(%d).MarshalText: %v", ordinal, err)
		}
		if string(text) != label {
			t.Errorf("Pass(%d) marshalled as %q but renders as %q", ordinal, text, label)
		}
	}
	if got := outbound.Pass(0xFF).String(); got != "unknown" {
		t.Errorf("an out-of-vocabulary pass rendered %q, want the bounded %q", got, "unknown")
	}
	if got := outbound.PassUnattributed.String(); got != "unattributed" {
		t.Errorf("the zero value rendered %q, want %q", got, "unattributed")
	}
	// The two shipped passes must be distinguishable, or the dimension would collapse into
	// the single figure it exists to break apart.
	if outbound.PassAttempt == outbound.PassRequestPart {
		t.Error("the two shipped passes share one ordinal")
	}
}

// TestTheReportRendersThePassAsABoundedLabel is the render guarantee for the new field.
//
// The sibling test asserts no enum renders as an ordinal; this asserts the new field is
// actually PRESENT and is a label, because a field that silently encoded as 0 would satisfy
// that negative assertion perfectly while telling an operator nothing.
func TestTheReportRendersThePassAsABoundedLabel(t *testing.T) {
	t.Parallel()
	for _, pass := range []outbound.Pass{outbound.PassAttempt, outbound.PassRequestPart} {
		encoded, err := json.Marshal(outbound.Report{
			Pass:    pass,
			Outcome: outbound.OutcomeRewriterRan,
			Stats:   rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 40, BytesAfter: 10},
		})
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		// The rendered member name is the Go field name, exactly as the sibling
		// [outbound.Report] fields render - this type carries no struct tags, and adding
		// one for one field would make the export's shape inconsistent.
		value, present := decoded["Pass"]
		if !present {
			t.Fatalf("the rendered report has no Pass field: %s", encoded)
		}
		text, isText := value.(string)
		if !isText {
			t.Fatalf("pass rendered as %T, want the bounded string %q", value, value)
		}
		if text != pass.String() {
			t.Errorf("pass rendered as %q, want %q", text, pass.String())
		}
		if strings.ContainsAny(text, "0123456789") {
			t.Errorf("the rendered pass label %q reads as an ordinal", text)
		}
	}
}
