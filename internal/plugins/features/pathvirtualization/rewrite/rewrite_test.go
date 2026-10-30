package rewrite_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The fixture root is long enough that the V1 alias is strictly shorter than the
// real root, which is the condition for an active mapping (requirement 1.4). The
// expected alias is spelled out literally here rather than recomputed, so a drift
// in the tag algorithm cannot make these tests agree with a wrong implementation.
const (
	fixtureRoot   = "/home/dev/projects/aiproxer-example-project"
	fixtureAlias  = "/.__aip_v1__/w_hr7xnivm5at2ku356qya/"
	fixtureTarget = fixtureRoot + "/pkg/aipapi/call.go"
	fixtureVPath  = fixtureAlias + "pkg/aipapi/call.go"
)

// fixtureMapping derives the mapping every test rewrites through.
func fixtureMapping(t *testing.T) pathvirtualization.Mapping {
	t.Helper()

	mapping, reason := pathvirtualization.DeriveMapping(fixtureRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q, want none", fixtureRoot, reason)
	}
	if mapping.VirtualRoot != fixtureAlias {
		t.Fatalf("DeriveMapping(%q) VirtualRoot = %q, want %q", fixtureRoot, mapping.VirtualRoot, fixtureAlias)
	}
	return mapping
}

// fixtureRewriter binds the fixture mapping to the shipped built-in profile layer,
// which is the policy a deployment gets with no operator configuration.
func fixtureRewriter(t *testing.T) *rewrite.Rewriter {
	t.Helper()

	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles(builtin) reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver(builtin) reject = %q, want none", reject)
	}
	return rewrite.New(fixtureMapping(t), resolver)
}

// operatorRewriter binds the fixture mapping to an operator profile layer that
// replaces the built-in layer wholesale.
func operatorRewriter(t *testing.T, profiles []pathvirtualization.ToolProfile, inference pathvirtualization.ArgumentInference) *rewrite.Rewriter {
	t.Helper()

	compiled, reject := pathvirtualization.CompileToolProfiles(profiles)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, inference)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	return rewrite.New(fixtureMapping(t), resolver)
}

// itemToolCall builds an item-authoritative tool call over the fixture arguments.
func itemToolCall(arguments string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		ID:     "item_call",
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call_7f3a",
			Name:      "read_file",
			Arguments: json.RawMessage(arguments),
		},
	}
}

// skipCount returns the recorded count for one bounded reason, and whether it was
// recorded at all.
func skipCount(stats rewrite.Stats, reason rewrite.SkipReason) (int, bool) {
	for _, skip := range stats.Skips {
		if skip.Reason == reason {
			return skip.Count, true
		}
	}
	return 0, false
}

// TestRewriteCallVirtualizesItemAuthoritativeToolCall proves the item-authoritative
// surface is rewritten: the selected leaf is virtualized, an unselected leaf that
// merely contains the real root is untouched (requirement 2.3), and IDs, names,
// ordering, and unrelated values survive (requirement 2.8).
func TestRewriteCallVirtualizesItemAuthoritativeToolCall(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, ID: "item_user", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "read " + fixtureTarget},
			}},
			itemToolCall(`{"file_path":"` + fixtureTarget + `","content":"literal ` + fixtureRoot + ` text","patch":"--- a/` + fixtureRoot + `","script":"cat ` + fixtureRoot + `/x.sh","limit":10,"ok":true,"empty":null}`),
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, stats, err := fixtureRewriter(t).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if got == call {
		t.Fatal("RewriteCall returned the input pointer; a rewrite must publish a new call")
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2: ordering must be preserved", len(got.Items))
	}
	if got.Items[0].ID != "item_user" || got.Items[0].Content[0].Text != "read "+fixtureTarget {
		t.Fatalf("ordinary user message changed: %+v", got.Items[0])
	}
	rewritten := got.Items[1].ToolCall
	if rewritten == nil {
		t.Fatal("tool call data lost")
	}
	if rewritten.CallID != "call_7f3a" || rewritten.Name != "read_file" {
		t.Fatalf("call ID or tool name changed: %+v", rewritten)
	}
	if got.Items[1].ID != "item_call" || got.Items[1].Status != aipapi.ItemStatusCompleted || got.Items[1].Kind != aipapi.ItemKindToolCall {
		t.Fatalf("item identity changed: %+v", got.Items[1])
	}

	var args struct {
		FilePath string          `json:"file_path"`
		Content  string          `json:"content"`
		Patch    string          `json:"patch"`
		Script   string          `json:"script"`
		Limit    int             `json:"limit"`
		OK       bool            `json:"ok"`
		Empty    json.RawMessage `json:"empty"`
	}
	if err := json.Unmarshal(rewritten.Arguments, &args); err != nil {
		t.Fatalf("rewritten arguments are not valid JSON: %v (%s)", err, rewritten.Arguments)
	}
	if args.FilePath != fixtureVPath {
		t.Errorf("file_path = %q, want %q", args.FilePath, fixtureVPath)
	}
	if args.Content != "literal "+fixtureRoot+" text" {
		t.Errorf("unselected content leaf was rewritten: %q", args.Content)
	}
	if args.Patch != "--- a/"+fixtureRoot {
		t.Errorf("unselected patch leaf was rewritten: %q", args.Patch)
	}
	if args.Script != "cat "+fixtureRoot+"/x.sh" {
		t.Errorf("unselected script leaf was rewritten: %q", args.Script)
	}
	if args.Limit != 10 || !args.OK || string(args.Empty) != "null" {
		t.Errorf("unrelated values changed: %+v", args)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}

	if stats.Eligible != 1 || stats.Rewritten != 1 {
		t.Errorf("stats eligible/rewritten = %d/%d, want 1/1", stats.Eligible, stats.Rewritten)
	}
	if stats.BytesBefore != len(fixtureTarget) {
		t.Errorf("BytesBefore = %d, want %d", stats.BytesBefore, len(fixtureTarget))
	}
	if stats.BytesAfter != len(fixtureVPath) {
		t.Errorf("BytesAfter = %d, want %d", stats.BytesAfter, len(fixtureVPath))
	}
	if stats.BytesSaved() != stats.BytesBefore-stats.BytesAfter {
		t.Errorf("BytesSaved = %d, want %d", stats.BytesSaved(), stats.BytesBefore-stats.BytesAfter)
	}
}

