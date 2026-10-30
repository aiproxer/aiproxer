package pathvirtualization_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzParseSelectorIsTotal locks the pointer parser for arbitrary operator
// bytes: parsing never panics, is deterministic, and an accepted pointer is
// reproduced exactly by its canonical form and rebuilds itself. An accepted
// pointer also never hides a rejection class: it is absolute, its tokens are
// non-empty, and no token contains an unescaped or over-encoded `~`.
func FuzzParseSelectorIsTotal(f *testing.F) {
	for _, tc := range canonicalPointers() {
		f.Add(tc.pointer)
	}
	for _, tc := range resolveCases() {
		if tc.wantRejectForParse == pathvirtualization.SelectorRejectNone {
			f.Add(tc.pointer)
		}
	}
	f.Add("")
	f.Add("#/a/b")
	f.Add("/a~")
	f.Add("/a/")
	f.Add("/-")
	f.Add(pointerOfDepth(pathvirtualization.MaxPointerDepth + 1))
	f.Fuzz(func(t *testing.T, pointer string) {
		selector, reject := pathvirtualization.ParseSelector(pointer)
		again, againReject := pathvirtualization.ParseSelector(pointer)
		if reject != againReject || selector.String() != again.String() {
			t.Fatalf("ParseSelector(%q) is not deterministic: %q/%v then %q/%v",
				pointer, selector.String(), reject, again.String(), againReject)
		}
		if reject != pathvirtualization.SelectorRejectNone {
			if selector.String() != "" || len(selector.Tokens()) != 0 {
				t.Fatalf("rejected %q still produced %q/%q", pointer, selector.String(), selector.Tokens())
			}
			return
		}
		if selector.String() != pointer {
			t.Fatalf("accepted %q but canonicalized to %q", pointer, selector.String())
		}
		if !strings.HasPrefix(selector.String(), "/") {
			t.Fatalf("accepted non-absolute pointer %q", pointer)
		}
		tokens := selector.Tokens()
		if len(tokens) == 0 {
			t.Fatalf("accepted %q with no tokens", pointer)
		}
		if len(tokens) > pathvirtualization.MaxPointerDepth {
			t.Fatalf("accepted %q with %d tokens, past the depth bound %d",
				pointer, len(tokens), pathvirtualization.MaxPointerDepth)
		}
		for _, token := range tokens {
			if token == "" {
				t.Fatalf("accepted %q with an empty token", pointer)
			}
		}
		// Re-encoding the decoded tokens must reproduce the accepted pointer byte
		// for byte. That proves every accepted pointer is the unique canonical
		// spelling of its reference: no other spelling can decode to the same
		// tokens, because the encoder here and the parser there are inverses.
		if encoded := encodeTokens(tokens); encoded != pointer {
			t.Fatalf("accepted %q but its tokens %q re-encode to %q", pointer, tokens, encoded)
		}
		if rebuilt, rebuiltReject := pathvirtualization.ParseSelector(selector.String()); rebuiltReject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("canonical form %q of %q was refused with %v", selector.String(), pointer, rebuiltReject)
		} else if len(rebuilt.Tokens()) != len(tokens) {
			t.Fatalf("canonical form %q rebuilt %d tokens, want %d", selector.String(), len(rebuilt.Tokens()), len(tokens))
		}
	})
}

