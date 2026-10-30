package pathvirtualization_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// This file covers task 3.3 of design.md 197-230: the profile type itself, the
// closed opaque-result mode set, the four-step resolution order, exact-name
// matching as the only name authority, and the deterministic precedence rule
// between operator and built-in profiles (requirements 2.5, 2.6, 3.6, 3.7,
// 3.8). The canonical rewriter (task 4.x) and the bounded opaque recognizer
// (task 4.2) are deliberately absent here: this task records WHICH selectors
// and WHICH mode a tool resolves to, and never touches payload bytes.

// stubInference is one optional schema-inference step. It records how often the
// resolution order reached it, which is what proves an exact profile is consulted
// before any inference happens.
type stubInference struct {
	selectors pathvirtualization.SelectorSet
	calls     int
	schemaLen int
}

// InferArgumentSelectors implements the lexical core's optional-inference port.
func (s *stubInference) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	s.calls++
	s.schemaLen = len(declaredSchema)
	return s.selectors
}

// declaredSchema is one tool's declared argument schema bytes, in the shape the
// optional-inference port consumes.
var declaredSchema = []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)

// newTestResolver compiles two declared profile layers and binds them into one
// resolver, failing the test if either layer or the binding is refused.
func newTestResolver(
	t *testing.T,
	operator, builtin []pathvirtualization.ToolProfile,
	inference pathvirtualization.ArgumentInference,
) *pathvirtualization.Resolver {
	t.Helper()

	resolver, reject := newTestResolverChecked(t, operator, builtin, inference)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("binding the two profile layers rejected with %v", reject)
	}
	return resolver
}

// newTestResolverChecked is newTestResolver without the accept check, so the
// fail-closed tests can assert the bounded reason.
func newTestResolverChecked(
	t *testing.T,
	operator, builtin []pathvirtualization.ToolProfile,
	inference pathvirtualization.ArgumentInference,
) (*pathvirtualization.Resolver, pathvirtualization.SelectorReject) {
	t.Helper()

	operatorProfiles, reject := pathvirtualization.CompileToolProfiles(operator)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling the operator layer rejected with %v", reject)
	}
	builtinProfiles, reject := pathvirtualization.CompileToolProfiles(builtin)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling the built-in layer rejected with %v", reject)
	}
	return pathvirtualization.NewResolver(operatorProfiles, builtinProfiles, inference)
}

// permissiveInference publishes a selector for every declared schema, including an
// absent one. The order has to keep answering steps 1 and 2 without consulting it,
// so a step that is eager must not be able to observe an exact profile at all.
type permissiveInference struct {
	calls int
}

// InferArgumentSelectors implements the lexical core's optional-inference port.
func (p *permissiveInference) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	p.calls++
	return pathvirtualization.SelectorSet{pathvirtualization.Selector{}}
}

// argForms is the canonical spelling of every argument selector in a set.
func argForms(set pathvirtualization.SelectorSet) []string {
	forms := make([]string, 0, len(set))
	for _, selector := range set {
		forms = append(forms, selector.String())
	}
	return forms
}

// TestToolProfileCarriesExactlyTheDesignShape pins design.md 202-207: the
// profile type is the exact tool names, argument pointers, structured-result
// pointers, and opaque-result mode, with no field left over. It is also the
// structural half of requirement 7.4 for this task, because no profile field can
// name the reserved alias namespace, its version, or the workspace-tag encoding,
// and no field carries a struct tag that a decoder could populate by surprise.
func TestToolProfileCarriesExactlyTheDesignShape(t *testing.T) {
	t.Parallel()

	profileType := reflect.TypeFor[pathvirtualization.ToolProfile]()
	wantFields := []struct {
		name string
		kind reflect.Kind
	}{
		{name: "Names", kind: reflect.Slice},
		{name: "ArgPointers", kind: reflect.Slice},
		{name: "ResultJSONPointers", kind: reflect.Slice},
		{name: "OpaqueResultMode", kind: reflect.Uint8},
	}
	if got := profileType.NumField(); got != len(wantFields) {
		t.Fatalf("ToolProfile has %d fields, want %d", got, len(wantFields))
	}
	for i, want := range wantFields {
		field := profileType.Field(i)
		if field.Name != want.name {
			t.Fatalf("ToolProfile field %d = %q, want %q", i, field.Name, want.name)
		}
		if field.Type.Kind() != want.kind {
			t.Fatalf("ToolProfile.%s has kind %v, want %v", field.Name, field.Type.Kind(), want.kind)
		}
		if field.Tag != "" {
			t.Fatalf("ToolProfile.%s carries tag %q; no decode surface belongs here", field.Name, field.Tag)
		}
	}
	if got := profileType.Field(3).Type; got != reflect.TypeFor[pathvirtualization.OpaqueResultMode]() {
		t.Fatalf("ToolProfile.OpaqueResultMode has type %v, want OpaqueResultMode", got)
	}
}

// TestOpaqueResultRewriteIsDisabledByDefault is requirement 2.5 and 3.8 in one
// assertion per shape: every way a profile or a resolution can exist starts with
// opaque-result rewriting off, so only an explicitly enabled mode can ever turn it
// on.
func TestOpaqueResultRewriteIsDisabledByDefault(t *testing.T) {
	t.Parallel()

	var declared pathvirtualization.ToolProfile
	if declared.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("a profile that declares no mode = %v, want the disabled default", declared.OpaqueResultMode)
	}
	if !pathvirtualization.OpaqueResultModeNone.Valid() {
		t.Fatal("the disabled default is not a member of the closed mode set")
	}
	var compiled pathvirtualization.CompiledProfile
	if compiled.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("a compiled profile that declares no mode = %v, want the disabled default", compiled.OpaqueResultMode)
	}
	var resolved pathvirtualization.Resolved
	if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("a resolution with no profile = %v, want the disabled default", resolved.OpaqueResultMode)
	}

	// A compiled profile that never mentioned a mode must also be disabled, so the
	// default survives compilation rather than only surviving the struct literal.
	compiledProfiles, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{
		{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}},
	})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling a mode-free profile rejected with %v", reject)
	}
	if compiledProfiles[0].OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("compiled mode = %v, want the disabled default", compiledProfiles[0].OpaqueResultMode)
	}

	resolver := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{Names: []string{"edit_file"}, ArgPointers: []string{"/file_path"}}},
		[]pathvirtualization.ToolProfile{{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}}},
		nil)
	if got := resolver.Resolve("read_file", declaredSchema).OpaqueResultMode; got != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("built-in resolution mode = %v, want the disabled default", got)
	}
	if got := resolver.Resolve("edit_file", declaredSchema).OpaqueResultMode; got != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("operator resolution mode = %v, want the disabled default", got)
	}
	if got := resolver.Resolve("unknown_tool", declaredSchema).OpaqueResultMode; got != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("resolution with no profile mode = %v, want the disabled default", got)
	}
}

