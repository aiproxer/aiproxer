package pathvirtualization_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// This file covers task 3.1 of design.md 197-229: validated JSON Pointer
// selectors for explicit argument and structured-result locations, the bounded
// profile/pointer/depth/path-key limits that keep selector work finite
// (requirement 7.9), and the string / array-of-strings leaf restriction of
// requirement 3.5. Schema-assisted inference and profile precedence are tasks 3.2
// and 3.3 and are deliberately absent here.

// canonicalPointer is one accepted JSON Pointer spelling together with the exact
// decoded tokens it must produce.
type canonicalPointer struct {
	name       string
	pointer    string
	wantTokens []string
}

func canonicalPointers() []canonicalPointer {
	return []canonicalPointer{
		{name: "single_member", pointer: `/path`, wantTokens: []string{"path"}},
		{name: "nested_members", pointer: `/options/target_path`, wantTokens: []string{"options", "target_path"}},
		{name: "array_index", pointer: `/edits/0/file_path`, wantTokens: []string{"edits", "0", "file_path"}},
		{name: "index_zero", pointer: `/files/0`, wantTokens: []string{"files", "0"}},
		{name: "escaped_slash", pointer: `/a~1b`, wantTokens: []string{"a/b"}},
		{name: "escaped_tilde", pointer: `/a~0b`, wantTokens: []string{"a~b"}},
		{name: "escaped_tilde_before_digit", pointer: `/a~01b`, wantTokens: []string{"a~1b"}},
		{name: "escapes_in_several_members", pointer: `/src~1dirs~0old/main~1go`, wantTokens: []string{"src/dirs~old", "main/go"}},
		{name: "member_named_slash", pointer: `/~1`, wantTokens: []string{"/"}},
		{name: "member_named_tilde_one", pointer: `/~01`, wantTokens: []string{"~1"}},
		{name: "member_named_zero", pointer: `/0`, wantTokens: []string{"0"}},
		{name: "member_named_leading_zero", pointer: `/01`, wantTokens: []string{"01"}},
		{name: "member_named_plus", pointer: `/+1`, wantTokens: []string{"+1"}},
		{name: "member_named_with_space", pointer: `/file path`, wantTokens: []string{"file path"}},
		{name: "member_named_with_dot", pointer: `/a.b`, wantTokens: []string{"a.b"}},
		{name: "member_named_dollar_ref", pointer: `/$ref`, wantTokens: []string{"$ref"}},
		{name: "member_named_with_quote", pointer: `/a"b`, wantTokens: []string{`a"b`}},
		{name: "member_named_with_backslash", pointer: `/a\b`, wantTokens: []string{`a\b`}},
		{name: "member_named_with_backslash_index", pointer: `/files/0\a`, wantTokens: []string{"files", `0\a`}},
		{name: "non_ascii_member", pointer: `/ключ/путь`, wantTokens: []string{"ключ", "путь"}},
		{name: "windows_style_member", pointer: `/C:\dev\file`, wantTokens: []string{`C:\dev\file`}},
	}
}

// TestParseSelectorAcceptsCanonicalPointers pins the accepted pointer dialect to
// the plain RFC 6901 string form with exactly one spelling per pointer: the
// canonical form of an accepted pointer is the pointer itself, and its tokens are
// the decoded reference names compared byte-exactly against decoded JSON member
// names.
func TestParseSelectorAcceptsCanonicalPointers(t *testing.T) {
	t.Parallel()

	for _, tc := range canonicalPointers() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			selector, reject := pathvirtualization.ParseSelector(tc.pointer)
			if reject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("ParseSelector(%q) rejected with %v, want acceptance", tc.pointer, reject)
			}
			if got := selector.Tokens(); !slices.Equal(got, tc.wantTokens) {
				t.Fatalf("tokens = %q, want %q", got, tc.wantTokens)
			}
			if got := selector.String(); got != tc.pointer {
				t.Fatalf("canonical = %q, want the accepted spelling %q", got, tc.pointer)
			}
			again, againReject := pathvirtualization.ParseSelector(selector.String())
			if againReject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("re-parsing canonical %q rejected with %v", selector.String(), againReject)
			}
			if !slices.Equal(again.Tokens(), tc.wantTokens) {
				t.Fatalf("canonical %q is not stable: tokens %q, want %q", selector.String(), again.Tokens(), tc.wantTokens)
			}
		})
	}
}

// TestParseSelectorRejectsNonCanonicalForms proves every rejected spelling has
// its own bounded reason instead of collapsing into one opaque failure. The
// rejections are the fail-closed half of requirement 7.5: an ambiguous selector
// is refused at compile time rather than resolved by a guess.
func TestParseSelectorRejectsNonCanonicalForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		pointer    string
		wantReject pathvirtualization.SelectorReject
	}{
		// The empty pointer is RFC 6901's whole-document reference. It is refused
		// because a path-bearing selector must name a field, not a whole payload.
		{name: "empty_pointer", pointer: "", wantReject: pathvirtualization.SelectorRejectEmptyPointer},
		// The URI-fragment form is refused: accepting it would add a second
		// spelling of every pointer plus a percent-decoding step that could
		// reinterpret operator bytes.
		{name: "fragment_only", pointer: "#", wantReject: pathvirtualization.SelectorRejectURIFragmentPointer},
		{name: "fragment_pointer", pointer: "#/a/b", wantReject: pathvirtualization.SelectorRejectURIFragmentPointer},
		{name: "fragment_without_slash", pointer: "#a", wantReject: pathvirtualization.SelectorRejectURIFragmentPointer},
		{name: "percent_encoded_member", pointer: "%2Fa", wantReject: pathvirtualization.SelectorRejectNotAbsolutePointer},
		{name: "relative_reference", pointer: "a/b", wantReject: pathvirtualization.SelectorRejectNotAbsolutePointer},
		{name: "bare_name", pointer: "path", wantReject: pathvirtualization.SelectorRejectNotAbsolutePointer},
		{name: "leading_space", pointer: " /a", wantReject: pathvirtualization.SelectorRejectNotAbsolutePointer},
		{name: "trailing_tilde", pointer: "/a~", wantReject: pathvirtualization.SelectorRejectInvalidEscape},
		{name: "tilde_at_token_end", pointer: "/~", wantReject: pathvirtualization.SelectorRejectInvalidEscape},
		{name: "invalid_escape_digit", pointer: "/a~2b", wantReject: pathvirtualization.SelectorRejectInvalidEscape},
		{name: "invalid_escape_tilde", pointer: "/a~~0b", wantReject: pathvirtualization.SelectorRejectInvalidEscape},
		{name: "invalid_escape_uppercase", pointer: "/a~1B~0b~", wantReject: pathvirtualization.SelectorRejectInvalidEscape},
		// An empty token names a member whose name is the empty string. Refusing
		// it also removes the trailing-slash spelling, which otherwise looks like a
		// different location than the same pointer without the slash.
		{name: "root_slash", pointer: "/", wantReject: pathvirtualization.SelectorRejectEmptyToken},
		{name: "double_slash", pointer: "//", wantReject: pathvirtualization.SelectorRejectEmptyToken},
		{name: "trailing_slash", pointer: "/a/", wantReject: pathvirtualization.SelectorRejectEmptyToken},
		{name: "interior_double_slash", pointer: "/a//b", wantReject: pathvirtualization.SelectorRejectEmptyToken},
		// `-` is RFC 6901's "element after the last one". It never names an
		// existing location, so it can never be a proven path-bearing leaf.
		{name: "array_end_token", pointer: "/-", wantReject: pathvirtualization.SelectorRejectArrayEndToken},
		{name: "nested_array_end_token", pointer: "/files/-", wantReject: pathvirtualization.SelectorRejectArrayEndToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			selector, reject := pathvirtualization.ParseSelector(tc.pointer)
			if reject != tc.wantReject {
				t.Fatalf("ParseSelector(%q) rejected with %v, want %v", tc.pointer, reject, tc.wantReject)
			}
			if got := selector.String(); got != "" {
				t.Fatalf("rejected pointer %q still produced %q", tc.pointer, got)
			}
			if got := selector.Tokens(); len(got) != 0 {
				t.Fatalf("rejected pointer %q still produced tokens %q", tc.pointer, got)
			}
		})
	}
}

