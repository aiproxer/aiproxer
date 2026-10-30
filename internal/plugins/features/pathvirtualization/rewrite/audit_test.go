package rewrite_test

// This file is the audit-mode accounting suite of requirements 7.2, 7.3, 7.6 and
// 9.5: the feature supports an `audit` mode beside `rewrite`, that mode performs
// candidate detection and savings estimation WITHOUT mutating the canonical call,
// and the measurement it reports is the same measurement the rewrite it describes
// actually performs.
//
// The property under test is a DIFFERENTIAL one, and it is the only one that
// matters: for any input, audit's content-free statistics equal rewrite's, field for
// field, while the published calls differ. Everything here is written so a drift
// between the two modes is a failing test rather than a quietly wrong savings
// number on a metric.
//
// Two structural properties back the empirical one, because a corpus can only
// cover the inputs somebody thought of:
//
//   - audit publishes the call it was given. It does not clone, so it cannot
//     mutate, and a caller can hold on to the input without comparing it first;
//   - the rollout mode has exactly one reader, and it is not inside any detection
//     function. Detection is therefore mode-blind by construction, which is why
//     the two modes cannot disagree about what they detect.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The five supported project roots. Requirement 9.6 asks for a representative long
// POSIX and a representative long Windows path, and covering every flavor is what
// makes the measurement basis provably flavor-independent.
const (
	uncRoot           = `\\fileserver\projects\aiproxer-example-project`
	extendedDriveRoot = `\\?\C:\Users\dev\projects\aiproxer-example-project`
	extendedUNCRoot   = `\\?\UNC\fileserver\projects\aiproxer-example-project`
	// shortRoot is a usable root whose V1 alias is not strictly shorter, which
	// leaves outbound virtualization inactive for the whole call (requirement 1.4).
	shortRoot = "/a/b"
	// reservedRoot is a supported project root spelled inside the fixed V1 reserved
	// alias namespace at the namespace's own root segment, so deriving a mapping
	// from it is refused (requirement 1.8) and outbound virtualization stays
	// inactive.
	reservedRoot = "/.__aip_v1__/w_hr7xnivm5at2ku356qya"
)

// pointerBound is the frozen per-profile pointer bound (Task 3.1). A fixture at
// exactly the bound proves the accounting stays per-leaf correct at the limit the
// configuration surface allows.
const pointerBound = 64

// auditCase is one observable input of the audit/rewrite parity property.
type auditCase struct {
	// name identifies the case in test output.
	name string
	// root is the authoritative project root the mapping is derived from.
	root string
	// profiles is the operator profile layer. A nil value binds the shipped
	// built-in layer, which is the policy a deployment gets with no operator
	// configuration at all.
	profiles []pathvirtualization.ToolProfile
	// inference is the optional declared-schema inference port.
	inference pathvirtualization.ArgumentInference
	// call is the canonical request under test.
	call *aipapi.Call
}

// modeRewriter binds one mapping and one profile policy into a rewriter in one
// rollout mode.
func modeRewriter(t *testing.T, tc auditCase, mode rewrite.Mode) *rewrite.Rewriter {
	t.Helper()

	mapping, _ := pathvirtualization.DeriveMapping(tc.root)
	var (
		compiled []pathvirtualization.CompiledProfile
		reject   pathvirtualization.SelectorReject
	)
	if tc.profiles == nil {
		compiled, reject = pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	} else {
		compiled, reject = pathvirtualization.CompileToolProfiles(tc.profiles)
	}
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compile profile layer for %q: reject = %q", tc.name, reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, tc.inference)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("bind profile layer for %q: reject = %q", tc.name, reject)
	}
	return rewrite.NewWithMode(mapping, resolver, mode)
}

// marshalCall renders the whole canonical call as JSON so a mutation of any nested
// slice, RawMessage, extension map, or session metadata entry is visible even when
// a Go value comparison would not catch it.
func marshalCall(t *testing.T, call *aipapi.Call) string {
	t.Helper()

	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal call: %v", err)
	}
	return string(encoded)
}