// TestOpaqueResultModeSetIsClosed pins the bounded mode set requirement 2.6
// describes as "bounded path-token/line rules": the disabled default plus exactly
// two explicitly enabled modes, nothing else.
func TestOpaqueResultModeSetIsClosed(t *testing.T) {
	t.Parallel()

	modes := []struct {
		mode  pathvirtualization.OpaqueResultMode
		label string
	}{
		{mode: pathvirtualization.OpaqueResultModeNone, label: "none"},
		{mode: pathvirtualization.OpaqueResultModePathTokens, label: "path_tokens"},
		{mode: pathvirtualization.OpaqueResultModePathLines, label: "path_lines"},
	}
	labels := make(map[string]struct{}, len(modes))
	for _, tc := range modes {
		if !tc.mode.Valid() {
			t.Errorf("mode %v is not a member of the closed set", tc.mode)
		}
		if got := tc.mode.String(); got != tc.label {
			t.Errorf("mode %v label = %q, want %q", tc.mode, got, tc.label)
		}
		if _, duplicate := labels[tc.label]; duplicate {
			t.Errorf("mode label %q is shared", tc.label)
		}
		labels[tc.label] = struct{}{}
		// Every enabled mode must round-trip through its own configuration
		// spelling, so the compile-time validator and the diagnostics inventory
		// cannot disagree about what a mode is called.
		parsed, ok := pathvirtualization.ParseOpaqueResultMode(tc.label)
		if !ok || parsed != tc.mode {
			t.Errorf("ParseOpaqueResultMode(%q) = %v/%v, want %v/true", tc.label, parsed, ok, tc.mode)
		}
	}
	for value := range 256 {
		mode := pathvirtualization.OpaqueResultMode(value)
		if _, member := labels[mode.String()]; member && !mode.Valid() {
			t.Errorf("out-of-range value %d reports the member label %q", value, mode.String())
		}
	}
	if pathvirtualization.OpaqueResultMode(200).Valid() {
		t.Error("an out-of-range mode value reported itself as a member of the closed set")
	}
	if got := pathvirtualization.OpaqueResultMode(200).String(); got != "unknown" {
		t.Errorf("out-of-range mode label = %q, want %q", got, "unknown")
	}
}

// TestParseOpaqueResultModeAcceptsOnlyItsOwnSpelling proves the configuration
// surface is fail-closed: an unrecognized spelling is refused instead of being
// read as the disabled default, which would let a typo hide a configured mode, or
// as an enabled mode, which would let a typo widen rewriting.
func TestParseOpaqueResultModeAcceptsOnlyItsOwnSpelling(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		spelling string
		wantMode pathvirtualization.OpaqueResultMode
		wantOK   bool
	}{
		{name: "disabled", spelling: "none", wantMode: pathvirtualization.OpaqueResultModeNone, wantOK: true},
		{name: "tokens", spelling: "path_tokens", wantMode: pathvirtualization.OpaqueResultModePathTokens, wantOK: true},
		{name: "lines", spelling: "path_lines", wantMode: pathvirtualization.OpaqueResultModePathLines, wantOK: true},
		{name: "empty", spelling: "", wantOK: false},
		{name: "upper_case", spelling: "PATH_TOKENS", wantOK: false},
		{name: "mixed_case", spelling: "Path_Tokens", wantOK: false},
		{name: "hyphen_spelling", spelling: "path-tokens", wantOK: false},
		{name: "space_spelling", spelling: "path tokens", wantOK: false},
		{name: "leading_space", spelling: " path_tokens", wantOK: false},
		{name: "trailing_space", spelling: "path_tokens ", wantOK: false},
		{name: "unbounded_wording", spelling: "all", wantOK: false},
		{name: "unknown", spelling: "path_lines_v2", wantOK: false},
		{name: "camel_spelling", spelling: "pathTokens", wantOK: false},
		{name: "numeric", spelling: "1", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mode, ok := pathvirtualization.ParseOpaqueResultMode(tc.spelling)
			if ok != tc.wantOK {
				t.Fatalf("ParseOpaqueResultMode(%q) accepted = %v, want %v", tc.spelling, ok, tc.wantOK)
			}
			if mode != tc.wantMode {
				t.Fatalf("ParseOpaqueResultMode(%q) = %v, want %v", tc.spelling, mode, tc.wantMode)
			}
		})
	}
}

// TestCompileToolProfilesRefusesAnUnusableOpaqueMode is requirement 3.7's
// "validated at generation compilation" for the mode: a value outside the closed
// set refuses the whole profile set, so no partially valid profile is published
// and no request can ever observe a mode this build does not understand.
func TestCompileToolProfilesRefusesAnUnusableOpaqueMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mode pathvirtualization.OpaqueResultMode
	}{
		{name: "one_past_the_set", mode: pathvirtualization.OpaqueResultMode(3)},
		{name: "far_out_of_range", mode: pathvirtualization.OpaqueResultMode(200)},
		{name: "max_byte", mode: pathvirtualization.OpaqueResultMode(255)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{
				{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}},
				{Names: []string{"list_files"}, ArgPointers: []string{"/path"}, OpaqueResultMode: tc.mode},
			})
			if reject != pathvirtualization.SelectorRejectOpaqueMode {
				t.Fatalf("mode %d rejected with %v, want %v", tc.mode, reject, pathvirtualization.SelectorRejectOpaqueMode)
			}
			if len(compiled) != 0 {
				t.Fatalf("a refused profile set still published %d profiles", len(compiled))
			}
		})
	}

	// The same refusal applies to the mode-free compile entry point, so a caller
	// that uses it cannot smuggle an unusable mode past validation.
	_, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{
		{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}},
	})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileProfiles rejected a valid mode-free profile set with %v", reject)
	}
}

// TestBuiltinToolProfilesAreConservativeExactNameProfiles proves requirement 3.6
// of the shipped built-in layer: exact names only, versioned with the feature,
// and conservative enough that no built-in publishes a structured-result
// selector, enables opaque rewriting, or names a payload concept.
func TestBuiltinToolProfilesAreConservativeExactNameProfiles(t *testing.T) {
	t.Parallel()

	builtIn := pathvirtualization.BuiltinToolProfiles()
	if len(builtIn) == 0 {
		t.Fatal("the shipped built-in layer is empty; nothing implements requirement 3.6")
	}
	if len(builtIn) > pathvirtualization.MaxProfiles {
		t.Fatalf("the built-in layer holds %d profiles, past the bound %d", len(builtIn), pathvirtualization.MaxProfiles)
	}
	if pathvirtualization.BuiltinProfileVersion < 1 {
		t.Fatalf("built-in profile version = %d, want a positive version", pathvirtualization.BuiltinProfileVersion)
	}

	compiled, reject := pathvirtualization.CompileToolProfiles(builtIn)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("the shipped built-in layer does not compile: %v", reject)
	}
	claimed := make(map[string]struct{}, len(builtIn))
	for _, profile := range compiled {
		for _, name := range profile.Names {
			if name == "" {
				t.Fatal("a built-in profile claims the empty tool name")
			}
			if name != strings.TrimSpace(name) {
				t.Errorf("built-in tool name %q carries surrounding space", name)
			}
			if _, duplicate := claimed[name]; duplicate {
				t.Errorf("built-in tool name %q is claimed twice", name)
			}
			claimed[name] = struct{}{}
		}
		// Design.md 249-250 keeps opaque rewriting off for V1 built-ins: none of
		// these tool contracts is clearly path-list-only, so the shipped layer must
		// not enable a bounded opaque mode for any of them.
		if profile.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
			t.Errorf("built-in profile %v enables opaque mode %v, want the disabled default", profile.Names, profile.OpaqueResultMode)
		}
		// Design.md 247-248 keeps structured result rewriting explicit: the canonical
		// structured-result representation is not provider-stable, so a built-in must
		// not guess one.
		if len(profile.ResultJSONPointers) != 0 {
			t.Errorf("built-in profile %v publishes result selectors, want none", profile.Names)
		}
		for _, pointer := range argForms(profile.ArgPointers) {
			for _, payload := range payloadConceptMembers {
				if pointer == "/"+payload {
					t.Errorf("built-in profile %v names the payload member %q", profile.Names, pointer)
				}
			}
		}
	}
}