// TestRewriteCallVirtualizesLegacyPartJSONToolCall proves the legacy tool-call
// surface is rewritten by the same rewriter.
func TestRewriteCallVirtualizesLegacyPartJSONToolCall(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("please read")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "working on it"},
			{
				Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "read_file",
				Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `","content":"` + fixtureRoot + `"}`),
			},
		}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", Text: "ok"}}},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, stats, err := fixtureRewriter(t).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	part := got.Messages[1].Parts[1]
	if part.Kind != aipapi.PartJSON || part.ToolCallID != "call_7f3a" || part.ToolName != "read_file" {
		t.Fatalf("legacy tool-call identity changed: %+v", part)
	}
	if !strings.Contains(string(part.Content), fixtureVPath) {
		t.Errorf("legacy arguments not virtualized: %s", part.Content)
	}
	if !strings.Contains(string(part.Content), fixtureRoot) {
		t.Errorf("unselected content leaf was rewritten: %s", part.Content)
	}
	if got.Messages[1].Parts[0].Text != "working on it" {
		t.Errorf("unrelated part changed: %+v", got.Messages[1].Parts[0])
	}
	if got.Messages[2].Parts[0].Text != "ok" {
		t.Errorf("opaque tool result changed: %+v", got.Messages[2].Parts[0])
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
	if stats.Eligible != 1 || stats.Rewritten != 1 {
		t.Errorf("stats eligible/rewritten = %d/%d, want 1/1", stats.Eligible, stats.Rewritten)
	}
}

// TestRewriteCallIsIdempotent proves requirement 2.9: rewriting an already
// virtualized call is a no-op, with no eligible occurrence and no byte change.
func TestRewriteCallIsIdempotent(t *testing.T) {
	t.Parallel()

	rewriter := fixtureRewriter(t)
	first := &aipapi.Call{Items: []aipapi.Item{itemToolCall(`{"file_path":"` + fixtureTarget + `"}`)}}
	once, _, err := rewriter.RewriteCall(first)
	if err != nil {
		t.Fatalf("first RewriteCall: %v", err)
	}
	twice, stats, err := rewriter.RewriteCall(once)
	if err != nil {
		t.Fatalf("second RewriteCall: %v", err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("second pass changed the call:\nonce:  %s\ntwice: %s", once.Items[0].ToolCall.Arguments, twice.Items[0].ToolCall.Arguments)
	}
	if !isZeroStats(stats) {
		t.Errorf("second pass recorded work: %+v", stats)
	}

	// The alias must remain a recognized reserved alias, so a later expansion can
	// still reconstruct the client's bytes.
	if _, result := fixtureMapping(t).ExpandPath(fixtureVPath); result != pathvirtualization.ExpandResultExpanded {
		t.Errorf("ExpandPath(virtualized) = %v, want expanded", result)
	}
}

// TestRewriteCallDoesNotMutateInput proves the rewriter is pure: the caller's call
// is byte-identical after the rewrite, and the published call shares no mutable
// payload with it.
func TestRewriteCallDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Items: []aipapi.Item{itemToolCall(`{"file_path":"` + fixtureTarget + `"}`)},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{
					Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "read_file",
					Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
				},
			}},
		},
	}
	before := snapshotCall(t, call)

	got, _, err := fixtureRewriter(t).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if after := snapshotCall(t, call); !reflect.DeepEqual(before, after) {
		t.Fatalf("input call mutated:\nbefore: %s\nafter:  %s", before, after)
	}

	// Writing through the published call must not reach the input either.
	got.Items[0].ToolCall.Arguments[0] = '['
	got.Messages[0].Parts[0].Content[0] = '['
	if after := snapshotCall(t, call); !reflect.DeepEqual(before, after) {
		t.Fatal("published call shares payload bytes with the input call")
	}
}

// isZeroStats reports whether a rewrite recorded no work and no reason at all.
func isZeroStats(stats rewrite.Stats) bool {
	return stats.Eligible == 0 && stats.Rewritten == 0 &&
		stats.BytesBefore == 0 && stats.BytesAfter == 0 && len(stats.Skips) == 0
}

// snapshotCall renders every payload byte a rewrite could touch, so a mutation of
// the input is visible even when a Go value comparison would not catch it.
func snapshotCall(t *testing.T, call *aipapi.Call) string {
	t.Helper()

	var parts []string
	for _, item := range call.Items {
		if item.ToolCall != nil {
			parts = append(parts, string(item.ToolCall.Arguments))
		}
		if item.ToolResult != nil {
			parts = append(parts, item.ToolResult.Output)
			for _, part := range item.ToolResult.Parts {
				parts = append(parts, part.Text)
			}
		}
	}
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			parts = append(parts, string(part.Content), part.Text)
		}
	}
	if len(parts) == 0 {
		t.Fatal("snapshot found no payload")
	}
	return strings.Join(parts, "\x00")
}

// TestRewriteCallStructuredToolResultJSONPart proves requirement 2.2 and 3.2: an
// explicitly selected structured result location is virtualized, while an
// unselected member of the same payload is untouched.
func TestRewriteCallStructuredToolResultJSONPart(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:              []string{"list_dir"},
		ArgPointers:        []string{"/path"},
		ResultJSONPointers: []string{"/entries"},
	}}, nil)
	call := &aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: "call_7f3a", Name: "list_dir",
				Arguments: json.RawMessage(`{"path":"` + fixtureTarget + `"}`),
			},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_7f3a",
				Name:   "list_dir",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "listed " + fixtureRoot},
					{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureTarget + `"],"note":"` + fixtureRoot + `","count":1}`},
				},
			},
		},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	parts := got.Items[1].ToolResult.Parts
	if parts[0].Text != "listed "+fixtureRoot {
		t.Errorf("opaque text result part was rewritten: %q", parts[0].Text)
	}
	if !strings.Contains(parts[1].Text, fixtureVPath) {
		t.Errorf("structured result location not virtualized: %s", parts[1].Text)
	}
	if !strings.Contains(parts[1].Text, fixtureRoot) {
		t.Errorf("unselected result member was rewritten: %s", parts[1].Text)
	}
	if got.Items[1].ToolResult.CallID != "call_7f3a" || got.Items[1].ToolResult.Name != "list_dir" {
		t.Errorf("result identity changed: %+v", got.Items[1].ToolResult)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
	if stats.Eligible != 2 || stats.Rewritten != 2 {
		t.Errorf("stats eligible/rewritten = %d/%d, want 2/2", stats.Eligible, stats.Rewritten)
	}
}

