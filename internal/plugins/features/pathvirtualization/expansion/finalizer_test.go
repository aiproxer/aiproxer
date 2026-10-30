package expansion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	sdkworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 8.1, requirements.md 4.1, 4.3, 4.4, 4.7,
// 4.8, 4.9 and design.md section "7. Path Expansion Finalizer", whose eleven
// numbered steps this file walks one at a time.
//
// The fixture root is LONG on purpose. A short root is a supported root whose alias
// is correctly not shorter than the root itself, which leaves virtualization
// inactive by requirement 1.4 and would make every expectation below vacuous.

const (
	expansionProjectRoot = "/home/dev/workspaces/aip-path-virtualization-worktree"
	expansionOtherRoot   = "/home/dev/workspaces/aip-path-virtualization-main-checkout"

	expansionToolName = "custom_read"
)

// expansionFixture is one derived mapping plus the frozen facts every test needs.
type expansionFixture struct {
	mapping      pathvirtualization.Mapping
	virtualRoot  string
	workspaceTag string
	realRoot     string
	aliasPath    string
}

func newExpansionFixture(tb testing.TB) expansionFixture {
	tb.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(expansionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		tb.Fatalf("derive %q: reason %v", expansionProjectRoot, reason)
	}
	if mapping.VirtualRoot == "" {
		tb.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	if len(mapping.WorkspaceTag) != 20 {
		tb.Fatalf("the frozen V1 tag must be 20 characters, got %d", len(mapping.WorkspaceTag))
	}
	return expansionFixture{
		mapping:      mapping,
		virtualRoot:  mapping.VirtualRoot,
		workspaceTag: mapping.WorkspaceTag,
		realRoot:     mapping.RealRoot,
		aliasPath:    mapping.VirtualRoot + "src/main.go",
	}
}

// expansionResolver builds a resolver whose exact operator profile claims one tool
// name with one argument selector. Nothing else is claimed, so every other tool name
// resolves to no selector and proves the pass-through path.
func expansionResolver(tb testing.TB, toolName string, pointers ...string) *pathvirtualization.Resolver {
	tb.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{toolName},
		ArgPointers: pointers,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("compile profiles: reject %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		tb.Fatalf("new resolver: reject %v", reject)
	}
	return resolver
}

// expansionMeta builds the finalizer metadata the runtime hands to every
// finalizer: the authoritative per-turn workspace view is the ONLY authority the
// pass reads for a project root.
func expansionMeta(projectRoot string) toolcall.Meta {
	return toolcall.Meta{Workspace: sdkworkspace.WorkspaceView{ProjectRoot: projectRoot}}
}

// expansionCall builds one completed tool call.
func expansionCall(toolName, argsJSON string) toolcall.CompletedCall {
	return toolcall.CompletedCall{ToolCallID: "call-1", ToolName: toolName, ArgsJSON: []byte(argsJSON)}
}

// newFinalizer builds the shipped finalizer with the default mandatory bound.
func newFinalizer(tb testing.TB, resolver *pathvirtualization.Resolver) *expansion.Finalizer {
	tb.Helper()
	fin, err := expansion.NewFinalizer(resolver, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		tb.Fatalf("NewFinalizer: %v", err)
	}
	return fin
}

// expansionReason decodes the bounded reason code a finalizer published.
func expansionReason(tb testing.TB, code string) expansion.Reason {
	tb.Helper()
	reason, ok := expansion.ParseReason(code)
	if !ok {
		tb.Fatalf("reason code %q is outside the closed vocabulary", code)
	}
	return reason
}

// TestExpansionFinalizerExpandsASelectedAlias walks design.md section 7 steps 1
// through 10 on the positive path: derive the mapping from the authoritative
// project root, resolve selectors against the exact tool name, parse the completed
// arguments, expand the alias inside its own string literal, and hand the
// assembler a valid rewritten document. Step 11 is the runtime's, and the
// assembler plus tool policy own it (requirement 4.3).
func TestExpansionFinalizerExpandsASelectedAlias(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	args := fmt.Sprintf(`{"file_path":%q,"content":"unchanged prose"}`, fixture.aliasPath)
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	if res.ToolName != expansionToolName {
		t.Fatalf("tool name=%q want the call's own name %q", res.ToolName, expansionToolName)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpanded {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpanded)
	}
	if !json.Valid(res.ArgsJSON) {
		t.Fatalf("requirement 4.8 - published arguments are not valid JSON: %q", res.ArgsJSON)
	}
	want := fmt.Sprintf(`{"file_path":%q,"content":"unchanged prose"}`, fixture.realRoot+"/src/main.go")
	if string(res.ArgsJSON) != want {
		t.Fatalf("requirement 4.8 - published arguments are not the expanded splice:\n got %s\nwant %s",
			res.ArgsJSON, want)
	}
	if strings.Contains(string(res.ArgsJSON), ".__aip_v1__") {
		t.Fatalf("requirement 4.1 - the reserved alias survived expansion: %s", res.ArgsJSON)
	}
}

// TestExpansionFinalizerPreservesEveryUnselectedByte is requirement 4.8's other
// clause. The published document must be a byte splice: member order, duplicate
// keys, number spelling, escapes, whitespace, empty-versus-null presence, and the
// content field all survive unchanged, and nothing outside a selected leaf moves.
func TestExpansionFinalizerPreservesEveryUnselectedByte(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	args := `{ "z" : 1e400 , "file_path" : ` + strconv.Quote(fixture.aliasPath) + ` ,` +
		`"dup":"/kept","dup":"/also-kept",` +
		`"neg":-0,"frac":0.0,` +
		`"esc":"tab\there",` +
		`"empty":"","null":null,` +
		`"content":` + strconv.Quote(fixture.aliasPath) + ` }`

	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	published := string(res.ArgsJSON)
	if !json.Valid(res.ArgsJSON) {
		t.Fatalf("requirement 4.8 - published arguments are not valid JSON: %s", published)
	}
	for _, kept := range []string{
		`"z" : 1e400`,
		`"file_path" : "`,
		`"dup":"/kept","dup":"/also-kept"`,
		`"neg":-0,"frac":0.0`,
		`"esc":"tab\there"`,
		`"empty":"","null":null`,
		`"content":"/.__aip_v1__`,
	} {
		if !strings.Contains(published, kept) {
			t.Fatalf("requirement 4.8 - published document lost unselected bytes %q: %s", kept, published)
		}
	}
	// Requirement 4.9: the content field is not path-bearing, so the alias inside
	// it is neither expanded nor rejected.
	if !strings.Contains(published, `"content":"`+fixture.virtualRoot) {
		t.Fatalf("requirement 4.9 - the content field must keep its own bytes: %s", published)
	}
	// The selected leaf is the ONLY changed byte: the real root appears exactly
	// once, at the selected location.
	if strings.Count(published, fixture.realRoot) != 1 {
		t.Fatalf("the real root must appear exactly once, in the selected leaf: %s", published)
	}
}

// TestExpansionFinalizerExpandsEveryElementOfASelectedArray proves the array-of-
// string shape is handled element by element, with a mixture of alias-bearing and
// ordinary values surviving intact.
func TestExpansionFinalizerExpandsEveryElementOfASelectedArray(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))

	args := fmt.Sprintf(`{"paths":[%q,"relative/third.go",%q]}`,
		fixture.aliasPath, fixture.virtualRoot+"pkg/other.go")
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	want := fmt.Sprintf(`{"paths":[%q,"relative/third.go",%q]}`,
		fixture.realRoot+"/src/main.go", fixture.realRoot+"/pkg/other.go")
	if string(res.ArgsJSON) != want {
		t.Fatalf("published %s want %s", res.ArgsJSON, want)
	}
}