// checkAuditParity runs one input through both modes and asserts every property the
// two modes must share, plus the property that makes the shared numbers mean
// something: the rewrites the statistics count are byte changes the rewrite really
// performs.
//
// It returns both published statistics so a case can also assert its own numbers.
func checkAuditParity(t *testing.T, tc auditCase) (rewrite.Stats, rewrite.Stats) {
	t.Helper()

	// Every corpus fixture is a canonical request, so the parity property is stated
	// over inputs the feature is actually handed rather than over shapes canonical
	// validation would already have refused.
	if err := tc.call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	auditRewriter := modeRewriter(t, tc, rewrite.ModeAudit)
	rewriteRewriter := modeRewriter(t, tc, rewrite.ModeRewrite)

	auditInput := aipapi.CloneCall(*tc.call)
	rewriteInput := aipapi.CloneCall(*tc.call)
	auditBefore := marshalCall(t, &auditInput)
	rewriteBefore := marshalCall(t, &rewriteInput)

	auditOut, auditStats, auditErr := auditRewriter.RewriteCall(&auditInput)
	rewriteOut, rewriteStats, rewriteErr := rewriteRewriter.RewriteCall(&rewriteInput)

	if (auditErr != nil) != (rewriteErr != nil) {
		t.Fatalf("audit error = %v but rewrite error = %v; the modes must fail the same way", auditErr, rewriteErr)
	}
	if auditErr != nil {
		t.Fatalf("RewriteCall: %v", auditErr)
	}

	// Requirement 7.3: audit publishes the call it was given. It never builds the
	// clone it would have to publish from, so there is nothing to leak into the
	// caller's own value.
	if auditOut != &auditInput {
		t.Error("audit published a call other than the one it was given")
	}
	if got := marshalCall(t, auditOut); got != auditBefore {
		t.Errorf("audit mutated the call it was given:\nbefore: %s\nafter:  %s", auditBefore, got)
	}
	if got := marshalCall(t, &auditInput); got != auditBefore {
		t.Errorf("audit mutated its input through the published pointer:\nbefore: %s\nafter:  %s", auditBefore, got)
	}
	// The rewrite is pure too, so the two published calls can be compared against
	// the same baseline.
	if got := marshalCall(t, &rewriteInput); got != rewriteBefore {
		t.Errorf("rewrite mutated its input:\nbefore: %s\nafter:  %s", rewriteBefore, got)
	}

	// The measurement the feature reports is the measurement the rewrite performs.
	if !reflect.DeepEqual(auditStats, rewriteStats) {
		t.Errorf("audit and rewrite disagree about the same input:\naudit:   %+v %s\nrewrite: %+v %s",
			auditStats, reasonLabels(auditStats), rewriteStats, reasonLabels(rewriteStats))
	}

	// Requirement 9.5 and design.md 252: the saving is derived from the two totals,
	// never negative here because an active alias is strictly shorter, and never
	// larger than the count it was measured from.
	if got := auditStats.BytesSaved(); got != auditStats.BytesBefore-auditStats.BytesAfter {
		t.Errorf("BytesSaved = %d, want BytesBefore-BytesAfter = %d", got, auditStats.BytesBefore-auditStats.BytesAfter)
	}
	if auditStats.BytesSaved() < 0 {
		t.Errorf("BytesSaved = %d, want a non-negative saving", auditStats.BytesSaved())
	}
	if auditStats.Rewritten > auditStats.Eligible {
		t.Errorf("rewritten %d exceeds eligible %d", auditStats.Rewritten, auditStats.Eligible)
	}
	if auditStats.Eligible > 0 && auditStats.BytesBefore < auditStats.BytesAfter {
		t.Errorf("eligible leaves reported %d bytes before and %d after", auditStats.BytesBefore, auditStats.BytesAfter)
	}

	// The property is only meaningful if the counted rewrites are real byte
	// changes: audit reporting the rewrite's number is a claim about something the
	// rewrite demonstrably did.
	rewriteChanged := marshalCall(t, rewriteOut) != rewriteBefore
	if (auditStats.Rewritten > 0) != rewriteChanged {
		t.Errorf("audit reported rewritten=%d but the rewrite %s the call",
			auditStats.Rewritten, changedOrNot(rewriteChanged))
	}
	if (rewriteStats.Rewritten > 0) != rewriteChanged {
		t.Errorf("rewrite reported rewritten=%d but it %s the call",
			rewriteStats.Rewritten, changedOrNot(rewriteChanged))
	}

	assertStatsAreContentFree(t, auditStats, tc)
	return auditStats, rewriteStats
}