// TestRewriteCallLeavesOpaqueResultUnchanged proves requirement 2.5: an opaque
// result surface is left byte-for-byte unchanged and reported with its own bounded
// reason. The two authorities are exercised separately because canonical validation
// refuses a call that carries both ordered items and raw message parts.
func TestRewriteCallLeavesOpaqueResultUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("item_authoritative_output", func(t *testing.T) {
		t.Parallel()

		opaque := "diff --git a" + fixtureTarget + " b" + fixtureTarget
		call := &aipapi.Call{Items: []aipapi.Item{
			itemToolCall(`{"file_path":"` + fixtureTarget + `"}`),
			{
				Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{CallID: "call_7f3a", Name: "read_file", Output: opaque},
			},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("fixture call must be canonical: %v", err)
		}

		got, stats, err := fixtureRewriter(t).RewriteCall(call)
		if err != nil {
			t.Fatalf("RewriteCall: %v", err)
		}
		if output := got.Items[1].ToolResult.Output; output != opaque {
			t.Errorf("opaque Output was rewritten: %q", output)
		}
		if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
			t.Errorf("opaque skip count = %d (recorded %v), want 1", count, recorded)
		}
	})

	t.Run("legacy_part_tool_result_text", func(t *testing.T) {
		t.Parallel()

		opaque := "package aipapi // " + fixtureRoot
		call := &aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{
					Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "read_file",
					Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
				},
			}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: "read_file", Text: opaque},
			}},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("fixture call must be canonical: %v", err)
		}

		got, stats, err := fixtureRewriter(t).RewriteCall(call)
		if err != nil {
			t.Fatalf("RewriteCall: %v", err)
		}
		if text := got.Messages[1].Parts[0].Text; text != opaque {
			t.Errorf("opaque PartToolResult text was rewritten: %q", text)
		}
		if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
			t.Errorf("opaque skip count = %d (recorded %v), want 1", count, recorded)
		}
	})

	t.Run("item_authoritative_text_result_part", func(t *testing.T) {
		t.Parallel()

		opaque := "Total files: 1 under " + fixtureRoot
		call := &aipapi.Call{Items: []aipapi.Item{
			itemToolCall(`{"file_path":"` + fixtureTarget + `"}`),
			{
				Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: "read_file",
					Parts: []aipapi.ContentPart{{Kind: aipapi.ContentPartToolResult, Text: opaque}},
				},
			},
		}}
		if err := call.Validate(); err != nil {
			t.Fatalf("fixture call must be canonical: %v", err)
		}

		got, stats, err := fixtureRewriter(t).RewriteCall(call)
		if err != nil {
			t.Fatalf("RewriteCall: %v", err)
		}
		if text := got.Items[1].ToolResult.Parts[0].Text; text != opaque {
			t.Errorf("opaque text result part was rewritten: %q", text)
		}
		if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
			t.Errorf("opaque skip count = %d (recorded %v), want 1", count, recorded)
		}
	})
}

// TestRewriteCallRecordsDeclaredOpaqueModeSeparately proves that an opaque result an
// exact profile marked path-oriented is reported as such, and is still left
// unchanged: the declared recognizer ran, found no line it could prove was a
// location, and said so instead of guessing at a text rewrite. Its accounting keeps
// the two states apart.
func TestRewriteCallRecordsDeclaredOpaqueModeSeparately(t *testing.T) {
	t.Parallel()

	opaque := "matched " + fixtureRoot
	build := func() *aipapi.Call {
		return &aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{
					Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "list_paths",
					Content: json.RawMessage(`{"root":"` + fixtureTarget + `"}`),
				},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: "list_paths", Text: opaque},
			}},
		}}
	}

	declared := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:            []string{"list_paths"},
		OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines,
	}}, nil)
	got, stats, err := declared.RewriteCall(build())
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if text := got.Messages[1].Parts[0].Text; text != opaque {
		t.Errorf("opaque result was rewritten: %q", text)
	}
	if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultBounded); !recorded || count != 1 {
		t.Errorf("bounded skip count = %d (recorded %v), want 1", count, recorded)
	}
	if _, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); recorded {
		t.Error("a declared opaque mode must not be reported as the default refusal")
	}

	// The default is the other reason, for the same payload.
	undeclared := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names: []string{"list_paths"},
	}}, nil)
	_, stats, err = undeclared.RewriteCall(build())
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
		t.Errorf("default opaque skip count = %d (recorded %v), want 1", count, recorded)
	}
}

// TestRewriteCallLegacyPartToolResultStructuredContent proves the legacy result
// surface is structured only where the canonical model carries structured JSON, and
// that its text payload stays opaque.
func TestRewriteCallLegacyPartToolResultStructuredContent(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:              []string{"glob_files"},
		ResultJSONPointers: []string{"/matches"},
	}}, nil)
	call := &aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			{Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "glob_files", Content: json.RawMessage(`{"pattern":"**/*.go"}`)},
		}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{
				Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: "glob_files",
				Text:    "matched " + fixtureRoot,
				Content: json.RawMessage(`{"matches":["` + fixtureTarget + `"],"pattern":"**/*.go"}`),
			},
		}},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	result := got.Messages[1].Parts[0]
	if !strings.Contains(string(result.Content), fixtureVPath) {
		t.Errorf("legacy structured result not virtualized: %s", result.Content)
	}
	if !strings.Contains(string(result.Content), `"pattern":"**/*.go"`) {
		t.Errorf("unselected result member changed: %s", result.Content)
	}
	if result.Text != "matched "+fixtureRoot {
		t.Errorf("opaque result text was rewritten: %q", result.Text)
	}
	if stats.Rewritten != 1 {
		t.Errorf("stats rewritten = %d, want 1", stats.Rewritten)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
}