// TestExpansionFinalizerPassesThroughWhenThereIsNothingToExpand is requirement
// 4.7 plus design.md section 7 steps 2 and 3: a call with no virtual root, a tool
// no layer claims, an absent payload, and a usable root whose alias is not
// shorter all keep the pre-existing pass-through behavior with a bounded reason.
func TestExpansionFinalizerPassesThroughWhenThereIsNothingToExpand(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	shortRoot := "/srv/app"

	for _, tc := range []struct {
		name       string
		resolver   *pathvirtualization.Resolver
		toolName   string
		schema     []byte
		args       string
		project    string
		wantReason expansion.Reason
	}{
		{
			name:       "no_virtual_root_in_the_arguments",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/src/main.go"}`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "relative_value_is_not_a_location",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"relative/src/main.go"}`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "no_selectors_for_the_exact_tool_name",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   "some_other_tool",
			args:       fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoSelectors,
		},
		{
			name:       "tool_name_lookup_is_byte_exact",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   "CUSTOM_READ",
			args:       fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonNoSelectors,
		},
		{
			name:       "arguments_absent",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       ``,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonArgsAbsent,
		},
		{
			name:       "arguments_null",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `null`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonArgsAbsent,
		},
		{
			name:       "arguments_are_not_an_object",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `["` + fixture.aliasPath + `"]`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonPayloadNotObject,
		},
		{
			name:       "no_authoritative_project_root",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    "",
			wantReason: expansion.ReasonRootUnusable,
		},
		{
			name:       "relative_project_root",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    "relative/project",
			wantReason: expansion.ReasonRootUnusable,
		},
		{
			name:       "usable_root_whose_alias_is_not_shorter",
			resolver:   expansionResolver(t, expansionToolName, "/file_path"),
			toolName:   expansionToolName,
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    shortRoot,
			wantReason: expansion.ReasonMappingInactive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, tc.resolver)
			res, err := fin.Finalize(context.Background(), expansionCall(tc.toolName, tc.args),
				aipapi.ToolDef{Name: tc.toolName, Parameters: tc.schema}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionPass {
				t.Fatalf("requirement 4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("a pass must not carry a mutated document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}

// TestExpansionFinalizerRejectsRecognizedReservedAliases is design.md section 7
// step 8 and requirement 4.4: a selected value that spells the fixed V1 reserved
// namespace but cannot be mapped unambiguously to the CURRENT workspace fails the
// whole tool call closed, and no alias ever reaches the client.
func TestExpansionFinalizerRejectsRecognizedReservedAliases(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other root: reason %v", reason)
	}
	if other.WorkspaceTag == fixture.workspaceTag {
		t.Fatal("the two fixture roots must derive different workspace tags")
	}

	for _, tc := range []struct {
		name       string
		value      string
		project    string
		wantReason expansion.Reason
	}{
		{
			name:       "malformed_workspace_tag",
			value:      "/.__aip_v1__/w_short/src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonMalformedReservedAlias,
		},
		{
			name:       "tag_outside_the_base32_alphabet",
			value:      "/.__aip_v1__/w_0123456789abcdefgh01/src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonMalformedReservedAlias,
		},
		{
			name:       "stale_workspace_tag",
			value:      other.VirtualRoot + "src/main.go",
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "incompatible_path_flavor",
			value:      `C:\.__aip_v1__\w_` + fixture.workspaceTag + `\src\main.go`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "different_drive_of_the_same_flavor",
			value:      `D:\.__aip_v1__\w_` + fixture.workspaceTag + `\src\main.go`,
			project:    expansionProjectRoot,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "active_tag_against_an_inactive_mapping",
			value:      fixture.aliasPath,
			project:    "/srv/app",
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			args := fmt.Sprintf(`{"file_path":%q}`, tc.value)
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
				aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionReject {
				t.Fatalf("requirement 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("a rejection must not carry a mutated document: %q", res.ArgsJSON)
			}
		})
	}
}

// TestExpansionFinalizerRejectsAStaleAliasNeverAgainstTheNewRoot is requirement
// 6.5's security-critical half: a model-retained alias carrying another root's tag
// must never be expanded against the current one, so the published rejection must
// not contain either root.
func TestExpansionFinalizerRejectsAStaleAliasNeverAgainstTheNewRoot(t *testing.T) {
	t.Parallel()

	old, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive root A: reason %v", reason)
	}
	current := newExpansionFixture(t)
	if old.WorkspaceTag == current.workspaceTag {
		t.Fatal("roots A and B must derive different workspace tags")
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	res, err := fin.Finalize(context.Background(),
		expansionCall(expansionToolName, fmt.Sprintf(`{"file_path":%q}`, old.VirtualRoot+"src/main.go")),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject", res.Action)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonWorkspaceMismatch {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonWorkspaceMismatch)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("requirements.md 6.5 - a stale alias must never be expanded: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerRefusesOneStaleLeafEvenBesideExpandableOnes proves the
// refusal is whole-document. Publishing the sibling expansion would hand the client
// a document where one path was decided and another was not.
func TestExpansionFinalizerRefusesOneStaleLeafEvenBesideExpandableOnes(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other root: reason %v", reason)
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))

	args := fmt.Sprintf(`{"paths":[%q,%q]}`, fixture.aliasPath, other.VirtualRoot+"src/main.go")
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirement 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerFailsClosedOnUnparseableAliasBearingArguments is design.md
// "Error Handling": "Malformed model JSON: existing repair/finalizer policy may
// repair first; a recognized applicable alias must never bypass required expansion
// and reach the client."
//
// The order declaration puts the shipped tool-call-repair finalizer first, so a
// repairable document arrives here already valid. This is the residual shape Task
// 7.3 characterized: repair declined the call for its own size policy, the
// document is still unparseable, and it still carries an alias. Passing it through
// would release the reserved namespace to the client, so it is refused closed.
func TestExpansionFinalizerFailsClosedOnUnparseableAliasBearingArguments(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))

	// The same document shape Task 7.3 pins: no closing quote, no closing brace,
	// and the reserved alias cut across stream fragments.
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"` + strings.Repeat("x", 1024)
	if json.Valid([]byte(malformed)) {
		t.Fatal("the fixture must be unparseable JSON")
	}
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4/8.3 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerMalformedTagOnAnUnparseableDocumentIsAlsoRefused keeps the
// unparseable path aligned with the parseable one: a reserved alias with an unusable
// tag is `malformed_reserved_alias` there and must not become a pass-through here.
func TestExpansionFinalizerMalformedTagOnAnUnparseableDocumentIsAlsoRefused(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"/.__aip_v1__/w_short/src/main.go","content":"xxxx`
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
}

// TestExpansionFinalizerPassesThroughUnparseableArgumentsWithNoAlias is the
// control that makes the rule above bounded. An unparseable document with no
// reserved alias carries nothing this feature must expand, so requirement 4.7's
// pass-through behavior is preserved and an unrelated feature's malformed call is
// not failed by this one.
func TestExpansionFinalizerPassesThroughUnparseableArgumentsWithNoAlias(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	for _, tc := range []struct {
		name       string
		args       string
		wantReason expansion.Reason
	}{
		{
			name:       "no_reserved_namespace_anywhere",
			args:       `{"file_path":"/home/dev/other/src/main.go","content":"xxxx`,
			wantReason: expansion.ReasonNoAlias,
		},
		{
			name:       "marker_is_not_a_complete_segment",
			args:       `{"file_path":"/home/dev/my.__aip_v1__/src/main.go","content":"xxxx`,
			wantReason: expansion.ReasonNoAlias,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if json.Valid([]byte(tc.args)) {
				t.Fatal("the fixture must be unparseable JSON")
			}
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
				aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionPass {
				t.Fatalf("requirements.md 4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no decision may publish a mutated document: %q", res.ArgsJSON)
			}
		})
	}
}

// TestExpansionFinalizerUnparseableReservedNamespaceInContentIsStillRefused pins
// the ACCEPTED FAIL-CLOSED RESIDUAL of the unparseable-document rule, and it is
// deliberate rather than accidental.
//
// To decide anything about an unreadable document the pass must scan raw bytes,
// because there is no structure left to read. That scan sees the whole payload, so
// a reserved alias spelled inside a `content`/`patch`/`script` field of an
// UNPARSEABLE document is refused even though requirement 4.9 forbids expanding
// such a field. Two things bound the trade:
//
//   - nothing is expanded, so requirement 4.9's prohibition is never violated; the
//     call is refused, not rewritten;
//   - the fixed V1 reserved namespace can never be a legitimate real path, because
//     requirement 1.8 refuses to derive a mapping from a root spelled inside it, so
//     the byte pattern this rule keys on is unreachable in correct client data.
//
// The alternative - expanding nothing and releasing - is the exact outcome
// design.md "Error Handling" forbids, so the refusal is the safe direction. The
// parseable counterpart is pinned separately: a document that DOES parse keeps its
// content field byte-for-byte and is never refused for it.
func TestExpansionFinalizerUnparseableReservedNamespaceInContentIsStillRefused(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	prose := `{"file_path":"/home/dev/other/a.go","content":"see /.__aip_v1__/w_0123456789abcdefghij/ for details`
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, prose),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerUnparseableAliasForAnUnprovedToolStillPassesThrough is the
// applicability boundary of the unparseable-document rule. "A recognized
// APPLICABLE alias" is design.md's own wording, and a call is one path
// virtualization applies to exactly when its tool's policy names at least one
// argument location. That is answerable without parsing the document, because
// selector resolution reads the profile layer and the declared schema, never the
// payload. A tool no layer claims therefore keeps the pre-existing pass-through
// behavior of requirements.md 3.5 and 4.7 even when its unreadable arguments happen
// to contain the reserved marker.
func TestExpansionFinalizerUnparseableAliasForAnUnprovedToolStillPassesThrough(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"xxxx`

	res, err := fin.Finalize(context.Background(), expansionCall("unclaimed_tool", malformed),
		aipapi.ToolDef{Name: "unclaimed_tool"}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("requirements.md 3.5/4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonNoSelectors {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonNoSelectors)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("a pass must not publish a mutated document: %q", res.ArgsJSON)
	}
}

// TestExpansionFinalizerUnparseableAliasIsRefusedEvenWithoutADeclaredSchema keeps
// the refusal from depending on schema inference. An explicit profile is one way
// to prove a location path-bearing; it is not the only requirement for the tool to
// be one this feature applies to.
func TestExpansionFinalizerUnparseableAliasIsRefusedEvenWithoutADeclaredSchema(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	malformed := `{"file_path":"` + fixture.aliasPath + `","content":"xxxx`

	// No catalog entry at all: the assembler would still resolve the tool's
	// selectors from the exact-name profile layer.
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, malformed),
		aipapi.ToolDef{}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("requirements.md 4.4/8.3 - action=%v want reject (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonArgsUnparseable)
	}
}

// TestExpansionFinalizerDeclaresMandatoryBuffering is design.md section 6: the
// pass declares OverflowReject with a 1 MiB default, and requirement 7.5 makes an
// out-of-range configured bound a generation-compilation failure rather than a
// silently degraded declaration.
func TestExpansionFinalizerDeclaresMandatoryBuffering(t *testing.T) {
	t.Parallel()

	t.Run("default_bound_and_reject_policy", func(t *testing.T) {
		t.Parallel()
		fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
		var declared toolcall.Finalizer = fin
		req, ok := declared.(toolcall.BufferingRequirement)
		if !ok {
			t.Fatal("the expansion finalizer must declare the shipped mandatory buffering capability")
		}
		spec := req.ToolCallBufferingRequirement()
		if spec.Overflow != toolcall.OverflowReject {
			t.Fatalf("design.md section 6 - overflow policy=%q want %q", spec.Overflow, toolcall.OverflowReject)
		}
		if spec.MaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("default bound=%d want %d", spec.MaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if !spec.DeclaresMandatoryBound() {
			t.Fatalf("the default declaration must be well formed: %+v", spec)
		}
	})

	t.Run("the_configurable_range_is_accepted", func(t *testing.T) {
		t.Parallel()
		for _, bound := range []int{
			toolcall.MinMandatoryMaxArgsBytes,
			toolcall.DefaultMandatoryMaxArgsBytes,
			toolcall.MaxMandatoryMaxArgsBytes,
		} {
			fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
				rewrite.ModeRewrite, expansion.Policy{MandatoryMaxArgsBytes: bound})
			if err != nil {
				t.Fatalf("bound %d must compile: %v", bound, err)
			}
			if got := fin.ToolCallBufferingRequirement().MaxArgsBytes; got != bound {
				t.Fatalf("declared bound=%d want %d", got, bound)
			}
		}
	})

	t.Run("an_out_of_range_bound_fails_generation_compilation", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			bound int
		}{
			{name: "negative", bound: -1},
			{name: "below_the_floor", bound: toolcall.MinMandatoryMaxArgsBytes - 1},
			{name: "above_the_ceiling", bound: toolcall.MaxMandatoryMaxArgsBytes + 1},
			{name: "wildly_out_of_range", bound: 1 << 40},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				spec := toolcall.BufferingSpec{MaxArgsBytes: tc.bound, Overflow: toolcall.OverflowReject}
				if err := spec.Validate(); err == nil {
					t.Fatalf("the boundary fixture must be out of range: %d", tc.bound)
				}
				fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
					rewrite.ModeRewrite, expansion.Policy{MandatoryMaxArgsBytes: tc.bound})
				if err == nil {
					t.Fatal("requirement 7.5 - an unsupported mandatory bound must fail generation compilation")
				}
				if fin != nil {
					t.Fatal("a failed compilation must not return a usable finalizer")
				}
				if !strings.Contains(err.Error(), "path_virtualization") {
					t.Fatalf("the failure must name the feature: %v", err)
				}
			})
		}
	})
}

