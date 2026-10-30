package outbound

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

const (
	internalRoot   = "/home/dev/projects/aiproxer-example-project"
	internalTarget = internalRoot + "/pkg/aipapi/call.go"
)

// errUnexpected stands in for the one condition the shared rewriter's own contract
// reserves for a caller to handle: an internal transformation failure raised before
// publication. It is deliberately opaque: the pass must never surface or record its
// text.
var errUnexpected = errors.New("unexpected transformation failure")

// virtualizerFunc adapts a function to the pass's declared rewriter port.
type virtualizerFunc func(*aipapi.Call) (*aipapi.Call, rewrite.Stats, error)

func (f virtualizerFunc) RewriteCall(call *aipapi.Call) (*aipapi.Call, rewrite.Stats, error) {
	return f(call)
}

// reports collects the bounded reports one pass emits.
type reports struct {
	seen []Report
}

func (r *reports) record(report Report) { r.seen = append(r.seen, report) }

func (r *reports) only(t *testing.T) Report {
	t.Helper()
	if len(r.seen) != 1 {
		t.Fatalf("reports = %d, want exactly 1", len(r.seen))
	}
	return r.seen[0]
}

// all returns every report the sink collected, for a caller that drives one pass over
// several canonical surfaces and therefore needs to read the whole sequence rather than
// insist on a single record.
//
// The slice is a copy, so a caller cannot reach the sink's own storage.
func (r *reports) all() []Report { return append([]Report(nil), r.seen...) }

// internalCall builds one item-authoritative outgoing candidate carrying two
// path-bearing historical tool calls.
func internalCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "attempt:m"},
		Tools: []aipapi.ToolDef{{Name: "read_file", Parameters: []byte(`{"type":"object"}`)}},
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID: "call_7f3a", Name: "read_file",
					Arguments: json.RawMessage(`{"file_path":"` + internalTarget + `"}`),
				},
			},
			{
				Kind: aipapi.ItemKindToolCall, ID: "item_call_2", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID: "call_7f3b", Name: "read_file",
					Arguments: json.RawMessage(`{"file_path":"` + internalTarget + `.bak"}`),
				},
			},
		},
	}
}

func internalMeta() request.AttemptMeta {
	return request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ProjectRoot: internalRoot}}
}

// TestAttemptTransformFailsOpenOnUnexpectedTransformationError proves
// requirements.md 8.2 at the exact position design.md "Error Handling" names: an
// unexpected outbound transformation failure raised before any alias could become
// model-visible preserves the real path and records a bounded reason.
//
// The rewriter's own error path is documented as unreachable from untrusted input,
// so the branch is reached through the pass's declared rewriter port rather than
// through a payload no client can send. The failure deliberately hands back a
// PARTIALLY rewritten call, which makes this the strongest available proof: the
// pass must discard it, because publishing it would put a real path and a virtual
// alias in the same candidate.
func TestAttemptTransformFailsOpenOnUnexpectedTransformationError(t *testing.T) {
	t.Parallel()

	rec := &reports{}
	transform := NewAttemptTransform(rewrite.ModeRewrite, nil, WithReporter(rec.record))
	transform.bind = func(pathvirtualization.Mapping) virtualizer {
		return virtualizerFunc(func(call *aipapi.Call) (*aipapi.Call, rewrite.Stats, error) {
			partial := aipapi.CloneCall(*call)
			partial.Items[0].ToolCall.Arguments = json.RawMessage(`{"file_path":"/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"}`)
			return &partial, rewrite.Stats{Eligible: 9, Rewritten: 9, BytesBefore: 999, BytesAfter: 1}, errUnexpected
		})
	}

	call := internalCall()
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	decision, err := transform.HandleAttempt(t.Context(), call, internalMeta(), request.Services{})
	if err != nil {
		t.Fatalf("an unexpected outbound failure must fail open rather than surface an error: %v", err)
	}
	if decision.Kind != request.AttemptContinue || decision.ReasonCode != "" {
		t.Fatalf("decision = %+v, want continue with no reason code: a failure must never influence candidate choice", decision)
	}

	after, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal published call: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("requirements.md 8.2 - the real path must survive an unexpected outbound failure; a partially rewritten call escaped")
	}
	if strings.Contains(string(after), ".__aip_v1__") {
		t.Fatalf("requirements.md 8.2 - no alias may reach the backend when the pass failed")
	}

	report := rec.only(t)
	if report.Outcome != OutcomeTransformationFailed {
		t.Fatalf("report outcome = %v, want %v", report.Outcome, OutcomeTransformationFailed)
	}
	// The rewriter zeroes its statistics on failure; the pass must not invent them.
	if !reflect.DeepEqual(report.Stats, rewrite.Stats{}) {
		t.Fatalf("a failed pass must not report statistics it never earned: %+v", report.Stats)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{
		internalRoot, internalTarget, ".__aip_v1__", "w_aaaaaaaaaaaaaaaaaaaa", "read_file", "call_7f3a", errUnexpected.Error(),
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("requirements.md 7.7 - the bounded reason leaked content-bearing text")
		}
	}
}