// TestRewriteCallHonorsAnExplicitPayloadSelector proves requirement 2.4 is a
// profile-authority decision, not a hardcoded denylist: a payload concept is left
// alone by default and rewritten only when an exact profile explicitly names it as
// path-bearing. The shipped built-in layer names none, so the default holds there.
func TestRewriteCallHonorsAnExplicitPayloadSelector(t *testing.T) {
	t.Parallel()

	// The patch value leads with the real root, so it is a segment-boundary candidate
	// the way any other path-bearing value is; only the selector decides.
	arguments := `{"content":"literal ` + fixtureRoot + ` text","patch":"` + fixtureRoot + `/pkg/aipapi/call.go"}`
	call := func() *aipapi.Call {
		return &aipapi.Call{Items: []aipapi.Item{{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "apply_tool", Arguments: json.RawMessage(arguments)},
		}}}
	}

	// No profile names a payload concept, so both leaves keep their bytes.
	undeclared := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"apply_tool"},
		ArgPointers: []string{"/file_path"},
	}}, nil)
	got, stats, err := undeclared.RewriteCall(call())
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if string(got.Items[0].ToolCall.Arguments) != arguments {
		t.Errorf("payload concept was rewritten without an explicit selector: %s", got.Items[0].ToolCall.Arguments)
	}
	if stats.Rewritten != 0 {
		t.Errorf("stats rewritten = %d, want 0", stats.Rewritten)
	}

	// An exact profile that names one of them makes that one a path-bearing location.
	declared := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"apply_tool"},
		ArgPointers: []string{"/patch"},
	}}, nil)
	got, stats, err = declared.RewriteCall(call())
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !strings.Contains(string(got.Items[0].ToolCall.Arguments), fixtureAlias) {
		t.Errorf("explicitly selected payload location was not rewritten: %s", got.Items[0].ToolCall.Arguments)
	}
	if !strings.Contains(string(got.Items[0].ToolCall.Arguments), "literal "+fixtureRoot) {
		t.Errorf("unselected payload location was rewritten: %s", got.Items[0].ToolCall.Arguments)
	}
	if stats.Rewritten != 1 {
		t.Errorf("stats rewritten = %d, want 1", stats.Rewritten)
	}
}

// TestRewriteCallOnlySegmentBoundaryPrefixIsReplaced proves the replacement is
// limited to a segment-boundary real-root prefix, for every supported path flavor.
func TestRewriteCallOnlySegmentBoundaryPrefixIsReplaced(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		root  string
		value string
		alias string
	}{
		{
			name:  "posix_root_itself",
			root:  fixtureRoot,
			value: fixtureRoot,
			alias: fixtureAlias,
		},
		{
			name:  "posix_mid_segment_is_not_a_path_under_the_root",
			root:  fixtureRoot,
			value: fixtureRoot + "-other/pkg/aipapi/call.go",
			alias: fixtureRoot + "-other/pkg/aipapi/call.go",
		},
		{
			name:  "posix_sibling_directory_is_outside_the_root",
			root:  fixtureRoot,
			value: "/home/dev/projects/other-project/pkg/aipapi/call.go",
			alias: "/home/dev/projects/other-project/pkg/aipapi/call.go",
		},
		{
			name:  "posix_embedded_reference_is_not_rewritten",
			root:  fixtureRoot,
			value: "see " + fixtureRoot + "/pkg/aipapi/call.go for details",
			alias: "see " + fixtureRoot + "/pkg/aipapi/call.go for details",
		},
		{
			name:  "posix_relative_value",
			root:  fixtureRoot,
			value: "pkg/aipapi/call.go",
			alias: "pkg/aipapi/call.go",
		},
		{
			// Requirement 1.6: the alias keeps its own spelling while the client's
			// suffix bytes are preserved verbatim, so a lower-case drive letter in the
			// value does not survive into the alias.
			name:  "windows_drive_case_insensitive_root",
			root:  `C:\Users\dev\projects\aiproxer-example-project`,
			value: `c:\users\DEV\projects\aiproxer-example-project\pkg\aipapi\call.go`,
			alias: `C:\.__aip_v1__\w_w7y7ycrfzad7plvi3s7a\pkg\aipapi\call.go`,
		},
		{
			name:  "windows_drive_mid_segment_is_not_a_path_under_the_root",
			root:  `C:\Users\dev\projects\aiproxer-example-project`,
			value: `C:\Users\dev\projects\aiproxer-example-projectX\pkg\aipapi\call.go`,
			alias: `C:\Users\dev\projects\aiproxer-example-projectX\pkg\aipapi\call.go`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q, want none", tc.root, reason)
			}
			resolver, reject := pathvirtualization.NewResolver(nil, mustCompile(t, []pathvirtualization.ToolProfile{{
				Names:       []string{"any_tool"},
				ArgPointers: []string{"/file_path"},
			}}), nil)
			if reject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("NewResolver reject = %q, want none", reject)
			}

			arguments, _ := json.Marshal(map[string]string{"file_path": tc.value})
			call := &aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool", Arguments: arguments},
			}}}

			got, stats, err := rewrite.New(mapping, resolver).RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			var args struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(got.Items[0].ToolCall.Arguments, &args); err != nil {
				t.Fatalf("rewritten arguments invalid: %v", err)
			}
			if args.FilePath != tc.alias {
				t.Errorf("file_path = %q, want %q", args.FilePath, tc.alias)
			}
			rewritten := max(stats.Rewritten, 0)
			if tc.alias == tc.value && rewritten != 0 {
				t.Errorf("stats reported %d rewrites for an unchanged value", stats.Rewritten)
			}
			if tc.alias != tc.value && rewritten != 1 {
				t.Errorf("stats rewritten = %d, want 1", stats.Rewritten)
			}
		})
	}
}

// mustCompile compiles one operator profile layer or fails the test.
func mustCompile(t *testing.T, profiles []pathvirtualization.ToolProfile) []pathvirtualization.CompiledProfile {
	t.Helper()

	compiled, reject := pathvirtualization.CompileToolProfiles(profiles)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q, want none", reject)
	}
	return compiled
}

// TestRewriteCallRefusesAmbiguousAndUnresolvedSelectors proves requirement 3.5: a
// location that cannot be proven path-bearing is skipped whole and reported by a
// bounded reason, and nothing else in the payload is touched.
func TestRewriteCallRefusesAmbiguousAndUnresolvedSelectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		arguments  string
		pointers   []string
		wantPath   string
		wantReason rewrite.SkipReason
	}{
		{
			name:       "unresolved_pointer",
			arguments:  `{"file_path":"` + fixtureTarget + `"}`,
			pointers:   []string{"/notebook_path"},
			wantPath:   fixtureTarget,
			wantReason: rewrite.SkipReasonSelectorUnresolved,
		},
		{
			name:       "object_is_never_descended",
			arguments:  `{"file_path":{"nested":"` + fixtureTarget + `"}}`,
			pointers:   []string{"/file_path"},
			wantPath:   fixtureTarget,
			wantReason: rewrite.SkipReasonSelectorObject,
		},
		{
			name:       "non_string_scalar",
			arguments:  `{"file_path":42}`,
			pointers:   []string{"/file_path"},
			wantPath:   "",
			wantReason: rewrite.SkipReasonSelectorNotString,
		},
		{
			name:       "mixed_array_refuses_every_element",
			arguments:  `{"file_path":["` + fixtureTarget + `",7]}`,
			pointers:   []string{"/file_path"},
			wantPath:   "",
			wantReason: rewrite.SkipReasonSelectorNotStringArray,
		},
		{
			name:       "string_array_rewrites_every_element",
			arguments:  `{"file_path":["` + fixtureTarget + `","` + fixtureRoot + `/pkg/aipapi/items.go"]}`,
			pointers:   []string{"/file_path"},
			wantPath:   fixtureVPath,
			wantReason: rewrite.SkipReasonNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
				Names:       []string{"any_tool"},
				ArgPointers: tc.pointers,
			}}, nil)
			call := &aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool", Arguments: json.RawMessage(tc.arguments)},
			}}}

			got, stats, err := rewriter.RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if tc.wantReason == rewrite.SkipReasonNone {
				if !strings.Contains(string(got.Items[0].ToolCall.Arguments), tc.wantPath) {
					t.Errorf("arguments not rewritten as expected: %s", got.Items[0].ToolCall.Arguments)
				}
				return
			}
			if !strings.Contains(string(got.Items[0].ToolCall.Arguments), tc.wantPath) {
				t.Errorf("refused location was rewritten anyway: %s", got.Items[0].ToolCall.Arguments)
			}
			count, recorded := skipCount(stats, tc.wantReason)
			if !recorded || count != 1 {
				t.Errorf("skip %q count = %d (recorded %v), want 1", tc.wantReason, count, recorded)
			}
			if stats.Rewritten != 0 {
				t.Errorf("stats rewritten = %d, want 0", stats.Rewritten)
			}
		})
	}
}