// TestBuiltinToolProfilesArePinnedAndImmutable is the version contract behind
// requirement 3.6: the shipped layer is fixed data, so its exact contents are
// pinned here, and every caller receives a private copy it cannot use to reach the
// policy another caller resolves against.
func TestBuiltinToolProfilesArePinnedAndImmutable(t *testing.T) {
	t.Parallel()

	want := []pathvirtualization.ToolProfile{
		{
			Names:       []string{"read_file", "write_file", "edit_file", "replace_in_file", "delete_file", "remove_file"},
			ArgPointers: []string{"/file_path"},
		},
		{
			Names:       []string{"notebook_read", "notebook_edit"},
			ArgPointers: []string{"/notebook_path"},
		},
	}
	got := pathvirtualization.BuiltinToolProfiles()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("built-in layer = %+v, want %+v; a change here must bump BuiltinProfileVersion", got, want)
	}

	// The table is data, not package state: a caller may edit what it received
	// without any effect on the next reader.
	mutated := pathvirtualization.BuiltinToolProfiles()
	mutated[0].Names[0] = "mutated"
	mutated[0].ArgPointers[0] = "/mutated"
	mutated[0].ArgPointers = append(mutated[0].ArgPointers, "/appended")
	mutated = append(mutated, pathvirtualization.ToolProfile{Names: []string{"injected"}})
	if reflect.DeepEqual(mutated, want) {
		t.Fatalf("the mutation fixture did not change the caller copy, so the check below proves nothing: %+v", mutated)
	}
	if again := pathvirtualization.BuiltinToolProfiles(); !reflect.DeepEqual(again, want) {
		t.Fatalf("the built-in layer was mutated through a returned copy: %+v", again)
	}
}

// TestResolutionFollowsTheDesignOrder walks design.md 210-214 one entry at a time.
// Every case fixes the same two exact layers and changes only which surface is
// reachable, so the reported source proves the order rather than the outcome.
func TestResolutionFollowsTheDesignOrder(t *testing.T) {
	t.Parallel()

	inferred := compileSelectorSet(t, "/inferred")
	cases := []struct {
		name           string
		tool           string
		withInference  bool
		wantSource     pathvirtualization.ProfileSource
		wantArgs       []string
		wantResults    []string
		wantMode       pathvirtualization.OpaqueResultMode
		wantNoInferenc bool
	}{
		{
			name:          "step_1_operator_profile_wins",
			tool:          "read_file",
			withInference: true,
			wantSource:    pathvirtualization.ProfileSourceOperator,
			wantArgs:      []string{"/operator_arg"},
			wantResults:   []string{"/operator_result"},
			wantMode:      pathvirtualization.OpaqueResultModePathLines,
		},
		{
			name:          "step_2_builtin_profile_applies",
			tool:          "list_files",
			withInference: true,
			wantSource:    pathvirtualization.ProfileSourceBuiltin,
			wantArgs:      []string{"/builtin_arg"},
			wantResults:   []string{"/builtin_result"},
			wantMode:      pathvirtualization.OpaqueResultModeNone,
		},
		{
			name:          "step_3_inference_is_the_fallback",
			tool:          "unprofiled_tool",
			withInference: true,
			wantSource:    pathvirtualization.ProfileSourceInference,
			wantArgs:      []string{"/inferred"},
		},
		{
			name:          "step_3_never_runs_behind_an_exact_profile",
			tool:          "read_file",
			withInference: true,
			wantSource:    pathvirtualization.ProfileSourceOperator,
			wantArgs:      []string{"/operator_arg"},
			wantResults:   []string{"/operator_result"},
			wantMode:      pathvirtualization.OpaqueResultModePathLines,
		},
		{
			name:       "step_4_no_profile_no_inference_selects_nothing",
			tool:       "unprofiled_tool",
			wantSource: pathvirtualization.ProfileSourceNone,
		},
	}
	// The two exact layers are fixed for every case, so only which step is reachable
	// varies and the reported source is what proves the order.
	operatorLayer := []pathvirtualization.ToolProfile{{
		Names:              []string{"read_file"},
		ArgPointers:        []string{"/operator_arg"},
		ResultJSONPointers: []string{"/operator_result"},
		OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
	}}
	builtinLayer := []pathvirtualization.ToolProfile{{
		Names:              []string{"read_file", "list_files"},
		ArgPointers:        []string{"/builtin_arg"},
		ResultJSONPointers: []string{"/builtin_result"},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var inference pathvirtualization.ArgumentInference
			if tc.withInference {
				inference = &stubInference{selectors: inferred}
			}
			resolved := newTestResolver(t, operatorLayer, builtinLayer, inference).Resolve(tc.tool, declaredSchema)
			assertResolution(t, resolved, tc.wantSource, tc.wantArgs, tc.wantResults, tc.wantMode)
		})
	}

	// An inference step that publishes no selector is the same answer as no step at
	// all: the fourth entry of the order, not an error and not a guess.
	empty := newTestResolver(t, operatorLayer, builtinLayer, &stubInference{})
	assertResolution(t, empty.Resolve("unprofiled_tool", declaredSchema), pathvirtualization.ProfileSourceNone,
		nil, nil, pathvirtualization.OpaqueResultModeNone)

	// Steps 1 and 2 do not depend on the inference step at all.
	exactOnly := newTestResolver(t, operatorLayer, builtinLayer, nil)
	assertResolution(t, exactOnly.Resolve("read_file", declaredSchema), pathvirtualization.ProfileSourceOperator,
		[]string{"/operator_arg"}, []string{"/operator_result"}, pathvirtualization.OpaqueResultModePathLines)
	assertResolution(t, exactOnly.Resolve("list_files", declaredSchema), pathvirtualization.ProfileSourceBuiltin,
		[]string{"/builtin_arg"}, []string{"/builtin_result"}, pathvirtualization.OpaqueResultModeNone)
	assertResolution(t, exactOnly.Resolve("unprofiled_tool", declaredSchema), pathvirtualization.ProfileSourceNone,
		nil, nil, pathvirtualization.OpaqueResultModeNone)
}

// assertResolution compares one resolution against the expected source, pointer
// sets, and mode.
func assertResolution(
	t *testing.T,
	resolved pathvirtualization.Resolved,
	wantSource pathvirtualization.ProfileSource,
	wantArgs, wantResults []string,
	wantMode pathvirtualization.OpaqueResultMode,
) {
	t.Helper()

	if resolved.Source != wantSource {
		t.Fatalf("source = %v, want %v", resolved.Source, wantSource)
	}
	if got := argForms(resolved.ArgPointers); !slices.Equal(got, wantArgs) {
		t.Fatalf("argument selectors = %q, want %q", got, wantArgs)
	}
	if got := argForms(resolved.ResultJSONPointers); !slices.Equal(got, wantResults) {
		t.Fatalf("result selectors = %q, want %q", got, wantResults)
	}
	if resolved.OpaqueResultMode != wantMode {
		t.Fatalf("opaque mode = %v, want %v", resolved.OpaqueResultMode, wantMode)
	}
}

