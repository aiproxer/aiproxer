package rewrite

// This file implements the payload half of the rewriter: it applies one compiled
// selector set to one canonical JSON payload and returns the replacement bytes.
//
// The engine is a byte splice, not a re-encode. A selected leaf is replaced in
// place, inside the exact bytes of its own JSON string literal, and every other byte
// of the payload is carried across untouched. That is what makes requirement 2.8
// hold literally: member order, number spelling, string escapes, whitespace, and
// empty-versus-null presence all survive, and a payload that was never selected is
// returned as the same bytes rather than a normalized equivalent. It also makes
// reapplication safe, because a payload that already carries the alias produces no
// match at all and therefore no bytes.
//
// Selection authority stays where the spec put it. The compiled selector set is
// resolved by the lexical core, which enforces the whole-document pointer refusal
// and the all-or-nothing string, string-array, object, and mixed-array rules, and
// this file never re-implements any of them. It only locates the leaves that core
// published and asks the mapping what each value means as a path locator.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// stringSpan is the exact byte range of one JSON string literal inside a payload,
// together with its decoded value.
type stringSpan struct {
	// start is the offset of the literal's opening quote.
	start int
	// end is the offset just past the literal's closing quote.
	end int
	// value is the literal's decoded string.
	value string
}

// rewriteDocument applies pointers to one canonical JSON payload.
//
// It returns the replacement bytes and true when at least one selected leaf changed,
// or nil and false when the payload must stay exactly as it arrived. Every refusal
// is a bounded reason recorded on acc, and the payload is left untouched in each of
// them: a payload this step cannot prove path-bearing is never partially rewritten.
//
// The engine itself is [ApplySelectedValues], shared with the inbound expansion
// pass so requirement 2.8's byte-for-byte property has exactly one implementation.
// This method supplies the OUTBOUND half of that contract: the mapping decides what
// a value means, the rewriter's own closed reason vocabulary records every refusal,
// and the counters are folded into the walk's accounting.
//
// The only error it returns is a disagreement between two decoders over bytes that
// already decoded as one valid JSON value, which untrusted input cannot produce. It
// is reported so a caller can fail open with real paths instead of publishing a
// half-rewritten payload.
func (r *Rewriter) rewriteDocument(raw []byte, pointers pathvirtualization.SelectorSet, acc *account) ([]byte, bool, error) {
	published, pass, err := ApplySelectedValues(raw, pointers, virtualizeDecider(r.mapping))
	if err != nil {
		return nil, false, err
	}
	acc.record(pass)
	return published, pass.Changed, nil
}

// decodePayload decodes one complete JSON value, preserving number literals.
//
// Numbers are decoded as literals rather than as float64 so a payload that is
// entirely valid JSON, including a magnitude beyond float64, is still a readable
// document instead of an error. That matters because this step refuses a payload it
// cannot read, and a value like 1e400 must not be refused for being out of range
// when no selected leaf refers to it.
func decodePayload(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	// One complete value is the whole payload. A second value, or any trailing
	// bytes, means the payload is not one argument document, and reading it whole
	// would mean rewriting a fragment of something else.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("payload holds more than one JSON value")
		}
		return nil, err
	}
	return document, nil
}

// isAbsentOrNullPayload reports whether a payload field carries no value at all,
// which is either an empty field or the JSON null literal.
//
// The comparison is byte-exact and never trims, so a quoted "null", a padded null
// literal, and an absent field stay three distinguishable states. The feature cannot
// import internal/core/jsonpresence, which is where the runtime keeps this
// predicate for canonical paths, so the rule is implemented here in the two bytes it
// actually needs.
func isAbsentOrNullPayload(raw []byte) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// selectedLeafKeys returns the canonical pointer text of every published leaf.
//
// A leaf that resolved to one string is keyed by its pointer; a leaf inside an
// array-of-string target is keyed by its own element pointer, so a location selected
// twice through two configured pointers still contributes exactly one span and one
// count.
func selectedLeafKeys(leaves []pathvirtualization.Leaf) map[string]struct{} {
	keys := make(map[string]struct{}, len(leaves))
	for _, leaf := range leaves {
		key := leaf.Selector.String()
		if leaf.Index != pathvirtualization.LeafIndexSingle {
			// strconv renders the canonical array index the same way the walk below
			// does, so the two spellings always agree.
			key += "/" + strconv.Itoa(leaf.Index)
		}
		keys[key] = struct{}{}
	}
	return keys
}

