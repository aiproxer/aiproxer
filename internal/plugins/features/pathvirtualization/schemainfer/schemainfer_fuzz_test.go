package schemainfer_test

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FuzzInferArgumentsIsTotal locks the inference step for arbitrary schema bytes
// and arbitrary declared names: it never panics, is deterministic, mutates
// nothing, and any published pointer is a pointer the accepted selector dialect
// itself accepts. It also pins the fail-closed invariants no input may break: a
// non-inferred outcome publishes nothing at all rather than a truncated prefix,
// the profile pointer bound holds, and no published token is a payload concept.
func FuzzInferArgumentsIsTotal(f *testing.F) {
	for _, seed := range []string{
		`{"type":"object","properties":{"path":{"type":"string"}}}`,
		`{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}}}`,
		`{"type":"object","properties":{"content":{"type":"object","properties":{"path":{"type":"string"}}}}}`,
		`{"type":"object","properties":{"options":{"type":"object","properties":{"cwd":{"type":"string"}}}}}`,
		`{"$ref":"#/$defs/X"}`,
		`{"type":"object","properties":{"path":{"oneOf":[{"type":"string"}]}}}`,
		`{"type":"object","properties":{"a/b":{"type":"string"},"~x":{"type":"string"},"-":{"type":"string"},"":{"type":"string"}}}`,
		`{"type":"object","properties":{"path":{"type":"string"},"path":{"type":"string"}}}`,
		`null`,
		`true`,
		`{`,
		``,
	} {
		f.Add(seed, "path")
	}
	for _, name := range []string{"path", "dir", "content", "a/b", "~x", "-", "", "PATH", "  paths  "} {
		f.Add(`{"type":"object","properties":{"k":{"type":"string"}}}`, name)
	}
	f.Fuzz(func(t *testing.T, schema, name string) {
		// An empty key can never enter the bounded vocabulary, so a hostile name
		// falls back to one fixed key instead of weakening the assertion below.
		keys := []string{name}
		if name == "" {
			keys = []string{"path"}
		}
		inferrer, reject := schemainfer.New(keys)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("New(%q) rejected with %v", name, reject)
		}
		tool := aipapi.ToolDef{Name: "fuzz", Parameters: json.RawMessage(schema)}
		before := append(json.RawMessage(nil), tool.Parameters...)

		result := inferrer.InferArguments(tool)
		again := inferrer.InferArguments(tool)
		if result.Outcome != again.Outcome || !slices.Equal(canonicalPointers(result.Pointers), canonicalPointers(again.Pointers)) {
			t.Fatalf("inference is not deterministic: %q/%q then %q/%q",
				canonicalPointers(result.Pointers), result.Outcome,
				canonicalPointers(again.Pointers), again.Outcome)
		}
		if !slices.Equal([]byte(tool.Parameters), before) {
			t.Fatal("inference rewrote the declared schema")
		}
		if result.Outcome == schemainfer.OutcomeInferred {
			if len(result.Pointers) == 0 {
				t.Fatal("inferred outcome with no pointer")
			}
		} else if len(result.Pointers) != 0 {
			t.Fatalf("outcome %q published %q; every non-inferred outcome must publish nothing",
				result.Outcome, canonicalPointers(result.Pointers))
		}
		if len(result.Pointers) > pathvirtualization.MaxPointersPerProfile {
			t.Fatalf("published %d pointers, above the profile bound", len(result.Pointers))
		}
		denylist := schemainfer.PayloadConceptKeys()
		for _, pointer := range result.Pointers {
			selector, selectorReject := pathvirtualization.ParseSelector(pointer.String())
			if selectorReject != pathvirtualization.SelectorRejectNone {
				t.Fatalf("published pointer %q is outside the accepted dialect: %v", pointer, selectorReject)
			}
			tokens := selector.Tokens()
			if len(tokens) == 0 || len(tokens) > pathvirtualization.MaxPointerDepth {
				t.Fatalf("published pointer %q has %d tokens", pointer, len(tokens))
			}
			for _, token := range tokens {
				if slices.Contains(denylist, schemainfer.NormalizeKey(token)) {
					t.Fatalf("published pointer %q descends through payload concept %q", pointer, token)
				}
			}
		}
	})
}

