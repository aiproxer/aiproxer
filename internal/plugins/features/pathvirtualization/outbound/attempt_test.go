package outbound_test

import (
	"context"
	"encoding/json"
	"reflect"
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

// The fixture project root is long enough that the fixed V1 alias is strictly
// shorter than the real root, which is the condition for an active mapping
// (requirements.md 1.4). The expected alias is spelled out literally rather than
// recomputed, so a drift in the tag algorithm cannot make these tests agree with a
// wrong implementation.
const (
	attemptRoot    = "/home/dev/projects/aiproxer-example-project"
	attemptAlias   = "/.__aip_v1__/w_hr7xnivm5at2ku356qya/"
	attemptSuffix  = "pkg/aipapi/call.go"
	attemptTarget  = attemptRoot + "/" + attemptSuffix
	attemptVirtual = attemptAlias + attemptSuffix
	attemptTool    = "read_file"
	attemptCallID  = "call_7f3a"
)

// attemptMeta is the authoritative view a deployment publishes for the fixture
// root. The workspace projection is the ONLY project-root authority this pass is
// allowed to read (requirements.md 5.7).
func attemptMeta() request.AttemptMeta {
	return request.AttemptMeta{
		Workspace: aipworkspace.WorkspaceView{
			ID:          "ws_fixture",
			ProjectRoot: attemptRoot,
			DirtyTree:   true,
			Markers:     []string{"go"},
			Labels:      map[string]string{"team": "core"},
		},
	}
}

// attemptWorkspace is a readability alias for the common fixture metadata.
func attemptWorkspace() request.AttemptMeta { return attemptMeta() }

// attemptResolver is the policy a deployment gets with no operator configuration:
// the shipped conservative built-in profile layer, which claims /file_path on the
// canonical coding-agent file tools.
func attemptResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles(builtin) reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver(builtin) reject = %q, want none", reject)
	}
	return resolver
}

// attemptRecorder collects the content-free reports one transform pass emits.
type attemptRecorder struct {
	reports []outbound.Report
}

func (r *attemptRecorder) record(report outbound.Report) { r.reports = append(r.reports, report) }

func (r *attemptRecorder) only(t *testing.T) outbound.Report {
	t.Helper()
	if len(r.reports) != 1 {
		t.Fatalf("reports = %d, want exactly 1: one attempt transform pass emits one bounded report", len(r.reports))
	}
	return r.reports[0]
}

// attemptRewriteTransform builds the pass under test in rewrite mode.
func attemptRewriteTransform(t *testing.T, opts ...outbound.Option) *outbound.AttemptTransform {
	t.Helper()
	return outbound.NewAttemptTransform(rewrite.ModeRewrite, attemptResolver(t), opts...)
}

// attemptLegacyCall builds the legacy message-part authority: one historical
// path-bearing tool call plus an opaque tool result that no shipped profile
// marks path-oriented.
func attemptLegacyCall(arguments string) *aipapi.Call {
	return &aipapi.Call{
		ID:             "call_attempt",
		Route:          aipapi.RouteIntent{Selector: "attempt:m"},
		PromptCacheKey: "attempt-cache-key",
		Tools: []aipapi.ToolDef{{
			Name:       attemptTool,
			Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("open " + attemptTarget)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: attemptCallID,
				ToolName:   attemptTool,
				Content:    json.RawMessage(arguments),
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: attemptCallID,
				ToolName:   attemptTool,
				Content:    json.RawMessage(`{"paths":["` + attemptTarget + `"]}`),
				Text:       "read " + attemptTarget,
			}}},
		},
	}
}

// attemptItemCall builds the item authority carrying the same historical tool call
// plus the same opaque tool result, so both authorities are exercised over the same
// two surface kinds.
func attemptItemCall(arguments string) *aipapi.Call {
	return &aipapi.Call{
		ID:             "call_attempt",
		Route:          aipapi.RouteIntent{Selector: "attempt:m"},
		PromptCacheKey: "attempt-cache-key",
		Tools: []aipapi.ToolDef{{
			Name:       attemptTool,
			Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, ID: "item_user", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "open " + attemptTarget},
			}},
			{
				Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{CallID: attemptCallID, Name: attemptTool, Arguments: json.RawMessage(arguments)},
			},
			{
				Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{CallID: attemptCallID, Name: attemptTool, Output: "read " + attemptTarget},
			},
		},
	}
}