// TestSelectorCanonicalFormHasExactlyOneSpelling pins the canonicalization
// contract: every reference name has exactly one accepted spelling, so two
// spellings of one pointer either collapse to identical tokens or are refused.
// Nothing is case-folded, trimmed, or Unicode-normalized, matching the lexical
// rules the rest of this package uses for client bytes.
func TestSelectorCanonicalFormHasExactlyOneSpelling(t *testing.T) {
	t.Parallel()

	accepted := canonicalPointers()
	seen := make(map[string]string, len(accepted))
	for _, tc := range accepted {
		selector, reject := pathvirtualization.ParseSelector(tc.pointer)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("ParseSelector(%q) rejected with %v", tc.pointer, reject)
		}
		key := strings.Join(selector.Tokens(), "\x00")
		if other, dup := seen[key]; dup {
			t.Fatalf("pointers %q and %q decode to the same reference", other, tc.pointer)
		}
		seen[key] = tc.pointer
	}

	// Distinct spellings that differ only by a byte stay distinct references.
	pairs := [][2]string{
		{`/a~1b`, `/a/b`},
		{`/a~0b`, `/a~1b`},
		{`/Path`, `/path`},
		{`/path `, `/path`},
		{`/files/0`, `/files/00`},
	}
	for _, pair := range pairs {
		first, firstReject := pathvirtualization.ParseSelector(pair[0])
		second, secondReject := pathvirtualization.ParseSelector(pair[1])
		if firstReject != pathvirtualization.SelectorRejectNone || secondReject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("pair %q/%q was refused (%v/%v)", pair[0], pair[1], firstReject, secondReject)
		}
		if first.String() == second.String() {
			t.Fatalf("distinct pointers %q and %q canonicalized identically", pair[0], pair[1])
		}
		if slices.Equal(first.Tokens(), second.Tokens()) {
			t.Fatalf("distinct pointers %q and %q share tokens %q", pair[0], pair[1], first.Tokens())
		}
	}
}

// TestParseSelectorIsDeterministic keeps a compiled selector a pure value: the
// same pointer always yields the same tokens, canonical form, and tokens copy.
func TestParseSelectorIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, tc := range canonicalPointers() {
		first, firstReject := pathvirtualization.ParseSelector(tc.pointer)
		second, secondReject := pathvirtualization.ParseSelector(tc.pointer)
		if firstReject != secondReject || first.String() != second.String() {
			t.Fatalf("ParseSelector(%q) is not deterministic: %q/%v then %q/%v",
				tc.pointer, first.String(), firstReject, second.String(), secondReject)
		}
		tokens := first.Tokens()
		if len(tokens) == 0 {
			t.Fatalf("ParseSelector(%q) exposed no tokens", tc.pointer)
		}
		tokens[0] = "mutated"
		if first.Tokens()[0] == "mutated" {
			t.Fatalf("Tokens() for %q exposes the selector's own storage", tc.pointer)
		}
	}
}

// leaf is the expected identity of one selected string leaf: either a single
// string at LeafIndexSingle, or one element of an array-of-strings target.
type leaf struct {
	index int
	value string
}

type resolveCase struct {
	name    string
	doc     string
	pointer string
	// wantRejectForParse marks a row whose pointer is refused at compile time. Such
	// a row documents why the case is not part of the resolution table and is
	// covered by the pointer rejection table instead.
	wantRejectForParse pathvirtualization.SelectorReject
	wantLeaves         []leaf
	wantSkip           pathvirtualization.SelectorSkip
}

