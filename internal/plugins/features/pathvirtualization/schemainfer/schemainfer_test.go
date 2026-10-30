package schemainfer_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file covers task 3.2 of design.md 197-229: the conservative
// schema-assisted inference step that turns a tool's declared argument schema
// into canonical JSON Pointer argument selectors (requirements 3.3, 3.4, 3.5,
// 3.8).
//
// Every case is a table over a representative tool schema spelled the way real
// coding-agent tool surfaces spell it, and every expectation is stated as the
// canonical pointers plus one bounded, content-free outcome label. Nothing here
// asserts anything about a filesystem, a host, or a path value: the inference
// step never looks at an argument value at all.

// canonicalPointers returns the canonical spellings of a selector set in the
// order inference publishes them.
func canonicalPointers(set pathvirtualization.SelectorSet) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for _, selector := range set {
		out = append(out, selector.String())
	}
	return out
}

// defaultVocabulary returns the fixed V1 default path-key vocabulary.
func defaultVocabulary() []string {
	return schemainfer.DefaultPathKeys()
}

// inferAgainst runs inference for one schema with an explicit vocabulary.
func inferAgainst(t *testing.T, schema string, pathKeys []string) schemainfer.Result {
	t.Helper()
	inferrer, reject := schemainfer.New(pathKeys)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("New(%d keys) rejected with %v", len(pathKeys), reject)
	}
	return inferrer.InferArguments(aipapi.ToolDef{Name: "tool", Parameters: json.RawMessage(schema)})
}

// infer runs inference for one schema with the fixed default vocabulary.
func infer(t *testing.T, schema string) schemainfer.Result {
	t.Helper()
	return inferAgainst(t, schema, defaultVocabulary())
}