// FuzzInferArgumentsIgnoresMemberOrder locks the property the reader's whole
// refusal signal depends on: re-declaring a schema's members in a different order
// must produce the identical outcome and the identical selector set.
//
// The order a schema spells its members in is an authoring detail, never a
// declaration of meaning. A reader that folds members in sequence can leak that
// detail — an accumulated flag cleared by a later member, or a first-wins value
// interpreted before an order-independent one — and the leak is invisible to a
// table of hand-written schemas, because each table row fixes one order.
//
// The fuzzer supplies a permutation, not a schema, so it reaches orders no seed
// would have been written with.
func FuzzInferArgumentsIgnoresMemberOrder(f *testing.F) {
	for _, seed := range []struct {
		schema string
		order  string
	}{
		{`{"type":"object","properties":{"path":{"type":"string"}}}`, "a"},
		{`{"$ref":"#/$defs/X","type":"object","properties":{"path":{"type":"string"}}}`, "b"},
		{`{"type":"object","allOf":[{"type":"object"}],"properties":{"path":{"type":"string"}}}`, "c"},
		{`{"type":"object","properties":{"a":{"$ref":"#/$defs/A","type":"object","properties":{"path":{"type":"string"}}}}}`, "d"},
		{`{"type":"array","type":"object","properties":{"path":{"type":"string"}}}`, "e"},
		{`{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"},"contains":{"type":"string"}}}}`, "f"},
		{`{"properties":[],"type":"object"}`, "g"},
		{`{"description":"d","type":"object","properties":{"path":{"type":"string","description":"the content"}}}`, "h"},
		{`{"type":"object","properties":{"path":{"type":"string"}}}`, ""},
		{`{}`, "i"},
		{`[]`, "j"},
		{`null`, "k"},
		{`{`, "l"},
	} {
		f.Add(seed.schema, seed.order)
	}
	f.Fuzz(func(t *testing.T, schema, order string) {
		permuted, ok := permuteDeclaredMembers(schema, order)
		if !ok {
			// Not a JSON object with at least two members, so there is no declaration
			// order to change. Totality is FuzzInferArgumentsIsTotal's assertion.
			return
		}
		inferrer, reject := schemainfer.New(schemainfer.DefaultPathKeys())
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("New(default) rejected with %v", reject)
		}
		first := inferrer.InferArguments(aipapi.ToolDef{Name: "fuzz", Parameters: json.RawMessage(schema)})
		second := inferrer.InferArguments(aipapi.ToolDef{Name: "fuzz", Parameters: json.RawMessage(permuted)})
		if first.Outcome != second.Outcome {
			t.Fatalf("outcome changed with member order: %s => %q, %s => %q",
				schema, first.Outcome, permuted, second.Outcome)
		}
		if got, want := canonicalPointers(second.Pointers), canonicalPointers(first.Pointers); !slices.Equal(got, want) {
			t.Fatalf("pointers changed with member order: %s => %q, %s => %q",
				schema, want, permuted, got)
		}
	})
}

// permuteDeclaredMembers re-spells a JSON object's top-level members in a
// deterministic order derived from the given seed. It reports false when the
// input is not an object, cannot be read whole, or holds fewer than two
// members, so the caller skips rather than comparing a degenerate spelling.
func permuteDeclaredMembers(schema, seed string) (string, bool) {
	members, ok := readTopLevelMembers(schema)
	if !ok || len(members) < 2 {
		return "", false
	}
	order := seededOrder(len(members), seed)
	permuted := make([]string, 0, len(members))
	for _, index := range order {
		permuted = append(permuted, members[index])
	}
	return "{" + strings.Join(permuted, ",") + "}", true
}

// readTopLevelMembers reads one JSON object's members as re-spellable
// `"name":value` pairs, keeping their declaration order.
//
// The name is re-escaped rather than copied: a decoded name may hold a quote, a
// backslash, or a control byte, and pasting those bytes back unescaped would
// change the schema instead of only reordering it. The value is kept as the raw
// bytes the decoder produced, which is already its canonical spelling.
func readTopLevelMembers(schema string) ([]string, bool) {
	decoder := json.NewDecoder(strings.NewReader(schema))
	head, err := decoder.Token()
	if err != nil || head != json.Delim('{') {
		return nil, false
	}
	members := make([]string, 0, 8)
	for decoder.More() {
		name, nameErr := decoder.Token()
		if nameErr != nil {
			return nil, false
		}
		key, ok := name.(string)
		if !ok {
			return nil, false
		}
		escaped, escapeErr := json.Marshal(key)
		if escapeErr != nil {
			return nil, false
		}
		var value json.RawMessage
		if valueErr := decoder.Decode(&value); valueErr != nil {
			return nil, false
		}
		members = append(members, string(escaped)+":"+string(value))
	}
	closing, closeErr := decoder.Token()
	if closeErr != nil || closing != json.Delim('}') {
		return nil, false
	}
	if _, trailingErr := decoder.Token(); !errors.Is(trailingErr, io.EOF) {
		return nil, false
	}
	return members, true
}

// seededOrder returns a Fisher-Yates ordering of n indices, driven by a byte
// stream derived from seed so a failing input reproduces its own permutation.
func seededOrder(n int, seed string) []int {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(seed))
	state := digest.Sum64()
	next := func(limit int) int {
		// SplitMix64: a bounded, allocation-free bit source good enough to pick
		// permutations, and reproducible for a given seed.
		state += 0x9e3779b97f4a7c15
		z := state
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		return int((z ^ (z >> 32)) % uint64(limit))
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := next(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	return order
}