// TestOperatorProfileReplacesTheBuiltInForItsExactName is the precedence rule of
// requirement 3.7. The operator layer is authoritative for a name it claims: the
// built-in's selectors, result selectors, and mode are all replaced, never merged
// in, so an operator can both widen a built-in and withdraw a built-in selector it
// finds wrong. The operator's own declaration is the complete one, which is what
// design.md 347-351 shows in the configuration example.
func TestOperatorProfileReplacesTheBuiltInForItsExactName(t *testing.T) {
	t.Parallel()

	builtIn := []pathvirtualization.ToolProfile{{
		Names:              []string{"read_file"},
		ArgPointers:        []string{"/builtin_a", "/builtin_b"},
		ResultJSONPointers: []string{"/builtin_result"},
		OpaqueResultMode:   pathvirtualization.OpaqueResultModePathTokens,
	}}
	cases := []struct {
		name        string
		operator    pathvirtualization.ToolProfile
		wantArgs    []string
		wantResults []string
		wantMode    pathvirtualization.OpaqueResultMode
	}{
		{
			name:        "replaces_pointers_outright",
			operator:    pathvirtualization.ToolProfile{Names: []string{"read_file"}, ArgPointers: []string{"/operator_a"}},
			wantArgs:    []string{"/operator_a"},
			wantResults: nil,
			wantMode:    pathvirtualization.OpaqueResultModeNone,
		},
		{
			name: "extends_by_declaring_the_superset",
			operator: pathvirtualization.ToolProfile{
				Names:       []string{"read_file"},
				ArgPointers: []string{"/builtin_a", "/builtin_b", "/operator_a"},
			},
			wantArgs: []string{"/builtin_a", "/builtin_b", "/operator_a"},
		},
		{
			name:        "suppresses_the_builtin_selectors_entirely",
			operator:    pathvirtualization.ToolProfile{Names: []string{"read_file"}},
			wantArgs:    nil,
			wantResults: nil,
			wantMode:    pathvirtualization.OpaqueResultModeNone,
		},
		{
			name: "withdraws_the_opaque_mode_the_builtin_declared",
			operator: pathvirtualization.ToolProfile{
				Names:       []string{"read_file"},
				ArgPointers: []string{"/builtin_a"},
			},
			wantArgs: []string{"/builtin_a"},
			wantMode: pathvirtualization.OpaqueResultModeNone,
		},
		{
			name: "enables_a_bounded_opaque_mode_explicitly",
			operator: pathvirtualization.ToolProfile{
				Names:            []string{"read_file"},
				ArgPointers:      []string{"/builtin_a"},
				OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines,
			},
			wantArgs: []string{"/builtin_a"},
			wantMode: pathvirtualization.OpaqueResultModePathLines,
		},
		{
			name: "narrows_the_opaque_mode_to_the_other_bounded_mode",
			operator: pathvirtualization.ToolProfile{
				Names:              []string{"read_file"},
				ArgPointers:        []string{"/builtin_a", "/builtin_b"},
				ResultJSONPointers: []string{"/operator_result"},
				OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
			},
			wantArgs:    []string{"/builtin_a", "/builtin_b"},
			wantResults: []string{"/operator_result"},
			wantMode:    pathvirtualization.OpaqueResultModePathLines,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := newTestResolver(t, []pathvirtualization.ToolProfile{tc.operator}, builtIn, nil)
			resolved := resolver.Resolve("read_file", declaredSchema)
			assertResolution(t, resolved, pathvirtualization.ProfileSourceOperator, tc.wantArgs, tc.wantResults, tc.wantMode)
			if resolved.Source == pathvirtualization.ProfileSourceBuiltin {
				t.Fatal("the built-in layer was consulted for a name the operator claims")
			}
		})
	}
}

// TestOperatorProfileAppliesToNamesWithoutABuiltIn is the extension half of
// requirement 3.7: an operator profile for a tool the feature has no built-in for
// is a complete profile in its own right, and it enables nothing for any other
// tool.
func TestOperatorProfileAppliesToNamesWithoutABuiltIn(t *testing.T) {
	t.Parallel()

	resolver := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{
			Names:              []string{"custom_read", "custom_list"},
			ArgPointers:        []string{"/path"},
			ResultJSONPointers: []string{"/result_path"},
			OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
		}},
		pathvirtualization.BuiltinToolProfiles(),
		nil)

	assertResolution(t, resolver.Resolve("custom_read", declaredSchema), pathvirtualization.ProfileSourceOperator,
		[]string{"/path"}, []string{"/result_path"}, pathvirtualization.OpaqueResultModePathLines)
	assertResolution(t, resolver.Resolve("custom_list", declaredSchema), pathvirtualization.ProfileSourceOperator,
		[]string{"/path"}, []string{"/result_path"}, pathvirtualization.OpaqueResultModePathLines)
	// A name the operator never claimed keeps the shipped built-in surface, and a
	// name neither layer claims selects nothing.
	assertResolution(t, resolver.Resolve("read_file", declaredSchema), pathvirtualization.ProfileSourceBuiltin,
		[]string{"/file_path"}, nil, pathvirtualization.OpaqueResultModeNone)
	assertResolution(t, resolver.Resolve("custom_write", declaredSchema), pathvirtualization.ProfileSourceNone,
		nil, nil, pathvirtualization.OpaqueResultModeNone)
}

// TestOperatorProfileAffectsOnlyItsOwnExactName keeps an operator's widening
// local. Requirement 3.8 refuses opaque rewriting for an unknown tool unless it is
// explicitly configured, so one enabled profile may not become a policy for the
// next tool that happens to look similar.
func TestOperatorProfileAffectsOnlyItsOwnExactName(t *testing.T) {
	t.Parallel()

	resolver := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{
			Names:            []string{"custom_list"},
			ArgPointers:      []string{"/path"},
			OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines,
		}},
		pathvirtualization.BuiltinToolProfiles(),
		nil)

	for _, tool := range []string{"custom_list2", "list_files", "custom", "Custom_List", "custom_list "} {
		resolved := resolver.Resolve(tool, declaredSchema)
		if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
			t.Errorf("tool %q resolved opaque mode %v, want the disabled default", tool, resolved.OpaqueResultMode)
		}
		if resolved.Source == pathvirtualization.ProfileSourceOperator {
			t.Errorf("tool %q resolved through the operator layer it does not claim", tool)
		}
	}
}