// TestRewriteCallSkipsUnusablePayloads proves the bounded payload refusals: absent,
// null, and non-object argument payloads, and payloads that are not one complete
// JSON value, are reported and never rewritten.
func TestRewriteCallSkipsUnusablePayloads(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		arguments  string
		wantReason rewrite.SkipReason
	}{
		{name: "absent", arguments: ``, wantReason: rewrite.SkipReasonPayloadAbsent},
		{name: "null", arguments: `null`, wantReason: rewrite.SkipReasonPayloadAbsent},
		{name: "array_root", arguments: `["` + fixtureTarget + `"]`, wantReason: rewrite.SkipReasonPayloadNotObject},
		{name: "string_root", arguments: `"` + fixtureTarget + `"`, wantReason: rewrite.SkipReasonPayloadNotObject},
		{name: "trailing_content", arguments: `{"file_path":"` + fixtureTarget + `"} {}`, wantReason: rewrite.SkipReasonPayloadInvalid},
		{name: "truncated", arguments: `{"file_path":`, wantReason: rewrite.SkipReasonPayloadInvalid},
		{name: "not_json", arguments: `file_path=`, wantReason: rewrite.SkipReasonPayloadInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
				Names:       []string{"any_tool"},
				ArgPointers: []string{"/file_path"},
			}}, nil)
			call := &aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool", Arguments: json.RawMessage(tc.arguments)},
			}}}

			got, stats, err := rewriter.RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if string(got.Items[0].ToolCall.Arguments) != tc.arguments {
				t.Errorf("payload changed: %q", got.Items[0].ToolCall.Arguments)
			}
			count, recorded := skipCount(stats, tc.wantReason)
			if !recorded || count != 1 {
				t.Errorf("skip %q count = %d (recorded %v), want 1", tc.wantReason, count, recorded)
			}
		})
	}
}

// TestRewriteCallRequiresExactToolName proves requirement 3.6: a tool name is
// exact, so no near-miss spelling reaches a profile, and an unknown tool selects
// nothing at all (requirement 3.8).
func TestRewriteCallRequiresExactToolName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"read_file ", " read_file", "Read_File", "READ_FILE", "read_files", "my_read_file", "read\u200b_file", "read_file\n"} {
		t.Run(strings.ReplaceAll(name, "\n", "_nl_"), func(t *testing.T) {
			t.Parallel()

			call := &aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: name, Arguments: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`)},
			}}}
			got, stats, err := fixtureRewriter(t).RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if string(got.Items[0].ToolCall.Arguments) != `{"file_path":"`+fixtureTarget+`"}` {
				t.Errorf("near-miss tool name %q reached a profile: %s", name, got.Items[0].ToolCall.Arguments)
			}
			if stats.Rewritten != 0 {
				t.Errorf("stats rewritten = %d, want 0", stats.Rewritten)
			}
			if _, recorded := skipCount(stats, rewrite.SkipReasonNoSelectors); !recorded {
				t.Errorf("no NoSelectors reason recorded for unknown tool %q", name)
			}
		})
	}
}

// TestRewriteCallUsesDeclaredSchemaForInference proves requirement 2.1's "safely
// inferred" half: a tool no profile claims is still rewritten when its own declared
// schema proves the location path-bearing.
func TestRewriteCallUsesDeclaredSchemaForInference(t *testing.T) {
	t.Parallel()

	inference := &stubInference{pointers: mustSelectors(t, "/file_path")}
	resolver, reject := pathvirtualization.NewResolver(nil, nil, inference)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	rewriter := rewrite.New(fixtureMapping(t), resolver)

	call := &aipapi.Call{
		Items: []aipapi.Item{{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "vendor_tool", Arguments: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`)},
		}},
		Tools: []aipapi.ToolDef{{
			Name:       "vendor_tool",
			Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		}},
	}

	got, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !strings.Contains(string(got.Items[0].ToolCall.Arguments), fixtureVPath) {
		t.Errorf("inferred selector was not applied: %s", got.Items[0].ToolCall.Arguments)
	}
	if stats.Rewritten != 1 {
		t.Errorf("stats rewritten = %d, want 1", stats.Rewritten)
	}
	if inference.calls != 1 {
		t.Errorf("inference step ran %d times, want 1", inference.calls)
	}
	if string(inference.lastSchema) != `{"type":"object","properties":{"file_path":{"type":"string"}}}` {
		t.Errorf("inference received %q, want the declared parameters of the exact-named tool", inference.lastSchema)
	}

	// A tool that declares no schema gives the inference step nothing to prove, so
	// the same tool name without a declaration selects nothing at all.
	undeclared := &aipapi.Call{Items: []aipapi.Item{{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "vendor_tool", Arguments: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`)},
	}}}
	got, stats, err = rewriter.RewriteCall(undeclared)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if string(got.Items[0].ToolCall.Arguments) != `{"file_path":"`+fixtureTarget+`"}` {
		t.Errorf("a tool without a declared schema was rewritten: %s", got.Items[0].ToolCall.Arguments)
	}
	if stats.Rewritten != 0 {
		t.Errorf("stats rewritten = %d, want 0", stats.Rewritten)
	}
	if inference.calls != 2 {
		t.Errorf("inference step ran %d times, want 2", inference.calls)
	}
}

// TestRewriteCallDoesNotInferThroughResultSurfaces proves the resolution contract
// stays honest: the declared argument schema can only ever select argument
// locations, never result locations.
func TestRewriteCallDoesNotInferThroughResultSurfaces(t *testing.T) {
	t.Parallel()

	resolver, reject := pathvirtualization.NewResolver(nil, nil, &stubInference{pointers: mustSelectors(t, "/entries")})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	call := &aipapi.Call{Items: []aipapi.Item{
		itemToolCall(`{"file_path":"` + fixtureTarget + `"}`),
		{
			Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_7f3a", Name: "read_file",
				Parts: []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureTarget + `"]}`}},
			},
		},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, _, err := rewrite.New(fixtureMapping(t), resolver).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !strings.Contains(got.Items[1].ToolResult.Parts[0].Text, fixtureRoot) {
		t.Errorf("inference selected a result location: %s", got.Items[1].ToolResult.Parts[0].Text)
	}
}