// changedOrNot renders a boolean as a phrase that reads inside an error message.
func changedOrNot(changed bool) string {
	if changed {
		return "did change"
	}
	return "did not change"
}

// assertStatsAreContentFree proves requirement 7.7 for the measured value itself:
// the whole statistics struct, rendered as JSON, carries no root, no alias, no tool
// name, and no payload byte.
func assertStatsAreContentFree(t *testing.T, stats rewrite.Stats, tc auditCase) {
	t.Helper()

	encoded, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	mapping, _ := pathvirtualization.DeriveMapping(tc.root)
	forbidden := []string{tc.root, mapping.RealRoot, mapping.VirtualRoot, mapping.WorkspaceTag}
	for _, profile := range tc.profiles {
		forbidden = append(forbidden, profile.Names...)
	}
	for _, secret := range forbidden {
		if secret == "" {
			continue
		}
		if strings.Contains(string(encoded), secret) {
			t.Errorf("statistics leak %q: %s", secret, encoded)
		}
	}
}

// toolCallItem builds an item-authoritative tool call over one argument document.
func toolCallItem(tool, arguments string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		ID:     "item_call",
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call_7f3a",
			Name:      tool,
			Arguments: json.RawMessage(arguments),
		},
	}
}

// itemResult wraps one tool-result item.
func itemResult(callID, tool string, result *aipapi.ToolResultItem) aipapi.Item {
	return aipapi.Item{
		Kind:       aipapi.ItemKindToolResult,
		ID:         "item_result",
		Status:     aipapi.ItemStatusCompleted,
		ToolResult: result,
	}
}

// pathProfiles is the operator layer every structured-surface case declares: one
// claimed tool name with one argument selector and one structured-result selector.
func pathProfiles(tool string) []pathvirtualization.ToolProfile {
	return []pathvirtualization.ToolProfile{{
		Names:              []string{tool},
		ArgPointers:        []string{"/file_path"},
		ResultJSONPointers: []string{"/entries"},
	}}
}

// TestAuditAndRewriteReportIdenticalAccounting is the parity suite. Every case is
// run through both modes on independent copies of one canonical call, and the two
// published statistics must be identical field for field while the published calls
// are not.
//
// The corpus deliberately over-represents the shapes where a naive second
// implementation would drift: partially accepted opaque payloads, a refused line
// beside an accepted one, a line mixing an alias with a real path, an inactive
// mapping, a reserved-namespace root, a tool no profile claims, a declared mode
// with nothing to recognize, a payload that is valid JSON but not an object, a leaf
// selected through two pointers, a leaf equal to the real root itself, an empty
// argument object, a string array with an empty element, a differently cased Windows
// root, and a payload at the frozen pointer bound.
func TestAuditAndRewriteReportIdenticalAccounting(t *testing.T) {
	t.Parallel()

	for _, tc := range auditCorpus(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			checkAuditParity(t, tc)
		})
	}
}