// TestToolNameMatchingIsExactAndNeverPrefixed is the behavioral half of
// requirement 3.6. Neither layer may reach a tool whose name merely contains,
// extends, shortens, folds, trims, or imitates a claimed name, so every
// near-miss name below must fall past both exact layers.
func TestToolNameMatchingIsExactAndNeverPrefixed(t *testing.T) {
	t.Parallel()

	nearMiss := []struct {
		name   string
		tool   string
		reason string
	}{
		{name: "longer_name_with_claimed_name_as_prefix", tool: "read_file_extra", reason: "prefix extension"},
		{name: "longer_name_with_claimed_name_as_suffix", tool: "my_read", reason: "suffix extension"},
		{name: "longer_name_wrapping_the_claimed_name", tool: "pre_read_post", reason: "substring"},
		{name: "shorter_name", tool: "rea", reason: "truncation"},
		{name: "separator_removed", tool: "readfile", reason: "normalization"},
		{name: "upper_case_variant", tool: "READ", reason: "case folding"},
		{name: "title_case_variant", tool: "Read", reason: "case folding"},
		{name: "mixed_case_variant", tool: "rEaD", reason: "case folding"},
		{name: "trailing_space", tool: "read ", reason: "trimming"},
		{name: "leading_space", tool: " read", reason: "trimming"},
		{name: "trailing_newline", tool: "read\n", reason: "whitespace"},
		{name: "trailing_tab", tool: "read\t", reason: "whitespace"},
		{name: "non_breaking_space", tool: "read\u00a0", reason: "unicode whitespace"},
		{name: "cyrillic_homoglyph", tool: "reаd", reason: "confusable letter"},
		{name: "cyrillic_ie_homoglyph", tool: "rеad", reason: "confusable letter"},
		{name: "fullwidth_letters", tool: "ｒｅａｄ", reason: "confusable letters"},
		{name: "trailing_underscore", tool: "read_", reason: "separator extension"},
		{name: "empty_name", tool: "", reason: "nameless call"},
	}
	// The claimed names sit in both layers, so a match by either is a failure.
	operator := []pathvirtualization.ToolProfile{{
		Names:              []string{"write"},
		ArgPointers:        []string{"/operator_arg"},
		ResultJSONPointers: []string{"/operator_result"},
		OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
	}}
	builtIn := []pathvirtualization.ToolProfile{{
		Names:              []string{"read"},
		ArgPointers:        []string{"/builtin_arg"},
		ResultJSONPointers: []string{"/builtin_result"},
		OpaqueResultMode:   pathvirtualization.OpaqueResultModePathTokens,
	}}

	for _, tc := range nearMiss {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// With no inference step attached, a near-miss name must select nothing
			// at all.
			exactOnly := newTestResolver(t, operator, builtIn, nil)
			resolved := exactOnly.Resolve(tc.tool, declaredSchema)
			if resolved.Source != pathvirtualization.ProfileSourceNone {
				t.Errorf("tool %q resolved through %v for %s, want no selector", tc.tool, resolved.Source, tc.reason)
			}
			if len(resolved.ArgPointers) != 0 || len(resolved.ResultJSONPointers) != 0 {
				t.Errorf("tool %q selected %q/%q for %s, want nothing",
					tc.tool, argForms(resolved.ArgPointers), argForms(resolved.ResultJSONPointers), tc.reason)
			}
			if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
				t.Errorf("tool %q resolved opaque mode %v for %s, want the disabled default",
					tc.tool, resolved.OpaqueResultMode, tc.reason)
			}

			// With a permissive inference step attached, a near-miss name must fall
			// through to that step and never to a claimed name's selectors, which is
			// what proves no substring authority survives the fall-through.
			inference := &stubInference{selectors: compileSelectorSet(t, "/inferred")}
			withInference := newTestResolver(t, operator, builtIn, inference)
			fellThrough := withInference.Resolve(tc.tool, declaredSchema)
			if fellThrough.Source != pathvirtualization.ProfileSourceInference {
				t.Errorf("tool %q resolved through %v for %s, want the inference fallback", tc.tool, fellThrough.Source, tc.reason)
			}
			if got := argForms(fellThrough.ArgPointers); !slices.Equal(got, []string{"/inferred"}) {
				t.Errorf("tool %q selected %q for %s, want only the inferred selectors", tc.tool, got, tc.reason)
			}
			if len(fellThrough.ResultJSONPointers) != 0 {
				t.Errorf("tool %q selected result selectors %q from inference, want none",
					tc.tool, argForms(fellThrough.ResultJSONPointers))
			}
			if fellThrough.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
				t.Errorf("inference enabled opaque mode %v for tool %q, want the disabled default",
					fellThrough.OpaqueResultMode, tc.tool)
			}
		})
	}

	// The two exact names themselves must still resolve, so the matrix above is
	// measuring exactness rather than a resolver that never matches anything.
	exact := newTestResolver(t, operator, builtIn, nil)
	assertResolution(t, exact.Resolve("read", declaredSchema), pathvirtualization.ProfileSourceBuiltin,
		[]string{"/builtin_arg"}, []string{"/builtin_result"}, pathvirtualization.OpaqueResultModePathTokens)
	assertResolution(t, exact.Resolve("write", declaredSchema), pathvirtualization.ProfileSourceOperator,
		[]string{"/operator_arg"}, []string{"/operator_result"}, pathvirtualization.OpaqueResultModePathLines)
}

// TestInferenceIsConsultedOnlyAfterBothExactLayersMiss keeps the order cheap and
// keeps explicit configuration authoritative: a tool either exact layer claims is
// answered from that layer, and its declared schema is never even read.
func TestInferenceIsConsultedOnlyAfterBothExactLayersMiss(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		tool       string
		wantSource pathvirtualization.ProfileSource
		wantCalls  int
	}{
		{name: "operator_profile_short_circuits", tool: "write", wantSource: pathvirtualization.ProfileSourceOperator, wantCalls: 0},
		{name: "builtin_profile_short_circuits", tool: "read", wantSource: pathvirtualization.ProfileSourceBuiltin, wantCalls: 0},
		{name: "unclaimed_tool_reaches_inference", tool: "other", wantSource: pathvirtualization.ProfileSourceInference, wantCalls: 1},
		{name: "near_miss_name_reaches_inference", tool: "read_file", wantSource: pathvirtualization.ProfileSourceInference, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inference := &stubInference{selectors: compileSelectorSet(t, "/inferred")}
			resolver := newTestResolver(t,
				[]pathvirtualization.ToolProfile{{Names: []string{"write"}, ArgPointers: []string{"/operator_arg"}}},
				[]pathvirtualization.ToolProfile{{Names: []string{"read"}, ArgPointers: []string{"/builtin_arg"}}},
				inference)
			resolver.Resolve(tc.tool, declaredSchema)
			if inference.calls != tc.wantCalls {
				t.Fatalf("inference called %d times for tool %q, want %d", inference.calls, tc.tool, tc.wantCalls)
			}
			if inference.calls > 0 && inference.schemaLen != len(declaredSchema) {
				t.Fatalf("inference read %d schema bytes, want the %d declared bytes",
					inference.schemaLen, len(declaredSchema))
			}
		})
	}
}

// TestResolutionHandlesHostileNamesWithoutPanicking feeds the exact-name lookup
// the byte shapes a hostile or merely odd caller can produce. A lookup table keyed
// by untrusted names must answer for all of them, and must never resolve one that no
// profile claims.
func TestResolutionHandlesHostileNamesWithoutPanicking(t *testing.T) {
	t.Parallel()

	names := []string{
		"",
		" ",
		"\x00",
		"read\x00_file",
		"/read_file",
		"../../read_file",
		strings.Repeat("a", 4096),
		strings.Repeat("tool_", 512),
		"réad_file",
		"read_file\x7f",
		"\"read_file\"",
		"'read_file'",
		"read_file\nread_file",
		"TOOL_1",
	}
	inference := &permissiveInference{}
	resolver := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}}},
		pathvirtualization.BuiltinToolProfiles(),
		inference)
	for _, name := range names {
		if got := len(name); got > 0 && name == "read_file" {
			continue
		}
		resolved := resolver.Resolve(name, declaredSchema)
		if resolved.Source == pathvirtualization.ProfileSourceOperator {
			t.Errorf("hostile name %q resolved through the operator layer", name)
		}
		if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
			t.Errorf("hostile name %q resolved opaque mode %v, want the disabled default", name, resolved.OpaqueResultMode)
		}
	}
	// No hostile name claimed an exact profile, so the fallback step saw every one.
	if inference.calls != len(names) {
		t.Fatalf("the fallback step ran %d times for %d names", inference.calls, len(names))
	}

	// The exact name still resolves from its layer, so the loop above proves
	// hostility is handled rather than every name being ignored.
	assertResolution(t, resolver.Resolve("read_file", declaredSchema), pathvirtualization.ProfileSourceOperator,
		[]string{"/file_path"}, nil, pathvirtualization.OpaqueResultModeNone)
}