func resolveCases() []resolveCase {
	return []resolveCase{
		{
			name:       "string_leaf",
			doc:        `{"path":"/workspace/project/file.txt"}`,
			pointer:    `/path`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/workspace/project/file.txt"}},
		},
		{
			name:       "array_of_strings",
			doc:        `{"paths":["/a","/b","/c"]}`,
			pointer:    `/paths`,
			wantLeaves: []leaf{{index: 0, value: "/a"}, {index: 1, value: "/b"}, {index: 2, value: "/c"}},
		},
		{
			name:     "empty_array_of_strings",
			doc:      `{"paths":[]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNone,
		},
		{
			name:       "index_into_array_of_strings",
			doc:        `{"paths":["/a","/b"]}`,
			pointer:    `/paths/1`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/b"}},
		},
		{
			name:       "nested_members",
			doc:        `{"options":{"target":{"file_path":"/a"},"workdir":"/w"}}`,
			pointer:    `/options/target/file_path`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/a"}},
		},
		{
			name:       "index_through_nested_array_of_objects",
			doc:        `{"edits":[{"file_path":"/a"},{"file_path":"/b"}]}`,
			pointer:    `/edits/1/file_path`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/b"}},
		},
		{
			name:       "member_name_that_looks_numeric",
			doc:        `{"files":{"01":"/a"}}`,
			pointer:    `/files/01`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/a"}},
		},
		{
			name:       "escaped_member_name",
			doc:        `{"a/b":{"c~d":"/x"}}`,
			pointer:    `/a~1b/c~0d`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/x"}},
		},
		{
			name:       "non_ascii_member_name",
			doc:        `{"ключ":"/x"}`,
			pointer:    `/ключ`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/x"}},
		},
		{
			name:       "empty_string_is_a_leaf",
			doc:        `{"path":""}`,
			pointer:    `/path`,
			wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: ""}},
		},
		// Object leaves are not path locators, and rewriting any string inside one
		// would be the recursive arbitrary-string rewrite requirement 2.3 forbids.
		{name: "object_leaf", doc: `{"path":{"nested":"/a"}}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipObject},
		{name: "empty_object_leaf", doc: `{"path":{}}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipObject},
		{name: "object_root", doc: `{"a":"/a"}`, pointer: ``, wantRejectForParse: pathvirtualization.SelectorRejectEmptyPointer},
		// Non-string scalars are refused: a number or boolean is not a locator, and
		// null carries no value at all.
		{name: "number_leaf", doc: `{"path":12}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipNotString},
		{name: "fractional_number_leaf", doc: `{"path":1.5}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipNotString},
		{name: "bool_leaf", doc: `{"path":true}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipNotString},
		{name: "null_leaf", doc: `{"path":null}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipNotString},
		{name: "number_index_leaf", doc: `{"files":["/a",7]}`, pointer: `/files/1`, wantSkip: pathvirtualization.SelectorSkipNotString},
		// A single non-string element refuses the whole pointer: a partially
		// rewritten array would leave the call internally inconsistent.
		{
			name:     "array_with_number",
			doc:      `{"paths":["/a",7]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNotStringArray,
		},
		{
			name:     "array_with_null",
			doc:      `{"paths":[null]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNotStringArray,
		},
		{
			name:     "array_of_objects",
			doc:      `{"paths":[{"path":"/a"}]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNotStringArray,
		},
		{
			name:     "nested_array",
			doc:      `{"paths":[["/a"]]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNotStringArray,
		},
		{
			name:     "array_of_arrays_of_strings",
			doc:      `{"paths":[["/a","/b"]]}`,
			pointer:  `/paths`,
			wantSkip: pathvirtualization.SelectorSkipNotStringArray,
		},
		// Unresolvable references are skipped rather than guessed at.
		{name: "missing_member", doc: `{"other":"/a"}`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipUnresolved},
		{name: "missing_index", doc: `{"paths":["/a"]}`, pointer: `/paths/5`, wantSkip: pathvirtualization.SelectorSkipUnresolved},
		{
			name:     "leading_zero_index_into_array",
			doc:      `{"paths":["/a","/b"]}`,
			pointer:  `/paths/01`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "sign_bearing_index_into_array",
			doc:      `{"paths":["/a","/b"]}`,
			pointer:  `/paths/+1`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "non_digit_index_into_array",
			doc:      `{"paths":["/a","/b"]}`,
			pointer:  `/paths/1.0`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "blank_index_into_array",
			doc:      `{"paths":["/a","/b"]}`,
			pointer:  `/paths/1 `,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "zero_padded_index_into_array",
			doc:      `{"paths":["/a","/b"]}`,
			pointer:  `/paths/00`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "index_equal_to_length",
			doc:      `{"paths":["/a"]}`,
			pointer:  `/paths/1`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "index_into_empty_array",
			doc:      `{"paths":[]}`,
			pointer:  `/paths/0`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		// A long digit run cannot address an element, and must not be accumulated
		// into an index that wraps into range.
		{
			name:     "unrepresentable_index",
			doc:      `{"paths":["/a"]}`,
			pointer:  `/paths/111191117777791111119111777`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "unrepresentable_max_index",
			doc:      `{"paths":["/a"]}`,
			pointer:  `/paths/99999999999999999999999999999999999999999999999999`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "member_below_scalar",
			doc:      `{"path":"/a"}`,
			pointer:  `/path/b`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "member_below_null",
			doc:      `{"path":null}`,
			pointer:  `/path/b`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{
			name:     "member_below_number",
			doc:      `{"path":3}`,
			pointer:  `/path/0`,
			wantSkip: pathvirtualization.SelectorSkipUnresolved,
		},
		{name: "top_level_scalar_document", doc: `"/a"`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipUnresolved},
		{name: "top_level_array_document", doc: `["/a"]`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipUnresolved},
		{name: "null_document", doc: `null`, pointer: `/path`, wantSkip: pathvirtualization.SelectorSkipUnresolved},
		{name: "object_document_index", doc: `{"0":"/a"}`, pointer: `/0`, wantLeaves: []leaf{{index: pathvirtualization.LeafIndexSingle, value: "/a"}}},
	}
}

// TestSelectorResolveAcceptsOnlyStringAndStringArrayLeaves is the behavioral
// proof of the leaf restriction: a selected location yields strings only when it
// is a string or an array whose every element is a string, and any other shape
// yields no leaf at all plus one bounded skip reason.
func TestSelectorResolveAcceptsOnlyStringAndStringArrayLeaves(t *testing.T) {
	t.Parallel()

	for _, tc := range resolveCases() {
		if tc.wantRejectForParse != pathvirtualization.SelectorRejectNone {
			// A case whose pointer is itself refused at compile time is covered by
			// TestParseSelectorRejectsNonCanonicalForms.
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			selector, reject := pathvirtualization.ParseSelector(tc.pointer)
			if reject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("ParseSelector(%q) rejected with %v", tc.pointer, reject)
			}
			leaves, skip := selector.Resolve(decodeDocument(t, tc.doc))
			if skip != tc.wantSkip {
				t.Fatalf("skip = %v, want %v", skip, tc.wantSkip)
			}
			if len(leaves) != len(tc.wantLeaves) {
				t.Fatalf("leaves = %+v, want %d leaf/leaves", leaves, len(tc.wantLeaves))
			}
			for i, want := range tc.wantLeaves {
				got := leaves[i]
				if got.Index != want.index || got.Value != want.value {
					t.Fatalf("leaf %d = %+v, want {index:%d value:%q}", i, got, want.index, want.value)
				}
				if got.Selector.String() != tc.pointer {
					t.Fatalf("leaf %d came from %q, want %q", i, got.Selector.String(), tc.pointer)
				}
			}
			if tc.wantSkip != pathvirtualization.SelectorSkipNone && len(leaves) != 0 {
				t.Fatalf("skip %v still returned %+v; a refused pointer must select nothing", tc.wantSkip, leaves)
			}
			again, againSkip := selector.Resolve(decodeDocument(t, tc.doc))
			if againSkip != skip || len(again) != len(leaves) {
				t.Fatalf("resolution is not deterministic: %d leaves/%v then %d leaves/%v",
					len(leaves), skip, len(again), againSkip)
			}
			for i := range leaves {
				if again[i].Value != leaves[i].Value || again[i].Index != leaves[i].Index {
					t.Fatalf("leaf %d is not deterministic: %+v then %+v", i, leaves[i], again[i])
				}
			}
		})
	}
}

// TestSelectorResolveNeverPartiallySelects pins the all-or-nothing rule for an
// array target: one non-string element refuses every element of that pointer.
func TestSelectorResolveNeverPartiallySelects(t *testing.T) {
	t.Parallel()

	selector, reject := pathvirtualization.ParseSelector(`/paths`)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector rejected with %v", reject)
	}
	leaves, skip := selector.Resolve(decodeDocument(t, `{"paths":["/a","/b",{"path":"/c"},"/d"]}`))
	if skip != pathvirtualization.SelectorSkipNotStringArray {
		t.Fatalf("skip = %v, want %v", skip, pathvirtualization.SelectorSkipNotStringArray)
	}
	if len(leaves) != 0 {
		t.Fatalf("mixed array returned %+v; no element may be selected", leaves)
	}
}

// TestSelectorResolveReadsDecodedDocumentOnly proves resolution observes the
// document a JSON decoder produced and never rewrites it: the same bytes come
// back after a resolution pass.
func TestSelectorResolveDoesNotMutateTheDocument(t *testing.T) {
	t.Parallel()

	const doc = `{"path":"/a","paths":["/b","/c"],"content":{"text":"/d"}}`
	decoded := decodeDocument(t, doc)
	before, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal before resolve: %v", err)
	}
	set := compileSelectorSet(t, `/path`, `/paths`, `/content`, `/paths/0`, `/missing`)
	selection := set.Resolve(decoded)
	if len(selection.Leaves) != 4 {
		t.Fatalf("leaves = %+v, want 4", selection.Leaves)
	}
	after, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal after resolve: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("document changed from %s to %s", before, after)
	}
}

// TestSelectorResolveTreatsJSONNumberAsNonString proves a numeric leaf cannot
// masquerade as a path locator when a decoder is configured to keep numbers
// exact: json.Number is a named string type, and only a real string is a leaf.
func TestSelectorResolveTreatsJSONNumberAsNonString(t *testing.T) {
	t.Parallel()

	decoder := json.NewDecoder(strings.NewReader(`{"path":12,"paths":["/a",34]}`))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	scalar, reject := pathvirtualization.ParseSelector(`/path`)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector rejected with %v", reject)
	}
	if leaves, skip := scalar.Resolve(decoded); skip != pathvirtualization.SelectorSkipNotString || len(leaves) != 0 {
		t.Fatalf("json.Number leaf = %+v/%v, want no leaf and %v", leaves, skip, pathvirtualization.SelectorSkipNotString)
	}
	list, reject := pathvirtualization.ParseSelector(`/paths`)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector rejected with %v", reject)
	}
	if leaves, skip := list.Resolve(decoded); skip != pathvirtualization.SelectorSkipNotStringArray || len(leaves) != 0 {
		t.Fatalf("json.Number array element = %+v/%v, want no leaf and %v", leaves, skip, pathvirtualization.SelectorSkipNotStringArray)
	}
}

// TestSelectorResolveAcceptsTypedStringArrays documents the one shape beyond a
// bare JSON decode that resolution understands: an already-typed []string is
// array-of-strings by construction, so every element is selectable.
func TestSelectorResolveAcceptsTypedStringArrays(t *testing.T) {
	t.Parallel()

	selector, reject := pathvirtualization.ParseSelector(`/paths`)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector rejected with %v", reject)
	}
	leaves, skip := selector.Resolve(map[string]any{"paths": []string{"/a", "/b"}})
	if skip != pathvirtualization.SelectorSkipNone {
		t.Fatalf("skip = %v, want %v", skip, pathvirtualization.SelectorSkipNone)
	}
	if len(leaves) != 2 || leaves[0].Value != "/a" || leaves[1].Value != "/b" {
		t.Fatalf("leaves = %+v, want two selected strings", leaves)
	}
	if leaves[0].Index != 0 || leaves[1].Index != 1 {
		t.Fatalf("leaves = %+v, want element positions 0 and 1", leaves)
	}

	// Descending into a typed array follows the same index rules as a decoded one.
	element, reject := pathvirtualization.ParseSelector(`/paths/1`)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector rejected with %v", reject)
	}
	selected, skip := element.Resolve(map[string]any{"paths": []string{"/a", "/b"}})
	if skip != pathvirtualization.SelectorSkipNone || len(selected) != 1 || selected[0].Value != "/b" {
		t.Fatalf("typed array element = %+v/%v, want [/b]", selected, skip)
	}
	for _, pointer := range []string{`/paths/2`, `/paths/01`, `/paths/1x`} {
		typed, reject := pathvirtualization.ParseSelector(pointer)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("ParseSelector(%q) rejected with %v", pointer, reject)
		}
		if leaves, skip := typed.Resolve(map[string]any{"paths": []string{"/a", "/b"}}); skip != pathvirtualization.SelectorSkipUnresolved || len(leaves) != 0 {
			t.Fatalf("typed array resolved %q to %+v/%v, want no leaf and %v",
				pointer, leaves, skip, pathvirtualization.SelectorSkipUnresolved)
		}
	}
}

// TestSelectorResolveRejectsAnIndexBeyondIntRange pins the narrowing order of the
// array-index rule: the parsed index is compared against the element count as an
// unsigned value before it is narrowed, so an index that is representable as
// uint64 but not as int is skipped as unresolved instead of being narrowed into a
// negative position that names a value outside the array.
func TestSelectorResolveRejectsAnIndexBeyondIntRange(t *testing.T) {
	t.Parallel()

	// 18446744073709551615 is math.MaxUint64: ParseUint accepts it, int cannot
	// hold it. The pointer itself is a legal RFC 6901 token, so the refusal has to
	// come from resolution and not from parsing.
	const pointer = `/paths/18446744073709551615`

	selector, reject := pathvirtualization.ParseSelector(pointer)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("ParseSelector(%q) rejected with %v, want acceptance", pointer, reject)
	}
	leaves, skip := selector.Resolve(decodeDocument(t, `{"paths":["/a","/b"]}`))
	if skip != pathvirtualization.SelectorSkipUnresolved {
		t.Fatalf("skip = %v, want %v", skip, pathvirtualization.SelectorSkipUnresolved)
	}
	if len(leaves) != 0 {
		t.Fatalf("leaves = %+v, want none: an index past the array addresses nothing", leaves)
	}
}

// TestSelectorSetResolutionIsOrderedAndBounded proves a compiled set resolves in
// configuration order and reports every refused pointer separately, with the
// result size bounded by the set rather than by payload size.
func TestSelectorSetResolutionIsOrderedAndBounded(t *testing.T) {
	t.Parallel()

	set := compileSelectorSet(t, `/path`, `/paths`, `/content`, `/missing`)
	selection := set.Resolve(decodeDocument(t, `{"path":"/a","paths":["/b","/c"],"content":{"text":"/d"}}`))

	wantLeaves := []leaf{
		{index: pathvirtualization.LeafIndexSingle, value: "/a"},
		{index: 0, value: "/b"},
		{index: 1, value: "/c"},
	}
	if len(selection.Leaves) != len(wantLeaves) {
		t.Fatalf("leaves = %+v, want %d", selection.Leaves, len(wantLeaves))
	}
	for i, want := range wantLeaves {
		got := selection.Leaves[i]
		if got.Index != want.index || got.Value != want.value {
			t.Fatalf("leaf %d = %+v, want {index:%d value:%q}", i, got, want.index, want.value)
		}
	}
	if got := selection.Leaves[0].Selector.String(); got != "/path" {
		t.Fatalf("first leaf came from %q, want %q", got, "/path")
	}
	if got := selection.Leaves[1].Selector.String(); got != "/paths" {
		t.Fatalf("second leaf came from %q, want %q", got, "/paths")
	}

	wantSkipped := []struct {
		pointer string
		reason  pathvirtualization.SelectorSkip
	}{
		{pointer: `/content`, reason: pathvirtualization.SelectorSkipObject},
		{pointer: `/missing`, reason: pathvirtualization.SelectorSkipUnresolved},
	}
	if len(selection.Skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %+v, want %d entries", selection.Skipped, len(wantSkipped))
	}
	for i, want := range wantSkipped {
		got := selection.Skipped[i]
		if got.Selector.String() != want.pointer || got.Reason != want.reason {
			t.Fatalf("skipped %d = %+v, want %q/%v", i, got, want.pointer, want.reason)
		}
	}
}

// TestEmptySelectorSetSelectsNothing keeps a profile with no explicit pointers a
// no-selector profile instead of an implicit every-field profile.
func TestEmptySelectorSetSelectsNothing(t *testing.T) {
	t.Parallel()

	var set pathvirtualization.SelectorSet
	selection := set.Resolve(decodeDocument(t, `{"path":"/a","paths":["/b"],"content":{"text":"/c"}}`))
	if len(selection.Leaves) != 0 || len(selection.Skipped) != 0 {
		t.Fatalf("empty set selected %+v and skipped %+v", selection.Leaves, selection.Skipped)
	}
}

// TestSelectorResolutionWorkStaysWithinTheConfiguredBounds exercises the largest
// permitted selector set at the deepest permitted pointer depth and proves the
// result is exactly the bounded set of selected leaves. Every pointer walk costs
// at most MaxPointerDepth container lookups, so one resolution pass over a
// profile cannot exceed MaxPointersPerProfile*MaxPointerDepth lookups
// (requirement 7.9).
func TestSelectorResolutionWorkStaysWithinTheConfiguredBounds(t *testing.T) {
	t.Parallel()

	depth := pathvirtualization.MaxPointerDepth
	count := pathvirtualization.MaxPointersPerProfile
	if count <= 0 || depth <= 0 {
		t.Fatalf("bounds are not positive: pointers=%d depth=%d", count, depth)
	}

	pointers := make([]string, 0, count)
	leaves := decodeDocument(t, nestedDocument(depth-1, count))
	for i := range count {
		pointers = append(pointers, pointerOfDepth(depth-1)+fmt.Sprintf("/leaf%d", i))
	}
	set := compileSelectorSet(t, pointers...)
	selection := set.Resolve(leaves)
	if len(selection.Leaves) != count {
		t.Fatalf("leaves = %d, want %d", len(selection.Leaves), count)
	}
	if len(selection.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", selection.Skipped)
	}
	for i := range count {
		want := fmt.Sprintf("value%d", i)
		if selection.Leaves[i].Value != want {
			t.Fatalf("leaf %d = %q, want %q", i, selection.Leaves[i].Value, want)
		}
	}
}

// TestSelectorBoundsRejectAtTheLimit proves every bound is enforced by
// rejection, never by truncation: input exactly at a limit compiles, and input
// one element past it fails with its own reason.
func TestSelectorBoundsRejectAtTheLimit(t *testing.T) {
	t.Parallel()

	t.Run("pointer_depth", func(t *testing.T) {
		t.Parallel()

		selector, reject := pathvirtualization.ParseSelector(pointerOfDepth(pathvirtualization.MaxPointerDepth))
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("depth %d rejected with %v, want acceptance", pathvirtualization.MaxPointerDepth, reject)
		}
		if got := len(selector.Tokens()); got != pathvirtualization.MaxPointerDepth {
			t.Fatalf("tokens = %d, want %d", got, pathvirtualization.MaxPointerDepth)
		}
		_, reject = pathvirtualization.ParseSelector(pointerOfDepth(pathvirtualization.MaxPointerDepth + 1))
		if reject != pathvirtualization.SelectorRejectPointerDepth {
			t.Fatalf("depth %d rejected with %v, want %v",
				pathvirtualization.MaxPointerDepth+1, reject, pathvirtualization.SelectorRejectPointerDepth)
		}
	})

	t.Run("pointers_per_profile", func(t *testing.T) {
		t.Parallel()

		if _, reject := compileProfileInput(t, pointersOfCount(pathvirtualization.MaxPointersPerProfile)); reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("%d pointers rejected with %v, want acceptance",
				pathvirtualization.MaxPointersPerProfile, reject)
		}
		if _, reject := compileProfileInput(t, pointersOfCount(pathvirtualization.MaxPointersPerProfile+1)); reject != pathvirtualization.SelectorRejectPointerCount {
			t.Fatalf("%d pointers rejected with %v, want %v",
				pathvirtualization.MaxPointersPerProfile+1, reject, pathvirtualization.SelectorRejectPointerCount)
		}
	})

	t.Run("pointer_count_spans_both_lists", func(t *testing.T) {
		t.Parallel()

		input := pathvirtualization.ProfileInput{
			Names:              []string{"read_file"},
			ArgPointers:        pointersOfCount(pathvirtualization.MaxPointersPerProfile - 1),
			ResultJSONPointers: pointersOfCount(2),
		}
		if _, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{input}); reject != pathvirtualization.SelectorRejectPointerCount {
			t.Fatalf("pointer lists totaling past the bound rejected with %v, want %v",
				reject, pathvirtualization.SelectorRejectPointerCount)
		}
	})

	t.Run("profile_count", func(t *testing.T) {
		t.Parallel()

		if _, reject := pathvirtualization.CompileProfiles(profilesOfCount(pathvirtualization.MaxProfiles)); reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("%d profiles rejected with %v, want acceptance", pathvirtualization.MaxProfiles, reject)
		}
		if _, reject := pathvirtualization.CompileProfiles(profilesOfCount(pathvirtualization.MaxProfiles + 1)); reject != pathvirtualization.SelectorRejectProfileCount {
			t.Fatalf("%d profiles rejected with %v, want %v",
				pathvirtualization.MaxProfiles+1, reject, pathvirtualization.SelectorRejectProfileCount)
		}
	})

	t.Run("path_keys", func(t *testing.T) {
		t.Parallel()

		if _, reject := pathvirtualization.CompilePathKeys(pathKeysOfCount(pathvirtualization.MaxPathKeys)); reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("%d path keys rejected with %v, want acceptance", pathvirtualization.MaxPathKeys, reject)
		}
		if _, reject := pathvirtualization.CompilePathKeys(pathKeysOfCount(pathvirtualization.MaxPathKeys + 1)); reject != pathvirtualization.SelectorRejectPathKeyCount {
			t.Fatalf("%d path keys rejected with %v, want %v",
				pathvirtualization.MaxPathKeys+1, reject, pathvirtualization.SelectorRejectPathKeyCount)
		}
	})
}

// TestCompileProfilesFailsClosedOnAmbiguousConfiguration covers the fail-closed
// compile rules of requirement 7.5: an unusable, ambiguous, or over-limit profile
// refuses the whole configuration instead of publishing a partial selector set.
func TestCompileProfilesFailsClosedOnAmbiguousConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		inputs     []pathvirtualization.ProfileInput
		wantReject pathvirtualization.SelectorReject
	}{
		{
			name:       "profile_without_names",
			inputs:     []pathvirtualization.ProfileInput{{ArgPointers: []string{"/path"}}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name:       "empty_tool_name",
			inputs:     []pathvirtualization.ProfileInput{{Names: []string{"read_file", ""}}},
			wantReject: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "duplicate_name_within_profile",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file", "read_file"}, ArgPointers: []string{"/path"}},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "duplicate_name_across_profiles",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ArgPointers: []string{"/path"}},
				{Names: []string{"read_file"}, ArgPointers: []string{"/path"}},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "duplicate_pointer_within_list",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ArgPointers: []string{"/path", "/path"}},
			},
			wantReject: pathvirtualization.SelectorRejectDuplicatePointer,
		},
		{
			name: "same_pointer_in_both_lists",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ArgPointers: []string{"/path"}, ResultJSONPointers: []string{"/path"}},
			},
			wantReject: pathvirtualization.SelectorRejectNone,
		},
		{
			name: "invalid_pointer",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ArgPointers: []string{"/a~2b"}},
			},
			wantReject: pathvirtualization.SelectorRejectInvalidEscape,
		},
		{
			name: "invalid_result_pointer",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ResultJSONPointers: []string{"#/content"}},
			},
			wantReject: pathvirtualization.SelectorRejectURIFragmentPointer,
		},
		{
			name: "pointer_past_depth_bound",
			inputs: []pathvirtualization.ProfileInput{
				{Names: []string{"read_file"}, ArgPointers: []string{pointerOfDepth(pathvirtualization.MaxPointerDepth + 1)}},
			},
			wantReject: pathvirtualization.SelectorRejectPointerDepth,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			compiled, reject := pathvirtualization.CompileProfiles(tc.inputs)
			if reject != tc.wantReject {
				t.Fatalf("CompileProfiles rejected with %v, want %v", reject, tc.wantReject)
			}
			if reject != pathvirtualization.SelectorRejectNone && len(compiled) != 0 {
				t.Fatalf("rejected configuration still published %d profile(s)", len(compiled))
			}
		})
	}
}

// TestCompileProfilesProducesUsableSelectors proves compilation preserves
// configuration order and canonical spelling, so the compiled sets resolve
// exactly the configured locations.
func TestCompileProfilesProducesUsableSelectors(t *testing.T) {
	t.Parallel()

	inputs := []pathvirtualization.ProfileInput{
		{
			Names:              []string{"custom_read", "custom_write"},
			ArgPointers:        []string{"/file_path", `/a~1b`},
			ResultJSONPointers: []string{"/paths"},
		},
		{
			Names: []string{"custom_list"},
		},
	}
	compiled, reject := pathvirtualization.CompileProfiles(inputs)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileProfiles rejected with %v", reject)
	}
	if len(compiled) != len(inputs) {
		t.Fatalf("compiled %d profiles, want %d", len(compiled), len(inputs))
	}
	if got := compiled[0].Names; !slices.Equal(got, inputs[0].Names) {
		t.Fatalf("names = %q, want %q", got, inputs[0].Names)
	}
	if got := canonicalForms(t, compiled[0].ArgPointers); !slices.Equal(got, []string{"/file_path", "/a~1b"}) {
		t.Fatalf("argument pointers = %q, want %q", got, []string{"/file_path", "/a~1b"})
	}
	if got := canonicalForms(t, compiled[0].ResultJSONPointers); !slices.Equal(got, []string{"/paths"}) {
		t.Fatalf("result pointers = %q, want %q", got, []string{"/paths"})
	}
	if len(compiled[1].ArgPointers) != 0 || len(compiled[1].ResultJSONPointers) != 0 {
		t.Fatalf("selector-free profile compiled %+v", compiled[1])
	}

	selection := compiled[0].ArgPointers.Resolve(decodeDocument(t, `{"file_path":"/a","a/b":"/b"}`))
	if len(selection.Leaves) != 2 {
		t.Fatalf("argument selection = %+v, want 2 leaves", selection.Leaves)
	}
	if selection.Leaves[0].Value != "/a" || selection.Leaves[1].Value != "/b" {
		t.Fatalf("argument selection = %+v, want [/a /b]", selection.Leaves)
	}
	result := compiled[0].ResultJSONPointers.Resolve(decodeDocument(t, `{"paths":["/r1","/r2"]}`))
	if len(result.Leaves) != 2 || result.Leaves[0].Value != "/r1" || result.Leaves[1].Value != "/r2" {
		t.Fatalf("result selection = %+v, want two result leaves", result.Leaves)
	}
}

// TestCompiledProfileNamesAreByteExact keeps tool-name matching an exact-byte
// concern: compilation never trims, case-folds, or normalizes a name, so no
// prefix or substring match can ever become authority.
func TestCompiledProfileNamesAreByteExact(t *testing.T) {
	t.Parallel()

	inputs := []pathvirtualization.ProfileInput{
		{Names: []string{"Read_File"}, ArgPointers: []string{"/path"}},
		{Names: []string{"read_file"}, ArgPointers: []string{"/path"}},
		{Names: []string{"read_file_v2"}, ArgPointers: []string{"/path"}},
		{Names: []string{"read_file "}, ArgPointers: []string{"/path"}},
	}
	compiled, reject := pathvirtualization.CompileProfiles(inputs)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileProfiles rejected with %v", reject)
	}
	seen := make(map[string]bool, len(compiled))
	for i, profile := range compiled {
		if profile.Names[0] != inputs[i].Names[0] {
			t.Fatalf("name %d = %q, want %q", i, profile.Names[0], inputs[i].Names[0])
		}
		if seen[profile.Names[0]] {
			t.Fatalf("name %q compiled twice", profile.Names[0])
		}
		seen[profile.Names[0]] = true
	}
}

// TestCompilePathKeysKeepsAnExactBoundedVocabulary proves the schema path-key
// vocabulary this task bounds for inference accepts the documented default set
// unchanged and refuses blanks, duplicates, and over-limit input.
func TestCompilePathKeysKeepsAnExactBoundedVocabulary(t *testing.T) {
	t.Parallel()

	defaultVocabulary := []string{
		"path", "file_path", "filepath", "directory", "dir",
		"cwd", "workdir", "root", "target_path", "paths",
	}
	keys, reject := pathvirtualization.CompilePathKeys(defaultVocabulary)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompilePathKeys rejected the default vocabulary with %v", reject)
	}
	if !slices.Equal(keys, defaultVocabulary) {
		t.Fatalf("keys = %q, want %q", keys, defaultVocabulary)
	}
	if len(keys) > pathvirtualization.MaxPathKeys {
		t.Fatalf("default vocabulary holds %d keys, past the bound %d", len(keys), pathvirtualization.MaxPathKeys)
	}

	// An absent vocabulary is valid configuration, not an error: it simply
	// publishes no key to compare a declared property name against.
	if keys, reject := pathvirtualization.CompilePathKeys(nil); reject != pathvirtualization.SelectorRejectNone || len(keys) != 0 {
		t.Fatalf("CompilePathKeys(nil) = %q/%v, want no keys and acceptance", keys, reject)
	}

	cases := []struct {
		name       string
		keys       []string
		wantReject pathvirtualization.SelectorReject
	}{
		{
			name:       "empty_key",
			keys:       []string{"path", ""},
			wantReject: pathvirtualization.SelectorRejectEmptyPathKey,
		},
		{
			name:       "duplicate_key",
			keys:       []string{"path", "file_path", "path"},
			wantReject: pathvirtualization.SelectorRejectDuplicatePathKey,
		},
		{
			name:       "duplicate_key_exact_bytes",
			keys:       []string{"path", "Path"},
			wantReject: pathvirtualization.SelectorRejectNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys, reject := pathvirtualization.CompilePathKeys(tc.keys)
			if reject != tc.wantReject {
				t.Fatalf("CompilePathKeys(%q) rejected with %v, want %v", tc.keys, reject, tc.wantReject)
			}
			if reject != pathvirtualization.SelectorRejectNone && len(keys) != 0 {
				t.Fatalf("rejected vocabulary still published %q", keys)
			}
		})
	}
}

// TestProfileInputCarriesNoNamespaceOrVersionSetting is the structural half of
// requirement 7.4 for this task: the compile-time selector input has exactly the
// tool-name and pointer fields and nothing else, so no operator bytes can name the
// reserved alias namespace, its version, or the workspace-tag encoding. That
// contract stays a fixed implementation value in the mapper, and this input
// surface has no field that could move it.
func TestProfileInputCarriesNoNamespaceOrVersionSetting(t *testing.T) {
	t.Parallel()

	profileType := reflect.TypeFor[pathvirtualization.ProfileInput]()
	fields := make([]string, 0, profileType.NumField())
	for field := range profileType.Fields() {
		fields = append(fields, field.Name)
	}
	want := []string{"Names", "ArgPointers", "ResultJSONPointers"}
	if !slices.Equal(fields, want) {
		t.Fatalf("ProfileInput fields = %q, want %q", fields, want)
	}
	for i, field := range fields {
		if kind := profileType.Field(i).Type.Kind(); kind != reflect.Slice {
			t.Fatalf("ProfileInput.%s has kind %v, want a slice of operator text", field, kind)
		}
	}
}

// TestZeroSelectorSelectsNothing makes the zero selector safe by construction:
// it is the state of a refused pointer, and resolving it must not fall back to
// the document root, which would turn one invalid selector into a whole-document
// rewrite.
func TestZeroSelectorSelectsNothing(t *testing.T) {
	t.Parallel()

	var zero pathvirtualization.Selector
	for _, raw := range []string{`"/root/a"`, `{"path":"/root/a"}`, `["/root/a"]`, `null`} {
		leaves, skip := zero.Resolve(decodeDocument(t, raw))
		if skip != pathvirtualization.SelectorSkipUnresolved || len(leaves) != 0 {
			t.Fatalf("zero selector resolved %q to %+v/%v, want no leaf and %v",
				raw, leaves, skip, pathvirtualization.SelectorSkipUnresolved)
		}
	}
	if set := (pathvirtualization.SelectorSet{zero}); len(set.Resolve(decodeDocument(t, `"/root/a"`)).Leaves) != 0 {
		t.Fatal("a set holding only a zero selector selected a leaf")
	}
}

// TestSelectorReasonLabelsAreBounded pins the fixed label of every bounded
// reason code. These labels are the only thing that may reach metrics, so each
// one is unique, fixed, and free of pointer or payload bytes.
func TestSelectorReasonLabelsAreBounded(t *testing.T) {
	t.Parallel()

	rejects := []struct {
		reject pathvirtualization.SelectorReject
		label  string
	}{
		{pathvirtualization.SelectorRejectNone, ""},
		{pathvirtualization.SelectorRejectEmptyPointer, "empty_pointer"},
		{pathvirtualization.SelectorRejectNotAbsolutePointer, "not_absolute_pointer"},
		{pathvirtualization.SelectorRejectURIFragmentPointer, "uri_fragment_pointer"},
		{pathvirtualization.SelectorRejectInvalidEscape, "invalid_escape"},
		{pathvirtualization.SelectorRejectEmptyToken, "empty_token"},
		{pathvirtualization.SelectorRejectArrayEndToken, "array_end_token"},
		{pathvirtualization.SelectorRejectPointerDepth, "pointer_depth"},
		{pathvirtualization.SelectorRejectDuplicatePointer, "duplicate_pointer"},
		{pathvirtualization.SelectorRejectProfileCount, "profile_count"},
		{pathvirtualization.SelectorRejectPointerCount, "pointer_count"},
		{pathvirtualization.SelectorRejectEmptyToolName, "empty_tool_name"},
		{pathvirtualization.SelectorRejectDuplicateToolName, "duplicate_tool_name"},
		{pathvirtualization.SelectorRejectPathKeyCount, "path_key_count"},
		{pathvirtualization.SelectorRejectEmptyPathKey, "empty_path_key"},
		{pathvirtualization.SelectorRejectDuplicatePathKey, "duplicate_path_key"},
	}
	skips := []struct {
		skip  pathvirtualization.SelectorSkip
		label string
	}{
		{pathvirtualization.SelectorSkipNone, ""},
		{pathvirtualization.SelectorSkipUnresolved, "selector_unresolved"},
		{pathvirtualization.SelectorSkipNotString, "selector_not_string"},
		{pathvirtualization.SelectorSkipObject, "selector_object"},
		{pathvirtualization.SelectorSkipNotStringArray, "selector_not_string_array"},
	}

	labels := make(map[string]string, len(rejects)+len(skips))
	claim := func(label, owner string) {
		if label == "" {
			// The two "no reason" labels are both empty by design.
			return
		}
		if other, dup := labels[label]; dup {
			t.Errorf("reason label %q is shared with %s", label, other)
		}
		labels[label] = owner
	}
	for _, tc := range rejects {
		if got := tc.reject.String(); got != tc.label {
			t.Errorf("SelectorReject(%d).String() = %q, want %q", tc.reject, got, tc.label)
		}
		claim(tc.label, "SelectorReject "+tc.label)
	}
	for _, tc := range skips {
		if got := tc.skip.String(); got != tc.label {
			t.Errorf("SelectorSkip(%d).String() = %q, want %q", tc.skip, got, tc.label)
		}
		claim(tc.label, "SelectorSkip "+tc.label)
	}
	if got := pathvirtualization.SelectorReject(200).String(); got != "unknown" {
		t.Errorf("out-of-range compile reason = %q, want %q", got, "unknown")
	}
	if got := pathvirtualization.SelectorSkip(200).String(); got != "unknown" {
		t.Errorf("out-of-range skip reason = %q, want %q", got, "unknown")
	}
}

// TestSelectorBoundsArePositiveAndDocumented keeps the limits defensible: every
// bound must be a positive integer, and the product of pointers and depth must
// stay small enough that one resolution pass is a bounded amount of container
// lookups per document.
func TestSelectorBoundsArePositiveAndBounded(t *testing.T) {
	t.Parallel()

	bounds := map[string]int{
		"MaxProfiles":           pathvirtualization.MaxProfiles,
		"MaxPointersPerProfile": pathvirtualization.MaxPointersPerProfile,
		"MaxPointerDepth":       pathvirtualization.MaxPointerDepth,
		"MaxPathKeys":           pathvirtualization.MaxPathKeys,
	}
	for name, bound := range bounds {
		if bound <= 0 {
			t.Errorf("%s = %d, want a positive bound", name, bound)
		}
	}
	const maxLookupsPerDocument = 4096
	lookups := pathvirtualization.MaxPointersPerProfile * pathvirtualization.MaxPointerDepth
	if lookups <= 0 || lookups > maxLookupsPerDocument {
		t.Errorf("MaxPointersPerProfile*MaxPointerDepth = %d, want 1..%d container lookups per document",
			lookups, maxLookupsPerDocument)
	}
}

// compileProfileInput compiles one single-name profile, which is the shape the
// pointer-count bound assertions need.
func compileProfileInput(t *testing.T, pointers []string) ([]pathvirtualization.CompiledProfile, pathvirtualization.SelectorReject) {
	t.Helper()

	return pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{
		{Names: []string{"read_file"}, ArgPointers: pointers},
	})
}

// compileSelectorSet compiles pointers into a selector set and fails the test if
// any pointer is refused.
func compileSelectorSet(t *testing.T, pointers ...string) pathvirtualization.SelectorSet {
	t.Helper()

	compiled, reject := compileProfileInput(t, pointers)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compiling %q rejected with %v", pointers, reject)
	}
	return compiled[0].ArgPointers
}

// canonicalForms returns the canonical spelling of every selector in a set.
func canonicalForms(t *testing.T, set pathvirtualization.SelectorSet) []string {
	t.Helper()

	forms := make([]string, 0, len(set))
	for _, selector := range set {
		forms = append(forms, selector.String())
	}
	return forms
}

// decodeDocument decodes a JSON document into the shape selector resolution
// consumes.
func decodeDocument(t *testing.T, raw string) any {
	t.Helper()

	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return decoded
}

// pointerOfDepth builds the canonical pointer with exactly depth tokens.
func pointerOfDepth(depth int) string {
	if depth <= 0 {
		return "/"
	}
	return "/" + strings.TrimSuffix(strings.Repeat("a/", depth), "/")
}

// pointersOfCount builds count distinct canonical pointers.
func pointersOfCount(count int) []string {
	pointers := make([]string, 0, count)
	for i := range count {
		pointers = append(pointers, fmt.Sprintf("/field%d", i))
	}
	return pointers
}

// profilesOfCount builds count operator profiles with distinct exact names.
func profilesOfCount(count int) []pathvirtualization.ProfileInput {
	profiles := make([]pathvirtualization.ProfileInput, 0, count)
	for i := range count {
		profiles = append(profiles, pathvirtualization.ProfileInput{
			Names:       []string{fmt.Sprintf("tool_%d", i)},
			ArgPointers: []string{"/path"},
		})
	}
	return profiles
}

// pathKeysOfCount builds count distinct vocabulary keys.
func pathKeysOfCount(count int) []string {
	keys := make([]string, 0, count)
	for i := range count {
		keys = append(keys, fmt.Sprintf("key_%d", i))
	}
	return keys
}

// nestedDocument builds a document nested depth levels deep whose deepest object
// holds count string leaves named leaf0..leaf{count-1}.
func nestedDocument(depth, count int) string {
	var document strings.Builder
	for range depth {
		document.WriteString(`{"a":`)
	}
	document.WriteByte('{')
	for i := range count {
		if i > 0 {
			document.WriteByte(',')
		}
		fmt.Fprintf(&document, `"leaf%d":"value%d"`, i, i)
	}
	document.WriteByte('}')
	for range depth {
		document.WriteByte('}')
	}
	return document.String()
}