// attemptPathArguments is one selected path-bearing argument document with a
// payload-concept sibling no shipped profile claims, plus a non-path sibling that
// must survive byte-for-byte (requirements.md 2.4, 2.8).
const attemptPathArguments = `{"file_path":"` + attemptTarget + `","content":"literal ` + attemptRoot + ` text","limit":10,"empty":null}`

// attemptMarshal renders a call for a byte-identity comparison.
func attemptMarshal(t *testing.T, call aipapi.Call) string {
	t.Helper()
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal call: %v", err)
	}
	return string(encoded)
}

// attemptContinue asserts the decision this pass is required to return: a candidate
// is never excluded and a reason code is never invented (requirements.md 5.5).
func attemptContinue(t *testing.T, decision request.AttemptDecision) {
	t.Helper()
	if decision.Kind != request.AttemptContinue {
		t.Fatalf("decision kind = %q, want %q: this feature must not influence candidate choice", decision.Kind, request.AttemptContinue)
	}
	if decision.ReasonCode != "" {
		t.Fatalf("decision reason code = %q, want empty: an exclusion reason is never produced by this pass", decision.ReasonCode)
	}
}

// attemptSkipCount returns the recorded count for one bounded rewriter reason.
func attemptSkipCount(stats rewrite.Stats, reason rewrite.SkipReason) int {
	for _, skip := range stats.Skips {
		if skip.Reason == reason {
			return skip.Count
		}
	}
	return 0
}

// attemptEnvelopeUnchanged proves the pass rewrote no envelope field: route
// identity, session, tools, options, and invocation are the routing/authority
// surfaces requirements.md 5.5 and 5.7 put out of this feature's reach.
func attemptEnvelopeUnchanged(t *testing.T, before, after aipapi.Call) {
	t.Helper()
	if !reflect.DeepEqual(before.ID, after.ID) {
		t.Errorf("call ID changed: %q -> %q", before.ID, after.ID)
	}
	if !reflect.DeepEqual(before.Route, after.Route) {
		t.Errorf("route identity changed: %+v -> %+v", before.Route, after.Route)
	}
	if !reflect.DeepEqual(before.Session, after.Session) {
		t.Errorf("session changed: %+v -> %+v", before.Session, after.Session)
	}
	if !reflect.DeepEqual(before.Instructions, after.Instructions) {
		t.Error("instructions changed: ordinary user/system content is never rewritten (requirements.md 2.7)")
	}
	if !reflect.DeepEqual(before.Tools, after.Tools) {
		t.Error("tool declarations changed")
	}
	if !reflect.DeepEqual(before.ToolChoice, after.ToolChoice) {
		t.Errorf("tool choice changed: %+v -> %+v", before.ToolChoice, after.ToolChoice)
	}
	if !reflect.DeepEqual(before.Options, after.Options) {
		t.Errorf("generation options changed: %+v -> %+v", before.Options, after.Options)
	}
	if !reflect.DeepEqual(before.Invocation, after.Invocation) {
		t.Errorf("invocation changed: %+v -> %+v", before.Invocation, after.Invocation)
	}
	if !reflect.DeepEqual(before.PromptCacheKey, after.PromptCacheKey) {
		t.Error("prompt cache key changed")
	}
	if !reflect.DeepEqual(before.Extensions, after.Extensions) {
		t.Error("canonical extensions changed")
	}
	if before.PreviousResponseID != after.PreviousResponseID {
		t.Error("continuation parent changed")
	}
}

