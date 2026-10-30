package expansion_test

// This file pins the FEATURE-SIDE observable of the mandatory-buffer condition that
// requirements.md 7.6 asks to be counted.
//
// The condition is real and reachable, but not where a first reading puts it. The
// tool-call assembler owns the refusal: past the EFFECTIVE assembly bound it drops the
// buffer and returns a runtime MandatoryBufferingError without invoking any finalizer,
// so this pass never runs for those calls and cannot report them. What it CAN observe
// is the neighbouring case, and the neighbouring case is not rare: the effective bound
// is the MAXIMUM over every declaring finalizer, never the minimum
// (internal/core/runtime/tool_call_mandatory_buffering.go), so a deployment whose
// repair pass declares a larger bound than this one hands every pass a completed call
// that may exceed the bound THIS pass declared. That call is assembled, reaches
// Finalize, and is decided normally - the assembler refusal did not happen - but the
// completeness this pass declared did not hold for it either.
//
// Reporting that is the honest half of 7.6's counter. Claiming the assembler's own
// refusal here would misstate where the decision is made, and staying silent would
// leave an operator who lowered `mandatory_max_args_bytes` unable to discover that the
// effective bound is not what they configured.
//
// The observation is deliberately a BOOLEAN rather than a length. A length is a number
// derived from a payload, which is one step away from the content requirements.md 7.7
// forbids, and it would also be a second unbounded series. "This call was past the bound
// I declared" is the whole fact, and the count of such calls is the counter.

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// overflowFinalizer builds one pass declaring bound, recording into out when it is
// non-nil. The shared form is what lets the flag-protocol half compare a reporting pass
// against a silent one on identical construction.
func overflowFinalizer(t *testing.T, bound int, out *[]expansion.Report) *expansion.Finalizer {
	t.Helper()
	opts := []expansion.Option{}
	if out != nil {
		opts = append(opts, expansion.WithReporter(func(r expansion.Report) { *out = append(*out, r) }))
	}
	fin, err := expansion.NewFinalizer(nil, rewrite.ModeRewrite,
		expansion.Policy{MandatoryMaxArgsBytes: bound}, opts...)
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	return fin
}

// overflowingCall builds a completed call whose arguments exceed bound bytes, with a
// path-bearing field so the pass also exercises its ordinary selection path.
func overflowingCall(bound int) toolcall.CompletedCall {
	pad := make([]byte, bound)
	for i := range pad {
		pad[i] = 'a'
	}
	return toolcall.CompletedCall{
		ToolCallID: "call_overflow",
		ToolName:   "read_file",
		ArgsJSON:   append([]byte(`{"file_path":"/home/dev/projects/x","pad":"`), append(pad, []byte(`"}`)...)...),
	}
}

var overflowTool = aipapi.ToolDef{Name: "read_file"}