// TestExpansionFinalizerOrdersAboveToolCallRepair is Task 7.3's hand-off
// evidence. requirements.md 8.4 requires mandatory expansion to receive valid
// completed JSON, and the whole mechanism is one number: the shipped
// tool-call-repair finalizer's DefaultFinalizerOrder plus one. This test imports
// the shipped repair feature to read that constant, exactly as Task 7.3's
// composition evidence does.
func TestExpansionFinalizerOrdersAboveToolCallRepair(t *testing.T) {
	t.Parallel()

	if expansion.FinalizerOrder != 41 {
		t.Fatalf("the declared order changed: %d want 41", expansion.FinalizerOrder)
	}
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	if fin.Order() != expansion.FinalizerOrder {
		t.Fatalf("Order()=%d want the declared %d", fin.Order(), expansion.FinalizerOrder)
	}
	if fin.Order() <= repair.DefaultFinalizerOrder {
		t.Fatalf("requirements.md 8.4 - the expansion order must sit strictly above tool-call repair: %d <= %d",
			fin.Order(), repair.DefaultFinalizerOrder)
	}
}

// TestExpansionFinalizerAuditModeMeasuresWithoutExpanding is requirement 7.3 for
// the inbound direction: audit runs the identical detection and publishes nothing,
// so the client sees what the rewrite would have seen and the operator sees the
// same numbers the rewrite would have produced.
func TestExpansionFinalizerAuditModeMeasuresWithoutExpanding(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	var reports []expansion.Report
	fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
		rewrite.ModeAudit, expansion.Policy{},
		expansion.WithReporter(func(r expansion.Report) { reports = append(reports, r) }))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	args := fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath)
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("requirement 7.3 - audit mode must publish nothing: action=%v", res.Action)
	}
	if res.ArgsJSON != nil {
		t.Fatalf("requirement 7.3 - audit mode must publish nothing: %q", res.ArgsJSON)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonAuditMode {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonAuditMode)
	}
	if len(reports) != 1 {
		t.Fatalf("reports=%d want 1", len(reports))
	}
	if reports[0].Stats.Eligible != 1 || reports[0].Stats.Rewritten != 1 {
		t.Fatalf("requirement 7.3 - audit must measure the rewrite's own numbers: %+v", reports[0].Stats)
	}
	// Requirement 9.5's savings are the OUTBOUND direction's number. The inbound
	// expansion necessarily grows the value it replaces, so the measured totals must be
	// the exact decoded lengths of the alias it read and the real path it would publish.
	expandedValue := fixture.realRoot + "/src/main.go"
	if reports[0].Stats.BytesBefore != len(fixture.aliasPath) {
		t.Fatalf("BytesBefore=%d want the alias length %d", reports[0].Stats.BytesBefore, len(fixture.aliasPath))
	}
	if reports[0].Stats.BytesAfter != len(expandedValue) {
		t.Fatalf("BytesAfter=%d want the expanded length %d", reports[0].Stats.BytesAfter, len(expandedValue))
	}

	// The rewrite-mode pass over the identical bytes must report identical
	// statistics, which is what makes an audit number a claim about the rewrite.
	rewriteReports := make([]expansion.Report, 0, 1)
	rewriteFin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
		rewrite.ModeRewrite, expansion.Policy{},
		expansion.WithReporter(func(r expansion.Report) { rewriteReports = append(rewriteReports, r) }))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	rewriteRes, err := rewriteFin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if rewriteRes.Action != toolcall.ActionRewrite {
		t.Fatalf("rewrite mode action=%v want rewrite", rewriteRes.Action)
	}
	if !sameStats(reports[0].Stats, rewriteReports[0].Stats) {
		t.Fatalf("requirement 7.3 - audit and rewrite must report the same statistics:\naudit  %+v\nrewrite %+v",
			reports[0].Stats, rewriteReports[0].Stats)
	}
}

// sameStats compares two content-free statistics values field for field, including the
// skip tallies, which a Go struct comparison cannot do because one holds a slice.
func sameStats(a, b rewrite.Stats) bool {
	if a.Eligible != b.Eligible || a.Rewritten != b.Rewritten ||
		a.BytesBefore != b.BytesBefore || a.BytesAfter != b.BytesAfter {
		return false
	}
	if len(a.Skips) != len(b.Skips) {
		return false
	}
	for i := range a.Skips {
		if a.Skips[i] != b.Skips[i] {
			return false
		}
	}
	return true
}

// TestExpansionFinalizerResolvesSelectorsAgainstTheDeclaredSchema walks design.md
// section 7 step 2 with the optional inference step enabled. Without a declared
// schema the tool resolves to no selector at all; with one, the same exact tool
// name resolves through inference and the alias is expanded.
func TestExpansionFinalizerResolvesSelectorsAgainstTheDeclaredSchema(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new inferrer: reject %v", reject)
	}
	inference, reject := pathvirtualization.NewResolver(nil, nil, inferrer)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	fin, err := expansion.NewFinalizer(inference, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	const schema = `{"type":"object","properties":{"path":{"type":"string"}}}`
	args := fmt.Sprintf(`{"path":%q}`, fixture.aliasPath)

	withoutSchema, err := fin.Finalize(context.Background(), expansionCall("inferred_tool", args),
		aipapi.ToolDef{Name: "inferred_tool"}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if withoutSchema.Action != toolcall.ActionPass {
		t.Fatalf("requirement 3.5 - an undeclared tool must resolve to no selector: action=%v",
			withoutSchema.Action)
	}

	withSchema, err := fin.Finalize(context.Background(), expansionCall("inferred_tool", args),
		aipapi.ToolDef{Name: "inferred_tool", Parameters: []byte(schema)}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if withSchema.Action != toolcall.ActionRewrite {
		t.Fatalf("the declared schema must resolve a selector: action=%v reason=%q",
			withSchema.Action, withSchema.ReasonCode)
	}

	// A near-miss tool name never receives another tool's schema.
	nearMiss, err := fin.Finalize(context.Background(), expansionCall("inferred_tool ", args),
		aipapi.ToolDef{Name: "inferred_tool", Parameters: []byte(schema)}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if nearMiss.Action != toolcall.ActionPass {
		t.Fatalf("requirement 3.6 - tool-name lookup must be byte-exact: action=%v", nearMiss.Action)
	}
}

// TestExpansionFinalizerReportsAreContentFree proves nothing a caller can observe
// about one decision carries a path, an alias, a workspace tag, a tool name, a
// pointer, or argument bytes.
func TestExpansionFinalizerReportsAreContentFree(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other: reason %v", reason)
	}

	// Report.RootReason is the one report field that can carry a root, because it is
	// a string-typed enum on the refusal paths. The case set therefore has to reach
	// the two branches that populate it and assert the closed code those branches
	// must record, or a report that substituted the raw project root for the code
	// would pass every content check on the cases that never set the field.
	relativeRoot := "relative/secret-workspace-xxxx"
	collisionRoot := "/.__aip_v1__/w_0123456789abcdefghij/proj"
	for _, tc := range []struct {
		args           string
		project        string
		wantRootReason pathvirtualization.SkipReason
	}{
		{
			args:    fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project: expansionProjectRoot,
		},
		{
			args:    fmt.Sprintf(`{"file_path":%q}`, other.VirtualRoot+"secret/payload.go"),
			project: expansionProjectRoot,
		},
		{
			args:    `{"file_path":"/.__aip_v1__/w_short/secret/payload.go"}`,
			project: expansionProjectRoot,
		},
		{
			args:    `{"file_path":"/home/dev/other/` + strings.Repeat("x", 64) + `.go"}`,
			project: expansionProjectRoot,
		},
		{
			args:           fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath),
			project:        relativeRoot,
			wantRootReason: pathvirtualization.SkipReasonRelativeRoot,
		},
		{
			args:           `{"file_path":"/home/dev/elsewhere/a.go"}`,
			project:        collisionRoot,
			wantRootReason: pathvirtualization.SkipReasonReservedNamespaceCollision,
		},
	} {
		var reports []expansion.Report
		fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/file_path"),
			rewrite.ModeRewrite, expansion.Policy{},
			expansion.WithReporter(func(r expansion.Report) { reports = append(reports, r) }))
		if err != nil {
			t.Fatalf("NewFinalizer: %v", err)
		}
		if _, ferr := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
			aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.project)); ferr != nil {
			t.Fatalf("Finalize returned a Go error: %v", ferr)
		}
		if len(reports) != 1 {
			t.Fatalf("reports=%d want 1", len(reports))
		}
		if reports[0].RootReason != tc.wantRootReason {
			t.Fatalf("the case must reach the branch it pins: RootReason=%q want %q",
				reports[0].RootReason, tc.wantRootReason)
		}
		rendered := fmt.Sprintf("%v|%v|%v|%v", reports[0].Outcome, reports[0].Reason,
			reports[0].RootReason, reports[0].Stats)
		for _, forbidden := range []string{
			expansionProjectRoot, expansionOtherRoot, fixture.virtualRoot, other.VirtualRoot,
			fixture.workspaceTag, other.WorkspaceTag, "w_", ".__aip_v1__",
			expansionToolName, "file_path", "payload.go", "xxxx", relativeRoot,
		} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("requirements.md 7.7 - a report leaked %q: %s", forbidden, rendered)
			}
		}
	}
}