// attemptMessagesUnchangedExcept proves that every legacy message part other than
// the named tool-call argument documents kept every byte it arrived with.
func attemptMessagesUnchangedExcept(t *testing.T, before, after aipapi.Call, selected map[[2]int]bool) {
	t.Helper()
	if len(before.Messages) != len(after.Messages) {
		t.Fatalf("message count changed: %d -> %d", len(before.Messages), len(after.Messages))
	}
	for i := range before.Messages {
		if before.Messages[i].Role != after.Messages[i].Role {
			t.Errorf("messages[%d] role changed: %q -> %q", i, before.Messages[i].Role, after.Messages[i].Role)
		}
		if len(before.Messages[i].Parts) != len(after.Messages[i].Parts) {
			t.Fatalf("messages[%d] part count changed: %d -> %d", i, len(before.Messages[i].Parts), len(after.Messages[i].Parts))
		}
		for j := range before.Messages[i].Parts {
			part := before.Messages[i].Parts[j]
			got := after.Messages[i].Parts[j]
			if selected[[2]int{i, j}] {
				continue
			}
			if part.Kind != got.Kind {
				t.Errorf("messages[%d][%d] kind changed: %q -> %q", i, j, part.Kind, got.Kind)
			}
			if part.Text != got.Text {
				t.Errorf("messages[%d][%d] text changed", i, j)
			}
			if part.ToolCallID != got.ToolCallID || part.ToolName != got.ToolName {
				t.Errorf("messages[%d][%d] tool identity changed", i, j)
			}
			if !reflect.DeepEqual(part.Content, got.Content) {
				t.Errorf("messages[%d][%d] content changed", i, j)
			}
		}
	}
}

// TestAttemptTransformIdentifiesItselfForTheCandidateAttemptStage pins the stable
// plugin identity, the stage order, and the fail-open failure mode of the pass.
// The identity and the order are written out literally because they are the stable
// selection contract other components depend on across builds: a suppression rule
// names the identity and the stage sorts on the order, so comparing either with the
// constant the implementation returns would let a changed contract pass unnoticed.
// The failure mode is what makes requirement 8.2 hold even when the pass panics or
// its bounded deadline expires: the stage rolls the call back and continues the
// candidate with real paths instead of failing the turn.
func TestAttemptTransformIdentifiesItselfForTheCandidateAttemptStage(t *testing.T) {
	t.Parallel()

	transform := attemptRewriteTransform(t)
	if got := transform.ID(); got != "path-virtualization-attempt-transform" {
		t.Fatalf("ID() = %q, want the stable %q", got, "path-virtualization-attempt-transform")
	}
	if got := transform.Order(); got != 0 {
		t.Fatalf("Order() = %d, want 0 so the pass keeps the default mid-stage position", got)
	}
	if transform.FailureMode() != sdkhooks.FailOpen {
		t.Fatalf("FailureMode() = %v, want FailOpen so an unexpected failure preserves the real path", transform.FailureMode())
	}
}

// attemptItemsUnchangedExcept proves that every item other than the named ones kept
// every field it arrived with, including the identity, status, and content parts a
// virtualized tool call must never lose (requirements.md 2.8).
func attemptItemsUnchangedExcept(t *testing.T, before, after aipapi.Call, selected map[int]bool) {
	t.Helper()
	if len(before.Items) != len(after.Items) {
		t.Fatalf("item count changed: %d -> %d", len(before.Items), len(after.Items))
	}
	for i := range before.Items {
		if selected[i] {
			continue
		}
		want, got := before.Items[i], after.Items[i]
		if want.Kind != got.Kind || want.ID != got.ID || want.Status != got.Status || want.Role != got.Role || want.Phase != got.Phase {
			t.Errorf("items[%d] identity changed: %+v -> %+v", i, want, got)
		}
		if !reflect.DeepEqual(want.Content, got.Content) {
			t.Errorf("items[%d] content changed", i)
		}
		if !reflect.DeepEqual(want.ToolCall, got.ToolCall) {
			t.Errorf("items[%d] tool call changed", i)
		}
		if !reflect.DeepEqual(want.ToolResult, got.ToolResult) {
			t.Errorf("items[%d] tool result changed", i)
		}
		if !reflect.DeepEqual(want.Reasoning, got.Reasoning) ||
			!reflect.DeepEqual(want.Compaction, got.Compaction) ||
			!reflect.DeepEqual(want.Reference, got.Reference) ||
			!reflect.DeepEqual(want.Extension, got.Extension) {
			t.Errorf("items[%d] non-tool payload changed", i)
		}
	}
}