// TestTheFinalizerReportsACallPastItsOwnDeclaredBound is the positive case, and it
// also pins that the DECISION is unchanged: this is an observation, not a refusal, so
// requirement 4.5's fail-closed answer stays where it belongs, with the assembler.
func TestTheFinalizerReportsACallPastItsOwnDeclaredBound(t *testing.T) {
	t.Parallel()
	bound := toolcall.MinMandatoryMaxArgsBytes
	var reports []expansion.Report
	fin := overflowFinalizer(t, bound, &reports)

	result, err := fin.Finalize(t.Context(), overflowingCall(bound+16), overflowTool, nil, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if result.ArgsJSON != nil {
		t.Fatalf("the fixture must not produce a publication, got %q", result.ArgsJSON)
	}
	if len(reports) != 1 {
		t.Fatalf("recorded %d reports, want exactly 1", len(reports))
	}
	if !reports[0].ArgsOverDeclaredBound {
		t.Fatalf("a completed call past the declared bound did not report the condition: %+v", reports[0])
	}
	// A boolean, so the export cannot carry a length, a path, or an argument byte.
	rendered, marshalErr := json.Marshal(reports[0])
	if marshalErr != nil {
		t.Fatalf("marshal report: %v", marshalErr)
	}
	var decoded struct {
		Oversize *bool `json:"ArgsOverDeclaredBound"`
	}
	if unmarshalErr := json.Unmarshal(rendered, &decoded); unmarshalErr != nil {
		t.Fatalf("decode %s: %v", rendered, unmarshalErr)
	}
	if decoded.Oversize == nil || !*decoded.Oversize {
		t.Fatalf("the observation must render as a true boolean field: %s", rendered)
	}
}

// TestTheFinalizerDoesNotReportTheOversizeConditionForAnInBoundCall is the negative
// case, and it is what keeps the counter meaningful: a counter that always reads true
// is indistinguishable from no counter at all.
func TestTheFinalizerDoesNotReportTheOversizeConditionForAnInBoundCall(t *testing.T) {
	t.Parallel()
	bound := toolcall.MinMandatoryMaxArgsBytes
	var reports []expansion.Report
	fin := overflowFinalizer(t, bound, &reports)

	if _, err := fin.Finalize(t.Context(), overflowingCall(16), overflowTool, nil, toolcall.Meta{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("recorded %d reports, want exactly 1", len(reports))
	}
	if reports[0].ArgsOverDeclaredBound {
		t.Fatalf("an in-bound call reported the oversize condition: %+v", reports[0])
	}
}

// TestTheOversizeObservationNeverChangesTheDecision is the flag-protocol half. Two
// passes differing ONLY in whether they report are compared on the published result, so
// the new field cannot acquire a behavioral meaning later.
func TestTheOversizeObservationNeverChangesTheDecision(t *testing.T) {
	t.Parallel()
	bound := toolcall.MinMandatoryMaxArgsBytes
	call := overflowingCall(bound + 16)

	silent := overflowFinalizer(t, bound, nil)
	var reports []expansion.Report
	observed := overflowFinalizer(t, bound, &reports)

	silentResult, silentErr := silent.Finalize(t.Context(), call, overflowTool, nil, toolcall.Meta{})
	observedResult, observedErr := observed.Finalize(t.Context(), call, overflowTool, nil, toolcall.Meta{})
	if silentErr != nil || observedErr != nil {
		t.Fatalf("errors differ: silent=%v observed=%v", silentErr, observedErr)
	}
	silentJSON, marshalErr := json.Marshal(silentResult)
	if marshalErr != nil {
		t.Fatalf("marshal silent result: %v", marshalErr)
	}
	observedJSON, marshalErr := json.Marshal(observedResult)
	if marshalErr != nil {
		t.Fatalf("marshal observed result: %v", marshalErr)
	}
	if string(silentJSON) != string(observedJSON) {
		t.Fatalf("the reporter changed the published result: silent=%s observed=%s",
			silentJSON, observedJSON)
	}
	if len(reports) != 1 || !reports[0].ArgsOverDeclaredBound {
		t.Fatalf("the observing pass did not record the condition: %+v", reports)
	}
}

// TestANilFinalizerReportsNoOversizeCondition keeps the nil receiver honest: the
// inactive pass decides nothing, so it has nothing to observe either, even for a call
// past the bound the active pass declares. The result it publishes is the whole
// statement: pass through, no document, and the no-selectors reason.
func TestANilFinalizerReportsNoOversizeCondition(t *testing.T) {
	t.Parallel()

	var nilPass *expansion.Finalizer
	result, err := nilPass.Finalize(t.Context(), overflowingCall(1<<20), overflowTool, nil, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if result.Action != toolcall.ActionPass {
		t.Fatalf("action = %v, want ActionPass from an inactive pass", result.Action)
	}
	if result.ArgsJSON != nil {
		t.Fatalf("an inactive pass published a document: %q", result.ArgsJSON)
	}
	if result.ReasonCode != "no_selectors" {
		t.Fatalf("reason = %q, want the inactive reason %q", result.ReasonCode, "no_selectors")
	}
}