// TestExpansionFinalizerReasonVocabularyIsClosedAndContentFree pins the bounded
// label set both as a metric dimension and as the string the assembler turns into
// a client-facing refusal, and proves the reason is a valid
// toolcall.Result.ReasonCode rather than free-form text.
func TestExpansionFinalizerReasonVocabularyIsClosedAndContentFree(t *testing.T) {
	t.Parallel()

	known := []struct {
		reason expansion.Reason
		want   string
	}{
		{reason: expansion.ReasonNone, want: ""},
		{reason: expansion.ReasonExpanded, want: "expanded"},
		{reason: expansion.ReasonAuditMode, want: "audit_mode"},
		{reason: expansion.ReasonNoAlias, want: "no_alias"},
		{reason: expansion.ReasonNoSelectors, want: "no_selectors"},
		{reason: expansion.ReasonArgsAbsent, want: "args_absent"},
		{reason: expansion.ReasonPayloadNotObject, want: "payload_not_object"},
		{reason: expansion.ReasonRootUnusable, want: "root_unusable"},
		{reason: expansion.ReasonMappingInactive, want: "mapping_inactive"},
		{reason: expansion.ReasonArgsUnparseable, want: "args_unparseable"},
		{reason: expansion.ReasonMalformedReservedAlias, want: "malformed_reserved_alias"},
		{reason: expansion.ReasonWorkspaceMismatch, want: "workspace_mismatch"},
		{reason: expansion.ReasonExpandedTooLarge, want: "expanded_too_large"},
		{reason: expansion.ReasonInvalidRewrite, want: "invalid_rewrite"},
	}
	for _, tc := range known {
		if got := tc.reason.String(); got != tc.want {
			t.Fatalf("Reason(%d).String()=%q want %q", int(tc.reason), got, tc.want)
		}
	}
	if got := expansion.Reason(200).String(); got != "unknown" {
		t.Fatalf("an undefined reason must degrade to a bounded label, got %q", got)
	}
	for _, tc := range known {
		if tc.reason == expansion.ReasonNone {
			continue
		}
		if !strings.HasPrefix(tc.want, "reason_") && strings.ContainsAny(tc.want, "/\\.:") {
			t.Fatalf("reason label %q is not a safe bounded token", tc.want)
		}
		for _, forbidden := range []string{".__aip_v1__", "w_", "/home", `\Users`, "C:", "file_path"} {
			if strings.Contains(tc.want, forbidden) {
				t.Fatalf("reason label %q leaks %q", tc.want, forbidden)
			}
		}
	}
}

// TestExpansionFinalizerNilAndZeroValuesAreInactive keeps the shipped type safe
// in the shapes a composition root can produce: a nil finalizer, a nil resolver,
// and a nil reporter must all behave as the required "no policy, nothing proven"
// answers rather than panicking or guessing.
func TestExpansionFinalizerNilAndZeroValuesAreInactive(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	args := fmt.Sprintf(`{"file_path":%q}`, fixture.aliasPath)

	var nilFin *expansion.Finalizer
	res, err := nilFin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("a nil finalizer must not surface a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("a nil finalizer must pass through: action=%v", res.Action)
	}
	if nilFin.ID() != expansion.FinalizerID {
		t.Fatalf("a nil finalizer must still report its stable identity, got %q", nilFin.ID())
	}
	if nilFin.Order() != expansion.FinalizerOrder {
		t.Fatalf("a nil finalizer must still report its declared order, got %d", nilFin.Order())
	}
	if spec := nilFin.ToolCallBufferingRequirement(); !spec.DeclaresMandatoryBound() {
		t.Fatalf("a nil finalizer must still declare the mandatory bound, got %+v", spec)
	}

	fin, err := expansion.NewFinalizer(nil, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("a nil resolver must compile: %v", err)
	}
	res, err = fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass || expansionReason(t, res.ReasonCode) != expansion.ReasonNoSelectors {
		t.Fatalf("a nil resolver must select nothing: action=%v reason=%q", res.Action, res.ReasonCode)
	}
}

// TestExpansionFinalizerDecisionsAreDeterministicAndIsolated proves one shared
// instance derives every decision from that call's own bytes and nothing else:
// repeating a call is byte-identical, and a call that refuses cannot recolour the
// reason a later, different refusal publishes.
//
// The isolation half is the part that is easy to lose. The refusal reason is
// recorded on per-call state created inside Finalize, and hoisting that state onto
// the shared instance - an obvious-looking allocation win for a pass that serves
// every request of a generation - makes the first refusal's reason stick: every
// later refusal is published under the earlier bounded label, so a client is told
// the wrong thing about its own call even though both labels are valid refusals.
// The reason a call is refused is client-visible, so the shared instance must
// carry no decision state between calls (requirements.md 6.1, 6.5).
func TestExpansionFinalizerDecisionsAreDeterministicAndIsolated(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	other, reason := pathvirtualization.DeriveMapping(expansionOtherRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other root: reason %v", reason)
	}
	if other.WorkspaceTag == fixture.workspaceTag {
		t.Fatal("the two fixture roots must derive different workspace tags")
	}
	args := fmt.Sprintf(`{"file_path":%q,"paths":[%q],"content":"kept"}`,
		fixture.aliasPath, fixture.virtualRoot+"pkg/other.go")
	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path", "/paths"))
	meta := expansionMeta(expansionProjectRoot)
	tool := aipapi.ToolDef{Name: expansionToolName}

	first, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args), tool, nil, meta)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if first.Action != toolcall.ActionRewrite {
		t.Fatalf("non-vacuity: the fixture call must expand, got action=%v reason=%q",
			first.Action, first.ReasonCode)
	}
	for range 5 {
		again, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args), tool, nil, meta)
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if again.Action != first.Action || again.ReasonCode != first.ReasonCode ||
			again.ToolName != first.ToolName || !bytes.Equal(again.ArgsJSON, first.ArgsJSON) {
			t.Fatalf("a repeated decision differed:\nfirst  %+v\nsecond %+v", first, again)
		}
	}

	// Both orders are exercised: whichever refusal runs first must not become the
	// reason the second one publishes. The subtests stay sequential so the sequence
	// under test is the sequence the test names.
	stale := toolcall.CompletedCall{
		ToolCallID: "call-stale",
		ToolName:   expansionToolName,
		ArgsJSON:   fmt.Appendf(nil, `{"file_path":%q}`, other.VirtualRoot+"src/main.go"),
	}
	malformed := toolcall.CompletedCall{
		ToolCallID: "call-malformed",
		ToolName:   expansionToolName,
		ArgsJSON:   []byte(`{"file_path":"/.__aip_v1__/w_short/src/main.go"}`),
	}
	for _, order := range []struct {
		name         string
		a, b         toolcall.CompletedCall
		wantA, wantB expansion.Reason
	}{
		{
			name:  "stale_before_malformed",
			a:     stale,
			b:     malformed,
			wantA: expansion.ReasonWorkspaceMismatch,
			wantB: expansion.ReasonMalformedReservedAlias,
		},
		{
			name:  "malformed_before_stale",
			a:     malformed,
			b:     stale,
			wantA: expansion.ReasonMalformedReservedAlias,
			wantB: expansion.ReasonWorkspaceMismatch,
		},
	} {
		t.Run(order.name, func(t *testing.T) {
			for _, tc := range []struct {
				call toolcall.CompletedCall
				want expansion.Reason
			}{{order.a, order.wantA}, {order.b, order.wantB}} {
				res, err := fin.Finalize(context.Background(), tc.call, tool, nil, meta)
				if err != nil {
					t.Fatalf("Finalize: %v", err)
				}
				if res.Action != toolcall.ActionReject {
					t.Fatalf("non-vacuity: %s must refuse, got action=%v reason=%q",
						tc.call.ToolCallID, res.Action, res.ReasonCode)
				}
				if got := expansionReason(t, res.ReasonCode); got != tc.want {
					t.Fatalf("call %s published reason %q, want %q: one shared instance must not "+
						"carry a previous call's refusal reason", tc.call.ToolCallID, got, tc.want)
				}
				if res.ArgsJSON != nil {
					t.Fatalf("a refusal must publish nothing: %q", res.ArgsJSON)
				}
			}
			// The refusal state cannot leak into a later accepted call either.
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args), tool, nil, meta)
			if err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if res.Action != toolcall.ActionRewrite || expansionReason(t, res.ReasonCode) != expansion.ReasonExpanded {
				t.Fatalf("a call after a refusal must still expand, got action=%v reason=%q",
					res.Action, res.ReasonCode)
			}
		})
	}
}

