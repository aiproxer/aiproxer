package schemainfer_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file proves the real implementation of the lexical core's optional
// inference port. Task 3.2 built the inference step; task 3.3 owns the resolution
// order that consults it, and this file is the seam between the two: it shows that
// the subpackage's conservative step satisfies the port, and that a resolver bound
// to the real step resolves a real tool schema exactly as the order says.

// compileInference binds the default vocabulary into one immutable inference step
// and fails the test if the vocabulary is refused.
func compileInference(t *testing.T) *schemainfer.Inferrer {
	t.Helper()

	inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling the default vocabulary rejected with %v", reject)
	}
	return inferrer
}

// bindResolver binds the shipped built-in layer, no operator layer, and the real
// inference step into one resolver.
func bindResolver(t *testing.T, inference pathvirtualization.ArgumentInference) *pathvirtualization.Resolver {
	t.Helper()

	builtIn, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling the built-in layer rejected with %v", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtIn, inference)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("binding the layers rejected with %v", reject)
	}
	return resolver
}

// TestRealInferenceResolvesThroughTheDesignOrder walks the whole order with the
// real inference step behind it, using real declared schemas. This is the only test
// that proves step 3 is reachable in production wiring rather than only against a
// stub: an exact profile answers first, a tool with no exact profile is answered
// from its declared schema, and an unprovable schema selects nothing.
func TestRealInferenceResolvesThroughTheDesignOrder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		operator    []pathvirtualization.ToolProfile
		tool        string
		schema      string
		wantSource  pathvirtualization.ProfileSource
		wantArgs    []string
		wantResults []string
		wantMode    pathvirtualization.OpaqueResultMode
	}{
		{
			name:     "step_1_operator_profile_answers_before_any_schema_is_read",
			operator: []pathvirtualization.ToolProfile{{Names: []string{"declared_tool"}, ArgPointers: []string{"/operator_arg"}}},
			tool:     "declared_tool",
			schema:   `{"type":"object","properties":{"path":{"type":"string"}}}`,
			// The schema would have proven /path, and it must not: the operator profile
			// is authoritative and its answer is the whole answer.
			wantSource: pathvirtualization.ProfileSourceOperator,
			wantArgs:   []string{"/operator_arg"},
		},
		{
			name:       "step_2_builtin_profile_answers_before_any_schema_is_read",
			tool:       "read_file",
			schema:     `{"type":"object","properties":{"file_path":{"type":"string"}}}`,
			wantSource: pathvirtualization.ProfileSourceBuiltin,
			wantArgs:   []string{"/file_path"},
		},
		{
			name:       "step_3_inferred_from_a_provable_schema",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"path":{"type":"string"}}}`,
			wantSource: pathvirtualization.ProfileSourceInference,
			wantArgs:   []string{"/path"},
		},
		{
			name:       "step_3_infers_every_provable_location_at_once",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}},"options":{"type":"object","properties":{"cwd":{"type":"string"}}}}}`,
			wantSource: pathvirtualization.ProfileSourceInference,
			// Inference publishes in canonical spelling order, so the result is
			// independent of how the schema happened to declare its members.
			wantArgs: []string{"/options/cwd", "/paths"},
		},
		{
			name:       "step_3_never_publishes_a_payload_concept",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"content":{"type":"string"},"path":{"type":"string"}}}`,
			wantSource: pathvirtualization.ProfileSourceInference,
			wantArgs:   []string{"/path"},
		},
		{
			name:        "step_3_publishes_no_result_selector",
			tool:        "prover_tool",
			schema:      `{"type":"object","properties":{"path":{"type":"string"}}}`,
			wantSource:  pathvirtualization.ProfileSourceInference,
			wantArgs:    []string{"/path"},
			wantResults: nil,
		},
		{
			name:       "step_4_unprovable_schema_selects_nothing",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"content":{"type":"string"}}}`,
			wantSource: pathvirtualization.ProfileSourceNone,
		},
		{
			name:       "step_4_ambiguous_schema_selects_nothing",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"path":{"oneOf":[{"type":"string"}]}}}`,
			wantSource: pathvirtualization.ProfileSourceNone,
		},
		{
			name:       "step_4_absent_schema_selects_nothing",
			tool:       "prover_tool",
			schema:     "",
			wantSource: pathvirtualization.ProfileSourceNone,
		},
		{
			name:       "step_4_unknown_tool_is_never_given_an_opaque_mode",
			tool:       "prover_tool",
			schema:     `{"type":"object","properties":{"path":{"type":"string"}}}`,
			wantSource: pathvirtualization.ProfileSourceInference,
			wantArgs:   []string{"/path"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := bindResolver(t, compileInference(t))
			if len(tc.operator) > 0 {
				operator, reject := pathvirtualization.CompileToolProfiles(tc.operator)
				if reject != pathvirtualization.SelectorRejectNone {
					t.Fatalf("compiling the operator layer rejected with %v", reject)
				}
				builtIn, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
				if reject != pathvirtualization.SelectorRejectNone {
					t.Fatalf("compiling the built-in layer rejected with %v", reject)
				}
				rebound, reject := pathvirtualization.NewResolver(operator, builtIn, compileInference(t))
				if reject != pathvirtualization.SelectorRejectNone {
					t.Fatalf("binding the layers rejected with %v", reject)
				}
				resolver = rebound
			}

			resolved := resolver.Resolve(tc.tool, []byte(tc.schema))
			if resolved.Source != tc.wantSource {
				t.Fatalf("source = %v, want %v", resolved.Source, tc.wantSource)
			}
			if got := canonicalPointers(resolved.ArgPointers); !equalStrings(got, tc.wantArgs) {
				t.Fatalf("argument selectors = %q, want %q", got, tc.wantArgs)
			}
			if got := canonicalPointers(resolved.ResultJSONPointers); len(got) != len(tc.wantResults) {
				t.Fatalf("result selectors = %q, want %q", got, tc.wantResults)
			}
			if resolved.OpaqueResultMode != tc.wantMode {
				t.Fatalf("opaque mode = %v, want the disabled default", resolved.OpaqueResultMode)
			}
		})
	}
}

// TestRealInferenceResolvesAreRepeatableAndIndependent proves the real step
// publishes the same answer for the same declared bytes every time, and that the
// selectors a resolution hands back are a private copy the caller cannot use to
// reach the policy behind it or the next resolution.
func TestRealInferenceResolvesAreRepeatableAndIndependent(t *testing.T) {
	t.Parallel()

	resolver := bindResolver(t, compileInference(t))
	schema := []byte(`{"type":"object","properties":{"path":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}}}`)

	first := resolver.Resolve("prover_tool", schema)
	for i := range 100 {
		again := resolver.Resolve("prover_tool", schema)
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("resolution %d = %+v, want %+v", i, again, first)
		}
	}

	wantSelectors := canonicalPointers(first.ArgPointers)
	if len(wantSelectors) != 2 {
		t.Fatalf("declared schema proved %q, want two selectors", wantSelectors)
	}
	// Overwrite what a resolution handed back and prove the next resolution is
	// unaffected. Truncating the slice would only change the caller's own header, so
	// the probe writes through every element the caller received.
	clear(first.ArgPointers)
	if got := canonicalPointers(resolver.Resolve("prover_tool", schema).ArgPointers); !equalStrings(got, wantSelectors) {
		t.Fatalf("argument selectors after caller overwrite = %q, want %q", got, wantSelectors)
	}
	// The exact layers are the case that actually shares: one compiled selector set
	// answers every resolution of an exactly named tool, so overwriting a result must
	// not corrupt the compiled policy either.
	builtIn := resolver.Resolve("read_file", nil)
	if len(builtIn.ArgPointers) == 0 {
		t.Fatal("the built-in profile resolved no selectors to test independence against")
	}
	wantBuiltIn := canonicalPointers(builtIn.ArgPointers)
	clear(builtIn.ArgPointers)
	if got := canonicalPointers(resolver.Resolve("read_file", nil).ArgPointers); !equalStrings(got, wantBuiltIn) {
		t.Fatalf("built-in selectors after caller overwrite = %q, want %q", got, wantBuiltIn)
	}
}

// TestRealInferenceRefusedVocabularyStaysOff proves a refused vocabulary cannot
// become a working inference step. The refused step is published as a nil pointer by
// the compiler, so binding it must yield a resolver that answers every tool from an
// exact profile alone and selects nothing for any other tool.
func TestRealInferenceRefusedVocabularyStaysOff(t *testing.T) {
	t.Parallel()

	inferrer, reject := schemainfer.New([]string{"path", "path"})
	if reject != pathvirtualization.SelectorRejectDuplicatePathKey {
		t.Fatalf("a duplicate-key vocabulary was accepted: %v", reject)
	}
	if inferrer != nil {
		t.Fatal("a refused vocabulary still published an inference step")
	}

	// A nil *Inferrer satisfies the port and answers every schema with nothing, so a
	// resolver bound to it behaves exactly like one with no step at all.
	resolver := bindResolver(t, inferrer)
	if got := resolver.Resolve("prover_tool", []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)); got.Source != pathvirtualization.ProfileSourceNone {
		t.Fatalf("source = %v, want %v", got.Source, pathvirtualization.ProfileSourceNone)
	}
	// The exact layers still answer, which is what proves only the fallback was off.
	if got := resolver.Resolve("read_file", nil); got.Source != pathvirtualization.ProfileSourceBuiltin {
		t.Fatalf("built-in source = %v, want %v", got.Source, pathvirtualization.ProfileSourceBuiltin)
	}
}

// TestRealInferenceReadsOnlyTheDeclaredStructure proves the two halves of the
// resolution order draw their authority from different places, which is what makes
// the order itself trustworthy.
//
// The port passes the declared schema bytes through unchanged and reads nothing
// else. A tool's own name and description are never consulted, because exact-name
// authority belongs to the profile layers and prose is never a selector signal. Only
// a declared PROPERTY's description can suppress a candidate, and only by removing
// one: it can never create one.
func TestRealInferenceReadsOnlyTheDeclaredStructure(t *testing.T) {
	t.Parallel()

	provable := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	tool := aipapi.ToolDef{Name: "prover_tool", Parameters: provable}
	before := append(json.RawMessage(nil), tool.Parameters...)

	if result := compileInference(t).InferArguments(tool); result.Outcome != schemainfer.OutcomeInferred {
		t.Fatalf("outcome = %v, want %v", result.Outcome, schemainfer.OutcomeInferred)
	}
	if !equalBytes(tool.Parameters, before) {
		t.Fatal("inference rewrote the declared schema")
	}

	// The tool's own description names a payload concept and changes nothing, because
	// the step reads declared structure only.
	tool.Description = "Writes the file contents at path"
	if result := compileInference(t).InferArguments(tool); result.Outcome != schemainfer.OutcomeInferred {
		t.Fatalf("outcome with payload prose in the tool description = %v, want %v",
			result.Outcome, schemainfer.OutcomeInferred)
	}
	// The tool's own name is not a path key and changes nothing either.
	tool.Name = "content"
	if result := compileInference(t).InferArguments(tool); result.Outcome != schemainfer.OutcomeInferred {
		t.Fatalf("outcome with a payload-concept tool name = %v, want %v",
			result.Outcome, schemainfer.OutcomeInferred)
	}

	// A declared property description naming a payload concept does suppress the
	// candidate. That is the difference: the signal lives in the declared structure,
	// not in the tool's identity.
	suppressed := aipapi.ToolDef{
		Name:       "prover_tool",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"the file contents to write"}}}`),
	}
	if result := compileInference(t).InferArguments(suppressed); result.Outcome != schemainfer.OutcomeNoPathKeys {
		t.Fatalf("outcome with payload prose in the declared description = %v, want %v",
			result.Outcome, schemainfer.OutcomeNoPathKeys)
	}
	// The same schema resolves to nothing through the port too, so the suppression
	// survives the seam rather than being reintroduced by the order.
	if got := compileInference(t).InferArgumentSelectors(suppressed.Parameters); got != nil {
		t.Fatalf("the port published %q for a suppressed candidate, want nothing", canonicalPointers(got))
	}
}

// equalStrings compares two string slices.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// equalBytes compares two byte slices.
func equalBytes(got, want []byte) bool {
	return equalStrings(bytesAsStrings(got), bytesAsStrings(want))
}

// bytesAsStrings views each byte of a slice as a string, which keeps the comparison
// helpers byte-exact without pulling in a third-party assertion package.
func bytesAsStrings(raw []byte) []string {
	view := make([]string, len(raw))
	for i, b := range raw {
		view[i] = string([]byte{b})
	}
	return view
}