// stubInference is a declared-schema inference port that records what it saw and
// refuses to prove anything from an absent schema, which is what the real
// inference step does.
type stubInference struct {
	pointers   pathvirtualization.SelectorSet
	calls      int
	lastSchema []byte
}

func (s *stubInference) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	s.calls++
	s.lastSchema = append(s.lastSchema[:0], declaredSchema...)
	if len(declaredSchema) == 0 {
		return nil
	}
	return s.pointers
}

// mustSelectors compiles one selector set or fails the test.
func mustSelectors(t *testing.T, pointers ...string) pathvirtualization.SelectorSet {
	t.Helper()

	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{"stub"},
		ArgPointers: pointers,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileProfiles reject = %q, want none", reject)
	}
	return compiled[0].ArgPointers
}

// TestRewriteCallInactiveMappingChangesNothing proves that a usable root whose
// alias is not beneficial leaves every surface byte-for-byte unchanged and is
// reported with its own bounded reason, so the accounting of a later stage can
// distinguish it from a payload refusal (requirement 1.4, design.md 252).
func TestRewriteCallInactiveMappingChangesNothing(t *testing.T) {
	t.Parallel()

	shortRoot := "/a/b"
	mapping, reason := pathvirtualization.DeriveMapping(shortRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q, want none", shortRoot, reason)
	}
	if mapping.VirtualRoot != "" {
		t.Fatalf("VirtualRoot = %q, want empty for a root the alias cannot shorten", mapping.VirtualRoot)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, mustCompile(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"read_file"},
		ArgPointers: []string{"/file_path"},
	}}), nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}

	arguments := json.RawMessage(`{"file_path":"` + shortRoot + `/pkg/aipapi/call.go"}`)
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "read_file", Arguments: arguments},
	}}}

	got, stats, err := rewrite.New(mapping, resolver).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !reflect.DeepEqual(got, call) {
		t.Errorf("inactive mapping changed the call: %s", got.Items[0].ToolCall.Arguments)
	}
	if count, recorded := skipCount(stats, rewrite.SkipReasonMappingInactive); !recorded || count != 1 {
		t.Errorf("skip MappingInactive count = %d (recorded %v), want 1", count, recorded)
	}
	if stats.Eligible != 0 || stats.Rewritten != 0 {
		t.Errorf("stats eligible/rewritten = %d/%d, want 0/0", stats.Eligible, stats.Rewritten)
	}
}

// TestRewriteCallNilInputsAreSafe proves the rewriter fails closed on absent
// inputs instead of panicking, which is what an unwired or rejected policy needs.
func TestRewriteCallNilInputsAreSafe(t *testing.T) {
	t.Parallel()

	rewriter := fixtureRewriter(t)
	if got, stats, err := rewriter.RewriteCall(nil); got != nil || err != nil || !isZeroStats(stats) {
		t.Errorf("RewriteCall(nil) = %v, %+v, %v; want nil, zero stats, no error", got, stats, err)
	}
	var absent *rewrite.Rewriter
	call := &aipapi.Call{Items: []aipapi.Item{itemToolCall(`{"file_path":"` + fixtureTarget + `"}`)}}
	got, stats, err := absent.RewriteCall(call)
	if err != nil {
		t.Fatalf("nil rewriter: %v", err)
	}
	if got != call {
		t.Error("nil rewriter must publish the input call unchanged")
	}
	if !isZeroStats(stats) {
		t.Errorf("nil rewriter stats = %+v, want zero", stats)
	}
	if _, stats, err := rewrite.New(fixtureMapping(t), nil).RewriteCall(call); err != nil || stats.Rewritten != 0 {
		t.Errorf("nil resolver = %+v, %v; want no rewrite and no error", stats, err)
	}
}

// TestRewriteCallPreservesPresenceAndNumbers proves the rewriter never re-encodes a
// document: absent stays absent, null stays null, and every unrelated byte of the
// payload survives, including member order, number spelling, and escapes.
func TestRewriteCallPreservesPresenceAndNumbers(t *testing.T) {
	t.Parallel()

	arguments := `{ "file_path" : "` + fixtureTarget + `" , "big" : 12345678901234567890 , "exp" : 1e400 , "esc" : "line\nbreak\ttab" , "html" : "<a href=\"x\">&amp;</a>" , "order_first" : 1 }`
	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"any_tool"},
		ArgPointers: []string{"/file_path"},
	}}, nil)
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool", Arguments: json.RawMessage(arguments)},
	}}}

	got, _, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	rewritten := string(got.Items[0].ToolCall.Arguments)
	if !json.Valid(got.Items[0].ToolCall.Arguments) {
		t.Fatalf("rewritten payload is not valid JSON: %s", rewritten)
	}
	want := `{ "file_path" : "` + fixtureVPath + `" , "big" : 12345678901234567890 , "exp" : 1e400 , "esc" : "line\nbreak\ttab" , "html" : "<a href=\"x\">&amp;</a>" , "order_first" : 1 }`
	if rewritten != want {
		t.Errorf("payload bytes changed beyond the selected leaf:\ngot:  %s\nwant: %s", rewritten, want)
	}
}