// auditCorpus returns every input the parity suite runs, in a stable order.
//
// The corpus is built once, so it takes the handle that compiles the one inference
// selector set it needs; every other compilation happens per case inside the
// subtest, which is where a failure belongs.
func auditCorpus(t *testing.T) []auditCase {
	t.Helper()
	const tool = "list_paths"
	cases := []auditCase{
		{
			// The shipped built-in layer: an argument selector plus an unselected
			// member that merely mentions the root.
			name: "builtin_item_tool_call",
			root: fixtureRoot,
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("read_file", `{"file_path":"`+fixtureTarget+`","note":"`+fixtureRoot+`/README.md"}`),
			}},
		},
		{
			// Requirement 2.9 and 6.3: an already virtualized value is not eligible,
			// and audit must measure THIS input rather than a hypothetical fresh one.
			name: "builtin_already_virtualized_arguments",
			root: fixtureRoot,
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("read_file", `{"file_path":"`+fixtureVPath+`"}`),
			}},
		},
		{
			name:     "builtin_item_tool_call_without_arguments",
			root:     fixtureRoot,
			call:     &aipapi.Call{Items: []aipapi.Item{toolCallItem("read_file", ``)}},
			profiles: pathProfiles("read_file"),
		},
		{
			name: "item_tool_no_profile_claims",
			root: fixtureRoot,
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("mystery_tool", `{"file_path":"`+fixtureTarget+`"}`),
			}},
		},
		{
			// A near-miss spelling is a different exact name, so it claims nothing.
			// Canonical validation already refuses a padded name, so the near miss
			// here is a case variant, which is a legal canonical name no profile
			// spells identically (requirement 3.6).
			name:     "item_tool_near_miss_name",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("LIST_PATHS", `{"file_path":"`+fixtureTarget+`"}`),
			}},
		},
		{
			name: "inactive_mapping_short_alias",
			root: shortRoot,
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("read_file", `{"file_path":"`+shortRoot+`/pkg/aipapi/call.go"}`),
			}},
		},
		{
			name: "reserved_namespace_root_is_inactive",
			root: reservedRoot,
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("read_file", `{"file_path":"`+reservedRoot+`/pkg/aipapi/call.go"}`),
			}},
		},
		{
			name:     "null_payload",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `null`),
			}},
		},
		{
			name:     "empty_arguments_object",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{}`),
			}},
		},
		{
			name:     "selected_leaf_is_a_number",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":7}`),
			}},
		},
		{
			name:     "selected_leaf_is_an_object",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":{"nested":"`+fixtureTarget+`"}}`),
			}},
		},
		{
			name:     "selected_array_mixes_a_number",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":["`+fixtureTarget+`",7]}`),
			}},
		},
		{
			name:     "selected_leaf_is_the_real_root_itself",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"`+fixtureRoot+`"}`),
			}},
		},
		{
			name:     "selected_leaf_mid_segment_prefix",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"`+fixtureRoot+`-other/pkg/aipapi/call.go"}`),
			}},
		},
		{
			// Requirement 9.5 measured on the DECODED value: the wire spelling is
			// longer than the value, and both modes must use the value.
			name:     "selected_leaf_spelled_with_escapes",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"\u002fhome\u002fdev\u002fprojects\u002faiproxer\u002fpkg\u002faipapi\u002fcall.go"}`),
			}},
		},
		{
			// A duplicate member name is a byte splice with two literals under one
			// pointer: the decoded document keeps the last, and only that one is
			// located and counted.
			name:     "duplicate_member_names",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"`+fixtureRoot+`/first.go","file_path":"`+fixtureTarget+`"}`),
			}},
		},
		{
			name:     "string_array_with_an_empty_element",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{tool}, ArgPointers: []string{"/list"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"list":["","`+fixtureTarget+`","`+fixtureRoot+`"]}`),
			}},
		},
		{
			// One location named through two configured pointers is one occurrence.
			name:     "leaf_selected_through_two_pointers",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{tool}, ArgPointers: []string{"/list", "/list/0"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"list":["`+fixtureTarget+`"]}`),
			}},
		},
		{
			name:     "structured_result_json_surface",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"`+fixtureTarget+`"}`),
				itemResult("call_7f3a", tool, &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: tool,
					Parts: []aipapi.ContentPart{
						{Kind: aipapi.ContentPartText, Text: "listed " + fixtureRoot},
						{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureTarget + `"],"note":"` + fixtureRoot + `","count":1}`},
					},
				}),
			}},
		},
		{
			name:     "opaque_output_without_a_declared_mode",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, `{"file_path":"`+fixtureTarget+`"}`),
				itemResult("call_7f3a", tool, &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: tool, Output: fixtureRoot + "/pkg/aipapi/call.go",
				}),
			}},
		},
		{
			// Requirement 2.1's inferred half, reached only through a tool-call
			// surface that declares its own schema.
			name:      "inferred_from_the_declared_argument_schema",
			root:      fixtureRoot,
			inference: &stubInference{pointers: mustSelectors(t, "/file_path")},
			call: &aipapi.Call{
				Tools: []aipapi.ToolDef{{
					Name:       "undocumented_tool",
					Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
				}},
				Items: []aipapi.Item{
					toolCallItem("undocumented_tool", `{"file_path":"`+fixtureTarget+`"}`),
					itemResult("call_7f3a", "undocumented_tool", &aipapi.ToolResultItem{
						CallID: "call_7f3a", Name: "undocumented_tool",
						Parts: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartJSON, Text: `{"file_path":"` + fixtureTarget + `"}`},
						},
					}),
				},
			},
		},
		{
			// Legacy message authority: a tool call part, a structured result
			// payload, and an opaque result text payload on one call.
			name:     "legacy_tool_call_and_result_part",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{
						Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: tool,
						Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
					},
				}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{
						Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: tool,
						Content: json.RawMessage(`{"entries":["` + fixtureTarget + `"]}`), Text: fixtureRoot + "/pkg/aipapi/call.go",
					},
				}},
			}},
		},
		{
			name:     "legacy_result_with_null_structured_content",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{
						Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: tool,
						Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
					},
				}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: tool, Content: json.RawMessage(`null`)},
				}},
			}},
		},
		{
			// Assistant content is not a tool call: it carries no canonical tool
			// name, so no profile can be resolved and the part is not inspected.
			name:     "legacy_part_json_without_a_tool_name",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`)},
				}},
			}},
		},
		{
			name:     "call_without_any_path_bearing_surface",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "hello"},
				}},
			}},
		},
		{
			// Item authority with an empty item list: the walk has no surface to
			// visit, which is a distinct answer from a refusal and must be measured
			// as one rather than as zero-by-accident.
			name:     "call_with_no_path_bearing_item",
			root:     fixtureRoot,
			profiles: pathProfiles(tool),
			call: &aipapi.Call{
				ID:                 "call_empty",
				PreviousResponseID: "resp_1",
				Items:              []aipapi.Item{},
			},
		},
		{
			name: "pointer_bound_payload",
			root: fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{
				Names:       []string{tool},
				ArgPointers: pointerBoundSelectors(),
			}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, pointerBoundPayload()),
			}},
		},
	}

	// Every opaque surface, under every declared mode, including the shapes a
	// second implementation would most plausibly account differently.
	for _, modeCase := range []struct {
		name string
		mode pathvirtualization.OpaqueResultMode
		text string
	}{
		{
			name: "path_tokens_listing", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go",
		},
		{
			name: "path_tokens_comma_separated", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureTarget + ", " + fixtureRoot + "/pkg/aipapi/items.go",
		},
		{
			name: "path_tokens_partially_accepted", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureTarget + "\ngrep -n hit " + fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go",
		},
		{
			name: "path_tokens_mixed_alias_and_real_line", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureVPath + " " + fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go",
		},
		{
			name: "path_tokens_already_virtualized", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureVPath + "\n" + fixtureAlias + "pkg/aipapi/items.go",
		},
		{
			name: "path_tokens_indentation_only_and_blank", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: "\n   \n\t" + fixtureTarget + "\n,",
		},
		{
			name: "path_lines_single_per_line", mode: pathvirtualization.OpaqueResultModePathLines,
			text: fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go",
		},
		{
			name: "path_lines_refuses_multi_token_line", mode: pathvirtualization.OpaqueResultModePathLines,
			text: fixtureTarget + ", " + fixtureRoot + "/pkg/aipapi/items.go\n" + fixtureTarget,
		},
		{
			name: "declared_mode_with_nothing_to_recognize", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: "2 files changed\nno paths here at all\n",
		},
		{
			name: "declared_mode_over_a_windows_listing", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: winFixtureRoot + `\pkg\aipapi\call.go` + "\n" + winFixtureRoot + `\pkg\aipapi\items.go`,
		},
		{
			name: "declared_mode_near_miss_tool_name", mode: pathvirtualization.OpaqueResultModePathTokens,
			text: fixtureTarget,
		},
	} {
		text := modeCase.text
		profiles := []pathvirtualization.ToolProfile{{
			Names:            []string{opaqueTool},
			OpaqueResultMode: modeCase.mode,
		}}
		if strings.HasSuffix(modeCase.name, "near_miss_tool_name") {
			// Requirement 3.6: the exact name is the whole authority, so a
			// differently spelled tool reaches no profile and no mode.
			profiles = []pathvirtualization.ToolProfile{{
				Names:            []string{opaqueTool + "!"},
				OpaqueResultMode: modeCase.mode,
			}}
		}
		for _, surface := range opaqueSurfaceNames(opaqueCalls(text)) {
			call := aipapi.CloneCall(*opaqueCalls(text)[surface])
			cases = append(cases, auditCase{
				name:     "opaque_" + surface + "_" + modeCase.name,
				root:     fixtureRoot,
				profiles: profiles,
				call:     &call,
			})
		}
	}

	// Every supported path flavor, over the same selected leaf.
	for _, flavor := range []struct{ name, root, target string }{
		{name: "posix", root: fixtureRoot, target: fixtureTarget},
		{name: "windows_drive", root: winFixtureRoot, target: winFixtureRoot + `\pkg\aipapi\call.go`},
		{name: "windows_drive_other_case", root: winFixtureRoot, target: `c:\users\DEV\projects\aiproxer-example-project\pkg\aipapi\call.go`},
		{name: "windows_unc", root: uncRoot, target: uncRoot + `\pkg\aipapi\call.go`},
		{name: "windows_extended_drive", root: extendedDriveRoot, target: extendedDriveRoot + `\pkg\aipapi\call.go`},
		{name: "windows_extended_unc", root: extendedUNCRoot, target: extendedUNCRoot + `\pkg\aipapi\call.go`},
	} {
		// The payload is marshalled rather than spelled as a raw string literal,
		// because a Windows path carries backslashes that a JSON string literal
		// escapes and a fixture must not hand the rewriter a document the client's
		// own encoder would never produce.
		arguments, err := json.Marshal(map[string]string{
			"file_path": flavor.target,
			"other":     flavor.root + `-sibling/x.go`,
		})
		if err != nil {
			t.Fatalf("marshal %s arguments: %v", flavor.name, err)
		}
		cases = append(cases, auditCase{
			name:     "flavor_" + flavor.name,
			root:     flavor.root,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{tool}, ArgPointers: []string{"/file_path"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(tool, string(arguments)),
			}},
		})
	}
	return cases
}

// pointerBoundSelectors returns exactly the frozen per-profile pointer bound, so the
// corpus measures a payload at the limit the configuration surface allows.
func pointerBoundSelectors() []string {
	selectors := make([]string, 0, pointerBound)
	for i := range pointerBound {
		selectors = append(selectors, fmt.Sprintf("/leaf%02d", i))
	}
	return selectors
}

// pointerBoundPayload builds the argument document the bound-sized selector set
// resolves against, with half the leaves under the real root and half outside it.
func pointerBoundPayload() string {
	members := make([]string, 0, pointerBound)
	for i := range pointerBound {
		value := fixtureRoot + fmt.Sprintf("/pkg/aipapi/file%02d.go", i)
		if i%2 == 1 {
			value = fmt.Sprintf("/usr/local/share/doc/file%02d.txt", i)
		}
		members = append(members, fmt.Sprintf(`"leaf%02d":%q`, i, value))
	}
	return "{" + strings.Join(members, ",") + "}"
}

// TestAuditReportsSavingsWithoutMutatingAnySurface proves requirement 7.3 and 9.5 on
// one concrete request: audit reports the eligible occurrences, the occurrences that
// would be rewritten, the byte totals before and after, and the saving those two
// define, while the call it was handed comes back byte-identical.
func TestAuditReportsSavingsWithoutMutatingAnySurface(t *testing.T) {
	t.Parallel()

	tc := auditCase{
		root: fixtureRoot,
		profiles: []pathvirtualization.ToolProfile{{
			Names:       []string{opaqueTool},
			ArgPointers: []string{"/file_path"},
		}},
		call: &aipapi.Call{Items: []aipapi.Item{
			toolCallItem(opaqueTool, `{"file_path":"`+fixtureTarget+`","note":"`+fixtureRoot+`/README.md"}`),
			itemResult("call_7f3a", opaqueTool, &aipapi.ToolResultItem{
				CallID: "call_7f3a", Name: opaqueTool, Output: fixtureRoot,
			}),
		}},
	}
	audit, _ := checkAuditParity(t, tc)

	if audit.Eligible != 1 {
		t.Errorf("eligible = %d, want 1", audit.Eligible)
	}
	if audit.Rewritten != 1 {
		t.Errorf("rewritten = %d, want 1", audit.Rewritten)
	}
	if want := len(fixtureTarget); audit.BytesBefore != want {
		t.Errorf("bytes before = %d, want %d (the decoded value length)", audit.BytesBefore, want)
	}
	if want := len(fixtureVPath); audit.BytesAfter != want {
		t.Errorf("bytes after = %d, want %d (the decoded value length)", audit.BytesAfter, want)
	}
	if want := len(fixtureTarget) - len(fixtureVPath); audit.BytesSaved() != want {
		t.Errorf("bytes saved = %d, want %d", audit.BytesSaved(), want)
	}
	if audit.BytesSaved() <= 0 {
		t.Error("audit must report a positive saving for a path the rewrite shortens")
	}
	// The opaque result carries no declared mode, so it contributes a bounded
	// refusal and no eligible occurrence at all.
	if count, recorded := skipCount(audit, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
		t.Errorf("opaque_result_unchanged = %d (recorded %v), want 1", count, recorded)
	}
}

// TestAuditReportsNothingForAnAlreadyVirtualizedRequest proves requirement 2.9's
// measurement half: a request that already carries the alias has no eligible
// occurrence and no saving, in audit mode exactly as in rewrite mode. Audit measures
// the input it was given, not the input a fresh client would have sent.
func TestAuditReportsNothingForAnAlreadyVirtualizedRequest(t *testing.T) {
	t.Parallel()

	tc := auditCase{
		root:     fixtureRoot,
		profiles: pathProfiles("list_paths"),
		call: &aipapi.Call{Items: []aipapi.Item{
			toolCallItem("list_paths", `{"file_path":"`+fixtureVPath+`"}`),
			itemResult("call_7f3a", "list_paths", &aipapi.ToolResultItem{
				CallID: "call_7f3a", Name: "list_paths",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartJSON, Text: `{"entries":["` + fixtureVPath + `"]}`},
				},
			}),
		}},
	}
	audit, rewriteStats := checkAuditParity(t, tc)

	if !isZeroStats(audit) {
		t.Errorf("already virtualized request reported work: %+v %s", audit, reasonLabels(audit))
	}
	if !isZeroStats(rewriteStats) {
		t.Errorf("rewrite disagreed with audit on an already virtualized request: %+v", rewriteStats)
	}
}

// TestAuditModeFailsClosed proves requirement 7.2's toggle is total and safe: the
// two defined modes are the two behaviors, an undefined value behaves like audit
// rather than like rewrite, a nil rewriter measures nothing, and the zero
// rewriter of a caller that has no policy yet rewrites nothing.
func TestAuditModeFailsClosed(t *testing.T) {
	t.Parallel()

	labels := map[rewrite.Mode]string{
		rewrite.ModeAudit:   "audit",
		rewrite.ModeRewrite: "rewrite",
		rewrite.Mode(200):   "unknown",
	}
	for mode, want := range labels {
		if got := mode.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", uint8(mode), got, want)
		}
	}

	tc := auditCase{
		root:     fixtureRoot,
		profiles: pathProfiles("list_paths"),
		call: &aipapi.Call{Items: []aipapi.Item{
			toolCallItem("list_paths", `{"file_path":"`+fixtureTarget+`"}`),
		}},
	}
	// New keeps publishing the rewritten call, so the historical constructor and
	// the explicit mode cannot drift apart.
	if mode := modeRewriter(t, tc, rewrite.ModeRewrite).Mode(); mode != rewrite.ModeRewrite {
		t.Errorf("Mode() = %q, want rewrite", mode)
	}

	// A mode value outside the closed set must not be read as rewrite: the gate is
	// written so only the defined rewrite value publishes.
	mapping := fixtureMapping(t)
	resolver, reject := pathvirtualization.NewResolver(nil, mustCompile(t, tc.profiles), nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	for _, undefined := range []rewrite.Mode{rewrite.Mode(200), rewrite.Mode(7)} {
		input := aipapi.CloneCall(*tc.call)
		before := marshalCall(t, &input)
		got, stats, err := rewrite.NewWithMode(mapping, resolver, undefined).RewriteCall(&input)
		if err != nil {
			t.Fatalf("RewriteCall: %v", err)
		}
		if got != &input || marshalCall(t, &input) != before {
			t.Errorf("undefined mode %d published a rewritten call", uint8(undefined))
		}
		if stats.Rewritten != 1 || stats.Eligible != 1 {
			t.Errorf("undefined mode %d did not measure: %+v", uint8(undefined), stats)
		}
	}

	var absent *rewrite.Rewriter
	input := aipapi.CloneCall(*tc.call)
	before := marshalCall(t, &input)
	got, stats, err := absent.RewriteCall(&input)
	if err != nil {
		t.Fatalf("nil rewriter: %v", err)
	}
	if got != &input || marshalCall(t, &input) != before {
		t.Error("nil rewriter published a rewritten call")
	}
	if !isZeroStats(stats) {
		t.Errorf("nil rewriter stats = %+v, want zero", stats)
	}
	if mode := absent.Mode(); mode != rewrite.ModeAudit {
		t.Errorf("nil rewriter Mode() = %q, want audit: it publishes the call unchanged", mode)
	}

	// An audit walk over a nil call is the same no-op a rewrite walk gives it.
	if nilOut, nilStats, nilErr := absent.RewriteCall(nil); nilOut != nil || nilErr != nil || !isZeroStats(nilStats) {
		t.Errorf("nil rewriter RewriteCall(nil) = %v, %+v, %v", nilOut, nilStats, nilErr)
	}
}

// detectionPrimitives are the calls that decide what a payload byte means: the
// mapping's own path decision, the selector layer's resolution, the byte splice's
// traversal, and the opaque recognizers. Every one of them is mode-blind, which is
// what makes the audit/rewrite parity property structural rather than empirical.
var detectionPrimitives = map[string]struct{}{
	"VirtualizePath":          {},
	"Resolve":                 {},
	"rewriteDocument":         {},
	"findSelectedStringSpans": {},
	"rewriteOpaqueText":       {},
	"opaqueLineAccepted":      {},
	"opaqueLineRewrite":       {},
	"nextOpaqueToken":         {},
}

// TestAuditMeasurementIsModeBlind parses this package's own non-test sources and
// proves the rollout mode cannot change what the two modes detect.
//
// The invariant is structural, and it is what requirement 7.3's "without mutating"
// rests on: no function that detects anything reads the mode, and exactly one
// function in the package does read it. A second detection path, a mode branch
// inside a recognizer, or a duplicated selector walk all fail this test, because
// each of them would make the measured numbers able to drift from the rewrite they
// claim to describe.
func TestAuditMeasurementIsModeBlind(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	var modeReaders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		checked++
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			detects, readsMode := classifyFunctionBody(fn.Body)
			// The Mode accessor is the mode's own read-back, so it is counted
			// separately: a caller asking which mode it holds is not a detection
			// decision and cannot cause drift.
			if readsMode {
				if isModeAccessor(fn) {
					continue
				}
				modeReaders = append(modeReaders, name+":"+fn.Name.Name)
			}
			if detects && readsMode {
				t.Errorf("%s reads the rollout mode while detecting candidates (%s); "+
					"audit and rewrite must share one detection pass or their numbers drift",
					fn.Name.Name, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the mode-blindness guard proved nothing")
	}
	if len(modeReaders) != 1 {
		t.Errorf("%d functions read the rollout mode (%v), want exactly 1: the mode must "+
			"gate publication and nothing else", len(modeReaders), modeReaders)
	}
}

// isModeAccessor reports whether one function is the exported mode read-back rather
// than a publication gate.
//
// It is identified structurally: a one-statement function whose single return
// statement mentions the mode and which calls nothing. Any accessor added later
// needs no change here, while a gate that detects candidates at the same time does
// fail the caller.
func isModeAccessor(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) != 1 {
		return false
	}
	ret, isReturn := fn.Body.List[0].(*ast.ReturnStmt)
	if !isReturn || len(ret.Results) != 1 {
		return false
	}
	readsMode := false
	callsAnything := false
	ast.Inspect(ret.Results[0], func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if typed.Sel.Name == "mode" {
				readsMode = true
			}
		case *ast.CallExpr:
			callsAnything = true
		}
		return true
	})
	return readsMode && !callsAnything
}

// classifyFunctionBody reports whether one function body detects candidates and
// whether it reads the rollout mode.
func classifyFunctionBody(body *ast.BlockStmt) (detects, readsMode bool) {
	ast.Inspect(body, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if _, isDetection := detectionPrimitives[selector.Sel.Name]; isDetection {
				detects = true
			}
			if selector.Sel.Name == "mode" {
				readsMode = true
			}
		}
		return true
	})
	return detects, readsMode
}
