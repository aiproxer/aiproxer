package rewrite_test

// This file is a self-review probe, not a spec assertion. It exists so the parity
// claim in audit_test.go can be checked against inputs nobody designed the corpus
// around, and so each probe's outcome is printed rather than merely asserted.
//
// The adversarial shapes are the ones where a measuring implementation and a mutating
// implementation would plausibly diverge: a payload only partly accepted, a refused
// line beside an accepted one, an alias and a real path on the same line, a mapping
// that is inactive for a different reason than "the alias is long", a tool no profile
// claims, a declared mode whose recognizer finds nothing, a document that is valid
// JSON with the wrong root shape, a leaf selected twice, a leaf equal to the real root
// itself, an empty argument object, a string array with an empty element, a Windows
// path spelled in a different case, and a payload at the frozen pointer bound.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// probeStats is the human-readable outcome of one input in one mode.
type probeStats struct {
	eligible  int
	rewritten int
	before    int
	after     int
	saved     int
	skips     string
	mutated   bool
}

func (p probeStats) String() string {
	return fmt.Sprintf("eligible=%d rewritten=%d before=%d after=%d saved=%d skips=[%s] mutated=%t",
		p.eligible, p.rewritten, p.before, p.after, p.saved, p.skips, p.mutated)
}

// probeCase is one adversarial input.
type probeCase struct {
	name     string
	root     string
	profiles []pathvirtualization.ToolProfile
	call     *aipapi.Call
}

// probeMapping derives one case's mapping and tolerates a refusal, because two of the
// adversarial shapes are roots that legitimately cannot produce an active alias.
func probeMapping(t *testing.T, root string) pathvirtualization.Mapping {
	t.Helper()

	mapping, _ := pathvirtualization.DeriveMapping(root)
	return mapping
}

// probeRuns reports what both modes produced for one input and fails when they
// disagree, printing the probe either way.
func probeRuns(t *testing.T, tc probeCase) (probeStats, probeStats) {
	t.Helper()

	mapping := probeMapping(t, tc.root)
	compiled, reject := pathvirtualization.CompileToolProfiles(tc.profiles)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("%s: compile profiles: reject = %q", tc.name, reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("%s: bind resolver: reject = %q", tc.name, reject)
	}

	run := func(mode rewrite.Mode) probeStats {
		input := aipapi.CloneCall(*tc.call)
		before, err := json.Marshal(&input)
		if err != nil {
			t.Fatalf("%s: marshal input: %v", tc.name, err)
		}
		_, stats, err := rewrite.NewWithMode(mapping, resolver, mode).RewriteCall(&input)
		if err != nil {
			t.Fatalf("%s: RewriteCall: %v", tc.name, err)
		}
		after, err := json.Marshal(&input)
		if err != nil {
			t.Fatalf("%s: marshal output: %v", tc.name, err)
		}
		return probeStats{
			eligible:  stats.Eligible,
			rewritten: stats.Rewritten,
			before:    stats.BytesBefore,
			after:     stats.BytesAfter,
			saved:     stats.BytesSaved(),
			skips:     reasonLabels(stats),
			mutated:   string(before) != string(after),
		}
	}

	audit := run(rewrite.ModeAudit)
	rewriteMode := run(rewrite.ModeRewrite)

	t.Logf("%-58s audit:   %s", tc.name, audit)
	t.Logf("%-58s rewrite: %s", tc.name, rewriteMode)

	if audit != rewriteMode {
		t.Errorf("%s: audit and rewrite disagree:\n  audit:   %s\n  rewrite: %s",
			tc.name, audit, rewriteMode)
	}
	if audit.mutated {
		t.Errorf("%s: audit mutated the canonical call", tc.name)
	}
	if audit.saved < 0 {
		t.Errorf("%s: audit reported a negative saving of %d", tc.name, audit.saved)
	}
	return audit, rewriteMode
}