// TestResolutionIsDeterministicAcrossDeclarationOrder keeps the resolution output
// free of map iteration order: both layers are lookup tables, so the only thing
// that can order a result is the profile's own declared pointer order.
func TestResolutionIsDeterministicAcrossDeclarationOrder(t *testing.T) {
	t.Parallel()

	first := []pathvirtualization.ToolProfile{
		{Names: []string{"a_tool", "b_tool"}, ArgPointers: []string{"/first", "/second"}},
		{Names: []string{"c_tool"}, ArgPointers: []string{"/third"}, OpaqueResultMode: pathvirtualization.OpaqueResultModePathTokens},
	}
	second := []pathvirtualization.ToolProfile{
		{Names: []string{"d_tool"}, ResultJSONPointers: []string{"/result"}},
	}
	builtIn := pathvirtualization.BuiltinToolProfiles()
	reversedBuiltIn := []pathvirtualization.ToolProfile{builtIn[1], builtIn[0]}
	// Every ordering of the same two layers, crossed with both orderings of the
	// shipped built-in layer. If the answer depended on declaration order or on map
	// layout, one of these twenty combinations would disagree.
	layers := [][]pathvirtualization.ToolProfile{
		{first[0], first[1], second[0]},
		{first[1], first[0], second[0]},
		{second[0], first[0], first[1]},
		{first[0], second[0], first[1]},
	}
	builtInLayers := [][]pathvirtualization.ToolProfile{builtIn, reversedBuiltIn}
	names := []string{"a_tool", "b_tool", "c_tool", "d_tool", "read_file", "notebook_edit", "unknown"}

	type snapshot struct {
		source  pathvirtualization.ProfileSource
		args    string
		results string
		mode    pathvirtualization.OpaqueResultMode
	}
	var want []snapshot
	combination := 0
	for _, operator := range layers {
		for _, builtin := range builtInLayers {
			var got []snapshot
			inference := &stubInference{selectors: compileSelectorSet(t, "/inferred")}
			resolver := newTestResolver(t, operator, builtin, inference)
			for _, name := range names {
				resolved := resolver.Resolve(name, declaredSchema)
				got = append(got, snapshot{
					source:  resolved.Source,
					args:    strings.Join(argForms(resolved.ArgPointers), ","),
					results: strings.Join(argForms(resolved.ResultJSONPointers), ","),
					mode:    resolved.OpaqueResultMode,
				})
			}
			if combination == 0 {
				want = got
				combination++
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("layer order %d resolved to %+v, want %+v", combination, got, want)
			}
			combination++
		}
	}
	if combination != len(layers)*len(builtInLayers) {
		t.Fatalf("exercised %d layer orderings, want %d", combination, len(layers)*len(builtInLayers))
	}
}

// TestResolutionIsRepeatableAndHoldsNoCallerReachableState proves one resolution
// can never influence the next one: repeated calls are identical, and the returned
// selector sets are private copies, so a caller that edits them cannot corrupt the
// policy every later call resolves against.
func TestResolutionIsRepeatableAndHoldsNoCallerReachableState(t *testing.T) {
	t.Parallel()

	resolver := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{Names: []string{"write"}, ArgPointers: []string{"/one", "/two", "/three"}}},
		pathvirtualization.BuiltinToolProfiles(),
		nil)

	first := resolver.Resolve("write", declaredSchema)
	for i := range 1000 {
		again := resolver.Resolve("write", declaredSchema)
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("resolution %d = %+v, want %+v", i, again, first)
		}
	}

	// The caller owns what it received.
	first.ArgPointers[0] = pathvirtualization.Selector{}
	first.ArgPointers = append(first.ArgPointers, compileSelectorSet(t, "/injected")...)
	first.ResultJSONPointers = append(first.ResultJSONPointers, compileSelectorSet(t, "/injected_result")...)
	after := resolver.Resolve("write", declaredSchema)
	if got := argForms(after.ArgPointers); !slices.Equal(got, []string{"/one", "/two", "/three"}) {
		t.Fatalf("argument selectors after caller mutation = %q, want the compiled set", got)
	}
	if len(after.ResultJSONPointers) != 0 {
		t.Fatalf("result selectors after caller mutation = %q, want none", argForms(after.ResultJSONPointers))
	}
}