// expansionCaseMarkerForms enumerates the case spellings of the reserved marker an
// unreadable argument document may carry.
//
// The set is the observable half of one rule: the lexical core recognizes an alias-root
// marker under a Windows flavor's ASCII-case-insensitive comparison, so an argument
// document may spell this build's own reserved root in any of these spellings. A finalizer
// whose byte scan matched the marker exactly reported such a document as carrying no
// reserved namespace and answered ActionPass with no_alias, which released the very
// namespace requirements.md 4.4 and 8.3 forbid from reaching the client.
var expansionCaseMarkerForms = map[string]string{
	"canonical":   ".__aip_v1__",
	"all_upper":   ".__AIP_V1__",
	"first":       ".__Aip_v1__",
	"trailing":    ".__aip_V1__",
	"alternating": ".__AiP_V1__",
}

// expansionUnreadableTag is a syntactically valid workspace tag segment. It never has to
// name a real workspace: the scan decides only whether the bytes spell the namespace.
const expansionUnreadableTag = "0123456789abcdefghij"

// TestExpansionFinalizerRefusesCaseVariedReservedNamespaceInUnreadableArguments is the
// end-to-end half of the case-folding requirement.
//
// Every fixture is an UNREADABLE argument document for a tool whose policy names an
// argument location, which is the one state where no parseable recognizer runs and the
// byte scan is the only recognizer there is. The document is deliberately truncated after
// the alias so repair declining it is a normal outcome rather than a contrivance.
func TestExpansionFinalizerRefusesCaseVariedReservedNamespaceInUnreadableArguments(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	for name, marker := range expansionCaseMarkerForms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				form   string
				layout string
			}{
				{form: "posix_alias_root", layout: `{"file_path":"/%s/w_` + expansionUnreadableTag + `/src/main.go","content":"unterminated`},
				{form: "windows_drive_alias", layout: `{"file_path":"C:\home\dev\%s\w_` + expansionUnreadableTag + `\src\main.go","content":"unterminated`},
				{form: "malformed_tag", layout: `{"file_path":"/%s/w_short/src/main.go","content":"unterminated`},
				{form: "escaped_case_folded_marker", layout: `{"file_path":"/.\u005f\u005f\u0041\u0049\u0050\u005f\u0056\u0031\u005f\u005f/w_` + expansionUnreadableTag + `/src/main.go","content":"unterminated`},
			} {
				if name != "canonical" && tc.form != "escaped_case_folded_marker" {
					// The escaped fixture spells one fixed folding; folding it again
					// would spell bytes the core never emits.
					tc.layout = strings.ReplaceAll(tc.layout, ".__aip_v1__", marker)
				}
				args := fmt.Sprintf(tc.layout, marker)
				if json.Valid([]byte(args)) {
					t.Fatal("the fixture must be unparseable JSON")
				}
				res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
					aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
				if err != nil {
					t.Fatalf("form %q: Finalize returned a Go error: %v", tc.form, err)
				}
				if res.Action != toolcall.ActionReject {
					t.Errorf("form %q: action=%v want reject (reason %q)",
						tc.form, res.Action, res.ReasonCode)
					continue
				}
				if expansionReason(t, res.ReasonCode) != expansion.ReasonArgsUnparseable {
					t.Errorf("form %q: reason=%q want %q", tc.form, res.ReasonCode, expansion.ReasonArgsUnparseable)
				}
				if res.ArgsJSON != nil {
					t.Errorf("form %q: a refusal must publish nothing", tc.form)
				}
			}
		})
	}
}

// TestExpansionFinalizerPassesCaseVariedOrdinaryNamesInUnreadableArguments is the
// near-miss half, and it is what keeps the fold from becoming a blanket refusal of any
// unreadable document whose text resembles the namespace.
//
// A real directory can be named after the namespace and a real file can begin with it, so
// each fixture here spells the marker inside or before a longer ordinary NAME. Recognition
// requires the marker to occupy a complete segment, so every one of them stays an ordinary
// path in every case spelling and requirement 4.7's pass-through is preserved.
func TestExpansionFinalizerPassesCaseVariedOrdinaryNamesInUnreadableArguments(t *testing.T) {
	t.Parallel()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
	for name, marker := range expansionCaseMarkerForms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				form   string
				layout string
			}{
				{form: "directory_named_by_a_prefix", layout: `{"file_path":"/home/dev/my%s/src/main.go","content":"unterminated`},
				{form: "member_extended_by_a_suffix", layout: `{"file_path":"/home/dev%sw_` + expansionUnreadableTag + `/src/main.go","content":"unterminated`},
				{form: "prose_mention_inside_content", layout: `{"file_path":"/home/dev/other/a.go","content":"see /home/dev/my%s/ for details`},
				{form: "sibling_with_a_folded_name", layout: `{"file_path":"/home/dev/%s_other/src/main.go","content":"unterminated`},
			} {
				args := fmt.Sprintf(tc.layout, marker)
				if json.Valid([]byte(args)) {
					t.Fatal("the fixture must be unparseable JSON")
				}
				res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, args),
					aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
				if err != nil {
					t.Fatalf("form %q: Finalize returned a Go error: %v", tc.form, err)
				}
				if res.Action != toolcall.ActionPass {
					t.Errorf("form %q: action=%v want pass (reason %q)", tc.form, res.Action, res.ReasonCode)
					continue
				}
				if expansionReason(t, res.ReasonCode) != expansion.ReasonNoAlias {
					t.Errorf("form %q: reason=%q want %q", tc.form, res.ReasonCode, expansion.ReasonNoAlias)
				}
				if res.ArgsJSON != nil {
					t.Errorf("form %q: a pass-through must publish nothing", tc.form)
				}
			}
		})
	}
}

// overLimitProjectRoot is a LONG synthetic workspace root, built so its derived alias is
// far SHORTER than the root it replaces.
//
// The direction of the inequality is the whole point of this fixture. Expansion substitutes
// the alias for the root, so a document grows past any bound only when the root it is
// expanded INTO is longer than the alias it came from - a short root would make expansion
// shrink every selected value and the case could never be built.
var overLimitProjectRoot = "/synthetic/build-agent/workspaces/" +
	strings.Repeat("deeply-nested-workspace-directory/", 16) + "worktree"

// overLimitAliases is how many selected aliases the over-limit fixture carries.
//
// It is chosen from the measured sizes rather than from a round number: at this count the
// expansion lands above the canonical delta bound while the INPUT document stays below the
// default mandatory argument bound, which is the only shape in which the defect is
// reachable at all. The test asserts both sides of that pair rather than trusting the
// arithmetic, so a fixture that stopped demonstrating the case fails loudly.
const overLimitAliases = 16000

// overLimitDocument builds one readable argument document carrying overLimitAliases
// selected aliases under a single array-of-strings location, plus a short sibling
// location, and returns it with the fixture's derived facts.
func overLimitDocument(tb testing.TB) (string, pathvirtualization.Mapping) {
	tb.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(overLimitProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		tb.Fatalf("derive: reason %v", reason)
	}
	if mapping.VirtualRoot == "" {
		tb.Fatal("the over-limit fixture root must derive an active alias (requirement 1.4)")
	}
	var doc strings.Builder
	doc.Grow(overLimitAliases * (len(mapping.VirtualRoot) + 4))
	doc.WriteString(`{"file_path":"`)
	doc.WriteString(mapping.VirtualRoot)
	doc.WriteString(`src/main.go","paths":[`)
	for i := range overLimitAliases {
		if i > 0 {
			doc.WriteByte(',')
		}
		doc.WriteByte('"')
		doc.WriteString(mapping.VirtualRoot)
		doc.WriteString(`pkg/module_`)
		doc.WriteString(strconv.Itoa(i))
		doc.WriteString(`.go"`)
	}
	doc.WriteString(`]}`)
	return doc.String(), mapping
}

// TestExpansionFinalizerRefusesAnExpansionPastTheCanonicalEnvelopeBound is the guard for
// the one thing the publication step was missing.
//
// The assembler publishes a finalizer's rewritten arguments as ONE canonical tool-call
// args delta, and canonical event validation bounds that delta. Syntax validity is not
// enough: a document of selected aliases can be entirely valid JSON and still expand past
// the bound, because expansion substitutes a long real root for a short alias in every
// selected element at once. Publishing that document hands the assembler a lifecycle the
// runtime itself will reject, and the call fails downstream of the feature that produced it.
//
// So the output is bounded before it is published. The bound is the canonical one the
// runtime enforces rather than a number chosen here, and the answer is a bounded refusal:
// the call fails closed under its own closed reason vocabulary, never a partially expanded
// document and never an over-limit delta.
func TestExpansionFinalizerRefusesAnExpansionPastTheCanonicalEnvelopeBound(t *testing.T) {
	t.Parallel()

	document, mapping := overLimitDocument(t)
	if !json.Valid([]byte(document)) {
		t.Fatal("the fixture must be a readable argument document")
	}
	if len(document) >= toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("fixture input is %d bytes, which must stay under the %d-byte default "+
			"mandatory bound for this case to be reachable",
			len(document), toolcall.DefaultMandatoryMaxArgsBytes)
	}
	// NON-VACUITY: the fixture must actually expand past the bound, or this test would
	// pass against a pass that never expanded anything.
	expandedSize := len(document) - len(mapping.VirtualRoot)*overLimitAliases +
		(len(mapping.RealRoot)-len(mapping.VirtualRoot))*overLimitAliases
	if expandedSize <= aipapi.MaxEventDeltaBytes {
		t.Fatalf("fixture expansion is about %d bytes, which must exceed the %d-byte canonical "+
			"delta bound for this case to be reachable", expandedSize, aipapi.MaxEventDeltaBytes)
	}

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path", "/paths"))
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, document),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(overLimitProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject for an over-limit expansion (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpandedTooLarge {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpandedTooLarge)
	}
	if res.ArgsJSON != nil {
		t.Fatal("an over-limit expansion must publish nothing")
	}
	// The refusal the runtime builds from the reason code must itself be publishable,
	// which is what keeps the bounded label off the oversized path.
	if res.ReasonCode != expansion.ReasonExpandedTooLarge.String() {
		t.Fatal("the published reason code must be the bounded label")
	}
	if len(res.ReasonCode) > aipapi.MaxEventCodeFieldBytes {
		t.Fatal("the bounded reason code exceeds the canonical code-field bound")
	}
}