// TestAttemptTransformRewritesOutgoingCandidateCallOverBothAuthorities proves the
// pass virtualizes the path-bearing tool history of one outgoing candidate before
// candidate sizing observes it (requirements.md 5.3), over both canonical
// authorities, while every byte it did not select survives (requirements.md 2.8)
// and the call stays canonically valid (requirements.md 8.5).
func TestAttemptTransformRewritesOutgoingCandidateCallOverBothAuthorities(t *testing.T) {
	t.Parallel()

	for _, authority := range []struct {
		name string
		call *aipapi.Call
		// wantRealRootOccurrences counts the places the fixture deliberately leaves
		// naming the real root: the ordinary user message, the payload-concept
		// argument sibling, the opaque tool result, and - for the legacy authority -
		// the structured result payload no selector claims. Anything more would mean
		// the pass published a replacement it should not have.
		wantRealRootOccurrences int
	}{
		{name: "item_authority", call: attemptItemCall(attemptPathArguments), wantRealRootOccurrences: 3},
		{name: "legacy_message_parts", call: attemptLegacyCall(attemptPathArguments), wantRealRootOccurrences: 4},
	} {
		t.Run(authority.name, func(t *testing.T) {
			t.Parallel()

			rec := &attemptRecorder{}
			transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
			call := authority.call
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture call must be canonical: %v", err)
			}
			before := aipapi.CloneCall(*call)

			decision, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			attemptContinue(t, decision)
			if err := call.Validate(); err != nil {
				t.Fatalf("published call must stay canonical (requirements.md 8.5): %v", err)
			}

			report := rec.only(t)
			if report.Outcome != outbound.OutcomeRewriterRan {
				t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeRewriterRan)
			}
			if report.RootReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("report root reason = %q, want none", report.RootReason)
			}
			stats := report.Stats
			if stats.Eligible != 1 || stats.Rewritten != 1 {
				t.Fatalf("stats eligible/rewritten = %d/%d, want 1/1", stats.Eligible, stats.Rewritten)
			}
			if stats.BytesBefore <= stats.BytesAfter || stats.BytesSaved() != stats.BytesBefore-stats.BytesAfter {
				t.Fatalf("stats must report a realized saving: before=%d after=%d saved=%d",
					stats.BytesBefore, stats.BytesAfter, stats.BytesSaved())
			}

			published := attemptMarshal(t, *call)
			if !strings.Contains(published, attemptVirtual) {
				t.Fatalf("published call must carry the virtual alias on the selected path-bearing field")
			}
			if strings.Contains(published, `"file_path":"`+attemptTarget+`"`) {
				t.Fatalf("the selected path-bearing field still carries the real root after rewrite")
			}
			if !strings.Contains(published, `"content":"literal `+attemptRoot+` text"`) {
				t.Fatalf("a payload-concept sibling no profile claims must keep its bytes (requirements.md 2.4)")
			}
			if !strings.Contains(published, `"limit":10`) || !strings.Contains(published, `"empty":null`) {
				t.Fatalf("non-path argument values must survive byte-for-byte (requirements.md 2.8)")
			}
			// The opaque tool-result payload names a real path but no shipped profile
			// marks that tool path-oriented, so it must reach the backend byte-for-byte
			// (requirements.md 2.5, and design.md 248-250).
			if !strings.Contains(published, "read "+attemptTarget) {
				t.Fatalf("an opaque tool-result payload must stay unchanged by default (requirements.md 2.5)")
			}
			if got := strings.Count(published, attemptRoot); got != authority.wantRealRootOccurrences {
				t.Fatalf("only the ordinary message, the payload-concept sibling, and the opaque result may still name the real root: occurrences=%d want=%d",
					got, authority.wantRealRootOccurrences)
			}

			attemptEnvelopeUnchanged(t, before, *call)
			if authority.call.HasItemAuthority() {
				attemptItemsUnchangedExcept(t, before, *call, map[int]bool{1: true})
			} else {
				attemptMessagesUnchangedExcept(t, before, *call, map[[2]int]bool{{1, 0}: true})
			}
		})
	}
}