// maxSelectableLeafDepth is the deepest reference-token count at which a selected
// string literal can sit.
//
// It is DERIVED from the selector layer's own bound rather than chosen here, and the
// derivation is two steps because a leaf is one token below the pointer that chose it
// only in the array case. A compiled pointer holds at most
// [pathvirtualization.MaxPointerDepth] tokens and names the selected LOCATION; a
// location that resolves to an array of strings contributes one leaf per element,
// and every such element sits one reference token below the pointer. So the deepest
// selectable leaf is MaxPointerDepth+1 tokens deep, and no literal below that depth
// can carry a key of the selected set. Off-by-one here would silently stop visiting a
// real selected leaf, so the boundary is asserted from the selector layer's own
// constants in TestSelectedLeavesAreVisitedAtEverySelectableDepth.
const maxSelectableLeafDepth = pathvirtualization.MaxPointerDepth + 1

// spanFrame is one open container while the payload is walked.
type spanFrame struct {
	// path is the canonical pointer text of this container, or the empty string
	// once this container sits below [maxSelectableLeafDepth]. See childPointerText
	// for why no leaf below that depth can be selected.
	path string
	// depth is this container's own reference-token count: 0 for the document root,
	// one more than its parent's for every container below it.
	depth int
	// index is the next array element position, for an array container.
	index int
	// object reports whether members are named rather than positional.
	object bool
	// expectKey reports whether the next token is a member name.
	expectKey bool
	// pending is the member name whose value is the current token.
	pending string
}

// findSelectedStringSpans returns the byte range of every string literal whose
// canonical pointer text is one of keys.
//
// The walk reads the payload with the same token grammar that decoded it, so the
// two passes cannot disagree about structure, and it reconstructs each value's
// location from the token stream instead of from a re-encoded document. Offsets come
// from the decoder itself, so a literal is located exactly, whatever whitespace,
// member order, or escaping the client used.
func findSelectedStringSpans(document []byte, keys map[string]struct{}) ([]stringSpan, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()

	var (
		spans    []stringSpan
		frames   []spanFrame
		rootSeen bool
	)
	for {
		previous := int(decoder.InputOffset())
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// The decoder's offset advances past each token it returns, so the bytes
		// between the previous token and this one are only separators and skipping
		// them lands exactly on this token's first byte. Offsets are narrowed to int
		// for byte indexing; the canonical size bounds keep every payload well inside
		// the range of a platform's int, so the narrowing cannot wrap.
		start := tokenStart(document, previous)
		end := int(decoder.InputOffset())

		if len(frames) == 0 {
			if rootSeen {
				// A second top-level value is not one payload, and the completeness
				// check that guards against it lives in decodePayload.
				return nil, errors.New("payload holds more than one JSON value")
			}
			rootSeen = true
			if delim, isDelim := token.(json.Delim); isDelim {
				if delim == '{' || delim == '[' {
					frames = append(frames, newSpanFrame("", 0, delim == '{'))
				}
			}
			// A top-level scalar names the whole document, which no compiled pointer
			// can address, so there is nothing to record.
			continue
		}

		top := &frames[len(frames)-1]
		if delim, isDelim := token.(json.Delim); isDelim && (delim == '}' || delim == ']') {
			// Closing a container completes the value the parent already accounted
			// for when it was opened, so the parent's position does not advance here.
			frames = frames[:len(frames)-1]
			continue
		}
		if top.expectKey {
			name, isString := token.(string)
			if !isString {
				return nil, errors.New("object member name is not a string")
			}
			top.expectKey = false
			top.pending = name
			continue
		}

		child, selectable := childPointerText(top)
		if top.object {
			// This member's value is complete, so the container expects the next
			// member name rather than another value.
			top.expectKey = true
		}
		if delim, isDelim := token.(json.Delim); isDelim {
			frames = append(frames, newSpanFrame(child, top.depth+1, delim == '{'))
			continue
		}
		if value, isString := token.(string); isString && selectable {
			if _, selected := keys[child]; selected {
				spans = append(spans, stringSpan{start: start, end: end, value: value})
			}
		}
	}
	if len(frames) != 0 {
		return nil, errors.New("payload ends inside an open container")
	}
	return spans, nil
}

// newSpanFrame opens one container at depth reference tokens below the document root.
//
// The depth is passed rather than derived from the frame stack because a frame is a
// value in a slice whose earlier entries may be reallocated: the depth is the one
// fact that decides whether this container's pointer text is ever needed, so it must
// travel with the frame rather than be looked up again.
func newSpanFrame(path string, depth int, object bool) spanFrame {
	return spanFrame{path: path, depth: depth, object: object, expectKey: object}
}