// TestExpansionFinalizerPublishesAnExpansionInsideTheCanonicalEnvelopeBound is the
// control that keeps the bound from refusing everything: a document that expands within
// the canonical bound is still published, with the expanded bytes, exactly as before.
//
// A guard that only ever sees refusals is satisfied by a pass that refuses all expansions,
// so the positive case has to be pinned next to the negative one on the same fixture
// family and the same tool.
func TestExpansionFinalizerPublishesAnExpansionInsideTheCanonicalEnvelopeBound(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(overLimitProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive: reason %v", reason)
	}
	// One hundredth of the over-limit fixture: the same shape, the same alias, and an
	// expansion far inside the bound.
	const aliases = overLimitAliases / 100
	var doc strings.Builder
	doc.WriteString(`{"paths":[`)
	for i := range aliases {
		if i > 0 {
			doc.WriteByte(',')
		}
		doc.WriteByte('"')
		doc.WriteString(mapping.VirtualRoot)
		doc.WriteString(`pkg/module_`)
		doc.WriteString(strconv.Itoa(i))
		doc.WriteString(`.go"`)
	}
	doc.WriteString(`]}`)
	document := doc.String()

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, document),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(overLimitProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite for an in-bound expansion (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpanded {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpanded)
	}
	if len(res.ArgsJSON) > aipapi.MaxEventDeltaBytes {
		t.Fatalf("the published document is %d bytes, which must stay inside the %d-byte "+
			"canonical delta bound", len(res.ArgsJSON), aipapi.MaxEventDeltaBytes)
	}
	if !bytes.Contains(res.ArgsJSON, []byte(mapping.RealRoot)) {
		t.Fatal("the published document must hold the expanded real root")
	}
	if bytes.Contains(res.ArgsJSON, []byte(mapping.VirtualRoot)) {
		t.Fatal("the published document must hold no unexpanded alias")
	}
	if !rewrite.PublishedJSONValid(res.ArgsJSON) {
		t.Fatal("the published document must be exactly one complete JSON value")
	}
}