// TestAttemptTransformAuditModeMeasuresWithoutMutating proves requirements 7.2 and
// 7.3 through the real pass: audit publishes a byte-identical call and still
// reports what a rewrite would have done, and the numbers are the rewrite's own
// because both modes run the same detection.
func TestAttemptTransformAuditModeMeasuresWithoutMutating(t *testing.T) {
	t.Parallel()

	rec := &attemptRecorder{}
	transform := outbound.NewAttemptTransform(rewrite.ModeAudit, attemptResolver(t), outbound.WithReporter(rec.record))
	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	decision, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	attemptContinue(t, decision)
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("audit mode must publish nothing (requirements.md 7.3)")
	}

	audited := rec.only(t)
	if audited.Stats.Eligible != 1 || audited.Stats.Rewritten != 1 {
		t.Fatalf("audit must still measure the candidate rewrite: %+v", audited.Stats)
	}

	// The same pass in rewrite mode must reach the same numbers for the same input,
	// because audit is the same detection pass with publication switched off.
	rewriteRec := &attemptRecorder{}
	rewritePass := outbound.NewAttemptTransform(rewrite.ModeRewrite, attemptResolver(t), outbound.WithReporter(rewriteRec.record))
	rewriteCall := attemptItemCall(attemptPathArguments)
	if _, err := rewritePass.HandleAttempt(t.Context(), rewriteCall, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("rewrite HandleAttempt: %v", err)
	}
	if got := rewriteRec.only(t).Stats; !reflect.DeepEqual(got, audited.Stats) {
		t.Fatalf("audit and rewrite stats diverged (requirements.md 7.3): audit=%+v rewrite=%+v", audited.Stats, got)
	}
}

// TestAttemptTransformRefusesUnusableProjectRootWithoutMutating proves requirement
// 1.8 end to end: a root the mapper refuses leaves the outgoing candidate
// untouched and reports the mapper's own bounded reason code rather than guessing
// which spelling the client meant.
func TestAttemptTransformRefusesUnusableProjectRootWithoutMutating(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		root       string
		wantReason pathvirtualization.SkipReason
	}{
		{name: "absent", root: "", wantReason: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: "relative/workspace", wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "windows_device_namespace", root: `\\.\PIPE\aip`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "malformed_volume_root", root: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "reserved_namespace_collision", root: `/.__aip_v1__/w_hr7xnivm5at2ku356qya/pkg`, wantReason: pathvirtualization.SkipReasonReservedNamespaceCollision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &attemptRecorder{}
			transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
			call := attemptItemCall(attemptPathArguments)
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture call must be canonical: %v", err)
			}
			before := attemptMarshal(t, *call)

			meta := attemptWorkspace()
			meta.Workspace.ProjectRoot = tc.root
			decision, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			attemptContinue(t, decision)
			if after := attemptMarshal(t, *call); after != before {
				t.Fatalf("an unusable project root must leave the outgoing candidate byte-identical")
			}

			report := rec.only(t)
			if report.Outcome != outbound.OutcomeProjectRootUnusable {
				t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeProjectRootUnusable)
			}
			if report.RootReason != tc.wantReason {
				t.Fatalf("report root reason = %q, want %q", report.RootReason, tc.wantReason)
			}
			if !reflect.DeepEqual(report.Stats, rewrite.Stats{}) {
				t.Fatalf("a refused root must not report rewrite statistics: %+v", report.Stats)
			}
		})
	}
}

// TestAttemptTransformLeavesCallUnchangedWhenTheAliasDoesNotShorten proves
// requirement 1.4 through the pass: a usable root whose alias is not strictly
// shorter than the root leaves outbound virtualization inactive for every surface
// of the candidate, with the rewriter's own bounded reason.
func TestAttemptTransformLeavesCallUnchangedWhenTheAliasDoesNotShorten(t *testing.T) {
	t.Parallel()

	const shortRoot = "/x"

	rec := &attemptRecorder{}
	transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
	call := attemptItemCall(`{"file_path":"` + shortRoot + `/src/main.go","limit":10}`)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	meta := attemptWorkspace()
	meta.Workspace.ProjectRoot = shortRoot
	decision, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	attemptContinue(t, decision)
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("an inactive mapping must leave the outgoing candidate byte-identical (requirements.md 1.4)")
	}

	report := rec.only(t)
	if report.Outcome != outbound.OutcomeRewriterRan {
		t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeRewriterRan)
	}
	if attemptSkipCount(report.Stats, rewrite.SkipReasonMappingInactive) != 1 {
		t.Fatalf("stats must record exactly one mapping_inactive skip: %+v", report.Stats)
	}
	if report.Stats.Eligible != 0 || report.Stats.Rewritten != 0 {
		t.Fatalf("an inactive mapping must measure nothing: %+v", report.Stats)
	}
}