// childPointerText returns the canonical pointer text of the value the innermost
// container is about to read, together with whether any compiled pointer can name that
// value at all.
//
// The container's own accounting always advances - an array position moves on and an
// object member name is consumed - because that is what keeps the walk's view of the
// payload correct regardless of which values are selectable.
//
// The pointer TEXT is built only at a depth a selection can reach, and that is the
// whole cost property of this walk. The text of a container at depth d names a leaf at
// depth d, and no compiled pointer reaches past maxSelectableLeafDepth, so for a deeper
// value the text cannot equal any key in the selected set. Rebuilding it anyway would
// be quadratic in nesting depth: a payload that nests one unselected container inside
// the next would allocate and RETAIN a string per level, each as long as every
// reference token above it, so a valid document well inside the argument bound could
// cost hundreds of megabytes and grow faster than the document it came from. Skipping
// the text below the selectable depth leaves the answer provably identical - the same
// keys, the same literals, the same byte ranges, in the same order - because no key
// that could match is ever dropped from consideration.
//
// This is a depth bound, not a depth shortcut: every depth a pointer can name, up to
// and including maxSelectableLeafDepth, is still materialised and still matched.
func childPointerText(frame *spanFrame) (string, bool) {
	if frame.depth >= maxSelectableLeafDepth {
		advanceSpanFrame(frame)
		return "", false
	}
	return frame.path + "/" + advanceSpanFrame(frame), true
}

// advanceSpanFrame returns the reference token of the value the innermost container is
// about to read and advances an array container past the position it just named.
//
// It is the unconditional half of childPointerText: the reference itself is one member
// name or one array index, so producing it costs nothing that grows with the depth
// above it.
func advanceSpanFrame(frame *spanFrame) string {
	if frame.object {
		name := canonicalPointerText(frame.pending)
		frame.pending = ""
		return name
	}
	reference := strconv.Itoa(frame.index)
	frame.index++
	return reference
}

// tokenStart returns the offset of the first byte of the token that follows the one
// ending at from, by stepping over the structural bytes that may separate them.
//
// Only whitespace and the two structural separators can appear there, and no value
// token begins with one of those bytes, so the scan can never step past the token it
// is looking for. The offset is clamped into the payload, which keeps the helper
// total even if it is ever handed an offset from beyond the end.
func tokenStart(document []byte, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(document) {
		from = len(document)
	}
	for from < len(document) && isJSONSeparator(document[from]) {
		from++
	}
	return from
}

// isJSONSeparator reports whether a byte can appear between two tokens of a payload.
func isJSONSeparator(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', ':', ',':
		return true
	default:
		return false
	}
}

// pointerEscapeChars are the two characters RFC 6901 escapes inside a reference
// token. A member name holding either is spelled escaped, which is what makes a
// walked path and a compiled pointer the same key.
const pointerEscapeChars = "~/"

// canonicalPointerText encodes one member name as its RFC 6901 reference token.
//
// The encoding is the same one ParseSelector decodes, so a walked location and a
// configured pointer are compared as identical canonical text and two different
// member names can never collide on one key.
func canonicalPointerText(name string) string {
	if !strings.ContainsAny(name, pointerEscapeChars) {
		return name
	}
	var encoded strings.Builder
	encoded.Grow(len(name) + 4)
	for i := range len(name) {
		switch name[i] {
		case '~':
			encoded.WriteString("~0")
		case '/':
			encoded.WriteString("~1")
		default:
			encoded.WriteByte(name[i])
		}
	}
	return encoded.String()
}

// splice replaces the literals the spans cover and returns the published payload.
//
// The engine moved to [ApplySelectedValues] when the inbound expansion pass needed
// the identical splice, so the byte-level work - ascending offset order, verbatim
// copy of everything between spans, refusal publication - is implemented once and
// asserted for both directions by that primitive's tests. A span the decision does
// not accept is left in place, which is how a payload that merely mentions the
// project root outside the project root keeps its bytes, and how a selected array can
// hold a mixture of aliased and untouched values without losing either.

// encodeSelectedValue renders one selected value as a JSON string literal.
//
// The value is the only thing this step ever re-spells, and it is re-spelled as one
// complete literal, so the payload stays a single valid document whatever bytes the
// client's path carried.
func encodeSelectedValue(value string) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