// TestRewriteCallKeepsEmptyAndNullDistinct proves the empty-vs-null JSON semantics
// survive: an absent argument payload stays absent, and an explicitly null member
// inside a rewritten payload stays null.
func TestRewriteCallKeepsEmptyAndNullDistinct(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"any_tool"},
		ArgPointers: []string{"/file_path"},
	}}, nil)
	withoutArguments := &aipapi.Call{Items: []aipapi.Item{{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool"},
	}}}
	got, _, err := rewriter.RewriteCall(withoutArguments)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if got.Items[0].ToolCall.Arguments != nil {
		t.Errorf("absent arguments became %s, want absent", got.Items[0].ToolCall.Arguments)
	}
	if encoded, err := json.Marshal(got.Items[0].ToolCall); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if strings.Contains(string(encoded), `"arguments"`) {
		t.Errorf("absent arguments were materialized: %s", encoded)
	}
}

// TestRewriteCallIsSafeForConcurrentUse proves the rewriter holds no mutable state:
// one bound rewriter shared by many callers must produce identical results.
func TestRewriteCallIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	rewriter := fixtureRewriter(t)
	call := &aipapi.Call{Items: []aipapi.Item{itemToolCall(`{"file_path":"` + fixtureTarget + `"}`)}}
	want, _, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, _, err := rewriter.RewriteCall(call)
			if err != nil {
				t.Errorf("concurrent RewriteCall: %v", err)
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("concurrent result drifted:\ngot:  %s\nwant: %s", got.Items[0].ToolCall.Arguments, want.Items[0].ToolCall.Arguments)
			}
		})
	}
	wg.Wait()
}

// TestStatsAreContentFree proves the published statistics carry counts and byte
// totals only: the reason vocabulary is a fixed label set, and the struct exposes
// no field that could hold a path, suffix, tool name, call ID, or digest.
func TestStatsAreContentFree(t *testing.T) {
	t.Parallel()

	wantLabels := map[rewrite.SkipReason]string{
		rewrite.SkipReasonNone:                   "",
		rewrite.SkipReasonMappingInactive:        "mapping_inactive",
		rewrite.SkipReasonNoSelectors:            "no_selectors",
		rewrite.SkipReasonPayloadAbsent:          "payload_absent",
		rewrite.SkipReasonPayloadInvalid:         "payload_invalid",
		rewrite.SkipReasonPayloadNotObject:       "payload_not_object",
		rewrite.SkipReasonSelectorUnresolved:     "selector_unresolved",
		rewrite.SkipReasonSelectorNotString:      "selector_not_string",
		rewrite.SkipReasonSelectorObject:         "selector_object",
		rewrite.SkipReasonSelectorNotStringArray: "selector_not_string_array",
		rewrite.SkipReasonOpaqueResultUnchanged:  "opaque_result_unchanged",
		rewrite.SkipReasonOpaqueResultBounded:    "opaque_result_bounded",
	}
	for reason, want := range wantLabels {
		if got := reason.String(); got != want {
			t.Errorf("SkipReason.String() = %q, want %q", got, want)
		}
	}
	if got := rewrite.SkipReason(200).String(); got != "unknown" {
		t.Errorf("undefined reason label = %q, want %q", got, "unknown")
	}

	statsType := reflect.TypeFor[rewrite.Stats]()
	wantFields := map[string]string{
		"Eligible":    "int",
		"Rewritten":   "int",
		"BytesBefore": "int",
		"BytesAfter":  "int",
		"Skips":       "[]rewrite.Skip",
	}
	if statsType.NumField() != len(wantFields) {
		t.Errorf("Stats has %d fields, want exactly %v", statsType.NumField(), wantFields)
	}
	for name, wantType := range wantFields {
		field, ok := statsType.FieldByName(name)
		if !ok {
			t.Errorf("Stats has no field %q", name)
			continue
		}
		if got := field.Type.String(); got != wantType {
			t.Errorf("Stats.%s type = %s, want %s", name, got, wantType)
		}
	}
	skipType := reflect.TypeFor[rewrite.Skip]()
	if skipType.NumField() != 2 {
		t.Errorf("Skip has %d fields, want exactly 2", skipType.NumField())
	}
	for _, name := range []string{"Reason", "Count"} {
		if _, ok := skipType.FieldByName(name); !ok {
			t.Errorf("Skip has no field %q", name)
		}
	}

	// A rewrite over distinctive content must not leak any of it into a label.
	marker := fixtureTarget
	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:              []string{"leaky_tool"},
		ArgPointers:        []string{"/file_path"},
		ResultJSONPointers: []string{"/entries"},
	}}, nil)
	call := &aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call_marker", Name: "leaky_tool", Arguments: json.RawMessage(`{"file_path":"` + marker + `","other":7}`)}},
			{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call_marker", Name: "leaky_tool", Output: marker}},
		},
	}
	_, stats, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	for _, skip := range stats.Skips {
		label := skip.Reason.String()
		if strings.Contains(label, marker) || strings.Contains(label, "leaky_tool") || strings.Contains(label, "call_marker") {
			t.Errorf("skip reason %q leaks payload content", label)
		}
	}
	if len(stats.Skips) == 0 {
		t.Error("fixture must record at least one skip reason")
	}
}

// TestStatsSkipOrderIsDeterministic proves the reason list is emitted in a fixed
// order, so two identical rewrites produce identical statistics.
func TestStatsSkipOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:       []string{"any_tool"},
		ArgPointers: []string{"/file_path", "/missing"},
	}}, nil)
	build := func() *aipapi.Call {
		return &aipapi.Call{
			Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{CallID: "call_7f3a", Name: "any_tool", Arguments: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`)},
			}},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: "other_tool", Content: json.RawMessage(`{"x":1}`)},
				}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", Text: fixtureRoot},
				}},
			},
		}
	}
	first := build()
	_, firstStats, err := rewriter.RewriteCall(first)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	second := build()
	_, secondStats, err := rewriter.RewriteCall(second)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !reflect.DeepEqual(firstStats, secondStats) {
		t.Errorf("statistics drifted between identical rewrites:\n%+v\n%+v", firstStats, secondStats)
	}
	for i := 1; i < len(firstStats.Skips); i++ {
		if firstStats.Skips[i-1].Reason >= firstStats.Skips[i].Reason {
			t.Errorf("skip reasons are not in ascending order: %+v", firstStats.Skips)
		}
	}
}

// TestRewriteCallVirtualizesStringWrappedItemArguments proves the wire's
// JSON-string argument spelling is normalized before selector resolution.
//
// Canonical item arguments arrive in two spellings: a raw argument document,
// and a JSON string wrapping one (the pinned wire form, which is always a
// string). Passing the wrapped spelling straight to the selector engine
// decodes a string rather than an object, so every configured selector is
// skipped and the real path reaches the backend. The rewritten document is
// re-wrapped into the string spelling, so the publication representation is
// preserved; an untouched document keeps its original bytes either way.
func TestRewriteCallVirtualizesStringWrappedItemArguments(t *testing.T) {
	t.Parallel()

	inner := `{"file_path":"` + fixtureTarget + `","content":"literal ` + fixtureRoot + ` text"}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("wrap fixture arguments: %v", err)
	}
	call := &aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_7f3a",
					Name:      "read_file",
					Arguments: json.RawMessage(wrapped),
				},
			},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, _, err := fixtureRewriter(t).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	rewritten := got.Items[0].ToolCall
	if rewritten == nil {
		t.Fatal("tool call data lost")
	}
	// The publication stays in the wire's string spelling.
	var outer string
	if err := json.Unmarshal(rewritten.Arguments, &outer); err != nil {
		t.Fatalf("rewritten arguments must stay a JSON string, got %s: %v", rewritten.Arguments, err)
	}
	var args struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(outer), &args); err != nil {
		t.Fatalf("rewrapped arguments are not valid JSON: %v (%s)", err, outer)
	}
	if args.FilePath != fixtureVPath {
		t.Errorf("file_path = %q, want %q", args.FilePath, fixtureVPath)
	}
	if args.Content != "literal "+fixtureRoot+" text" {
		t.Errorf("unselected content leaf was rewritten: %q", args.Content)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
}