// TestAttemptTransformIsIdempotent proves requirements 2.9 and 5.2's precondition
// for Task 5.2: applying the pass to an already virtualized outgoing candidate
// publishes nothing and measures nothing, so the later request-part pass can
// reapply the same pure rewrite safely.
func TestAttemptTransformIsIdempotent(t *testing.T) {
	t.Parallel()

	transform := attemptRewriteTransform(t)
	call := attemptItemCall(attemptPathArguments)
	if _, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("first HandleAttempt: %v", err)
	}
	firstPass := attemptMarshal(t, *call)

	rec := &attemptRecorder{}
	second := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
	if _, err := second.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("second HandleAttempt: %v", err)
	}
	if after := attemptMarshal(t, *call); after != firstPass {
		t.Fatalf("reapplying the pass must be idempotent (requirements.md 2.9)")
	}
	stats := rec.only(t).Stats
	if stats.Rewritten != 0 || stats.Eligible != 0 {
		t.Fatalf("an already virtualized candidate must measure zero: %+v", stats)
	}
}

// TestAttemptTransformLeavesUnprovedSurfacesAlone proves requirements 3.5, 3.6 and
// 3.8 through the pass: a tool no exact profile claims receives no rewriting, the
// selected field of a near-miss tool spelling stays real, and neither produces a
// path-bearing mutation.
func TestAttemptTransformLeavesUnprovedSurfacesAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		toolName string
	}{
		{name: "unknown_tool", toolName: "grep"},
		{name: "near_miss_spelling_of_a_profiled_tool", toolName: "read_files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &attemptRecorder{}
			transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
			call := attemptItemCall(`{"file_path":"` + attemptTarget + `","limit":10}`)
			call.Tools = []aipapi.ToolDef{{Name: tc.toolName, Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`)}}
			call.Items[1].ToolCall.Name = tc.toolName
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture call must be canonical: %v", err)
			}
			before := attemptMarshal(t, *call)

			decision, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			attemptContinue(t, decision)
			if after := attemptMarshal(t, *call); after != before {
				t.Fatalf("tool name authority is byte-exact; no selector may be applied to %q (requirements.md 3.6)", tc.toolName)
			}
			if attemptSkipCount(rec.only(t).Stats, rewrite.SkipReasonNoSelectors) == 0 {
				t.Fatalf("an unclaimed tool must record the bounded no_selectors reason: %+v", rec.only(t).Stats)
			}
		})
	}
}

// TestAttemptTransformResultIsIndependentOfCandidateAndBLegIdentity proves
// requirements 5.6 and 5.7 through the pass: the published call and the reported
// statistics are identical for two attempts of one logical A-leg turn that differ
// only in trace ID, A-leg ID, candidate key, backend, model, and replay support, so
// no mapping can be keyed to any of them and every retry, race participant, and
// failover candidate derives the same V1 alias.
func TestAttemptTransformResultIsIndependentOfCandidateAndBLegIdentity(t *testing.T) {
	t.Parallel()

	first := attemptMeta()
	first.TraceID = "trace-a"
	first.ALegID = "aleg-1"
	first.CandidateKey = "backend-a:model-a"
	first.BackendID = "backend-a"
	first.BackendPrefixes = []string{"backend-a"}
	first.Model = "model-a"
	first.ReplaySupport = aipapi.ReasoningReplaySupport{}

	second := attemptMeta()
	second.TraceID = "trace-b"
	second.ALegID = "aleg-2"
	second.CandidateKey = "backend-b:model-b"
	second.BackendID = "backend-b"
	second.BackendPrefixes = []string{"backend-b", "backend-c"}
	second.Model = "model-b"
	second.ReplaySupport = aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}

	run := func(meta request.AttemptMeta) (string, outbound.Report) {
		rec := &attemptRecorder{}
		transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
		call := attemptItemCall(attemptPathArguments)
		if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
			t.Fatalf("HandleAttempt: %v", err)
		}
		return attemptMarshal(t, *call), rec.only(t)
	}

	gotFirst, reportFirst := run(first)
	gotSecond, reportSecond := run(second)
	if gotFirst != gotSecond {
		t.Fatalf("two attempts of one turn must derive the same alias (requirements.md 5.6)")
	}
	if !reflect.DeepEqual(reportFirst, reportSecond) {
		t.Fatalf("reported statistics must not depend on candidate or B-leg identity (requirements.md 5.7): %+v vs %+v", reportFirst, reportSecond)
	}
}

// TestAttemptTransformMutatesOnlyTheCallItWasHanded proves the extension-point
// contract and the rewriter's purity at the same time: the pass publishes a deep
// copy onto the call the stage owns, and every value the caller still holds
// separately - the attempt metadata and its workspace view in particular - is left
// exactly as it arrived.
func TestAttemptTransformMutatesOnlyTheCallItWasHanded(t *testing.T) {
	t.Parallel()

	transform := attemptRewriteTransform(t)
	meta := attemptWorkspace()
	metaBefore := aipworkspace.WorkspaceView{
		ID: meta.Workspace.ID, ProjectRoot: meta.Workspace.ProjectRoot, DirtyTree: meta.Workspace.DirtyTree,
		Markers: meta.Workspace.Markers, Labels: meta.Workspace.Labels,
	}

	first := attemptItemCall(attemptPathArguments)
	heldElsewhere := aipapi.CloneCall(*first)
	if _, err := transform.HandleAttempt(t.Context(), first, meta, request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if !reflect.DeepEqual(meta.Workspace, metaBefore) {
		t.Fatalf("the pass must not mutate the authoritative workspace view it was handed")
	}
	if attemptMarshal(t, heldElsewhere) == attemptMarshal(t, *first) {
		t.Fatal("the pass must publish a rewritten candidate, not leave the call untouched")
	}
	// The published value is a deep copy, so the caller's own snapshot shares no
	// payload byte with it and cannot observe the rewrite through a shared buffer.
	if attemptToolCallDocument(&heldElsewhere) == nil {
		t.Fatal("the held snapshot lost its payload")
	}
	if got := attemptSelectedPath(t, &heldElsewhere); got != attemptTarget {
		t.Fatalf("the held snapshot's selected value changed to %q; the rewrite must not alias caller-owned payload bytes", got)
	}

	// Reusing one pass for a second candidate must not carry any state across.
	second := attemptItemCall(attemptPathArguments)
	if _, err := transform.HandleAttempt(t.Context(), second, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("second HandleAttempt: %v", err)
	}
	if attemptMarshal(t, *first) != attemptMarshal(t, *second) {
		t.Fatalf("requirements.md 5.6 - one shared pass must derive the same alias for every candidate")
	}
}

// TestAttemptTransformReportsAreContentFree proves requirements 7.6 and 7.7 for the
// value this pass records: a report is a closed outcome, a closed root refusal code,
// and counters. No real path, virtual alias, workspace tag, path suffix, tool name,
// call ID, or payload byte can reach it.
func TestAttemptTransformReportsAreContentFree(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		attemptRoot,
		attemptAlias,
		attemptTarget,
		attemptVirtual,
		attemptSuffix,
		attemptTool,
		attemptCallID,
		"hr7xnivm5at2ku356qya",
		"w_",
		attemptRoot[1:],
	}
	assertContentFree := func(t *testing.T, label string, report outbound.Report) {
		t.Helper()
		if report.Outcome.String() != outbound.OutcomeRewriterRan.String() &&
			report.Outcome.String() != outbound.OutcomeProjectRootUnusable.String() &&
			report.Outcome.String() != outbound.OutcomeTransformationFailed.String() &&
			report.Outcome.String() != "unknown" {
			t.Fatalf("%s: outcome label %q is outside the closed vocabulary", label, report.Outcome.String())
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("%s: marshal report: %v", label, err)
		}
		text := string(encoded)
		for _, needle := range forbidden {
			if strings.Contains(text, needle) {
				t.Fatalf("%s: report leaked content-bearing text (requirements.md 7.7)", label)
			}
		}
	}

	t.Run("published_pass", func(t *testing.T) {
		t.Parallel()
		rec := &attemptRecorder{}
		transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
		call := attemptItemCall(attemptPathArguments)
		if _, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
			t.Fatalf("HandleAttempt: %v", err)
		}
		assertContentFree(t, "published", rec.only(t))
	})

	t.Run("refused_root", func(t *testing.T) {
		t.Parallel()
		rec := &attemptRecorder{}
		transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
		call := attemptItemCall(attemptPathArguments)
		meta := attemptWorkspace()
		meta.Workspace.ProjectRoot = `\\.\PIPE\aip`
		if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
			t.Fatalf("HandleAttempt: %v", err)
		}
		assertContentFree(t, "refused_root", rec.only(t))
	})
}

// TestAttemptTransformToleratesAbsentCallAndNilPass proves the pass can never panic
// on the two absent inputs a nil-safe extension point must tolerate, and that both
// stay fail-open: no exclusion, no error, and a bounded reason rather than a crash.
func TestAttemptTransformToleratesAbsentCallAndNilPass(t *testing.T) {
	t.Parallel()

	rec := &attemptRecorder{}
	transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))
	decision, err := transform.HandleAttempt(t.Context(), nil, attemptWorkspace(), request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt(nil call): %v", err)
	}
	attemptContinue(t, decision)

	var absent *outbound.AttemptTransform
	decision, err = absent.HandleAttempt(t.Context(), attemptItemCall(attemptPathArguments), attemptWorkspace(), request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt on an absent pass: %v", err)
	}
	attemptContinue(t, decision)
	if len(rec.reports) != 1 {
		t.Fatalf("reports = %d, want 1: only the real pass records an outcome", len(rec.reports))
	}
}

// TestAttemptTransformWithoutPolicyPublishesNothing proves the fail-closed direction
// of an unbound policy: a deployment that has compiled no profile layer resolves no
// selector, so the pass measures the refusal and rewrites nothing.
func TestAttemptTransformWithoutPolicyPublishesNothing(t *testing.T) {
	t.Parallel()

	rec := &attemptRecorder{}
	transform := outbound.NewAttemptTransform(rewrite.ModeRewrite, nil, outbound.WithReporter(rec.record))
	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	if _, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("an absent policy must publish nothing (requirements.md 3.5)")
	}
	if attemptSkipCount(rec.only(t).Stats, rewrite.SkipReasonNoSelectors) == 0 {
		t.Fatalf("an absent policy must record the bounded no_selectors reason: %+v", rec.only(t).Stats)
	}
}

// TestAttemptTransformIsSafeForConcurrentCandidates proves the pass holds no mutable
// state, so the one instance a generation shares may serve every retry, race
// participant, and failover candidate of a logical A-leg turn concurrently
// (requirements.md 5.6).
func TestAttemptTransformIsSafeForConcurrentCandidates(t *testing.T) {
	t.Parallel()

	transform := attemptRewriteTransform(t)
	const candidates = 24
	type result struct {
		encoded string
		err     error
	}
	results := make(chan result, candidates)
	for range candidates {
		go func() {
			call := attemptItemCall(attemptPathArguments)
			if _, err := transform.HandleAttempt(context.Background(), call, attemptWorkspace(), request.Services{}); err != nil {
				results <- result{err: err}
				return
			}
			encoded, err := json.Marshal(call)
			results <- result{encoded: string(encoded), err: err}
		}()
	}
	first := <-results
	if first.err != nil {
		t.Fatal(first.err)
	}
	for range candidates - 1 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.encoded != first.encoded {
			t.Fatalf("concurrent candidates disagreed on the derived alias (requirements.md 5.6)")
		}
	}
}