// TestInferArgumentsReadsDeclaredArgumentSchemas is the positive table: real
// coding-agent tool schemas whose declared structure alone proves a path-bearing
// location must yield the canonical pointer for it, and nothing else.
func TestInferArgumentsReadsDeclaredArgumentSchemas(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		schema   string
		pathKeys []string
		want     []string
	}{
		{
			name:   "read_file_single_path",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
			want:   []string{`/path`},
		},
		{
			name:   "read_file_with_non_path_siblings",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"},"encoding":{"type":"string"}}}`,
			want:   []string{`/path`},
		},
		{
			name:   "write_file_path_beside_content",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`,
			want:   []string{`/path`},
		},
		{
			name:   "edit_file_underscore_spelling",
			schema: `{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}}}`,
			want:   []string{`/file_path`},
		},
		{
			name:   "edit_file_flat_spelling",
			schema: `{"type":"object","properties":{"filepath":{"type":"string"}}}`,
			want:   []string{`/filepath`},
		},
		{
			name:   "list_files_directory_and_paths",
			schema: `{"type":"object","properties":{"dir":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"recursive":{"type":"boolean"}}}`,
			want:   []string{`/dir`, `/paths`},
		},
		{
			name:   "run_command_cwd_only",
			schema: `{"type":"object","properties":{"command":{"type":"string"},"cwd":{"type":"string"},"timeout":{"type":"number"}}}`,
			want:   []string{`/cwd`},
		},
		{
			name:   "search_code_query_plus_path",
			schema: `{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"}}}`,
			want:   []string{`/path`},
		},
		{
			name:   "apply_patch_patch_plus_workdir",
			schema: `{"type":"object","properties":{"patch":{"type":"string"},"workdir":{"type":"string"}}}`,
			want:   []string{`/workdir`},
		},
		{
			name:   "delete_file_root_spelling",
			schema: `{"type":"object","properties":{"root":{"type":"string"},"force":{"type":"boolean"}}}`,
			want:   []string{`/root`},
		},
		{
			name:   "nested_declared_object",
			schema: `{"type":"object","properties":{"target":{"type":"object","properties":{"url":{"type":"string"},"target_path":{"type":"string"}}}}}`,
			want:   []string{`/target/target_path`},
		},
		{
			name:   "deeply_nested_declared_objects",
			schema: `{"type":"object","properties":{"edit":{"type":"object","properties":{"scope":{"type":"object","properties":{"directory":{"type":"string"}}}}}}}`,
			want:   []string{`/edit/scope/directory`},
		},
		{
			name:   "path_key_named_container_is_descended_not_inferred",
			schema: `{"type":"object","properties":{"paths":{"type":"object","properties":{"root":{"type":"string"}}}}}`,
			want:   []string{`/paths/root`},
		},
		{
			name:   "array_of_strings_is_inferred",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"},"minItems":1}}}`,
			want:   []string{`/paths`},
		},
		{
			name:   "declared_schema_without_root_type_is_still_read",
			schema: `{"properties":{"path":{"type":"string"}}}`,
			want:   []string{`/path`},
		},
		{
			name:   "published_order_is_canonical_not_declaration_order",
			schema: `{"type":"object","properties":{"workdir":{"type":"string"},"cwd":{"type":"string"},"directory":{"type":"string"}}}`,
			want:   []string{`/cwd`, `/directory`, `/workdir`},
		},
		{
			// A `contains` or `prefixItems` schema declared on an *item* schema binds
			// no position of the array: every position is still governed by the `items`
			// schema, so the array remains a proven array of strings. This pins the
			// reason `unevenArrayKeywords` is consulted on the array node rather than
			// anywhere in its subtree — the reasoning lives only in prose otherwise, so
			// extending that keyword list could silently start refusing this shape.
			name:   "tuple_keyword_on_an_item_schema_does_not_uneven_the_array",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string","contains":{"type":"string"},"prefixItems":[{"type":"string"}]}}}}`,
			want:   []string{`/paths`},
		},
		{
			name:   "additionalProperties_schema_never_contributes",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":{"type":"object","properties":{"path":{"type":"string"}}}}`,
			want:   []string{`/path`},
		},
		{
			name:   "additionalProperties_true_never_contributes",
			schema: `{"type":"object","properties":{"dir":{"type":"string"}},"additionalProperties":true}`,
			want:   []string{`/dir`},
		},
		{
			name:   "pattern_properties_channel_never_contributes",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"patternProperties":{"^x-":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
			want:   []string{`/path`},
		},
		{
			name:     "operator_vocabulary_extends_default",
			schema:   `{"type":"object","properties":{"source_path":{"type":"string"},"destination_path":{"type":"string"},"name":{"type":"string"}}}`,
			pathKeys: []string{"path", "file_path", "filepath", "directory", "dir", "cwd", "workdir", "root", "target_path", "paths", "source_path", "destination_path"},
			want:     []string{`/destination_path`, `/source_path`},
		},
		{
			name:   "two_members_normalizing_to_one_key_are_two_locations",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"Path":{"type":"string"}}}`,
			want:   []string{`/Path`, `/path`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pathKeys := tc.pathKeys
			if pathKeys == nil {
				pathKeys = defaultVocabulary()
			}
			got := inferAgainst(t, tc.schema, pathKeys)
			if got.Outcome != schemainfer.OutcomeInferred {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
			}
			if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, tc.want) {
				t.Fatalf("pointers = %q, want %q", pointers, tc.want)
			}
		})
	}
}

// TestInferArgumentsPublishesCanonicalOrderNotTraversalOrder pins the publication
// order contract: inferred selectors are ordered by canonical spelling, not by the
// order the walk happened to reach them.
//
// The two orders diverge when a container's subtree sorts after a sibling member
// whose declared name extends the container's own name with a byte below `/` (0x2F).
// The walk reaches the container first because `a` sorts before `a!path`, while the
// canonical spelling orders `/a!path` first because `!` sorts before the separator.
func TestInferArgumentsPublishesCanonicalOrderNotTraversalOrder(t *testing.T) {
	t.Parallel()

	// `a_path` is the normalized spelling of the declared member `a!path`, so the
	// widened vocabulary is what makes that member a candidate at all.
	got := inferAgainst(t, `{"type":"object","properties":{"a":{"type":"object","properties":{"path":{"type":"string"}}},"a!path":{"type":"string"}}}`, []string{"path", "a_path"})
	if got.Outcome != schemainfer.OutcomeInferred {
		t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
	}
	if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{`/a!path`, `/a/path`}) {
		t.Fatalf("pointers = %q, want the canonical order %q", pointers, []string{`/a!path`, `/a/path`})
	}
}

// TestInferArgumentsRefusesPayloadConcepts pins requirement 3.4 and design.md
// 221-222: a payload concept is never inferred and is never descended through,
// whether the payload signal is the declared name, a normalized spelling of it,
// or a declared description.
func TestInferArgumentsRefusesPayloadConcepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "path_key_nested_inside_content_object",
			schema: `{"type":"object","properties":{"content":{"type":"object","properties":{"file_path":{"type":"string"}}}}}`,
		},
		{
			name:   "path_key_nested_two_levels_inside_content_object",
			schema: `{"type":"object","properties":{"contents":{"type":"object","properties":{"new":{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}}}}}}}`,
		},
		{
			name:   "patch_container_holding_path_key",
			schema: `{"type":"object","properties":{"patch":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "diff_container_holding_path_key",
			schema: `{"type":"object","properties":{"diff":{"type":"object","properties":{"root":{"type":"string"}}}}}`,
		},
		{
			name:   "script_container_holding_path_key",
			schema: `{"type":"object","properties":{"script":{"type":"object","properties":{"cwd":{"type":"string"}}}}}`,
		},
		{
			name:   "command_container_holding_path_key",
			schema: `{"type":"object","properties":{"command":{"type":"object","properties":{"workdir":{"type":"string"}}}}}`,
		},
		{
			name:   "cmd_container_holding_path_key",
			schema: `{"type":"object","properties":{"cmd":{"type":"object","properties":{"dir":{"type":"string"}}}}}`,
		},
		{
			name:   "query_container_holding_path_key",
			schema: `{"type":"object","properties":{"query":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "expression_container_holding_path_key",
			schema: `{"type":"object","properties":{"expression":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "replacement_container_holding_path_key",
			schema: `{"type":"object","properties":{"replacement":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "body_container_holding_path_key",
			schema: `{"type":"object","properties":{"body":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "data_container_holding_path_key",
			schema: `{"type":"object","properties":{"data":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "text_container_holding_path_key",
			schema: `{"type":"object","properties":{"text":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "source_container_holding_path_key",
			schema: `{"type":"object","properties":{"source":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "source_code_container_holding_path_key",
			schema: `{"type":"object","properties":{"source_code":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "denylist_name_in_capitals",
			schema: `{"type":"object","properties":{"CONTENT":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "denylist_name_with_dotted_spelling",
			schema: `{"type":"object","properties":{"source.code":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "candidate_described_as_content",
			schema: `{"type":"object","properties":{"path":{"type":"string","description":"the file content to write at this location"}}}`,
		},
		{
			name:   "candidate_described_as_a_diff",
			schema: `{"type":"object","properties":{"path":{"type":"string","description":"unified diff target"}}}`,
		},
		{
			name:   "candidate_described_as_source_text",
			schema: `{"type":"object","properties":{"dir":{"type":"string","description":"directory holding the source text"}}}`,
		},
		{
			name:   "candidate_described_as_capitalized_content",
			schema: `{"type":"object","properties":{"path":{"type":"string","description":"Content to write at this location"}}}`,
		},
		{
			name:   "container_described_as_body_holding_path_key",
			schema: `{"type":"object","properties":{"where":{"type":"object","description":"the request body to inspect","properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "container_described_as_a_command_holding_path_key",
			schema: `{"type":"object","properties":{"where":{"type":"object","description":"shell command to run","properties":{"cwd":{"type":"string"}}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != schemainfer.OutcomeNoPathKeys {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNoPathKeys)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none for a denylisted payload concept", pointers)
			}
		})
	}
}

// TestInferArgumentsReadsStructureNotDescriptionProse pins design.md 225: a
// declared description can only ever suppress inference, never create it. A
// property whose only path signal lives in prose must not be inferred.
func TestInferArgumentsReadsStructureNotDescriptionProse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "described_as_absolute_path",
			schema: `{"type":"object","properties":{"target":{"type":"string","description":"absolute filesystem path of the file to read"}}}`,
		},
		{
			name:   "described_as_a_directory",
			schema: `{"type":"object","properties":{"where":{"type":"string","description":"the directory to list"}}}`,
		},
		{
			name:   "described_as_cwd",
			schema: `{"type":"object","properties":{"place":{"type":"string","description":"working directory (cwd) for the search"}}}`,
		},
		{
			name:   "described_as_a_file_path_list",
			schema: `{"type":"object","properties":{"selection":{"type":"array","items":{"type":"string"},"description":"the file paths to include"}}}`,
		},
		{
			name:   "title_and_examples_are_not_authority",
			schema: `{"type":"object","properties":{"location":{"type":"string","title":"file path","examples":["/etc/hosts"]}}}`,
		},
		{
			name:   "root_description_does_not_license_anything",
			schema: `{"type":"object","description":"arguments are a path and a target","properties":{"target":{"type":"string"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != schemainfer.OutcomeNoPathKeys {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNoPathKeys)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none; description prose is not authority", pointers)
			}
		})
	}
}

// TestInferArgumentsIgnoresToolLevelProse proves the tool's own declared
// description is not a selector signal: prose about a tool can never create or
// remove a path-bearing location, only the declared argument structure can.
func TestInferArgumentsIgnoresToolLevelProse(t *testing.T) {
	t.Parallel()

	inferrer, reject := schemainfer.New(defaultVocabulary())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("New rejected with %v", reject)
	}
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	for _, description := range []string{
		"",
		"reads a file and returns its whole content as a patch",
		"the absolute path of the file to read",
	} {
		got := inferrer.InferArguments(aipapi.ToolDef{Name: "read_file", Description: description, Parameters: schema})
		if got.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("tool description %q changed the outcome to %q", description, got.Outcome)
		}
		if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{"/path"}) {
			t.Fatalf("tool description %q changed the pointers to %q", description, pointers)
		}
	}
}

// TestInferArgumentsDoesNotInferThroughOpenOrUnprovableShapes pins the shape
// restriction of requirement 3.3 and the open-channel rule of design.md 226: only
// a declared string and a declared array of strings are inferable, and no
// arbitrary-key channel or undeclared kind is ever traversed.
func TestInferArgumentsDoesNotInferThroughOpenOrUnprovableShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "integer_path",
			schema: `{"type":"object","properties":{"path":{"type":"integer"}}}`,
		},
		{
			name:   "number_path",
			schema: `{"type":"object","properties":{"path":{"type":"number"}}}`,
		},
		{
			name:   "boolean_path",
			schema: `{"type":"object","properties":{"path":{"type":"boolean"}}}`,
		},
		{
			name:   "null_path",
			schema: `{"type":"object","properties":{"path":{"type":"null"}}}`,
		},
		{
			name:   "untyped_path",
			schema: `{"type":"object","properties":{"path":{"description":"the file path"}}}`,
		},
		{
			name:   "array_without_items",
			schema: `{"type":"object","properties":{"paths":{"type":"array"}}}`,
		},
		{
			name:   "array_of_integers",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"integer"}}}}`,
		},
		{
			name:   "array_of_untyped_items",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{}}}}`,
		},
		{
			name:   "array_with_prefix_items",
			schema: `{"type":"object","properties":{"paths":{"type":"array","prefixItems":[{"type":"string"}],"items":{"type":"string"}}}}`,
		},
		{
			name:   "array_with_additional_items",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"},"additionalItems":{"type":"object"}}}}`,
		},
		{
			name:   "array_with_contains",
			schema: `{"type":"object","properties":{"paths":{"type":"array","contains":{"type":"string"},"items":{"type":"string"}}}}`,
		},
		{
			name:   "array_items_boolean_form",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":true}}}`,
		},
		{
			name:   "array_items_number_form",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":42}}}`,
		},
		{
			name:   "object_path_key_with_no_path_key_below",
			schema: `{"type":"object","properties":{"root":{"type":"object","properties":{"name":{"type":"string"}}}}}`,
		},
		{
			name:   "array_of_objects_is_never_traversed",
			schema: `{"type":"object","properties":{"edits":{"type":"array","items":{"type":"object","properties":{"file_path":{"type":"string"}}}}}}`,
		},
		{
			name:   "array_of_path_key_arrays_is_never_traversed",
			schema: `{"type":"object","properties":{"edits":{"type":"array","items":{"type":"array","items":{"type":"string"}}}}}`,
		},

		{
			name:   "pattern_properties_only_schema",
			schema: `{"type":"object","patternProperties":{"^dir_":{"type":"string"}}}`,
		},
		{
			name:   "container_without_declared_type_is_not_traversed",
			schema: `{"type":"object","properties":{"options":{"properties":{"path":{"type":"string"}}}}}`,
		},
		{
			name:   "non_vocabulary_path_ish_name",
			schema: `{"type":"object","properties":{"source_path":{"type":"string"},"filename":{"type":"string"}}}`,
		},
		{
			name:   "empty_object_schema",
			schema: `{"type":"object"}`,
		},
		{
			name:   "boolean_true_property_schema",
			schema: `{"type":"object","properties":{"path":true}}`,
		},
		{
			name:   "property_schema_is_a_string",
			schema: `{"type":"object","properties":{"path":"a string"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != schemainfer.OutcomeNoPathKeys {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNoPathKeys)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none for an unprovable shape", pointers)
			}
		})
	}
}

// TestInferArgumentsSkipsAmbiguousSchemas pins design.md 228: an unknown or
// ambiguous tool is skipped with a bounded reason instead of being guessed at.
func TestInferArgumentsSkipsAmbiguousSchemas(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
		want   schemainfer.Outcome
	}{
		{
			name:   "root_reference_only",
			schema: `{"$ref":"#/$defs/ReadArgs"}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_all_of",
			schema: `{"allOf":[{"type":"object","properties":{"path":{"type":"string"}}}]}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_any_of",
			schema: `{"anyOf":[{"type":"object","properties":{"path":{"type":"string"}}}]}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_one_of",
			schema: `{"oneOf":[{"type":"object","properties":{"path":{"type":"string"}}}]}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_one_of",
			schema: `{"type":"object","properties":{"path":{"oneOf":[{"type":"string"},{"type":"null"}]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_any_of_with_paths",
			schema: `{"type":"object","properties":{"paths":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"string"}]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_all_of",
			schema: `{"type":"object","properties":{"path":{"allOf":[{"type":"string"}]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_not",
			schema: `{"type":"object","properties":{"path":{"type":"string","not":{"const":"/etc/shadow"}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_dependent_schemas",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"dependentSchemas":{"path":{"required":["other"]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "first_branch_in_canonical_order_decides_the_reason",
			schema: `{"type":"object","properties":{"aaa":{"type":"object","$ref":"#/$defs/A"},"bbb":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "later_branch_failure_still_stops_the_walk",
			schema: `{"type":"object","properties":{"aaa":{"type":"object","properties":{"path":{"type":"string"}}},"bbb":{"type":"object","$ref":"#/$defs/B"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "nested_later_branch_failure_stops_the_outer_walk_too",
			schema: `{"type":"object","properties":{"top":{"type":"object","properties":{"aaa":{"type":"object","$ref":"#/$defs/A"},"bbb":{"type":"object","properties":{"path":{"type":"string"}}}}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_if_then",
			schema: `{"type":"object","properties":{"path":{"type":"string","if":{"type":"string"},"then":{"type":"string"}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_type_union",
			schema: `{"type":"object","properties":{"path":{"type":["string","null"]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "sibling_reference_poisons_the_tool",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"options":{"$ref":"#/$defs/Options"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "reference_into_definitions_poisons_the_tool",
			schema: `{"type":"object","properties":{"options":{"$ref":"#/$defs/Options"}},"$defs":{"Options":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "array_items_one_of",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "array_items_reference",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"$ref":"#/$defs/Name"}}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "properties_is_not_an_object",
			schema: `{"type":"object","properties":[]}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "properties_is_a_string",
			schema: `{"type":"object","properties":"path"}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_type_is_not_a_string",
			schema: `{"type":["object"],"properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_type_is_an_object_value",
			schema: `{"type":{},"properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "candidate_type_is_null",
			schema: `{"type":"object","properties":{"path":{"type":null}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "root_declares_a_non_object_kind",
			schema: `{"type":"array","items":{"type":"string"}}`,
			want:   schemainfer.OutcomeSchemaNotObject,
		},
		{
			name:   "duplicate_property_name",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"path":{"type":"object"}}}`,
			want:   schemainfer.OutcomeSchemaDuplicate,
		},
		{
			name:   "duplicate_root_keyword",
			schema: `{"type":"object","type":"array","properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeSchemaDuplicate,
		},
		{
			name:   "duplicate_nested_property_name",
			schema: `{"type":"object","properties":{"options":{"type":"object","properties":{"path":{"type":"string"},"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeSchemaDuplicate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none for an ambiguous schema", pointers)
			}
		})
	}
}

// TestInferArgumentsReadsOnlyShapeBearingKeywords records, one case per keyword
// class, which declared keywords the reader deliberately does not read and why.
//
// The reader is not a JSON Schema implementation: it reads the keywords that
// decide a node's shape and refuses the rest. A keyword it does not read can
// therefore only fail in one direction — by making the step *more* permissive —
// so every case below is either a proof that the keyword cannot widen the proven
// surface, or an explicitly bounded decision rather than an oversight.
//
// The classes are:
//
//   - presence constraints (`required`, `dependentRequired`, `minProperties`,
//     `maxContains`, `propertyNames`): they constrain whether a location is
//     present or what it is called, never the kind of value behind it, so the
//     declared kind of an existing property is unchanged;
//   - identifiers and annotations (`$id`, `$schema`, `$vocabulary`,
//     `$dynamicAnchor`, `$recursiveAnchor`, `title`, `description` on a
//     non-candidate): they are metadata. The two reference keywords that do
//     change shape, `$dynamicRef` and `$recursiveRef`, are refused;
//   - `unevaluatedItems`: with a schema-form `items` every position is already
//     evaluated, so the keyword is vacuous; without `items` or `prefixItems` the
//     array has no proven element kind and is already not a candidate. It can
//     only ever narrow, and the two assertions below show it never widens;
//   - vendor extensions (`x-` and any other unread keyword): unknown keywords are
//     annotations, and refusing them would refuse the OpenAPI-shaped schemas real
//     tool surfaces publish. They must never *create* a candidate — a `path`
//     named only inside one is not inferred — and they must never *hide* an
//     ambiguity the reader does see;
//   - content-bearing annotations on a declared path key (`contentEncoding`,
//     `contentMediaType`, `format: "byte"`, `const`, `enum`): these describe how
//     the string is spelled, not whether it is a locator. Requirement 3.4 names
//     and design.md 221-222 list the payload signals as declared names and
//     declared descriptions, and this step does not widen that fixed contract to a
//     third signal class. The cases are pinned so that widening it later is a
//     visible, reviewed change; an operator who does not trust a class can already
//     drop the affected key from the vocabulary.
func TestInferArgumentsReadsOnlyShapeBearingKeywords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
		want   schemainfer.Outcome
		point  []string
	}{
		{
			name:   "presence_constraints_do_not_change_a_declared_kind",
			schema: `{"type":"object","required":["path","content"],"dependentRequired":{"path":["offset"]},"minProperties":1,"properties":{"path":{"type":"string"},"content":{"type":"string"},"offset":{"type":"integer"}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		{
			name:   "property_names_constraint_does_not_change_a_declared_kind",
			schema: `{"type":"object","propertyNames":{"pattern":"^[a-z]+$"},"properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		{
			name:   "identifiers_and_annotations_are_metadata",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"#ReadArgs","$vocabulary":{"https://json-schema.org/draft/2020-12/vocab/core":true},"$dynamicAnchor":"node","$recursiveAnchor":true,"title":"read file","type":"object","properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		{
			name:   "dynamic_and_recursive_references_are_still_refused",
			schema: `{"$dynamicRef":"#node","$recursiveRef":"#","type":"object","properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "unevaluated_items_beside_a_schema_form_items_is_vacuous",
			schema: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"},"unevaluatedItems":{"type":"object","properties":{"path":{"type":"string"}}}}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/paths"},
		},
		{
			name:   "unevaluated_items_cannot_supply_the_missing_element_kind",
			schema: `{"type":"object","properties":{"paths":{"type":"array","unevaluatedItems":{"type":"string"}}}}`,
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "unevaluated_properties_never_supplies_a_candidate",
			schema: `{"type":"object","unevaluatedProperties":{"type":"object","properties":{"path":{"type":"string"}}},"properties":{"offset":{"type":"integer"}}}`,
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "vendor_extension_never_supplies_a_candidate",
			schema: `{"type":"object","x-properties":{"path":{"type":"string"}},"properties":{"offset":{"type":"integer"}}}`,
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "vendor_extension_never_hides_a_visible_ambiguity",
			schema: `{"type":"object","x-note":"annotation","$ref":"#/$defs/ReadArgs","properties":{"path":{"type":"string"}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "content_bearing_annotations_are_outside_the_fixed_denylist",
			schema: `{"type":"object","properties":{"path":{"type":"string","contentEncoding":"base64","contentMediaType":"application/octet-stream","format":"byte","const":"Li9yb290","enum":["Li9yb290"]}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, tc.point) {
				t.Fatalf("pointers = %q, want %q", pointers, tc.point)
			}
		})
	}
}

// TestInferArgumentsReadsTheRootObjectWithoutRequiringADeclaredType names the one
// asymmetry in container handling, so it is not later read as an oversight.
//
// The root is read even when it declares no `type`, because the canonical
// argument payload is a JSON object by contract (requirements 3.3, 3.5): the
// bytes under the pointer come from the same object the root describes, so a
// missing `type` says nothing about whether the declared properties exist. A
// nested location is different — nothing in the payload guarantees that a
// non-candidate container is present at all — so a nested node that lists
// properties without declaring `type: "object"` is not traversed and publishes
// nothing. Both halves are asserted here from the same fixture shape.
func TestInferArgumentsReadsTheRootObjectWithoutRequiringADeclaredType(t *testing.T) {
	t.Parallel()

	t.Run("root_without_a_declared_type_is_read", func(t *testing.T) {
		t.Parallel()

		got := infer(t, `{"properties":{"path":{"type":"string"},"options":{"type":"object","properties":{"dir":{"type":"string"}}}}}`)
		if got.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
		}
		// The root is read, and so is the nested object, because the nested one does
		// declare its kind.
		if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{"/options/dir", "/path"}) {
			t.Fatalf("pointers = %q, want %q", pointers, []string{"/options/dir", "/path"})
		}
	})

	t.Run("nested_without_a_declared_type_is_not_a_container", func(t *testing.T) {
		t.Parallel()

		got := infer(t, `{"type":"object","properties":{"options":{"properties":{"dir":{"type":"string"}}}}}`)
		if got.Outcome != schemainfer.OutcomeNoPathKeys {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNoPathKeys)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none; the chain closes at the undeclared kind", pointers)
		}
	})
}

// TestInferArgumentsIgnoresMemberDeclarationOrder is the regression guard for the
// whole refusal signal, not for one schema: a declared member's position inside
// its object must never change what inference concludes.
//
// Two order-dependent shapes existed and both are pinned here. The first is an
// accumulated ambiguity flag being cleared by a later member — schema generators
// routinely emit `$ref` before `type`, and reading `type` as a whole-struct
// assignment let the later, unambiguous `type` erase the reference already
// recorded, so the tool was inferred through a branch the step cannot resolve.
// The second is a first-declaration-wins `type` being interpreted before the
// order-independent structural signals of the same object, so the reason code
// depended on which duplicate or ambiguous member happened to come first.
func TestInferArgumentsIgnoresMemberDeclarationOrder(t *testing.T) {
	t.Parallel()

	// Each keyword declares a structure this step cannot reduce to one proven
	// kind, so every spelling below must be skipped. `$ref` before `type` is the
	// spelling real generators emit, which is why the order that used to infer
	// is the one under test.
	ambiguousMembers := []struct {
		keyword string
		value   string
	}{
		{keyword: "$ref", value: `"#/$defs/X"`},
		{keyword: "allOf", value: `[{"type":"object"}]`},
		{keyword: "anyOf", value: `[{"type":"object"}]`},
		{keyword: "oneOf", value: `[{"type":"object"}]`},
		{keyword: "not", value: `{"type":"object"}`},
		{keyword: "if", value: `{"type":"object"}`},
	}
	for _, member := range ambiguousMembers {
		t.Run("ambiguous_keyword_"+member.keyword, func(t *testing.T) {
			t.Parallel()

			spelled := []struct {
				position string
				schema   string
			}{
				{
					position: "root_before_type",
					schema:   `{"` + member.keyword + `":` + member.value + `,"type":"object","properties":{"path":{"type":"string"}}}`,
				},
				{
					position: "root_after_type",
					schema:   `{"type":"object","properties":{"path":{"type":"string"}},"` + member.keyword + `":` + member.value + `}`,
				},
				{
					position: "nested_before_type",
					schema:   `{"type":"object","properties":{"a":{"` + member.keyword + `":` + member.value + `,"type":"object","properties":{"path":{"type":"string"}}}}}`,
				},
				{
					position: "nested_after_type",
					schema:   `{"type":"object","properties":{"a":{"type":"object","properties":{"path":{"type":"string"}},"` + member.keyword + `":` + member.value + `}}}`,
				},
				{
					position: "root_between_type_and_properties",
					schema:   `{"type":"object","` + member.keyword + `":` + member.value + `,"properties":{"path":{"type":"string"}}}`,
				},
			}
			for _, spelling := range spelled {
				t.Run(spelling.position, func(t *testing.T) {
					t.Parallel()

					got := infer(t, spelling.schema)
					if got.Outcome != schemainfer.OutcomeSchemaAmbiguous {
						t.Fatalf("outcome = %q, want %q for %q at %s", got.Outcome, schemainfer.OutcomeSchemaAmbiguous, member.keyword, spelling.position)
					}
					if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
						t.Fatalf("pointers = %q, want none; a declared %q must not be inferred through", pointers, member.keyword)
					}
				})
			}
		})
	}

	t.Run("repeated_type_member_reports_the_same_reason_either_way", func(t *testing.T) {
		t.Parallel()

		// A member declared twice is an order-independent fact about the object, so
		// which of the two kinds is read first must not decide the reason code.
		for _, schema := range []string{
			`{"type":"object","type":"array","properties":{"path":{"type":"string"}}}`,
			`{"type":"array","type":"object","properties":{"path":{"type":"string"}}}`,
		} {
			got := infer(t, schema)
			if got.Outcome != schemainfer.OutcomeSchemaDuplicate {
				t.Fatalf("outcome = %q, want %q for %s", got.Outcome, schemainfer.OutcomeSchemaDuplicate, schema)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none for a repeated member", pointers)
			}
		}
	})

	t.Run("ambiguous_root_reports_the_same_reason_as_a_non_object_kind", func(t *testing.T) {
		t.Parallel()

		// A root that both declares another kind and declares an unverifiable branch
		// is refused either way; only the reason label may have depended on order.
		for _, schema := range []string{
			`{"type":"array","$ref":"#/$defs/A","properties":{"path":{"type":"string"}}}`,
			`{"$ref":"#/$defs/A","type":"array","properties":{"path":{"type":"string"}}}`,
			`{"type":"array","not":{"type":"object"},"properties":{"path":{"type":"string"}}}`,
			`{"not":{"type":"object"},"type":"array","properties":{"path":{"type":"string"}}}`,
		} {
			got := infer(t, schema)
			if got.Outcome != schemainfer.OutcomeSchemaAmbiguous {
				t.Fatalf("outcome = %q, want %q for %s", got.Outcome, schemainfer.OutcomeSchemaAmbiguous, schema)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none for an ambiguous root", pointers)
			}
		}
	})
}

// TestInferArgumentsIsInvariantUnderMemberPermutation proves the general
// property the two order-dependent shapes above are instances of: for one
// declared schema, every order in which its members could have been declared
// yields the identical outcome and the identical selector set.
//
// It is a property test over the reader, not a set of examples. Each case is
// spelled as a list of distinct member declarations, every permutation of that
// list is built, and the first result becomes the expectation for all the rest.
func TestInferArgumentsIsInvariantUnderMemberPermutation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		members []string
		want    schemainfer.Outcome
		point   []string
	}{
		{
			name: "root_object_with_every_read_keyword",
			members: []string{
				`"type":"object"`,
				`"description":"arguments for a read tool"`,
				`"required":["path"]`,
				`"properties":{"path":{"type":"string"},"dir":{"type":"array","items":{"type":"string"}},"offset":{"type":"integer"}}`,
			},
			want:  schemainfer.OutcomeInferred,
			point: []string{"/dir", "/path"},
		},
		{
			name: "root_reference_leading",
			members: []string{
				`"$ref":"#/$defs/ReadArgs"`,
				`"type":"object"`,
				`"properties":{"path":{"type":"string"}}`,
			},
			want: schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name: "root_all_of_between_type_and_properties",
			members: []string{
				`"type":"object"`,
				`"allOf":[{"type":"object"}]`,
				`"properties":{"path":{"type":"string"}}`,
			},
			want: schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name: "nested_reference_before_type",
			members: []string{
				`"type":"object"`,
				`"properties":{"options":{"$ref":"#/$defs/Options","type":"object","properties":{"path":{"type":"string"}}}}`,
			},
			want: schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name: "array_of_strings_with_uneven_keyword_declared_last",
			members: []string{
				`"type":"object"`,
				`"properties":{"paths":{"type":"array","items":{"type":"string"},"contains":{"type":"string"}}}`,
			},
			// `contains` is an uneven-array signal, not an ambiguous one: the array is
			// simply not a provable array of strings wherever the member sits.
			want: schemainfer.OutcomeNoPathKeys,
		},
		{
			name: "description_suppresses_wherever_it_is_declared",
			members: []string{
				`"type":"object"`,
				`"properties":{"path":{"description":"file content to write","type":"string"}}`,
			},
			want: schemainfer.OutcomeNoPathKeys,
		},
		{
			name: "repeated_member_wherever_it_is_declared",
			members: []string{
				`"type":"object"`,
				`"type":"array"`,
				`"properties":{"path":{"type":"string"}}`,
			},
			want: schemainfer.OutcomeSchemaDuplicate,
		},
		{
			name: "root_non_object_kind",
			members: []string{
				`"type":"array"`,
				`"items":{"type":"string"}`,
			},
			want: schemainfer.OutcomeSchemaNotObject,
		},
		{
			name: "properties_not_an_object",
			members: []string{
				`"type":"object"`,
				`"properties":[]`,
			},
			want: schemainfer.OutcomeSchemaAmbiguous,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A repeated member name makes several orders spell the same bytes.
			// Collapsing them keeps a failure message readable without weakening the
			// comparison: every distinct spelling of the schema is still exercised.
			spellings := make(map[string]struct{}, 64)
			orders := 0
			for order := range memberOrders(len(tc.members)) {
				orders++
				spellings[objectFromMembers(tc.members, order)] = struct{}{}
			}
			if orders < 2 {
				t.Fatalf("case spells %d members, which has no second order to compare", len(tc.members))
			}
			distinct := slices.Sorted(maps.Keys(spellings))
			if len(distinct) < 2 {
				t.Fatalf("case spells only %d distinct order(s), so it proves nothing", len(distinct))
			}
			t.Logf("%d orders, %d distinct spellings", orders, len(distinct))

			for i, spelling := range distinct {
				got := infer(t, spelling)
				if got.Outcome != tc.want {
					t.Fatalf("outcome for %s = %q, want %q", spelling, got.Outcome, tc.want)
				}
				if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, tc.point) {
					t.Fatalf("pointers for %s = %q, want %q", spelling, pointers, tc.point)
				}
				if i == 0 {
					t.Logf("reference spelling: %s", spelling)
				}
			}
		})
	}
}

// memberOrders yields every ordering of n members as an index permutation.
//
// It is an iterator so a case with six members enumerates its 720 orders without
// holding them all, and it is exhaustive by construction rather than sampled: if
// a declared member's position could change the result, one of the orders it
// yields would show it.
func memberOrders(n int) func(func([]int) bool) {
	return func(yield func([]int) bool) {
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		// Heap's algorithm visits all n! index permutations.
		var walk func(k int) bool
		walk = func(k int) bool {
			if k == 0 {
				return yield(slices.Clone(order))
			}
			for i := range k {
				order[k-1], order[i] = order[i], order[k-1]
				cont := walk(k - 1)
				order[k-1], order[i] = order[i], order[k-1]
				if !cont {
					return false
				}
			}
			return true
		}
		walk(n)
	}
}

// objectFromMembers spells one object whose members appear in the given index
// order.
func objectFromMembers(members []string, order []int) string {
	parts := make([]string, 0, len(order))
	for _, index := range order {
		parts = append(parts, members[index])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// TestInferArgumentsSkipsAbsentAndUnreadableParameters pins the presence
// semantics of the canonical field: an unset schema, a JSON null schema, a
// non-object schema, and invalid JSON are four different bounded skips, and none
// of them is guessed at.
//
// The absent case is decided from the raw message length rather than through
// internal/core/jsonpresence, because this package must not depend on
// internal/core. The distinction the check preserves is the canonical one: a field
// that was never set is absent, while a field that was set to JSON null is a
// declared non-object value.
func TestInferArgumentsSkipsAbsentAndUnreadableParameters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		parameters json.RawMessage
		want       schemainfer.Outcome
	}{
		{name: "unset", parameters: nil, want: schemainfer.OutcomeSchemaAbsent},
		{name: "empty_bytes", parameters: json.RawMessage(``), want: schemainfer.OutcomeSchemaAbsent},
		{name: "whitespace_only", parameters: json.RawMessage("  \n\t "), want: schemainfer.OutcomeSchemaAbsent},
		{name: "json_null", parameters: json.RawMessage(`null`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "json_true", parameters: json.RawMessage(`true`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "json_false", parameters: json.RawMessage(`false`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "json_number", parameters: json.RawMessage(`7`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "json_string", parameters: json.RawMessage(`"object"`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "json_array", parameters: json.RawMessage(`[{"type":"object"}]`), want: schemainfer.OutcomeSchemaNotObject},
		{name: "truncated_object", parameters: json.RawMessage(`{"type":"object","properties":{"path":`), want: schemainfer.OutcomeSchemaMalformed},
		{name: "trailing_garbage", parameters: json.RawMessage(`{"type":"object"} trailing`), want: schemainfer.OutcomeSchemaMalformed},
		{name: "unquoted_key", parameters: json.RawMessage(`{type:"object"}`), want: schemainfer.OutcomeSchemaMalformed},
		{name: "not_json_at_all", parameters: json.RawMessage(`x`), want: schemainfer.OutcomeSchemaMalformed},
		{name: "byte_order_mark_prefix", parameters: json.RawMessage("\xef\xbb\xbf{}"), want: schemainfer.OutcomeSchemaMalformed},
		{name: "single_quotes", parameters: json.RawMessage(`{'type':'object'}`), want: schemainfer.OutcomeSchemaMalformed},
		{name: "truncated_nested", parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"`), want: schemainfer.OutcomeSchemaMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inferrer, reject := schemainfer.New(defaultVocabulary())
			if reject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("New rejected with %v", reject)
			}
			got := inferrer.InferArguments(aipapi.ToolDef{Name: "tool", Parameters: tc.parameters})
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none", pointers)
			}
		})
	}
}

// TestInferArgumentsUsesDeclaredNameForPointerAndNormalizedNameForMatching
// pins the two-name rule: the pointer addresses the declared member name exactly
// as the schema spells it, while vocabulary and denylist membership is decided on
// the normalized spelling of that name.
func TestInferArgumentsUsesDeclaredNameForPointerAndNormalizedNameForMatching(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		property   string
		want       string
		wantResult bool
	}{
		{name: "exact_key", property: "path", want: "/path", wantResult: true},
		{name: "capitalized", property: "Path", want: "/Path", wantResult: true},
		{name: "upper_case", property: "PATH", want: "/PATH", wantResult: true},
		{name: "mixed_case", property: "File_Path", want: "/File_Path", wantResult: true},
		{name: "camel_case", property: "filePath", want: "/filePath", wantResult: true},
		{name: "hyphen_spelling", property: "file-path", want: "/file-path", wantResult: true},
		{name: "dotted_spelling", property: "target.path", want: "/target.path", wantResult: true},
		{name: "spaced_spelling", property: "file path", want: "/file path", wantResult: true},
		{name: "trailing_space", property: "path ", want: "/path ", wantResult: true},
		{name: "leading_space", property: " path", want: "/ path", wantResult: true},
		{name: "near_miss_prefix", property: "pathspec"},
		{name: "near_miss_suffix", property: "pathname"},
		{name: "near_miss_plural_of_root", property: "roots"},
		{name: "hyphenated_non_key", property: "file-content"},
		{name: "cyrillic_lookalike", property: "pаth"},
		{name: "fullwidth_lookalike", property: "ＰＡＴＨ"},
		{name: "non_breaking_space_suffix", property: "path\u00a0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			schema, err := json.Marshal(map[string]any{
				"type":       "object",
				"properties": map[string]any{tc.property: map[string]any{"type": "string"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := infer(t, string(schema))
			if !tc.wantResult {
				if got.Outcome != schemainfer.OutcomeNoPathKeys || len(got.Pointers) != 0 {
					t.Fatalf("inference of %q = %q/%q, want no inference", tc.property, canonicalPointers(got.Pointers), got.Outcome)
				}
				return
			}
			if got.Outcome != schemainfer.OutcomeInferred {
				t.Fatalf("inference of %q outcome = %q, want %q", tc.property, got.Outcome, schemainfer.OutcomeInferred)
			}
			if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{tc.want}) {
				t.Fatalf("pointers = %q, want %q", pointers, []string{tc.want})
			}
		})
	}
}

// TestInferArgumentsEscapesDeclaredNamesThroughTheAcceptedDialect proves a
// declared name that needs RFC 6901 escaping is published in the one spelling the
// explicit-selector dialect accepts, and that the published selector decodes back
// to the exact declared member name. The vocabulary is widened to the declared
// name so the only remaining question is the pointer spelling.
func TestInferArgumentsEscapesDeclaredNamesThroughTheAcceptedDialect(t *testing.T) {
	t.Parallel()

	for _, declared := range []string{
		"path",
		"path ",
		" path",
		"path\t",
		"path with space",
		"path.",
		"path-",
		"-path",
		"-",
		"a/b",
		"a~/b",
		"~path",
		"path~1",
		"FILE PATH",
		"путь",
		"路径",
	} {
		t.Run(declared, func(t *testing.T) {
			t.Parallel()

			schema, err := json.Marshal(map[string]any{
				"type":       "object",
				"properties": map[string]any{declared: map[string]any{"type": "string"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := inferAgainst(t, string(schema), []string{declared})
			if declared == "-" {
				// `-` is RFC 6901's position after the last array element, so no plain
				// pointer can address a member with that name.
				if got.Outcome != schemainfer.OutcomePointerRejected {
					t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomePointerRejected)
				}
				if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
					t.Fatalf("pointers = %q, want none", pointers)
				}
				return
			}
			if got.Outcome != schemainfer.OutcomeInferred {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
			}
			if len(got.Pointers) != 1 {
				t.Fatalf("pointers = %q, want exactly one", canonicalPointers(got.Pointers))
			}
			selector := got.Pointers[0]
			if tokens := selector.Tokens(); !slices.Equal(tokens, []string{declared}) {
				t.Fatalf("selector %q decodes to %q, want the declared name %q", selector, tokens, declared)
			}
			again, reject := pathvirtualization.ParseSelector(selector.String())
			if reject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("published pointer %q is not in the accepted dialect: %v", selector, reject)
			}
			if !slices.Equal(again.Tokens(), []string{declared}) {
				t.Fatalf("re-parsed %q decodes to %q, want %q", selector, again.Tokens(), declared)
			}
		})
	}
}

// TestInferArgumentsRefusesNamesThePointerDialectCannotSpell pins the hand-off to
// the accepted pointer dialect: a declared name that no plain RFC 6901 pointer can
// address is refused with a bounded reason instead of being rewritten into a
// different location.
func TestInferArgumentsRefusesNamesThePointerDialectCannotSpell(t *testing.T) {
	t.Parallel()

	// The empty member name cannot appear in the vocabulary at all, so a nested
	// candidate is the only reachable form of a name the dialect refuses for being
	// empty. Both forms must still be refused rather than guessed at.
	cases := []struct {
		name     string
		schema   string
		pathKeys []string
	}{
		{
			name:     "top_level_array_end_token",
			schema:   `{"type":"object","properties":{"-":{"type":"string"}}}`,
			pathKeys: []string{"-"},
		},
		{
			name:     "nested_array_end_token",
			schema:   `{"type":"object","properties":{"where":{"type":"object","properties":{"-":{"type":"string"}}}}}`,
			pathKeys: []string{"-"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := inferAgainst(t, tc.schema, tc.pathKeys)
			if got.Outcome != schemainfer.OutcomePointerRejected {
				t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomePointerRejected)
			}
			if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
				t.Fatalf("pointers = %q, want none", pointers)
			}
		})
	}
}

// TestInferArgumentsRespectsSelectorBounds pins the "cap depth, path-key count,
// profiles, and pointers" rule of design.md 227: every bound is refused, never
// truncated.
func TestInferArgumentsRespectsSelectorBounds(t *testing.T) {
	t.Parallel()

	t.Run("depth_at_limit_is_inferred", func(t *testing.T) {
		t.Parallel()

		// MaxPointerDepth tokens means MaxPointerDepth-1 declared containers
		// followed by the path key itself.
		schema := nestedSchema(pathvirtualization.MaxPointerDepth - 1)
		got := infer(t, schema)
		if got.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
		}
		want := "/" + strings.Repeat("c/", pathvirtualization.MaxPointerDepth-1) + "path"
		if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{want}) {
			t.Fatalf("pointers = %q, want %q", pointers, []string{want})
		}
	})

	t.Run("depth_over_limit_is_refused", func(t *testing.T) {
		t.Parallel()

		got := infer(t, nestedSchema(pathvirtualization.MaxPointerDepth))
		if got.Outcome != schemainfer.OutcomeDepthExceeded {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeDepthExceeded)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none", pointers)
		}
	})

	t.Run("unrelated_deep_branch_is_not_a_depth_failure", func(t *testing.T) {
		t.Parallel()

		// A deep branch that declares no path key past the bound is simply not
		// traversed; it must not cost the provable top-level candidate.
		schema := `{"type":"object","properties":{"path":{"type":"string"},"deep":` +
			nestedSchema(pathvirtualization.MaxPointerDepth+2) + `}}`
		got := infer(t, schema)
		if got.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
		}
		if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, []string{"/path"}) {
			t.Fatalf("pointers = %q, want %q", pointers, []string{"/path"})
		}
	})

	t.Run("pointer_count_at_limit_is_inferred", func(t *testing.T) {
		t.Parallel()

		got := infer(t, siblingPathSchema(pathvirtualization.MaxPointersPerProfile))
		if got.Outcome != schemainfer.OutcomeInferred {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeInferred)
		}
		if len(got.Pointers) != pathvirtualization.MaxPointersPerProfile {
			t.Fatalf("pointers = %d, want %d", len(got.Pointers), pathvirtualization.MaxPointersPerProfile)
		}
		for _, pointer := range canonicalPointers(got.Pointers) {
			if !strings.HasPrefix(pointer, "/branch") {
				t.Fatalf("pointer %q is not one of the declared branches", pointer)
			}
		}
	})

	t.Run("pointer_count_over_limit_is_refused", func(t *testing.T) {
		t.Parallel()

		got := infer(t, siblingPathSchema(pathvirtualization.MaxPointersPerProfile+1))
		if got.Outcome != schemainfer.OutcomePointerBudget {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomePointerBudget)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none; an over-limit tool is refused, not truncated", pointers)
		}
	})

	t.Run("node_count_at_limit_is_read", func(t *testing.T) {
		t.Parallel()

		// The root object is one inspected node, so the limit allows
		// MaxSchemaNodes-1 declared children beside it.
		got := infer(t, inertPropertySchema(schemainfer.MaxSchemaNodes-1))
		if got.Outcome != schemainfer.OutcomeNoPathKeys {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNoPathKeys)
		}
	})

	t.Run("node_count_over_limit_is_refused", func(t *testing.T) {
		t.Parallel()

		got := infer(t, inertPropertySchema(schemainfer.MaxSchemaNodes))
		if got.Outcome != schemainfer.OutcomeNodeBudget {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeNodeBudget)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none", pointers)
		}
	})
}

// nestedSchema builds an object schema that nests `depth` declared objects before
// a `path` string property.
func nestedSchema(depth int) string {
	return declaredChain(depth, "path")
}

// siblingPathSchema builds an object schema with `count` declared child objects
// that each declare one `path` string, so inference would produce count pointers.
func siblingPathSchema(count int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i := range count {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"branch%03d":{"type":"object","properties":{"path":{"type":"string"}}}`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// inertPropertySchema builds an object schema with `count` declared string
// properties whose names are outside the vocabulary, so they are inspected but
// never inferred.
func inertPropertySchema(count int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i := range count {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"inert%04d":{"type":"string"}`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// TestNewValidatesTheVocabulary proves the path-key vocabulary is the single
// bounded authority and that a rejected vocabulary fails closed instead of
// degrading into an unbounded comparison.
func TestNewValidatesTheVocabulary(t *testing.T) {
	t.Parallel()

	t.Run("default_vocabulary_is_accepted", func(t *testing.T) {
		t.Parallel()

		keys := defaultVocabulary()
		if len(keys) == 0 {
			t.Fatal("the default vocabulary must not be empty")
		}
		inferrer, reject := schemainfer.New(keys)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("New(default) rejected with %v", reject)
		}
		if inferrer == nil {
			t.Fatal("New(default) returned no inferrer")
		}
	})

	t.Run("empty_vocabulary_is_reported_not_guessed", func(t *testing.T) {
		t.Parallel()

		inferrer, reject := schemainfer.New(nil)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("New(nil) rejected with %v, want an accepted empty vocabulary", reject)
		}
		got := inferrer.InferArguments(aipapi.ToolDef{
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		})
		if got.Outcome != schemainfer.OutcomeVocabularyEmpty {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeVocabularyEmpty)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none with no vocabulary", pointers)
		}
	})

	t.Run("over_limit_vocabulary_is_rejected", func(t *testing.T) {
		t.Parallel()

		keys := make([]string, 0, pathvirtualization.MaxPathKeys+1)
		for i := 0; i <= pathvirtualization.MaxPathKeys; i++ {
			keys = append(keys, fmt.Sprintf("key%03d", i))
		}
		inferrer, reject := schemainfer.New(keys)
		if reject != pathvirtualization.SelectorRejectPathKeyCount {
			t.Fatalf("New(%d keys) rejected with %v, want %v", len(keys), reject, pathvirtualization.SelectorRejectPathKeyCount)
		}
		if inferrer != nil {
			t.Fatal("a rejected vocabulary must not publish an inferrer")
		}
		// A rejected vocabulary must stay fail closed even if a caller ignores the
		// reject reason and calls the nil inferrer anyway.
		got := inferrer.InferArguments(aipapi.ToolDef{
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		})
		if got.Outcome != schemainfer.OutcomeVocabularyRejected {
			t.Fatalf("outcome = %q, want %q", got.Outcome, schemainfer.OutcomeVocabularyRejected)
		}
		if pointers := canonicalPointers(got.Pointers); len(pointers) != 0 {
			t.Fatalf("pointers = %q, want none", pointers)
		}
	})

	t.Run("empty_key_is_rejected", func(t *testing.T) {
		t.Parallel()

		if _, reject := schemainfer.New([]string{"path", ""}); reject != pathvirtualization.SelectorRejectEmptyPathKey {
			t.Fatalf("New with an empty key rejected with %v, want %v", reject, pathvirtualization.SelectorRejectEmptyPathKey)
		}
	})

	t.Run("duplicate_key_is_rejected", func(t *testing.T) {
		t.Parallel()

		if _, reject := schemainfer.New([]string{"path", "path"}); reject != pathvirtualization.SelectorRejectDuplicatePathKey {
			t.Fatalf("New with a duplicate key rejected with %v, want %v", reject, pathvirtualization.SelectorRejectDuplicatePathKey)
		}
	})

	t.Run("keys_colliding_after_normalization_are_rejected", func(t *testing.T) {
		t.Parallel()

		// `path` and `PATH` are distinct byte-exact keys but one normalized name, so
		// accepting both would make one declared name match two vocabulary entries.
		if _, reject := schemainfer.New([]string{"path", "PATH"}); reject != pathvirtualization.SelectorRejectDuplicatePathKey {
			t.Fatalf("New with normalization-colliding keys rejected with %v, want %v", reject, pathvirtualization.SelectorRejectDuplicatePathKey)
		}
	})

	t.Run("vocabulary_is_caller_owned", func(t *testing.T) {
		t.Parallel()

		keys := []string{"path"}
		inferrer, reject := schemainfer.New(keys)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("New rejected with %v", reject)
		}
		keys[0] = "mutated"
		got := inferrer.InferArguments(aipapi.ToolDef{
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"mutated":{"type":"string"}}}`),
		})
		if got.Outcome != schemainfer.OutcomeNoPathKeys {
			t.Fatalf("outcome = %q, want %q; a mutated caller slice must not widen the compiled vocabulary", got.Outcome, schemainfer.OutcomeNoPathKeys)
		}
	})
}

// TestDefaultPathKeysAndPayloadConceptKeysAreBoundedAndDisjoint pins the two
// fixed key lists of design.md 218-222: the vocabulary is the only thing an
// operator can extend, the payload denylist is an implementation contract, and
// the two can never overlap.
func TestDefaultPathKeysAndPayloadConceptKeysAreBoundedAndDisjoint(t *testing.T) {
	t.Parallel()

	pathKeys := schemainfer.DefaultPathKeys()
	denylist := schemainfer.PayloadConceptKeys()

	if got, want := pathKeys, []string{
		"path", "file_path", "filepath", "directory", "dir", "cwd", "workdir", "root", "target_path", "paths",
	}; !slices.Equal(got, want) {
		t.Fatalf("DefaultPathKeys() = %q, want the design list %q", got, want)
	}
	if got, want := denylist, []string{
		"content", "contents", "patch", "diff", "script", "command", "cmd",
		"query", "expression", "replacement", "body", "data", "text",
		"source", "source_code",
	}; !slices.Equal(got, want) {
		t.Fatalf("PayloadConceptKeys() = %q, want %q", got, want)
	}
	if _, reject := schemainfer.New(pathKeys); reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("the default vocabulary must satisfy the shared bound, got %v", reject)
	}
	// The payload denylist is compared against normalized declared names, so it
	// must itself normalizable and must never name a vocabulary key.
	normalizedDenylist := normalizeAll(t, denylist)
	normalizedVocabulary := normalizeAll(t, pathKeys)
	if !slices.IsSorted(normalizedDenylist) {
		t.Fatalf("normalized denylist %q is not sorted; a stable list keeps the policy auditable", normalizedDenylist)
	}
	if !slices.IsSorted(normalizedVocabulary) {
		t.Fatalf("normalized vocabulary %q is not sorted", normalizedVocabulary)
	}
	for _, key := range normalizedVocabulary {
		if slices.Contains(normalizedDenylist, key) {
			t.Fatalf("key %q is both a vocabulary key and a payload concept; the denylist must win unambiguously", key)
		}
	}
	// Both lists must be caller-owned copies so a caller cannot mutate the policy.
	pathKeys[0] = "mutated"
	if schemainfer.DefaultPathKeys()[0] != "path" {
		t.Fatal("DefaultPathKeys() exposes the fixed list to caller mutation")
	}
	denylist[0] = "mutated"
	if schemainfer.PayloadConceptKeys()[0] != "content" {
		t.Fatal("PayloadConceptKeys() exposes the fixed list to caller mutation")
	}
}

func normalizeAll(t *testing.T, keys []string) []string {
	t.Helper()
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		value := schemainfer.NormalizeKey(key)
		if value == "" {
			t.Fatalf("key %q normalizes to the empty name", key)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("keys collide on the normalized name %q", value)
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	slices.Sort(normalized)
	return normalized
}

// TestNormalizeKeyIsByteExactForNonASCII pins the normalization rule: only ASCII
// case and ASCII word separators are normalized, and no Unicode folding, non-ASCII
// trimming, or confusable mapping applies.
func TestNormalizeKeyIsByteExactForNonASCII(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{in: "path", want: "path"},
		{in: "PATH", want: "path"},
		{in: "File-Path", want: "file_path"},
		{in: "file path", want: "file_path"},
		{in: "file.path", want: "file_path"},
		{in: "  path  ", want: "path"},
		{in: "filePath", want: "filepath"},
		{in: "pаth", want: "pаth"},
		{in: "ＰＡＴＨ", want: "ＰＡＴＨ"},
		{in: "path\u00a0", want: "path\u00a0"},
		{in: "путь", want: "путь"},
		{in: "", want: ""},
		{in: "-", want: "_"},
		{in: "a--b", want: "a__b"},
		{in: "a\nb", want: "a_b"},
		{in: "a/b", want: "a_b"},
		{in: "~path", want: "_path"},
		{in: "a\tb", want: "a_b"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := schemainfer.NormalizeKey(tc.in); got != tc.want {
				t.Fatalf("NormalizeKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestOutcomeLabelsAreBoundedAndUnique pins the observability contract: every
// outcome has one fixed, low-cardinality, content-free label, and no two outcomes
// share one.
func TestOutcomeLabelsAreBoundedAndUnique(t *testing.T) {
	t.Parallel()

	labels := map[string]schemainfer.Outcome{}
	for _, outcome := range []schemainfer.Outcome{
		schemainfer.OutcomeNone,
		schemainfer.OutcomeInferred,
		schemainfer.OutcomeNoPathKeys,
		schemainfer.OutcomeVocabularyEmpty,
		schemainfer.OutcomeVocabularyRejected,
		schemainfer.OutcomeSchemaAbsent,
		schemainfer.OutcomeSchemaNotObject,
		schemainfer.OutcomeSchemaMalformed,
		schemainfer.OutcomeSchemaAmbiguous,
		schemainfer.OutcomeSchemaDuplicate,
		schemainfer.OutcomeDepthExceeded,
		schemainfer.OutcomePointerBudget,
		schemainfer.OutcomeNodeBudget,
		schemainfer.OutcomePointerRejected,
	} {
		label := outcome.String()
		if label == "" || label == "unknown" {
			t.Fatalf("outcome %d has no fixed label", outcome)
		}
		if label != strings.ToLower(label) || strings.ContainsAny(label, " /\\.:") {
			t.Fatalf("label %q is not a fixed bounded token", label)
		}
		if previous, duplicate := labels[label]; duplicate {
			t.Fatalf("label %q is shared by outcomes %d and %d", label, previous, outcome)
		}
		labels[label] = outcome
	}
	if got := schemainfer.Outcome(200).String(); got != "unknown" {
		t.Fatalf("out-of-range outcome label = %q, want %q", got, "unknown")
	}
}

// TestInferenceIsDeterministicAndLeavesTheToolDefinitionUntouched proves the
// step is a pure function of the declared schema bytes: the same tool always
// yields the same pointers, and the canonical tool definition is never modified.
func TestInferenceIsDeterministicAndLeavesTheToolDefinitionUntouched(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"dir":{"type":"string"},"opts":{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}}}}}`)
	original := append(json.RawMessage(nil), schema...)
	tool := aipapi.ToolDef{Name: "list_files", Description: "list files", Parameters: schema}

	first := mustInfer(t, tool)
	for range 8 {
		again := mustInfer(t, tool)
		if !slices.Equal(canonicalPointers(again.Pointers), canonicalPointers(first.Pointers)) {
			t.Fatalf("inference is not deterministic: %q then %q", canonicalPointers(first.Pointers), canonicalPointers(again.Pointers))
		}
		if again.Outcome != first.Outcome {
			t.Fatalf("outcome is not deterministic: %q then %q", first.Outcome, again.Outcome)
		}
	}
	if !slices.Equal([]byte(tool.Parameters), original) {
		t.Fatalf("inference rewrote the tool schema: %s, want %s", tool.Parameters, original)
	}
	if tool.Name != "list_files" || tool.Description != "list files" {
		t.Fatal("inference rewrote unrelated tool definition fields")
	}
	want := []string{"/dir", "/opts/paths", "/path"}
	if pointers := canonicalPointers(first.Pointers); !slices.Equal(pointers, want) {
		t.Fatalf("pointers = %q, want %q", pointers, want)
	}
	// The published selectors must be usable by the existing resolver, which is
	// the whole point of publishing pointers rather than names.
	selection := first.Pointers.Resolve(map[string]any{"opts": map[string]any{"paths": []any{"/srv/a", "/srv/b"}}})
	if len(selection.Leaves) != 2 || selection.Leaves[0].Value != "/srv/a" || selection.Leaves[1].Index != 1 {
		t.Fatalf("published selectors resolved %+v, want the two declared array elements", selection.Leaves)
	}
	if len(selection.Skipped) != 2 {
		t.Fatalf("published selectors reported %d skips, want the two absent locations", len(selection.Skipped))
	}
	for _, skipped := range selection.Skipped {
		if skipped.Reason != pathvirtualization.SelectorSkipUnresolved {
			t.Fatalf("published selector %q skipped with %v, want %v", skipped.Selector, skipped.Reason, pathvirtualization.SelectorSkipUnresolved)
		}
	}
}

// TestInferenceDescendsOnlyThroughDeclaredObjectProperties pins the traversal
// boundary. Requirement 3.3 constrains which declared properties become
// candidates, not which declared objects may be read to reach one, so a path key
// declared below any declared object is a candidate and keeps its full declared
// ancestor chain. The only declared node that closes a chain is a payload concept,
// and every declared object is still read for failure signals whether or not it is
// on a published chain.
func TestInferenceDescendsOnlyThroughDeclaredObjectProperties(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
		want   schemainfer.Outcome
		point  []string
	}{
		{
			name:   "unreadable_description_does_not_block_a_declared_string",
			schema: `{"type":"object","properties":{"path":{"type":"string","description":42}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		{
			name:   "unreadable_description_on_a_container_does_not_block_traversal",
			schema: `{"type":"object","properties":{"where":{"type":"object","description":[],"properties":{"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/where/path"},
		},
		{
			name:   "path_key_under_non_candidate_object",
			schema: `{"type":"object","properties":{"where":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/where/path"},
		},
		{
			name:   "path_key_under_two_non_candidate_holders",
			schema: `{"type":"object","properties":{"options":{"type":"object","properties":{"edit":{"type":"object","properties":{"dir":{"type":"string"}}}}}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/options/edit/dir"},
		},
		{
			name:   "path_key_beside_non_candidate_object",
			schema: `{"type":"object","properties":{"where":{"type":"object","properties":{"name":{"type":"string"}}},"dir":{"type":"string"}}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/dir"},
		},
		{
			name:   "path_key_under_a_non_candidate_array_is_never_reached",
			schema: `{"type":"object","properties":{"where":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"}}}}}}`,
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "non_candidate_object_declaring_a_composition_poisons_the_tool",
			schema: `{"type":"object","properties":{"dir":{"type":"string"},"where":{"type":"object","anyOf":[{"type":"object"}]}}}`,
			want:   schemainfer.OutcomeSchemaAmbiguous,
		},
		{
			name:   "non_candidate_object_declaring_a_duplicate_poisons_the_tool",
			schema: `{"type":"object","properties":{"dir":{"type":"string"},"where":{"type":"object","properties":{"a":{"type":"string"},"a":{"type":"object"}}}}}`,
			want:   schemainfer.OutcomeSchemaDuplicate,
		},
		{
			name:   "non_candidate_object_without_a_declared_type_closes_the_chain",
			schema: `{"type":"object","properties":{"where":{"properties":{"path":{"type":"string"}}}}}`,
			want:   schemainfer.OutcomeNoPathKeys,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, tc.point) {
				t.Fatalf("pointers = %q, want %q", pointers, tc.point)
			}
		})
	}
}

// deepReadParityFactor is how many times more BYTES a deep declaration may
// allocate than a comparable one-level declaration.
//
// The comparison is the scale-free half of the bound: both declarations are read
// whole, so a reader whose cost is proportional to declared size charges them
// alike, while a reader that rescans each subtree per nesting level charges the
// deep one enormously more. The factor is loose because the two shapes do not cost
// exactly the same — the deep one walks the window, the wide one stops at the node
// budget — but it is four to five orders of magnitude below what an unbounded read
// measured.
//
// It also happens to be the SAFE direction under measurement noise. Allocated
// bytes are read from a process-wide counter, so a concurrent test in the same
// binary adds the same offset to both sides; adding to both drives their ratio
// toward one, so interference can only make this assertion pass, never fail. That
// is the opposite of the wall-clock assertion this replaced, which failed under
// load because an allocation-heavy call pays proportionally more GC and scheduler
// cost than a light one.
const deepReadParityFactor = 20

// deepReadAmplification is how many times its own declared size one bounded read
// may allocate.
//
// It is an absolute net for the shapes below, expressed per declared byte so it
// holds for a chain and a sibling list alike rather than only for one fixture
// size. The reader decodes every member value inside the window, so the honest
// amplification is already in the hundreds; the factor leaves several times that
// headroom while staying far below what a reader that rescans a subtree per
// nesting level would allocate.
const deepReadAmplification = 1024

// TestInferArgumentsBoundsTheCostOfADeepDeclaredChain pins requirement 7.9 for
// the declared-schema reader: audit and rewrite processing is bounded in CPU and
// memory by canonical payload limits plus feature-specific selector and profile
// limits. The canonical limit on this input is aipapi.MaxToolParametersBytes
// (256 KiB), and 256 KiB of declared schema is in-contract — nothing in the
// repository bounds how deeply a provider nests its tool schema.
//
// The reader is where that bound has to be enforced. The walk refuses every
// location below pathvirtualization.MaxPointerDepth, so a declared chain deeper
// than that can never publish anything, yet a reader that re-enters the decoder
// on each child's full raw bytes re-validates the entire subtree once per level.
// That made the read quadratic in nesting depth rather than in declared size: a
// 148 KiB single-child chain measured 9.6 s here while the same bytes spent on
// siblings at one level measured 29 ms. The projection now materializes only the
// levels the walk can still decide on, so the read is a constant multiple of the
// declared size and every row below answers in milliseconds.
//
// The correctness rows are part of the same bound, because the cheapest way to
// stop reading is to stop reading too early. A chain that proves a path key
// inside the reachable window still infers it, a chain whose only path key is one
// level past the depth bound is still refused as depth_exceeded, and a chain
// whose only path key is far past it is still answered with the walk's own reason
// rather than with a read failure.
func TestInferArgumentsBoundsTheCostOfADeepDeclaredChain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
		want   schemainfer.Outcome
		point  []string
	}{
		{
			name:   "chain_of_2000_declared_levels_is_answered",
			schema: declaredChain(2000, "leaf"),
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "chain_of_4000_declared_levels_is_answered",
			schema: declaredChain(4000, "leaf"),
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "chain_of_4000_declared_levels_keeps_a_proven_shallow_key",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredChain(4000, "leaf") + `}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		{
			name:   "chain_of_4000_declared_levels_with_a_key_far_past_the_bound",
			schema: declaredChain(4000, "path"),
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "chain_of_4000_declared_levels_with_a_key_at_the_depth_bound",
			schema: declaredChain(pathvirtualization.MaxPointerDepth, "path"),
			want:   schemainfer.OutcomeDepthExceeded,
		},
		{
			name:   "chain_of_4000_declared_levels_with_a_payload_key_at_the_depth_bound",
			schema: declaredChain(pathvirtualization.MaxPointerDepth, "content"),
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "chain_of_5000_declared_item_levels_is_answered",
			schema: declaredItemChain(5000),
			want:   schemainfer.OutcomeNoPathKeys,
		},
		{
			name:   "chain_of_5000_declared_item_levels_keeps_a_proven_shallow_key",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredItemChain(5000) + `}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
		// The read is bounded, but it is still one validation pass over the whole
		// declaration: every member value is decoded, so bytes below the window
		// are still checked for readability even though nothing in them is
		// materialized. Truncating the read must not start accepting a declaration
		// the step could not read whole.
		{
			name:   "chain_truncated_below_the_window_is_still_malformed",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredChain(400, "leaf")[:len(declaredChain(400, "leaf"))-2],
			want:   schemainfer.OutcomeSchemaMalformed,
		},
		{
			name:   "chain_with_a_second_document_below_the_window_is_still_malformed",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredChain(400, "leaf") + `}}{}`,
			want:   schemainfer.OutcomeSchemaMalformed,
		},
		{
			name:   "chain_with_a_non_object_child_below_the_window_is_still_readable",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredChain(400, "leaf") + `,"also":42}}`,
			want:   schemainfer.OutcomeInferred,
			point:  []string{"/path"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := infer(t, tc.schema)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if pointers := canonicalPointers(got.Pointers); !slices.Equal(pointers, tc.point) {
				t.Fatalf("pointers = %q, want %q", pointers, tc.point)
			}
		})
	}
}

// TestInferArgumentsBoundsTheAllocatedCostOfADeepDeclaredChain is the cost half
// of TestInferArgumentsBoundsTheCostOfADeepDeclaredChain, held separately so the
// measurement is sound.
//
// Allocated bytes come from a process-wide counter, so measuring them inside a
// t.Parallel subtest would fold every sibling subtest's own allocations into the
// window and report the suite's concurrency rather than this reader's cost. This
// test therefore declares no t.Parallel, and takes no t.Parallel in any subtest:
// the whole binary runs these assertions in one goroutine at a time.
//
// It measures allocated bytes rather than wall-clock because that is what the
// bound is actually about. A reader that rescans each subtree per nesting level
// is quadratic in ALLOCATION as well as in time, so the same signal catches the
// same regression, but unlike a stopwatch it does not move when the machine is
// busy: an allocation-heavy read pays proportionally more GC and scheduler cost
// than a light one, which is exactly how a wall-clock assertion here used to fail
// under a loaded machine while the bounded reader was in fact still bounded.
func TestInferArgumentsBoundsTheAllocatedCostOfADeepDeclaredChain(t *testing.T) {
	// Two declarations of comparable size: 4000 declared levels on one branch, and
	// one level holding comparably many siblings. Both are read whole, so their
	// cost must stay in the same order. Before the bound this pair allocated its
	// subtree once per nesting level, a factor of 330 against this measurement.
	shapes := []struct {
		name   string
		schema string
	}{
		{
			name:   "deep",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"deep":` + declaredChain(4000, "leaf") + `}}`,
		},
		{
			name:   "wide",
			schema: declaredWideSchema(160 * 1024),
		},
	}

	cost := make(map[string]uint64, len(shapes))
	for _, shape := range shapes {
		cost[shape.name] = inferAllocatedBytes(t, shape.schema)
	}

	if deep, wide := cost["deep"], cost["wide"]; deep > deepReadParityFactor*wide {
		t.Fatalf("reading %d declared bytes nested 4000 levels deep allocated %d bytes against %d for a comparable declaration at one level, a factor of %d above the %d allowed; the reader must not scale with nesting depth",
			len(shapes[0].schema), deep, wide, deep/max(wide, 1), deepReadParityFactor)
	}

	for _, shape := range shapes {
		if allowed := uint64(deepReadAmplification) * uint64(len(shape.schema)); cost[shape.name] > allowed {
			t.Fatalf("reading the %s declaration allocated %d bytes for %d declared bytes, above the %d allowed; a bounded read must stay a constant multiple of declared size",
				shape.name, cost[shape.name], len(shape.schema), allowed)
		}
	}
}

// inferAllocatedBytes returns the bytes one inference call for schema allocates.
//
// The first call is discarded and the process is collected before the measured
// call, so the figure is the read itself rather than one-time package
// initialization. The declarations measured against each other are large enough
// that their cost dominates any residual noise, so one call per shape is enough.
func inferAllocatedBytes(t *testing.T, schema string) uint64 {
	t.Helper()
	infer(t, schema)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	infer(t, schema)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// declaredChain builds `depth` declared container levels, each a `properties`
// object holding one child named c, followed by the declared leaf member.
func declaredChain(depth int, leaf string) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for range depth {
		b.WriteString(`"c":{"type":"object","properties":{`)
	}
	fmt.Fprintf(&b, `"%s":{"type":"string"}`, leaf)
	b.WriteString(strings.Repeat("}}", depth))
	b.WriteString(`}}`)
	return b.String()
}

// declaredItemChain builds `depth` declared arrays that each declare the next
// array directly as their item schema, ending in a declared string element.
//
// A chain through `properties` and a chain through `items` are both shapes a
// real provider can emit, and both recurse in the reader, so the bound has to
// hold for either. One JSON brace per level also keeps the chain inside the
// standard decoder's own nesting limit where a `properties` chain of the same
// depth would not.
func declaredItemChain(depth int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{"c":`)
	for range depth {
		b.WriteString(`{"type":"array","items":`)
	}
	b.WriteString(`{"type":"string"}`)
	b.WriteString(strings.Repeat(`}`, depth))
	b.WriteString(`}}`)
	return b.String()
}

// declaredWideSchema builds a declaration at a single level that spells at least
// the requested number of bytes, so it can be compared against a deep one.
func declaredWideSchema(size int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"inert%06d":{"type":"string","description":"a declared annotation long enough to fill the budget"}`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func mustInfer(t *testing.T, tool aipapi.ToolDef) schemainfer.Result {
	t.Helper()
	inferrer, reject := schemainfer.New(defaultVocabulary())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("New rejected with %v", reject)
	}
	return inferrer.InferArguments(tool)
}