// TestProbeAuditParityOnAdversarialInputs runs the review probes and prints each
// outcome, so a reviewer can read what both modes actually produced for every shape
// that could plausibly have drifted.
func TestProbeAuditParityOnAdversarialInputs(t *testing.T) {
	// These shapes are constructed here rather than reused from the corpus so that
	// they are genuinely independent of it.
	pathTool := func(extra ...string) []pathvirtualization.ToolProfile {
		return []pathvirtualization.ToolProfile{{
			Names:       []string{anyTool},
			ArgPointers: append([]string{"/file_path"}, extra...),
		}}
	}

	cases := []probeCase{
		{
			name:     "partly_accepted_opaque_payload",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{opaqueTool}, OpaqueResultMode: pathvirtualization.OpaqueResultModePathTokens}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(opaqueTool, `{"file_path":"`+fixtureTarget+`"}`),
				itemResult("call_7f3a", opaqueTool, &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: opaqueTool,
					Output: fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/one.go\n" + "2 files changed\n",
				}),
			}},
		},
		{
			name:     "refused_line_beside_an_accepted_one",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{opaqueTool}, OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(opaqueTool, `{"file_path":"`+fixtureTarget+`"}`),
				itemResult("call_7f3a", opaqueTool, &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: opaqueTool,
					Output: fixtureTarget + "\n" + fixtureTarget + " " + fixtureRoot + "/pkg/aipapi/one.go\n",
				}),
			}},
		},
		{
			name:     "alias_and_real_path_on_one_line",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{opaqueTool}, OpaqueResultMode: pathvirtualization.OpaqueResultModePathTokens}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(opaqueTool, `{"file_path":"`+fixtureTarget+`"}`),
				itemResult("call_7f3a", opaqueTool, &aipapi.ToolResultItem{
					CallID: "call_7f3a", Name: opaqueTool, Output: fixtureVPath + "\n",
				}),
			}},
		},
		{
			name:     "inactive_mapping_because_the_alias_is_not_shorter",
			root:     shortRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"file_path":"`+shortRoot+`/pkg/aipapi/call.go"}`),
			}},
		},
		{
			name:     "reserved_namespace_collision_root",
			root:     reservedRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"file_path":"`+reservedRoot+`/pkg/aipapi/call.go"}`),
			}},
		},
		{
			name: "call_with_no_authority",
			root: fixtureRoot,
			call: &aipapi.Call{
				ID:                 "call_no_authority",
				PreviousResponseID: "resp_1",
				Items:              []aipapi.Item{},
			},
		},
		{
			name:     "tool_with_no_profile",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem("unclaimed_tool", `{"file_path":"`+fixtureTarget+`"}`),
			}},
		},
		{
			name: "profile_declares_only_an_opaque_mode",
			root: fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{
				Names:            []string{opaqueTool},
				OpaqueResultMode: pathvirtualization.OpaqueResultModePathTokens,
			}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(opaqueTool, `{"file_path":"`+fixtureTarget+`"}`),
			}},
		},
		{
			name:     "valid_json_with_an_array_root",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `["`+fixtureTarget+`","other"]`),
			}},
		},
		{
			name:     "valid_json_with_a_scalar_root",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `7`),
			}},
		},
		{
			// One location reachable through two pointers is one occurrence, not
			// two. An exactly repeated pointer is refused at compilation, so the
			// reachable duplicate is the element spelling of a whole-array
			// selection: both pointers resolve, the array holds two eligible
			// values, and neither is counted twice. The sibling `/file_path`
			// selector is absent here, which is the separate selector_unresolved
			// accounting this case also exercises.
			name:     "leaf_selected_through_two_spellings",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{anyTool}, ArgPointers: []string{"/list", "/list/0"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"list":["`+fixtureTarget+`","`+fixtureRoot+`"]}`),
			}},
		},
		{
			name:     "selected_leaf_equal_to_the_real_root",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"file_path":"`+fixtureRoot+`"}`),
			}},
		},
		{
			name:     "empty_arguments_object",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{}`),
			}},
		},
		{
			name:     "string_array_with_an_empty_element",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{anyTool}, ArgPointers: []string{"/list"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"list":["","`+fixtureTarget+`"]}`),
			}},
		},
		{
			name:     "windows_path_in_a_different_case",
			root:     winFixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{anyTool}, ArgPointers: []string{"/file_path"}}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"file_path":"c:\\USERS\\dev\\PROJECTS\\aiproxer-example-project\\pkg\\aipapi\\call.go"}`),
			}},
		},
		{
			name:     "payload_at_the_pointer_bound",
			root:     fixtureRoot,
			profiles: []pathvirtualization.ToolProfile{{Names: []string{anyTool}, ArgPointers: pointerBoundSelectors()}},
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, pointerBoundPayload()),
			}},
		},
		{
			// A path spelled with JSON escapes is the case that separates the two
			// plausible measurement bases. The wire literal is longer than the
			// decoded value, so an audit number taken on raw bytes would disagree
			// with a rewrite taken on decoded bytes. Both report the decoded length,
			// which is the length the model would otherwise see.
			name:     "path_spelled_with_json_escapes",
			root:     fixtureRoot,
			profiles: pathTool(),
			call: &aipapi.Call{Items: []aipapi.Item{
				toolCallItem(anyTool, `{"file_path":"\u002fhome\u002fdev\u002fprojects\u002faiproxer\u002fpkg\u002faipapi\u002fcall.go"}`),
			}},
		},
	}

	for _, tc := range cases {
		probeRuns(t, tc)
	}
}

// anyTool is a canonical tool name no shipped built-in claims, so these probes
// exercise operator policy rather than the built-in layer.
const anyTool = "probe_any_tool"

// TestProbeUndefinedModeMeasuresWithoutMutating is the standalone probe for the
// fail-closed direction: an out-of-set mode must still measure the request and must
// leave the canonical call byte-identical.
func TestProbeUndefinedModeMeasuresWithoutMutating(t *testing.T) {
	tc := probeCase{
		name:     "undefined_mode",
		root:     fixtureRoot,
		profiles: []pathvirtualization.ToolProfile{{Names: []string{anyTool}, ArgPointers: []string{"/file_path"}}},
		call: &aipapi.Call{Items: []aipapi.Item{
			toolCallItem(anyTool, `{"file_path":"`+fixtureTarget+`"}`),
		}},
	}

	mapping := probeMapping(t, tc.root)
	compiled, reject := pathvirtualization.CompileToolProfiles(tc.profiles)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compile profiles: reject = %q", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("bind resolver: reject = %q", reject)
	}

	for _, mode := range []rewrite.Mode{rewrite.Mode(3), rewrite.Mode(99), rewrite.Mode(255)} {
		input := aipapi.CloneCall(*tc.call)
		before, err := json.Marshal(&input)
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		out, stats, err := rewrite.NewWithMode(mapping, resolver, mode).RewriteCall(&input)
		if err != nil {
			t.Fatalf("mode %d: RewriteCall: %v", uint8(mode), err)
		}
		after, err := json.Marshal(&input)
		if err != nil {
			t.Fatalf("marshal output: %v", err)
		}
		t.Logf("mode=%d label=%q eligible=%d rewritten=%d saved=%d mutated=%t",
			uint8(mode), mode, stats.Eligible, stats.Rewritten, stats.BytesSaved(), string(before) != string(after))
		if out != &input {
			t.Errorf("mode %d published a call other than the input", uint8(mode))
		}
		if string(before) != string(after) {
			t.Errorf("mode %d mutated the canonical call", uint8(mode))
		}
		if stats.Eligible != 1 || stats.Rewritten != 1 {
			t.Errorf("mode %d measured %d/%d, want 1/1", uint8(mode), stats.Eligible, stats.Rewritten)
		}
	}
}