// TestResolverRefusesUnusableConfiguration keeps the whole resolution order
// fail-closed at binding time: an unusable layer publishes no resolver at all, so
// a request can never resolve against a partially valid policy.
func TestResolverRefusesUnusableConfiguration(t *testing.T) {
	t.Parallel()

	// These cases bypass the compiler on purpose. Compiling is the first place the
	// rules are enforced, and this test covers the second: binding must refuse the
	// same violations in a layer assembled directly, because a caller is free to
	// build a CompiledProfile without going through the compiler.
	valid := []pathvirtualization.CompiledProfile{{
		Names:       []string{"valid_tool"},
		ArgPointers: compileSelectorSet(t, "/path"),
	}}
	overLimitProfiles := make([]pathvirtualization.CompiledProfile, 0, pathvirtualization.MaxProfiles+1)
	for i := range pathvirtualization.MaxProfiles + 1 {
		overLimitProfiles = append(overLimitProfiles, pathvirtualization.CompiledProfile{
			Names:       []string{fmt.Sprintf("tool_%d", i)},
			ArgPointers: compileSelectorSet(t, "/path"),
		})
	}
	overLimitPointers := pointersOfCount(pathvirtualization.MaxPointersPerProfile + 1)
	overLimitArguments := SelectorSetOfCount(t, overLimitPointers)
	overLimitResults := SelectorSetOfCount(t, overLimitPointers)

	cases := []struct {
		name       string
		operator   []pathvirtualization.CompiledProfile
		builtin    []pathvirtualization.CompiledProfile
		wantReject pathvirtualization.SelectorReject
	}{
		{
			name:       "too_many_operator_profiles",
			operator:   overLimitProfiles,
			wantReject: pathvirtualization.SelectorRejectProfileCount,
		},
		{
			name:       "too_many_builtin_profiles",
			builtin:    overLimitProfiles,
			wantReject: pathvirtualization.SelectorRejectProfileCount,
		},
		{
			name: "empty_operator_name",
			operator: []pathvirtualization.CompiledProfile{{
				Names:       []string{""},
				ArgPointers: compileSelectorSet(t, "/path"),
			}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "nameless_builtin_profile",
			builtin: []pathvirtualization.CompiledProfile{{
				ArgPointers: compileSelectorSet(t, "/path"),
			}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "duplicate_name_inside_one_layer",
			operator: []pathvirtualization.CompiledProfile{
				{Names: []string{"dup"}, ArgPointers: compileSelectorSet(t, "/first")},
				{Names: []string{"dup"}, ArgPointers: compileSelectorSet(t, "/second")},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "duplicate_name_inside_one_profile",
			operator: []pathvirtualization.CompiledProfile{{
				Names:       []string{"dup", "dup"},
				ArgPointers: compileSelectorSet(t, "/path"),
			}},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "one_name_claimed_by_two_profiles",
			operator: []pathvirtualization.CompiledProfile{
				{Names: []string{"shared", "other"}, ArgPointers: compileSelectorSet(t, "/first")},
				{Names: []string{"shared"}, ArgPointers: compileSelectorSet(t, "/second")},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "over_limit_pointers_across_both_lists",
			operator: []pathvirtualization.CompiledProfile{{
				Names:              []string{"wide"},
				ArgPointers:        overLimitArguments,
				ResultJSONPointers: overLimitResults,
			}},
			wantReject: pathvirtualization.SelectorRejectPointerCount,
		},
		{
			name: "over_limit_builtin_pointers",
			builtin: []pathvirtualization.CompiledProfile{{
				Names:       []string{"wide"},
				ArgPointers: overLimitArguments,
			}},
			wantReject: pathvirtualization.SelectorRejectPointerCount,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver, reject := pathvirtualization.NewResolver(tc.operator, tc.builtin, nil)
			if reject != tc.wantReject {
				t.Fatalf("rejected with %v, want %v", reject, tc.wantReject)
			}
			if resolver != nil {
				t.Fatal("a refused configuration still published a resolver")
			}
		})
	}

	// The same violations are refused by the compile path, so the two entry points
	// cannot disagree about what a usable layer is. Declared form is used here, so
	// the compiler is what has to catch each one.
	declared := []struct {
		name       string
		operator   []pathvirtualization.ToolProfile
		builtin    []pathvirtualization.ToolProfile
		wantReject pathvirtualization.SelectorReject
	}{
		{
			name: "empty_operator_name",
			operator: []pathvirtualization.ToolProfile{{
				Names: []string{""}, ArgPointers: []string{"/path"},
			}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "nameless_builtin_profile",
			builtin: []pathvirtualization.ToolProfile{{
				ArgPointers: []string{"/path"},
			}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "one_name_claimed_by_two_profiles",
			operator: []pathvirtualization.ToolProfile{
				{Names: []string{"shared", "other"}, ArgPointers: []string{"/first"}},
				{Names: []string{"shared"}, ArgPointers: []string{"/second"}},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "over_limit_pointers_across_both_lists",
			operator: []pathvirtualization.ToolProfile{{
				Names:              []string{"wide"},
				ArgPointers:        overLimitPointers,
				ResultJSONPointers: []string{"/result"},
			}},
			wantReject: pathvirtualization.SelectorRejectPointerCount,
		},
	}
	for _, tc := range declared {
		t.Run(tc.name+"_at_compile_time", func(t *testing.T) {
			t.Parallel()

			compiled, reject := pathvirtualization.CompileToolProfiles(tc.operator)
			if tc.operator == nil {
				compiled, reject = pathvirtualization.CompileToolProfiles(tc.builtin)
			}
			if reject != tc.wantReject {
				t.Fatalf("compiled with %v, want %v", reject, tc.wantReject)
			}
			if len(compiled) != 0 {
				t.Fatalf("a refused layer still published %d profiles", len(compiled))
			}
		})
	}

	// One valid layer still binds, so the refusals above are specific rather than a
	// blanket rejection of every input.
	if resolver, reject := pathvirtualization.NewResolver(valid, valid, nil); reject != pathvirtualization.SelectorRejectNone || resolver == nil {
		t.Fatalf("binding one valid layer = %v/%v, want acceptance", reject, resolver)
	}
	// An absent layer binds to nothing, which is how a deployment with no operator
	// configuration keeps the built-in layer alone.
	if resolver, reject := pathvirtualization.NewResolver(nil, nil, nil); reject != pathvirtualization.SelectorRejectNone || resolver == nil {
		t.Fatalf("binding absent layers = %v/%v, want acceptance", reject, resolver)
	}
}

// TestResolutionStaysBoundedInProfileCountAndToolNameLength keeps one resolution a
// bounded amount of work. Two exact layers are lookup tables keyed by tool name, so
// a resolution costs one hash per layer plus one byte-exact comparison of the
// claimed name. The canonical contract already bounds a tool name at
// aipapi.MaxToolNameBytes, and this test pins the part of that contract the
// resolution depends on: the two lookups, the exact comparison, and no iteration.
func TestResolutionStaysBoundedInProfileCountAndToolNameLength(t *testing.T) {
	t.Parallel()

	// A layer at the profile bound, one name per profile.
	largest := make([]pathvirtualization.ToolProfile, 0, pathvirtualization.MaxProfiles)
	for i := range pathvirtualization.MaxProfiles {
		largest = append(largest, pathvirtualization.ToolProfile{
			Names:       []string{fmt.Sprintf("tool_%d", i)},
			ArgPointers: []string{"/path"},
		})
	}
	inference := &permissiveInference{}
	resolver := newTestResolver(t, largest, pathvirtualization.BuiltinToolProfiles(), inference)

	// Every claimed name resolves from a table, and no resolution walks the layer.
	for i := range pathvirtualization.MaxProfiles {
		resolved := resolver.Resolve(fmt.Sprintf("tool_%d", i), declaredSchema)
		if resolved.Source != pathvirtualization.ProfileSourceOperator {
			t.Fatalf("profile %d resolved through %v, want the operator layer", i, resolved.Source)
		}
	}
	if inference.calls != 0 {
		t.Fatalf("the fallback step ran %d times for %d claimed names", inference.calls, pathvirtualization.MaxProfiles)
	}

	// One unclaimed name costs one fallback call, not one per profile.
	resolver.Resolve("unclaimed", declaredSchema)
	if inference.calls != 1 {
		t.Fatalf("the fallback step ran %d times for one unclaimed name, want 1", inference.calls)
	}

	// A long name is compared against the claim, never scanned for a match inside, so
	// it falls through to the one optional call rather than matching anything.
	long := strings.Repeat("x", 4096)
	if got := resolver.Resolve(long, declaredSchema); got.Source != pathvirtualization.ProfileSourceInference {
		t.Fatalf("a 4096-byte unclaimed name resolved through %v, want the inference fallback", got.Source)
	}
	if inference.calls != 2 {
		t.Fatalf("the fallback step ran %d times for two unclaimed names, want 2", inference.calls)
	}
}

// TestBuiltinLayerNeverRedirectsToAnotherTool is requirement 3.8 read from the
// other direction. A built-in pointer names one tool's member, and an operator that
// publishes no profile for a near-miss name must get nothing rather than a selector
// authored for a different tool. Requirement 2.8 preserves the tool name itself, so
// this is also the guarantee that a rewrite never travels from one tool's argument
// surface to another's.
func TestBuiltinLayerNeverRedirectsToAnotherTool(t *testing.T) {
	t.Parallel()

	// A provider declaring a custom tool whose name merely extends a built-in name is
	// a real situation: MCP servers publish arbitrary tool names. A built-in profile
	// must not become that tool's policy.
	const customTool = "read_file_v2"
	// The fallback step answers with a proven-looking selector, so if any built-in
	// authority leaked into this name the result would be non-empty and therefore
	// indistinguishable from a legitimate answer by size alone. The source is what
	// distinguishes them.
	inference := &stubInference{selectors: compileSelectorSet(t, "/inferred")}
	resolver := newTestResolver(t, nil, pathvirtualization.BuiltinToolProfiles(), inference)

	resolved := resolver.Resolve(customTool, declaredSchema)
	if resolved.Source == pathvirtualization.ProfileSourceBuiltin {
		t.Fatalf("tool %q resolved through the built-in layer of a different tool", customTool)
	}
	if got := argForms(resolved.ArgPointers); !slices.Equal(got, []string{"/inferred"}) {
		t.Fatalf("tool %q selected %q, want only the fallback step's selector", customTool, got)
	}
	if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("tool %q resolved opaque mode %v, want the disabled default", customTool, resolved.OpaqueResultMode)
	}
	// The fallback step is the only thing that may answer it, and it is consulted once.
	if inference.calls != 1 {
		t.Fatalf("the fallback step ran %d times for one unclaimed name, want 1", inference.calls)
	}

	// An operator profile for that custom tool is the only way it gains a selector.
	operator := newTestResolver(t,
		[]pathvirtualization.ToolProfile{{Names: []string{customTool}, ArgPointers: []string{"/custom_path"}}},
		pathvirtualization.BuiltinToolProfiles(),
		nil)
	got := operator.Resolve(customTool, declaredSchema)
	assertResolution(t, got, pathvirtualization.ProfileSourceOperator, []string{"/custom_path"}, nil,
		pathvirtualization.OpaqueResultModeNone)
}

// TestResolverRefusesAnUncompiledProfileLayer closes the last way an unusable
// mode could reach resolution. Compilation validates the mode, but a caller may
// also assemble compiled profiles directly, so binding validates them again rather
// than trusting the caller to have gone through the compiler.
func TestResolverRefusesAnUncompiledProfileLayer(t *testing.T) {
	t.Parallel()

	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{
		{Names: []string{"read_file"}, ArgPointers: []string{"/file_path"}},
	})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling the valid profile rejected with %v", reject)
	}
	handBuilt := append([]pathvirtualization.CompiledProfile(nil), compiled...)
	handBuilt[0].OpaqueResultMode = pathvirtualization.OpaqueResultMode(200)
	handBuilt[0].Names = []string{"read_file"}

	if resolver, reject := pathvirtualization.NewResolver(handBuilt, nil, nil); reject != pathvirtualization.SelectorRejectOpaqueMode || resolver != nil {
		t.Fatalf("operator layer with mode 200 bound as %v/%v, want %v/nil",
			reject, resolver, pathvirtualization.SelectorRejectOpaqueMode)
	}
	if resolver, reject := pathvirtualization.NewResolver(nil, handBuilt, nil); reject != pathvirtualization.SelectorRejectOpaqueMode || resolver != nil {
		t.Fatalf("built-in layer with mode 200 bound as %v/%v, want %v/nil",
			reject, resolver, pathvirtualization.SelectorRejectOpaqueMode)
	}
}

// TestResolveOnAnAbsentResolverSelectsNothing makes a missing policy safe by
// construction: with no resolver there is no exact layer and no inference step, so
// resolution is the design's fourth answer rather than a guess.
func TestResolveOnAnAbsentResolverSelectsNothing(t *testing.T) {
	t.Parallel()

	var absent *pathvirtualization.Resolver
	resolved := absent.Resolve("read_file", declaredSchema)
	if resolved.Source != pathvirtualization.ProfileSourceNone {
		t.Fatalf("source = %v, want %v", resolved.Source, pathvirtualization.ProfileSourceNone)
	}
	if len(resolved.ArgPointers) != 0 || len(resolved.ResultJSONPointers) != 0 {
		t.Fatalf("selected %q/%q with no resolver, want nothing",
			argForms(resolved.ArgPointers), argForms(resolved.ResultJSONPointers))
	}
	if resolved.OpaqueResultMode != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("opaque mode = %v, want the disabled default", resolved.OpaqueResultMode)
	}

	// An empty policy behaves the same way: both layers absent, inference absent.
	empty, reject := pathvirtualization.NewResolver(nil, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("binding an empty policy rejected with %v", reject)
	}
	if got := empty.Resolve("read_file", declaredSchema); !reflect.DeepEqual(got, resolved) {
		t.Fatalf("empty policy resolved %+v, want %+v", got, resolved)
	}
}

// TestProfileSourceLabelsAreBounded pins the fixed label of every resolution
// source. These labels are the only part of a resolution that may reach metrics, so
// each one is unique, fixed, and free of tool-name or pointer bytes.
func TestProfileSourceLabelsAreBounded(t *testing.T) {
	t.Parallel()

	sources := []struct {
		source pathvirtualization.ProfileSource
		label  string
	}{
		{pathvirtualization.ProfileSourceNone, "none"},
		{pathvirtualization.ProfileSourceOperator, "operator"},
		{pathvirtualization.ProfileSourceBuiltin, "builtin"},
		{pathvirtualization.ProfileSourceInference, "inference"},
	}
	labels := make(map[string]struct{}, len(sources))
	for _, tc := range sources {
		if got := tc.source.String(); got != tc.label {
			t.Errorf("ProfileSource(%d).String() = %q, want %q", tc.source, got, tc.label)
		}
		if _, duplicate := labels[tc.label]; duplicate {
			t.Errorf("source label %q is shared", tc.label)
		}
		labels[tc.label] = struct{}{}
	}
	if got := pathvirtualization.ProfileSource(200).String(); got != "unknown" {
		t.Errorf("out-of-range source label = %q, want %q", got, "unknown")
	}
	if got := pathvirtualization.SelectorRejectOpaqueMode.String(); got != "opaque_mode" {
		t.Errorf("opaque-mode rejection label = %q, want %q", got, "opaque_mode")
	}
}

// TestCompiledProfileNamesStayByteExactAcrossBothLayers re-proves the 3.1 name
// contract for the mode-carrying compile path, so adding the opaque mode to a
// profile cannot also introduce trimming, folding, or prefix authority.
func TestCompiledProfileNamesStayByteExactAcrossBothLayers(t *testing.T) {
	t.Parallel()

	spelled := []string{"Read_File", "read_file", "read_file_v2", "read_file "}
	profiles := make([]pathvirtualization.ToolProfile, 0, len(spelled))
	for _, name := range spelled {
		profiles = append(profiles, pathvirtualization.ToolProfile{Names: []string{name}, ArgPointers: []string{"/path"}})
	}
	compiled, reject := pathvirtualization.CompileToolProfiles(profiles)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles rejected with %v", reject)
	}
	for i, profile := range compiled {
		if profile.Names[0] != spelled[i] {
			t.Fatalf("name %d = %q, want %q", i, profile.Names[0], spelled[i])
		}
	}

	resolver := newTestResolver(t, profiles, nil, nil)
	for _, name := range spelled {
		resolved := resolver.Resolve(name, declaredSchema)
		if resolved.Source != pathvirtualization.ProfileSourceOperator {
			t.Errorf("name %q resolved through %v, want the operator layer", name, resolved.Source)
		}
		if got := argForms(resolved.ArgPointers); !slices.Equal(got, []string{"/path"}) {
			t.Errorf("name %q selected %q, want the declared pointer", name, got)
		}
	}
}

// SelectorSetOfCount parses count distinct canonical pointers into a compiled set.
// It is the direct construction of a selector set, so the binding tests can build
// a profile whose pointer count is past the compile-time bound without the compiler
// refusing to produce it.
func SelectorSetOfCount(t *testing.T, pointers []string) pathvirtualization.SelectorSet {
	t.Helper()

	set := make(pathvirtualization.SelectorSet, 0, len(pointers))
	for _, pointer := range pointers {
		selector, reject := pathvirtualization.ParseSelector(pointer)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("pointer %q rejected with %v", pointer, reject)
		}
		set = append(set, selector)
	}
	return set
}

// payloadConceptMembers is the fixed requirement 2.4 / 3.4 payload vocabulary a
// built-in pointer may never name. Requirement 2.4 forbids rewriting
// content/patch/source/script arguments unless an explicit profile marks the field
// path-bearing, and a built-in that already named one would grant that authority
// without an operator ever asking for it.
var payloadConceptMembers = []string{
	"content",
	"contents",
	"patch",
	"diff",
	"script",
	"command",
	"cmd",
	"query",
	"expression",
	"replacement",
	"body",
	"data",
	"text",
	"source",
	"source_code",
}