// TestExpansionFinalizerAuditModeReportsTheOverLimitConditionWithoutRefusing keeps the
// bound inside the pass's own publication rule.
//
// Requirement 7.3 asks audit mode to run the IDENTICAL detection and publish nothing, so
// the bound is a publication decision and not a detection one: an audit pass measures the
// same over-limit document and reports the ordinary audit reason rather than failing the
// call. This is what proves the bound did not quietly become a detection rule that refuses
// calls in a mode whose whole contract is to measure them.
func TestExpansionFinalizerAuditModeReportsTheOverLimitConditionWithoutRefusing(t *testing.T) {
	t.Parallel()

	document, _ := overLimitDocument(t)
	fin, err := expansion.NewFinalizer(
		expansionResolver(t, expansionToolName, "/file_path", "/paths"),
		rewrite.ModeAudit, expansion.Policy{})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, document),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(overLimitProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionPass {
		t.Fatalf("action=%v want pass in audit mode (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpandedTooLarge {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpandedTooLarge)
	}
	if res.ArgsJSON != nil {
		t.Fatal("audit mode must publish nothing")
	}
}

// TestExpansionFinalizerFailsClosedWhenAnUnusableRootCannotResolveAnAlias is
// requirement 4.4 on the branch the pass used to take unconditionally.
//
// Deriving the workspace mapping happens BEFORE selector resolution, so an
// unusable project root returned a pass for every argument. That is right for an
// ordinary path - requirement 4.7 keeps a call with no virtual root on its
// existing pass-through - and wrong for an argument carrying the reserved
// namespace, which no mapping can resolve and which therefore reached the client
// unresolved. The distinction the payload makes, not where the alias came from.
//
// Each case is stated as a property of the ACTION, because the action is the
// fail-closed invariant; the reason is asserted too so a future change cannot
// pass by refusing for an unrelated reason.
func TestExpansionFinalizerFailsClosedWhenAnUnusableRootCannotResolveAnAlias(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(expansionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive fixture root: reason %v", reason)
	}
	// A well-formed alias of a root this build CAN derive, so the only thing
	// stopping it from expanding is the empty/relative root under test.
	alias := mapping.VirtualRoot + "src/main.go"
	malformedAlias := "/.__aip_v1__/w_short/src/main.go"

	for _, tc := range []struct {
		name       string
		args       string
		project    string
		wantAction toolcall.Action
		wantReason expansion.Reason
	}{
		{
			name:       "well_formed_alias_with_no_project_root",
			args:       `{"file_path":` + quote(alias) + `}`,
			project:    "",
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "well_formed_alias_with_relative_project_root",
			args:       `{"file_path":` + quote(alias) + `}`,
			project:    "relative/project",
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "malformed_alias_with_no_project_root",
			args:       `{"file_path":` + quote(malformedAlias) + `}`,
			project:    "",
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "malformed_alias_malformed_payload",
			args:       `{"file_path":` + quote(malformedAlias),
			project:    "",
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			// Requirement 4.7: with no reserved namespace present there is nothing to
			// protect, so the pre-existing pass must survive this change untouched.
			name:       "ordinary_path_with_no_project_root_still_passes",
			args:       `{"file_path":"/home/dev/somewhere-else/a.go"}`,
			project:    "",
			wantAction: toolcall.ActionPass,
			wantReason: expansion.ReasonRootUnusable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
				aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != tc.wantAction {
				t.Fatalf("action=%v want %v (reason %q)", res.Action, tc.wantAction, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no branch here may publish a document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}

// TestExpansionFinalizerUnusableRootPassesADocumentWithNoMappableAlias states
// requirement 4.7 on the shapes the unusable-root branch can read: when the project
// root yields no mapping, a selected call whose document is unreadable WITHOUT
// carrying the reserved marker, is not an object, holds no selected leaf, or carries
// no arguments at all must still pass through as root_unusable. The refusal
// requirement 4.4 demands is owed only when the reserved namespace is actually
// present, so the whole-payload marker scan of an unreadable document must not widen
// into "refuse every unreadable call".
func TestExpansionFinalizerUnusableRootPassesADocumentWithNoMappableAlias(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args string
	}{
		{
			// No structure to walk, no marker to protect: the payload cannot be
			// refused just because it cannot be read.
			name: "unparseable_without_the_marker",
			args: `{"file_path":"/home/dev/elsewhere/a.go`,
		},
		{
			name: "payload_is_not_an_object",
			args: `["/home/dev/elsewhere/a.go"]`,
		},
		{
			name: "object_holds_no_selected_leaf",
			args: `{"other":"value"}`,
		},
		{
			name: "arguments_absent",
			args: ``,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
				aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(""))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != toolcall.ActionPass {
				t.Fatalf("requirement 4.7 - action=%v want pass (reason %q)", res.Action, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no branch here may publish a document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != expansion.ReasonRootUnusable {
				t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonRootUnusable)
			}
		})
	}
}

// quote renders one JSON string literal for the table above.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestFinalizerToolCallBufferingAppliesMatchesTheSelectorsItReads is requirement
// 4.6 and 4.7 on the shipped pass, and the reason the capability exists at all.
//
// The assembler reads applicability BEFORE the arguments are complete, so a pass
// that does not answer is assumed to govern every call it is invoked for. That
// made enabling path virtualization refuse unrelated oversized tool calls: this
// pass had declared a whole-document completeness requirement, and the assembler
// had no way to learn the requirement only concerns calls with a path surface.
//
// The answer has to be the SAME question the pass asks itself when it runs, or the
// bound that gets buffered and the pass that consumes it disagree about which
// calls they cover. So this asserts the two agree rather than restating the rule:
// for each tool, whether the pass applies must equal whether decide() would find
// an argument location to read.
func TestFinalizerToolCallBufferingAppliesMatchesTheSelectorsItReads(t *testing.T) {
	t.Parallel()

	selected := expansionToolName
	// A tool this pass has no policy for at all: no explicit, built-in, or inferred
	// argument location. A mandatory bound governing it would refuse an unrelated
	// call for a feature that never touches it.
	unselected := "custom_tool_with_no_path_surface"

	fin := newFinalizer(t, expansionResolver(t, selected, "/file_path"))
	applier, ok := any(fin).(toolcall.BufferingApplicability)
	if !ok {
		t.Fatal("the shipped pass must publish its applicability, or the assembler keeps the assembler-wide reading")
	}

	for _, tc := range []struct {
		name    string
		tool    string
		schema  []byte
		want    bool
		explain string
	}{
		{
			name:    "selected_tool_applies",
			tool:    selected,
			want:    true,
			explain: "a tool with an argument location is the only case this pass reads",
		},
		{
			name:    "unselected_tool_does_not_apply",
			tool:    unselected,
			want:    false,
			explain: "requirement 4.7 - a call with no path surface keeps existing pass-through behavior",
		},
		{
			name:    "empty_tool_name_does_not_apply",
			tool:    "",
			want:    false,
			explain: "no name resolves no selector",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := applier.ToolCallBufferingApplies(tc.tool, aipapi.ToolDef{Name: tc.tool, Parameters: tc.schema}, nil)
			if got != tc.want {
				t.Fatalf("applies=%v want %v: %s", got, tc.want, tc.explain)
			}
		})
	}
}

// TestFinalizerWithNoResolverAppliesToNothing pins the degenerate composition
// root. A pass holding no policy selects nothing when it runs, so it must not
// claim a mandatory bound over every call either.
func TestFinalizerWithNoResolverAppliesToNothing(t *testing.T) {
	t.Parallel()

	var fin *expansion.Finalizer
	if fin.ToolCallBufferingApplies(expansionToolName, aipapi.ToolDef{Name: expansionToolName}, nil) {
		t.Fatal("a nil pass resolves no selector, so it must not claim every call")
	}
}

// TestExpansionFinalizerUnusableRootChecksSelectorsBeforeTheWholePayload is
// requirement 4.9 on the unusable-root branch.
//
// An unusable project root means no mapping, not a license to inspect every
// field. A tool with no argument selector is outside this pass whether or not
// an ordinary content field happens to spell the marker, so it must keep
// ReasonNoSelectors pass-through. A selected tool with the marker only in an
// unselected content field must keep the unusable-root pass, because the
// marker is not in a path-bearing argument.
func TestExpansionFinalizerUnusableRootChecksSelectorsBeforeTheWholePayload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		tool       string
		args       string
		wantReason expansion.Reason
	}{
		{
			name:       "unselected_tool_with_alias_shaped_content_passes",
			tool:       "custom_tool",
			args:       `{"note":"/.__aip_v1__/w_short/src/main.go"}`,
			wantReason: expansion.ReasonNoSelectors,
		},
		{
			name:       "selected_tool_with_marker_only_in_unselected_content_passes",
			tool:       expansionToolName,
			args:       `{"file_path":"/home/dev/elsewhere/a.go","content":"/.__aip_v1__/w_short/src/main.go"}`,
			wantReason: expansion.ReasonRootUnusable,
		},
		{
			name:       "selected_tool_with_marker_in_selected_leaf_refuses",
			tool:       expansionToolName,
			args:       `{"file_path":"/.__aip_v1__/w_short/src/main.go","content":"kept"}`,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			res, err := fin.Finalize(context.Background(), expansionCall(tc.tool, tc.args),
				aipapi.ToolDef{Name: tc.tool}, nil, expansionMeta(""))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if tc.wantReason == expansion.ReasonNoSelectors || tc.wantReason == expansion.ReasonRootUnusable {
				if res.Action != toolcall.ActionPass {
					t.Fatalf("action=%v want pass (reason %q)", res.Action, res.ReasonCode)
				}
			} else if res.Action != toolcall.ActionReject {
				t.Fatalf("action=%v want reject (reason %q)", res.Action, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no branch here may publish a document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}

// TestExpansionFinalizerUnusableRootKindsKeepTheAliasPolicySeparate pins the
// four root cases the adversarial review named: empty, relative,
// malformed/device, and reserved-namespace collision roots.
//
// Empty, relative, malformed-volume, and device roots all mean no mapping, so
// a selected alias-bearing argument fails closed. A collision root means the
// project genuinely occupies the reserved namespace, so requirement 1.8
// disables virtualization and the call passes as an ordinary real path.
func TestExpansionFinalizerUnusableRootKindsKeepTheAliasPolicySeparate(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(expansionProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive fixture root: reason %v", reason)
	}
	alias := mapping.VirtualRoot + "src/main.go"
	collisionRoot := "/.__aip_v1__/w_0123456789abcdefghij/proj"
	if _, got := pathvirtualization.DeriveMapping(collisionRoot); got != pathvirtualization.SkipReasonReservedNamespaceCollision {
		t.Fatalf("fixture collision root reason = %q, want reserved_namespace_collision", got)
	}

	for _, tc := range []struct {
		name       string
		args       string
		project    string
		wantAction toolcall.Action
		wantReason expansion.Reason
	}{
		{
			name:       "malformed_volume_root_with_alias_refuses",
			args:       `{"file_path":` + quote(alias) + `}`,
			project:    "C:",
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "device_root_with_alias_refuses",
			args:       `{"file_path":` + quote(alias) + `}`,
			project:    `\\.\PIPE\aip`,
			wantAction: toolcall.ActionReject,
			wantReason: expansion.ReasonWorkspaceMismatch,
		},
		{
			name:       "collision_root_with_alias_passes_as_real_path",
			args:       `{"file_path":` + quote(alias) + `}`,
			project:    collisionRoot,
			wantAction: toolcall.ActionPass,
			wantReason: expansion.ReasonRootUnusable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/file_path"))
			res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, tc.args),
				aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(tc.project))
			if err != nil {
				t.Fatalf("Finalize returned a Go error: %v", err)
			}
			if res.Action != tc.wantAction {
				t.Fatalf("action=%v want %v (reason %q)", res.Action, tc.wantAction, res.ReasonCode)
			}
			if res.ArgsJSON != nil {
				t.Fatalf("no branch here may publish a document: %q", res.ArgsJSON)
			}
			if expansionReason(t, res.ReasonCode) != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}

// TestExpansionFinalizerRefusesOverLimitWithoutBuildingTheDocument is the
// allocation half of the output bound.
//
// Refusing an over-limit expansion only closes the resource defect if the
// refusal does not first pay for the document it refuses. A selected array of
// repeated aliases expands each element at once, so building the published
// document before checking its size allocates the whole over-limit output - on
// the order of hundreds of megabytes for a document still inside the mandatory
// argument bound. The splice therefore aborts as soon as the output exceeds
// the canonical delta limit, and this test measures that in allocated bytes
// rather than wall-clock time, so it is host- and timing-independent.
//
// The bound is deliberately generous, and it is a ratchet on the SHAPE of the
// abort rather than on its exact byte count. The unfixed code allocates the full
// expanded document, well past 80 MB on this fixture. Deciding a document before
// building it measures 26 MB here: the decisions themselves, which cannot be
// avoided, and no output buffer at all. 40 MiB sits above that and far below the
// document being refused, so it fails if the build moves back into the abort path
// without making the fixture itself the fragile thing.
func TestExpansionFinalizerRefusesOverLimitWithoutBuildingTheDocument(t *testing.T) {
	// Not parallel: TotalAlloc is process-wide, like AllocsPerRun.
	root := "/" + strings.Repeat("workspace-directory-", 200) + "worktree"
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive: reason %v", reason)
	}
	if mapping.VirtualRoot == "" {
		t.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	const argsBytes = 960 * 1024
	var doc strings.Builder
	doc.WriteString(`{"paths":[`)
	first := true
	for doc.Len() < argsBytes {
		if !first {
			doc.WriteByte(',')
		}
		first = false
		doc.WriteByte('"')
		doc.WriteString(mapping.VirtualRoot)
		doc.WriteString(`pkg/module.go"`)
	}
	doc.WriteString(`]}`)
	document := doc.String()
	if len(document) >= toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("fixture input is %d bytes, which must stay under the %d-byte default mandatory bound",
			len(document), toolcall.DefaultMandatoryMaxArgsBytes)
	}

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, document),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(root))
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionReject {
		t.Fatalf("action=%v want reject for an over-limit expansion (reason %q)", res.Action, res.ReasonCode)
	}
	if expansionReason(t, res.ReasonCode) != expansion.ReasonExpandedTooLarge {
		t.Fatalf("reason=%q want %q", res.ReasonCode, expansion.ReasonExpandedTooLarge)
	}
	if res.ArgsJSON != nil {
		t.Fatal("an over-limit expansion must publish nothing")
	}
	const maxAllocBytes = 40 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > maxAllocBytes {
		t.Fatalf("refusing the over-limit expansion allocated %d bytes, want at most %d",
			allocated, maxAllocBytes)
	}
}

// TestExpansionFinalizerKeepsExactNameAuthorityOverNearMissSpellings guards
// requirements.md 3.6 at the one place it can actually be lost.
//
// Built-in profiles are EXACT-NAME profiles and the resolver's lookup is
// byte-equality, so a spelling that is merely a near miss of a profiled tool is
// a DIFFERENT tool and must resolve to ReasonNoSelectors. That property is easy
// to state and easy to lose one layer out: the near-miss case that
// TestExpansionFinalizerResolvesSelectorsAgainstTheDeclaredSchema covers passes an
// EMPTY catalog, so there is nothing there for any lookup to canonicalize
// against, and the assertion holds whether or not one exists.
//
// A populated catalog is what makes it bite. Tool-call repair treats a unique
// normalized spelling as the same tool, so `read-file` normalizes onto a catalog
// entry named `read_file`. Resolving the call's identity that way before handing
// the name to the resolver silently widened this feature's authority from "the
// exact tools an operator named" to "any name repair could hypothetically have
// rewritten" - with repair disabled, and with repair enabled but declining the
// call, a near-miss tool would inherit another tool's selectors, and therefore
// its expansion, refusal, and buffering policy.
//
// The first half is the non-vacuity control: the SAME fixture under the EXACT
// name does expand, so a pass in the second half cannot come from a resolver that
// simply never matches anything.
func TestExpansionFinalizerKeepsExactNameAuthorityOverNearMissSpellings(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	const (
		profiledTool = "read_file"
		nearMissTool = "read-file"
	)
	// A catalog that makes the normalization UNIQUE, which is the only shape in
	// which canonicalization can resolve it at all.
	catalog := []aipapi.ToolDef{{
		Name:       profiledTool,
		Parameters: []byte(`{"type":"object"}`),
	}}
	fin := newFinalizer(t, expansionResolver(t, profiledTool, "/path"))
	args := fmt.Sprintf(`{"path":%q}`, fixture.aliasPath)

	// CONTROL: the exact catalog name resolves this profile and expands.
	exact, err := fin.Finalize(context.Background(), expansionCall(profiledTool, args),
		aipapi.ToolDef{Name: profiledTool}, catalog, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if exact.Action != toolcall.ActionRewrite {
		t.Fatalf("non-vacuity: the exact catalog name must expand, got action=%v reason=%q",
			exact.Action, exact.ReasonCode)
	}
	if expansionReason(t, exact.ReasonCode) != expansion.ReasonExpanded {
		t.Fatalf("non-vacuity: reason=%q want %q", exact.ReasonCode, expansion.ReasonExpanded)
	}

	// The near-miss spelling is a different tool. The assembler hands over the
	// catalog entry for the EXACT name it looked up, which for a name the catalog
	// does not carry is the zero definition.
	nearMiss, err := fin.Finalize(context.Background(), expansionCall(nearMissTool, args),
		aipapi.ToolDef{}, catalog, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if got := expansionReason(t, nearMiss.ReasonCode); got != expansion.ReasonNoSelectors {
		t.Fatalf("requirements.md 3.6 - %q is a different tool from %q and must stay unprofiled, got %q",
			nearMissTool, profiledTool, got)
	}
	if nearMiss.Action != toolcall.ActionPass {
		t.Fatalf("requirements.md 3.6 - an unprofiled tool must pass through untouched, got action=%v",
			nearMiss.Action)
	}
	if nearMiss.ArgsJSON != nil {
		t.Fatal("requirements.md 4.7 - an unprofiled tool must publish nothing")
	}

	// A declared schema does not reopen it either, and the case needs inference
	// ENABLED to say so: a resolver without an inferrer ignores every schema, so a
	// pass there would prove nothing. The schema below belongs to the PROFILED tool,
	// which is what the assembler supplies when it looks a call up under the name it
	// believes the call has. Offering that schema on behalf of the near-miss call
	// would hand the near-miss tool another tool's argument location, so
	// declaredSchema's exact-name comparison is what keeps requirement 3.6 intact.
	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new inferrer: reject %v", reject)
	}
	profiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{profiledTool},
		ArgPointers: []string{"/path"},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compile profiles: reject %v", reject)
	}
	inferring, reject := pathvirtualization.NewResolver(profiled, nil, inferrer)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	inferringFin, err := expansion.NewFinalizer(inferring, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	profiledSchema := []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	// NON-VACUITY: the same schema DOES resolve when the definition names the call's
	// own tool, so the refusal below cannot come from an inert inference fixture.
	ownSchema, err := inferringFin.Finalize(context.Background(), expansionCall(nearMissTool, args),
		aipapi.ToolDef{Name: nearMissTool, Parameters: profiledSchema},
		catalog, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if ownSchema.Action != toolcall.ActionRewrite {
		t.Fatalf("non-vacuity: the call's own declared schema must resolve a selector, got action=%v reason=%q",
			ownSchema.Action, ownSchema.ReasonCode)
	}
	schemaInferred, err := inferringFin.Finalize(context.Background(), expansionCall(nearMissTool, args),
		aipapi.ToolDef{Name: profiledTool, Parameters: profiledSchema},
		catalog, expansionMeta(expansionProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if schemaInferred.Action != toolcall.ActionPass {
		t.Fatalf("requirements.md 3.6 - a near-miss name must not inherit another tool's schema "+
			"through inference, got action=%v reason=%q", schemaInferred.Action, schemaInferred.ReasonCode)
	}
	if got := expansionReason(t, schemaInferred.ReasonCode); got != expansion.ReasonNoSelectors {
		t.Fatalf("requirements.md 3.6 - a near-miss name must resolve to no selector, got %q", got)
	}
}

// mixedGrowthProjectRoot is long on purpose: expansion replaces a short alias
// with this whole root, so ONE alias grows the document by nearly four kilobytes.
// That is what makes the average-growth projection wrong rather than merely
// imprecise.
var mixedGrowthProjectRoot = "/" + strings.Repeat("workspace-directory-", 200) + "worktree"

// mixedGrowthOrdinaryLeaves is how many selected, non-virtual values follow the
// single alias. They are all inside the selected array, so every one of them is
// visited and decided, and every one of them is ineligible: they sit outside the
// reserved namespace and contribute exactly zero growth.
const mixedGrowthOrdinaryLeaves = 6000

// TestExpansionFinalizerExpandsOneGrowingAliasBesideManyInertLeaves guards
// requirements.md 4.1 against a resource guard that refuses valid work.
//
// The output bound is real and must stay: an expansion past
// aipapi.MaxEventDeltaBytes cannot be published, and refusing it without building
// it is what keeps that refusal cheap. What must not happen is refusing a
// document whose FINISHED size is inside the bound.
//
// A selector can target an array holding one alias that grows substantially
// beside thousands of ordinary strings that are selected and change nothing. Any
// guard that projects the remaining growth by AVERAGING the growth already
// observed multiplies that one alias's expansion across every leaf still to come,
// predicts a document many times larger than the real one, and answers
// `expanded_too_large` for a call that expands perfectly well. That is not
// failing closed on a violated bound; it is failing closed on a guess, and it
// converts a valid mapped path into a hard client rejection.
//
// The size assertions are NON-VACUITY guards in both directions: the input must be
// inside the bound and the exact expanded size must be inside it too, so a
// fixture that stopped demonstrating the case fails loudly instead of passing.
func TestExpansionFinalizerExpandsOneGrowingAliasBesideManyInertLeaves(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(mixedGrowthProjectRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive: reason %v", reason)
	}
	if mapping.VirtualRoot == "" {
		t.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}

	var doc strings.Builder
	doc.Grow(mixedGrowthOrdinaryLeaves * 40)
	doc.WriteString(`{"paths":[`)
	// The single growing alias is the FIRST member, so the projection has the
	// whole rest of the array still ahead of it when it forms its estimate.
	doc.WriteString(`"` + mapping.VirtualRoot + `pkg/module.go"`)
	for i := range mixedGrowthOrdinaryLeaves {
		doc.WriteString(`,"/home/dev/ordinary/module_`)
		doc.WriteString(strconv.Itoa(i))
		doc.WriteString(`.go"`)
	}
	doc.WriteString(`]}`)
	document := doc.String()

	if !json.Valid([]byte(document)) {
		t.Fatal("the fixture must be a readable argument document")
	}
	if len(document) >= aipapi.MaxEventDeltaBytes {
		t.Fatalf("fixture input is %d bytes, which must stay under the canonical delta bound",
			len(document))
	}
	// Exactly one leaf changes. Its finished size is taken from the mapping itself
	// rather than from hand arithmetic on the root, because the derivation is what
	// decides the spelling of both sides and a test that re-guesses it would drift
	// from the code instead of pinning it.
	aliasValue := mapping.VirtualRoot + "pkg/module.go"
	expandedValue, expandResult := mapping.ExpandPath(aliasValue)
	if expandResult != pathvirtualization.ExpandResultExpanded {
		t.Fatalf("non-vacuity: the fixture alias must expand, got %v", expandResult)
	}
	aliasLiteral, err := json.Marshal(aliasValue)
	if err != nil {
		t.Fatalf("marshal the fixture alias: %v", err)
	}
	expandedLiteral, err := json.Marshal(expandedValue)
	if err != nil {
		t.Fatalf("marshal the fixture expansion: %v", err)
	}
	growth := len(expandedLiteral) - len(aliasLiteral)
	if growth <= 0 {
		t.Fatalf("non-vacuity: the fixture alias must grow the document, got %d bytes", growth)
	}
	expandedBytes := len(document) + growth
	if expandedBytes >= aipapi.MaxEventDeltaBytes {
		t.Fatalf("fixture expands to %d bytes, which must stay under the canonical delta bound "+
			"(%d) or the case is not reachable", expandedBytes, aipapi.MaxEventDeltaBytes)
	}
	// The old projection is what this defeats: one leaf's growth spread across
	// every remaining selected leaf. Assert the fixture really would have tripped
	// it, so a regression that silently stopped exercising the case is visible.
	if projected := growth * (mixedGrowthOrdinaryLeaves - 1); projected <= aipapi.MaxEventDeltaBytes {
		t.Fatalf("non-vacuity: the averaged projection would only reach %d bytes, so this fixture "+
			"no longer distinguishes an exact bound from a guess", projected)
	}

	fin := newFinalizer(t, expansionResolver(t, expansionToolName, "/paths"))
	res, err := fin.Finalize(context.Background(), expansionCall(expansionToolName, document),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(mixedGrowthProjectRoot))
	if err != nil {
		t.Fatalf("Finalize returned a Go error: %v", err)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("requirements.md 4.1 - an expansion of %d bytes is inside the %d-byte canonical "+
			"bound and must publish, got action=%v reason=%q",
			expandedBytes, aipapi.MaxEventDeltaBytes, res.Action, res.ReasonCode)
	}
	if got := expansionReason(t, res.ReasonCode); got != expansion.ReasonExpanded {
		t.Fatalf("requirements.md 4.1 - reason=%q want %q", res.ReasonCode, expansion.ReasonExpanded)
	}
	if res.ArgsJSON == nil {
		t.Fatal("requirements.md 4.1 - a successful expansion must publish the document")
	}
	if len(res.ArgsJSON) != expandedBytes {
		t.Fatalf("requirements.md 4.8 - published %d bytes, want the %d the exact expansion produces",
			len(res.ArgsJSON), expandedBytes)
	}
	if !json.Valid(res.ArgsJSON) {
		t.Fatal("requirements.md 8.5 - the published document is not valid JSON")
	}
	if !strings.Contains(string(res.ArgsJSON), mixedGrowthProjectRoot) {
		t.Fatal("requirements.md 4.1 - the alias must have been replaced by the real root")
	}
	if strings.Contains(string(res.ArgsJSON), mapping.VirtualRoot) {
		t.Fatal("requirements.md 4.1 - no reserved alias may survive in the published document")
	}
	// Every inert leaf kept its own bytes: requirements.md 4.8 is what makes the
	// bound question worth asking at all.
	if got, want := strings.Count(string(res.ArgsJSON), "/home/dev/ordinary/module_"), mixedGrowthOrdinaryLeaves; got != want {
		t.Fatalf("requirements.md 4.8 - preserved %d inert leaves, want %d", got, want)
	}
}