// TestRewriteCallVirtualizesFunctionCallEnvelopeParts proves supported
// legacy tool-call envelopes are rewritten, not skipped as content.
//
// The Responses frontend decodes a wire function call into a PartJSON
// envelope carrying the tool identity and argument bytes inside the content,
// with the part's own tool name and call ID unset. Treating that part as
// ordinary content skips an exact profile's selectors even though the
// envelope names a selected tool. The envelope keeps its shape - the part
// still carries no tool name of its own - and its arguments keep the wire's
// string spelling; only the selected argument bytes change.
func TestRewriteCallVirtualizesFunctionCallEnvelopeParts(t *testing.T) {
	t.Parallel()

	innerArgs := `{"file_path":"` + fixtureTarget + `"}`
	envelope, err := json.Marshal(map[string]any{
		"type":      "function_call",
		"id":        "item_1",
		"call_id":   "call_7f3a",
		"name":      "read_file",
		"arguments": innerArgs,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			{Kind: aipapi.PartJSON, Content: envelope},
			{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"note":"` + fixtureTarget + `"}`)},
		}},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, _, err := fixtureRewriter(t).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	parts := got.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].ToolName != "" || parts[0].ToolCallID != "" {
		t.Fatalf("envelope part gained a tool identity it never carried: %+v", parts[0])
	}
	var env struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(parts[0].Content, &env); err != nil {
		t.Fatalf("rewritten envelope is not valid JSON: %v (%s)", err, parts[0].Content)
	}
	if env.Type != "function_call" || env.ID != "item_1" || env.CallID != "call_7f3a" || env.Name != "read_file" {
		t.Fatalf("envelope identity changed: %+v", env)
	}
	var args struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(env.Arguments), &args); err != nil {
		t.Fatalf("rewritten envelope arguments are not valid JSON: %v (%s)", err, env.Arguments)
	}
	if args.FilePath != fixtureVPath {
		t.Errorf("envelope file_path = %q, want %q", args.FilePath, fixtureVPath)
	}
	// The ordinary-content sibling is untouched: the envelope rule must not
	// widen into content inspection.
	if string(parts[1].Content) != `{"note":"`+fixtureTarget+`"}` {
		t.Errorf("ordinary content part changed: %s", parts[1].Content)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
}

// TestRewriteCallResolvesUnnamedResultsThroughHistoricalCallIDs proves a
// result that names no tool still rewrites through the profile of the call it
// answers.
//
// Decoders for several frontends produce tool results with a call ID and no
// tool name. Resolving the profile by the empty name disables configured
// structured-result rewriting on those histories. The historical call carrying
// the same call ID names the tool exactly; an ambiguous call ID - two
// different names for one ID - resolves to nothing rather than guessing.
func TestRewriteCallResolvesUnnamedResultsThroughHistoricalCallIDs(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:              []string{"list_dir"},
		ArgPointers:        []string{"/path"},
		ResultJSONPointers: []string{"/entries"},
	}}, nil)
	call := &aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: "call_7f3a", Name: "list_dir",
				Arguments: json.RawMessage(`{"path":"` + fixtureTarget + `"}`),
			},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_7f3a",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureTarget + `"],"note":"` + fixtureRoot + `"}`},
				},
			},
		},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, _, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	var resolved struct {
		Entries []string `json:"entries"`
		Note    string   `json:"note"`
	}
	if err := json.Unmarshal([]byte(got.Items[1].ToolResult.Parts[0].Text), &resolved); err != nil {
		t.Fatalf("rewritten result is not valid JSON: %v", err)
	}
	if len(resolved.Entries) != 1 || resolved.Entries[0] != fixtureVPath {
		t.Errorf("resolved result entries = %q, want the virtualized path", resolved.Entries)
	}
	if resolved.Note != fixtureRoot {
		t.Errorf("unselected result member was rewritten: %q", resolved.Note)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("rewritten call must stay canonical: %v", err)
	}
}

// TestRewriteCallLeavesAmbiguousCallIDResultsAlone proves the historical
// call-ID lookup never guesses.
//
// Canonical validation already forbids two different tool names sharing one
// call ID, so this trajectory is deliberately not validated: the guard exists
// for unvalidated inputs reaching the walker, and it must resolve to nothing
// rather than pick a profile.
func TestRewriteCallLeavesAmbiguousCallIDResultsAlone(t *testing.T) {
	t.Parallel()

	rewriter := operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:              []string{"list_dir"},
		ArgPointers:        []string{"/path"},
		ResultJSONPointers: []string{"/entries"},
	}}, nil)
	call := &aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: "call_7f3a", Name: "list_dir",
				Arguments: json.RawMessage(`{"path":"` + fixtureTarget + `"}`),
			},
		},
		{
			Kind: aipapi.ItemKindToolCall, ID: "item_other", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: "call_7f3a", Name: "other_tool",
				Arguments: json.RawMessage(`{"path":"` + fixtureTarget + `"}`),
			},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_7f3a",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureTarget + `"]}`},
				},
			},
		},
	}}

	got, _, err := rewriter.RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if got.Items[2].ToolResult.Parts[0].Text != `{"entries":["`+fixtureTarget+`"]}` {
		t.Errorf("ambiguous result changed: %q", got.Items[2].ToolResult.Parts[0].Text)
	}
}