// FuzzSelectorResolutionIsBounded locks shape enforcement for arbitrary decoded
// documents: resolution never panics, a refused pointer selects nothing at all,
// every selected leaf is a string that really sits where the selector says, and
// resolution is deterministic and read-only.
func FuzzSelectorResolutionIsBounded(f *testing.F) {
	for _, tc := range canonicalPointers() {
		f.Add(tc.pointer, `{"a":"x"}`)
		f.Add(tc.pointer, `{"a":["x","y"]}`)
	}
	for _, tc := range resolveCases() {
		if tc.wantRejectForParse == pathvirtualization.SelectorRejectNone {
			f.Add(tc.pointer, tc.doc)
		}
	}
	f.Add(`/path`, `null`)
	f.Add(`/path`, `"/leaf"`)
	f.Add(`/paths`, `{"paths":[null,"x"]}`)
	f.Add(`/0`, `{"0":1}`)
	f.Add(`/paths/0`, `{"paths":[["x"]]}`)
	f.Fuzz(func(t *testing.T, pointer, raw string) {
		selector, reject := pathvirtualization.ParseSelector(pointer)
		if reject != pathvirtualization.SelectorRejectNone {
			return
		}
		var document any
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			return
		}
		before, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}
		leaves, skip := selector.Resolve(document)
		again, againSkip := selector.Resolve(document)
		if skip != againSkip || len(leaves) != len(again) {
			t.Fatalf("resolution of %q against %q is not deterministic: %d/%v then %d/%v",
				pointer, raw, len(leaves), skip, len(again), againSkip)
		}
		for i, leaf := range leaves {
			if again[i].Value != leaf.Value || again[i].Index != leaf.Index {
				t.Fatalf("leaf %d is not deterministic: %+v then %+v", i, leaf, again[i])
			}
			if leaf.Selector.String() != pointer {
				t.Fatalf("leaf %d of %q claims selector %q", i, pointer, leaf.Selector.String())
			}
			if leaf.Value != leafAt(t, document, selector.Tokens(), leaf.Index) {
				t.Fatalf("leaf %d = %q does not sit where %q names it in %q",
					i, leaf.Value, pointer, raw)
			}
		}
		if skip != pathvirtualization.SelectorSkipNone && len(leaves) != 0 {
			t.Fatalf("pointer %q refused with %v but still returned %+v", pointer, skip, leaves)
		}
		after, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}
		if string(before) != string(after) {
			t.Fatalf("resolving %q changed %q into %q", pointer, before, after)
		}
	})
}

// encodeTokens builds the one canonical spelling of a reference from its decoded
// tokens, escaping `~` before `/` so the result is the exact inverse of RFC 6901
// decoding. It is written independently of the parser so agreement between the
// two is evidence, not a restatement.
func encodeTokens(tokens []string) string {
	var pointer strings.Builder
	for _, token := range tokens {
		pointer.WriteByte('/')
		for _, c := range []byte(token) {
			switch c {
			case '~':
				pointer.WriteString("~0")
			case '/':
				pointer.WriteString("~1")
			default:
				pointer.WriteByte(c)
			}
		}
	}
	return pointer.String()
}

// arrayPosition reports whether a decoded token can address an element of an
// array of the given length: the RFC 6901 index grammar, which forbids a leading
// zero, a sign, and any other byte. It parses the digits through strconv so an
// unrepresentable index fails instead of wrapping, and it repeats the rule
// resolution applies so the fuzz proves each claim independently of the
// implementation it checks.
func arrayPosition(token string, length int) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	position, err := strconv.Atoi(token)
	if err != nil || position < 0 || position >= length {
		return 0, false
	}
	return position, true
}

// leafAt walks the same tokens selector.Resolve walks and returns the string a
// selected leaf claims, so the fuzz can prove the claim instead of trusting it.
func leafAt(t *testing.T, document any, tokens []string, index int) string {
	t.Helper()

	current := document
	for _, token := range tokens {
		switch container := current.(type) {
		case map[string]any:
			value, ok := container[token]
			if !ok {
				t.Fatalf("token %q does not resolve in %v", token, current)
			}
			current = value
		case []any:
			position, ok := arrayPosition(token, len(container))
			if !ok {
				t.Fatalf("token %q is not an array position in %v", token, current)
			}
			current = container[position]
		default:
			t.Fatalf("token %q is applied to %T", token, current)
		}
	}
	if index < 0 {
		value, ok := current.(string)
		if !ok {
			t.Fatalf("leaf claims a scalar string but the location holds %T", current)
		}
		return value
	}
	values, ok := current.([]any)
	if !ok {
		t.Fatalf("leaf claims position %d but the location holds %T", index, current)
	}
	if index >= len(values) {
		t.Fatalf("leaf claims position %d but the array holds %d", index, len(values))
	}
	value, ok := values[index].(string)
	if !ok {
		t.Fatalf("leaf claims position %d holds %T", index, values[index])
	}
	return value
}