// TestAttemptTransformBindsTheOnePureRewriter proves the pass runs the SAME canonical
// outbound rewriter every other outbound pass runs, in the mode it was built with.
// It binds the real default binder, reads the rewriter's own mode back, and proves
// audit publishes nothing while measuring exactly what rewrite publishes.
func TestAttemptTransformBindsTheOnePureRewriter(t *testing.T) {
	t.Parallel()

	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q", reject)
	}

	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			transform := NewAttemptTransform(mode, resolver)
			mapping, reason := pathvirtualization.DeriveMapping(internalRoot)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", internalRoot, reason)
			}
			bound := transform.bind(mapping)
			rewriter, ok := bound.(*rewrite.Rewriter)
			if !ok {
				t.Fatalf("the default binder must produce the shared canonical rewriter, got %T", bound)
			}
			if rewriter.Mode() != mode {
				t.Fatalf("bound rewriter mode = %v, want %v", rewriter.Mode(), mode)
			}

			call := internalCall()
			published, stats, err := rewriter.RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if stats.Rewritten != 2 {
				t.Fatalf("stats rewritten = %d, want 2", stats.Rewritten)
			}
			if mode == rewrite.ModeRewrite && published == call {
				t.Fatal("rewrite mode must publish a new call")
			}
			if mode == rewrite.ModeAudit && published != call {
				t.Fatal("audit mode must publish nothing")
			}
		})
	}
}

// TestAttemptTransformHoldsNoMutableAttemptState pins the pass's whole field set. A
// generation shares one instance across every request, so anything a per-attempt
// value could be written into is a data race waiting to happen and a way for one
// candidate's alias to leak into another's accounting.
func TestAttemptTransformHoldsNoMutableAttemptState(t *testing.T) {
	t.Parallel()

	transform := NewAttemptTransform(rewrite.ModeRewrite, nil, WithReporter(func(Report) {}))
	value := reflect.ValueOf(transform).Elem()
	if value.NumField() != 4 {
		names := make([]string, 0, value.NumField())
		for i := range value.NumField() {
			names = append(names, value.Type().Field(i).Name)
		}
		t.Fatalf("pass fields = %v, want exactly the rollout mode, the policy, the reporter, and the rewriter binder", names)
	}
	if transform.mode != rewrite.ModeRewrite || transform.resolver != nil || transform.report == nil || transform.bind == nil {
		t.Fatalf("unexpected pass configuration: mode=%v resolver_set=%t report_set=%t bind_set=%t",
			transform.mode, transform.resolver != nil, transform.report != nil, transform.bind != nil)
	}
}

// TestAttemptTransformNeverReadsCandidateIdentityFields is the structural half of
// requirements.md 5.6 and 5.7: the pass reads the workspace projection and nothing
// else from the attempt metadata, so no mapping can be keyed to a B-leg ID,
// provider ID, model ID, retry ordinal, candidate key, or trace ID.
func TestAttemptTransformNeverReadsCandidateIdentityFields(t *testing.T) {
	t.Parallel()

	used := handleAttemptFieldUses(t)
	for _, forbidden := range []string{
		"TraceID", "ALegID", "CandidateKey", "BackendID", "BackendPrefixes", "Model", "ReplaySupport",
	} {
		if used[forbidden] {
			t.Fatalf("requirements.md 5.7 - HandleAttempt must not read %s; no mapping may be keyed to it", forbidden)
		}
	}
	if !used["Workspace"] {
		t.Fatal("HandleAttempt must read the Workspace projection; the project root is the only mapping authority")
	}
	if used["ProjectRoot"] == false {
		t.Fatal("HandleAttempt must derive the mapping from Workspace.ProjectRoot")
	}
}

// handleAttemptFieldUses reports which AttemptMeta/workspace projection fields the
// pass actually reads, by walking the method body of the real implementation.
//
// The walk is by field name on purpose: the fields it forbids are exactly the
// identity surfaces requirements.md 5.7 names, so a rename of one of them is itself
// a spec change and should fail loudly rather than silently pass.
func handleAttemptFieldUses(t *testing.T) map[string]bool {
	t.Helper()

	used := map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "attempt.go", nil, 0)
	if err != nil {
		t.Fatalf("parse attempt.go: %v", err)
	}
	var found bool
	ast.Inspect(file, func(node ast.Node) bool {
		method, ok := node.(*ast.FuncDecl)
		if !ok || method.Name.Name != "HandleAttempt" {
			return true
		}
		found = true
		ast.Inspect(method.Body, func(inner ast.Node) bool {
			selector, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			used[selector.Sel.Name] = true
			return true
		})
		return false
	})
	if !found {
		t.Fatal("HandleAttempt not found in attempt.go")
	}
	return used
}
