package pathvirtualization

import (
	"strconv"
	"strings"
)

// This file implements the explicit half of design.md 197-229: validated JSON
// Pointer selectors for path-bearing tool-call arguments and structured tool
// results, and the bounded limits that keep selector work finite.
//
// Explicit selectors are operator configuration, so they are validated once at
// configuration compile time and never again per request. The accepted dialect
// is the plain RFC 6901 string form with exactly one spelling per pointer: a
// pointer is absolute, its `~` escapes are complete, its tokens are non-empty, and
// it addresses an existing location by byte-exact reference name. Every rejected
// spelling returns its own bounded reason instead of being resolved by a guess.
//
// Resolution is deliberately shape-restricted. A selected location contributes
// leaves only when it is a string or an array whose every element is a string.
// An object, number, boolean, null, mixed array, or absent location is skipped
// with a bounded reason and yields nothing, so no arbitrary JSON string can ever
// be rewritten as if it were a filesystem locator (requirements 2.3, 3.1, 3.2,
// 3.5).
//
// Schema-assisted inference and profile precedence are separate layers: this file
// knows only about the selectors an operator spelled out, and it bounds the path-key
// vocabulary the inference step consumes.

// Selector bounds. They are fixed implementation contracts, not operator-tunable
// values: a configuration beyond a bound is refused rather than truncated, so
// every compiled selector set is finite and its per-document cost is bounded
// (requirement 7.9).
const (
	// MaxProfiles bounds how many operator tool profiles one configuration may
	// declare. A profile is an explicit allowlist entry for one tool surface, and
	// the bounded count keeps the exact-name lookup table and every later
	// per-profile traversal finite. 256 is far above the realistic number of
	// distinct tools in one agent surface while still bounding the table.
	MaxProfiles = 256
	// MaxPointersPerProfile bounds the pointers one profile may declare across
	// its argument and result lists together. Path-bearing surfaces name a
	// handful of locations, so 64 leaves room for a wide tool while keeping one
	// document's selector cost bounded by MaxPointersPerProfile*MaxPointerDepth.
	MaxPointersPerProfile = 64
	// MaxPointerDepth bounds the number of reference tokens one pointer may
	// contain. A selected argument location is a field of one tool payload, not a
	// deep document walk, so 16 levels is generous and keeps a single pointer's
	// traversal cost bounded regardless of how deep a payload happens to be.
	MaxPointerDepth = 16
	// MaxPathKeys bounds the schema path-key vocabulary that the schema-assisted
	// inference step may compare declared property names against. It is declared
	// here with the other selector limits because it is the same kind of
	// fail-closed configuration bound; the vocabulary contents and inference
	// policy themselves belong to that later task.
	MaxPathKeys = 64
)

// SelectorReject is the bounded, content-free reason a selector configuration
// was refused. It is an enum because the value only ever reaches fixed-count
// dimensions: it never carries pointer bytes, tool names, or payload text.
//
// The whole set is terminal. A refused configuration publishes no profile at all,
// so a partially valid selector set can never reach a request.
type SelectorReject uint8

const (
	// SelectorRejectNone marks an accepted selector configuration.
	SelectorRejectNone SelectorReject = iota
	// SelectorRejectEmptyPointer marks the empty pointer, which RFC 6901 defines
	// as a reference to the whole document. A path-bearing selector must name one
	// field, because selecting an entire argument or result payload would be the
	// arbitrary-string rewrite this feature must never perform.
	SelectorRejectEmptyPointer
	// SelectorRejectNotAbsolutePointer marks a pointer that does not begin with
	// the `/` anchor, so it names no location at all.
	SelectorRejectNotAbsolutePointer
	// SelectorRejectURIFragmentPointer marks the RFC 6901 URI-fragment spelling
	// (`#/a/b`). The fragment form is refused rather than decoded: it would add a
	// second accepted spelling of every pointer plus a percent-decoding step, and
	// an operator can always spell the plain form instead.
	SelectorRejectURIFragmentPointer
	// SelectorRejectInvalidEscape marks a `~` that is not followed by `0` or `1`.
	// Such a token has no single decoded meaning, so it is refused instead of
	// being read as a literal `~`.
	SelectorRejectInvalidEscape
	// SelectorRejectEmptyToken marks a pointer holding an empty reference token.
	// This also refuses the trailing-slash spelling `/a/`, whose trailing empty
	// token is the one place where two obviously intended spellings of a selector
	// could otherwise disagree about whether they name the same location.
	SelectorRejectEmptyToken
	// SelectorRejectArrayEndToken marks a bare `-` token. RFC 6901 defines it as
	// the position after the last array element, which is not an existing
	// location, so it can never address a path-bearing leaf.
	SelectorRejectArrayEndToken
	// SelectorRejectPointerDepth marks a pointer deeper than MaxPointerDepth.
	SelectorRejectPointerDepth
	// SelectorRejectDuplicatePointer marks one pointer declared twice inside the
	// same selector list, which would make a configured location ambiguous.
	SelectorRejectDuplicatePointer
	// SelectorRejectProfileCount marks more profiles than MaxProfiles.
	SelectorRejectProfileCount
	// SelectorRejectPointerCount marks more pointers in one profile than
	// MaxPointersPerProfile.
	SelectorRejectPointerCount
	// SelectorRejectEmptyToolName marks a profile that declares no tool name, or
	// an empty one. Selection is exact-name authority, so a nameless profile
	// could only match everything or nothing, and neither is a safe reading of an
	// operator's intent.
	SelectorRejectEmptyToolName
	// SelectorRejectDuplicateToolName marks two profiles claiming the same exact
	// tool name, or one profile listing the same name twice. Conflicting exact
	// profiles are ambiguous and are refused instead of resolved by precedence.
	SelectorRejectDuplicateToolName
	// SelectorRejectPathKeyCount marks more path keys than MaxPathKeys.
	SelectorRejectPathKeyCount
	// SelectorRejectEmptyPathKey marks an empty entry in the path-key vocabulary.
	SelectorRejectEmptyPathKey
	// SelectorRejectDuplicatePathKey marks one vocabulary key declared twice.
	SelectorRejectDuplicatePathKey
	// SelectorRejectOpaqueMode marks an opaque-result mode outside the closed set.
	// It is refused rather than read as the disabled default, because defaulting an
	// unrecognized mode to "off" would let a typo hide a mode an operator meant to
	// enable, while treating it as "on" would let a typo widen rewriting.
	SelectorRejectOpaqueMode
)

// String returns the fixed, low-cardinality label for a configuration rejection.
// It is safe for content-free observability dimensions: it never contains
// pointer, tool-name, or payload bytes.
func (r SelectorReject) String() string {
	switch r {
	case SelectorRejectNone:
		return ""
	case SelectorRejectEmptyPointer:
		return "empty_pointer"
	case SelectorRejectNotAbsolutePointer:
		return "not_absolute_pointer"
	case SelectorRejectURIFragmentPointer:
		return "uri_fragment_pointer"
	case SelectorRejectInvalidEscape:
		return "invalid_escape"
	case SelectorRejectEmptyToken:
		return "empty_token"
	case SelectorRejectArrayEndToken:
		return "array_end_token"
	case SelectorRejectPointerDepth:
		return "pointer_depth"
	case SelectorRejectDuplicatePointer:
		return "duplicate_pointer"
	case SelectorRejectProfileCount:
		return "profile_count"
	case SelectorRejectPointerCount:
		return "pointer_count"
	case SelectorRejectEmptyToolName:
		return "empty_tool_name"
	case SelectorRejectDuplicateToolName:
		return "duplicate_tool_name"
	case SelectorRejectPathKeyCount:
		return "path_key_count"
	case SelectorRejectEmptyPathKey:
		return "empty_path_key"
	case SelectorRejectDuplicatePathKey:
		return "duplicate_path_key"
	case SelectorRejectOpaqueMode:
		return "opaque_mode"
	default:
		return "unknown"
	}
}

// SelectorSkip is the bounded, content-free reason one selected location
// contributed no rewritable leaf. It is the resolution-time counterpart of
// SelectorReject and carries the same guarantee: every unusable location is
// skipped and reported, never partially rewritten (requirement 3.5).
type SelectorSkip uint8

const (
	// SelectorSkipNone marks a location that resolved and contributed leaves.
	SelectorSkipNone SelectorSkip = iota
	// SelectorSkipUnresolved marks a pointer that names no location in this
	// document: an absent member or array position, a token that is not a valid
	// array index, or a token applied below a scalar. An unknown or ambiguous
	// location is skipped, never guessed at.
	SelectorSkipUnresolved
	// SelectorSkipNotString marks a selected value that is not a string and not
	// an array of strings: a number, boolean, null, or any other JSON scalar.
	SelectorSkipNotString
	// SelectorSkipObject marks a selected object. Its own nested strings are not
	// selected, because descending into arbitrary structure is the recursive
	// rewrite requirement 2.3 forbids.
	SelectorSkipObject
	// SelectorSkipNotStringArray marks an array holding at least one element that
	// is not a string. The whole array is skipped rather than partially selected,
	// so a rewritten payload can never keep an unrewritten path element beside a
	// rewritten one.
	SelectorSkipNotStringArray
)

// String returns the fixed, low-cardinality label for a resolution skip. It is
// safe for content-free observability dimensions: it never contains pointer,
// path, or payload bytes.
func (s SelectorSkip) String() string {
	switch s {
	case SelectorSkipNone:
		return ""
	case SelectorSkipUnresolved:
		return "selector_unresolved"
	case SelectorSkipNotString:
		return "selector_not_string"
	case SelectorSkipObject:
		return "selector_object"
	case SelectorSkipNotStringArray:
		return "selector_not_string_array"
	default:
		return "unknown"
	}
}

const (
	// pointerAnchor is the mandatory first byte of an absolute JSON Pointer.
	pointerAnchor = '/'
	// fragmentAnchor introduces the URI-fragment spelling, which is refused
	// whole rather than percent-decoded.
	fragmentAnchor = '#'
	// escapeTilde and escapeSlash are the only two escapes RFC 6901 defines, and
	// escapePrefix introduces both of them.
	escapeTilde  = '0'
	escapeSlash  = '1'
	escapePrefix = '~'
	// arrayEndToken is RFC 6901's position after the last array element.
	arrayEndToken = "-"
)

// LeafIndexSingle is the Leaf.Index value for a selector that resolved to one
// string. An array-of-string target reports its own element positions instead, so
// a caller can address exactly which element it rewrote.
const LeafIndexSingle = -1

// Selector is one validated JSON Pointer: the immutable pair of its canonical
// spelling and its decoded reference tokens.
//
// Both representations are equivalent, and the canonical spelling is the single
// accepted form of that pointer. Tokens are decoded reference names compared
// byte-exactly against decoded JSON member names: no case folding, trimming, or
// Unicode normalization applies, matching how this package treats every other
// untrusted byte.
type Selector struct {
	// text is the canonical spelling, always beginning with the pointer anchor.
	text string
	// tokens are the decoded reference names, in document order. A compiled
	// selector holds at least one token and never more than MaxPointerDepth; the
	// zero value holds none and therefore selects nothing.
	tokens []string
}

// ParseSelector validates and canonicalizes one JSON Pointer.
//
// It accepts the plain RFC 6901 string form only: an anchored pointer whose `~`
// escapes are complete and whose tokens are non-empty. Every other spelling,
// including the empty whole-document pointer, the URI-fragment form, and a
// pointer deeper than MaxPointerDepth, is refused with its own bounded reason so
// an operator's typo can never be resolved by a guess.
//
// Parsing is pure and host-independent: it inspects the pointer's own bytes only.
func ParseSelector(pointer string) (Selector, SelectorReject) {
	switch {
	case pointer == "":
		// RFC 6901's empty pointer is a reference to the whole document, not to a
		// path-bearing field, so it is refused instead of selected.
		return Selector{}, SelectorRejectEmptyPointer
	case pointer[0] == fragmentAnchor:
		// The URI-fragment form would be a second spelling of every pointer and
		// would require percent-decoding operator bytes to compare them.
		return Selector{}, SelectorRejectURIFragmentPointer
	case pointer[0] != pointerAnchor:
		return Selector{}, SelectorRejectNotAbsolutePointer
	}
	tokens, reject := decodeTokens(pointer[1:])
	if reject != SelectorRejectNone {
		return Selector{}, reject
	}
	// The accepted spelling is already canonical: RFC 6901 decoding is injective
	// on canonical spellings, because `~` and `/` each have exactly one escaped
	// form, so no accepted pointer has a second spelling that would need to be
	// collapsed here.
	return Selector{text: pointer, tokens: tokens}, SelectorRejectNone
}

// decodeTokens splits the anchored remainder of a pointer into decoded reference
// names, refusing every non-canonical token and every token count past the depth
// bound as it goes, so a hostile pointer cannot make parsing walk an unbounded
// number of tokens.
func decodeTokens(body string) ([]string, SelectorReject) {
	tokens := make([]string, 0, min(len(body), MaxPointerDepth))
	for {
		token := body
		more := false
		if cut := indexPointerSeparator(body); cut >= 0 {
			token, body, more = body[:cut], body[cut+1:], true
		}
		if len(tokens) >= MaxPointerDepth {
			return nil, SelectorRejectPointerDepth
		}
		decoded, reject := decodeToken(token)
		if reject != SelectorRejectNone {
			return nil, reject
		}
		tokens = append(tokens, decoded)
		if !more {
			return tokens, SelectorRejectNone
		}
	}
}

// decodeToken unescapes one reference token, refusing an empty token, the array
// end token, and any `~` that does not introduce one of the two RFC 6901 escapes.
func decodeToken(token string) (string, SelectorReject) {
	switch token {
	case "":
		return "", SelectorRejectEmptyToken
	case arrayEndToken:
		// The array end token names a position that does not exist yet, so it can
		// never address a value this feature could rewrite.
		return "", SelectorRejectArrayEndToken
	}
	if !strings.ContainsRune(token, escapePrefix) {
		return token, SelectorRejectNone
	}
	decoded := make([]byte, 0, len(token))
	for i := 0; i < len(token); i++ {
		if token[i] != escapePrefix {
			decoded = append(decoded, token[i])
			continue
		}
		if i+1 >= len(token) {
			// A trailing `~` has no escaped byte behind it.
			return "", SelectorRejectInvalidEscape
		}
		switch token[i+1] {
		case escapeTilde:
			decoded = append(decoded, escapePrefix)
		case escapeSlash:
			decoded = append(decoded, pointerAnchor)
		default:
			// Any other byte after `~` is not an escape this dialect defines, so the
			// token has no single decoded meaning.
			return "", SelectorRejectInvalidEscape
		}
		i++
	}
	return string(decoded), SelectorRejectNone
}

// String returns the canonical spelling of the selector, which is the accepted
// form of the pointer it was compiled from.
func (s Selector) String() string { return s.text }

// Tokens returns a copy of the decoded reference names. Returning a copy keeps a
// compiled selector immutable, so a caller cannot corrupt another caller's
// resolution by editing the result.
func (s Selector) Tokens() []string {
	if len(s.tokens) == 0 {
		return nil
	}
	return append([]string(nil), s.tokens...)
}

// Resolve returns the string leaves one pointer selects from a decoded JSON
// document.
//
// It reports SelectorSkipNone together with the selected leaves, or one bounded
// skip reason together with no leaf at all. There is no partial outcome: a
// location that is not a string or an array of strings is skipped whole, so a
// caller can never rewrite half of an array.
//
// Resolution reads the document only. It performs no filesystem access, consults
// no host path semantics, and returns the selected strings by value, so the
// source document cannot be changed through the result. A document is walked at
// most MaxPointerDepth containers deep, which is what bounds selector cost per
// document (requirement 7.9).
func (s Selector) Resolve(document any) ([]Leaf, SelectorSkip) {
	if len(s.tokens) == 0 {
		// The zero value is the state of a refused pointer, and a pointer with no
		// tokens would address a whole document. Selecting a document root would
		// turn one invalid selector into the arbitrary rewrite this feature must
		// never perform, so a selector without tokens selects nothing.
		return nil, SelectorSkipUnresolved
	}
	current := document
	for _, token := range s.tokens {
		next, ok := resolveToken(token, current)
		if !ok {
			// An absent member, an out-of-range or non-index position, or a token
			// below a scalar all mean the same thing: this location does not exist
			// in this document, so nothing is selected and nothing is guessed.
			return nil, SelectorSkipUnresolved
		}
		current = next
	}
	return selectedLeaves(s, current)
}

// resolveToken descends one reference token into a decoded JSON value.
//
// A decoded document is `map[string]any`, `[]any`, `string`, `float64`, `bool`,
// or `nil`. A typed `[]string` is also accepted because an array of strings is
// already the array-of-strings shape by construction.
func resolveToken(token string, current any) (any, bool) {
	switch node := current.(type) {
	case map[string]any:
		// Member lookup is byte-exact, matching how the tokens were decoded.
		next, ok := node[token]
		return next, ok
	case []any:
		position, ok := arrayPosition(token, len(node))
		if !ok {
			return nil, false
		}
		return node[position], true
	case []string:
		position, ok := arrayPosition(token, len(node))
		if !ok {
			return nil, false
		}
		return node[position], true
	default:
		return nil, false
	}
}

// arrayPosition reports whether a decoded token can address an element of an
// array of the given length. RFC 6901's index grammar admits a run of digits with
// no leading zero and no sign, so `-`, `+1`, `01`, and `1.0` name no array
// position.
//
// The value is parsed as unsigned and compared against the element count before
// it is narrowed, so a long digit run cannot overflow into a valid-looking index
// on any platform. The leading-zero rule is separate because the parser itself
// would accept a padded run, and a padded run is not a legal index even though it
// denotes the same number.
func arrayPosition(token string, length int) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	position, err := strconv.ParseUint(token, 10, 64)
	if err != nil || position >= uint64(length) {
		// An unrepresentable or out-of-range position addresses nothing.
		return 0, false
	}
	return int(position), true
}

// selectedLeaves classifies a fully resolved location and returns its leaves.
//
// Only a string and an array whose every element is a string are selectable. An
// object is skipped because its nested strings are not the configured location,
// and a mixed array is skipped whole because rewriting only its string elements
// would leave the payload internally inconsistent.
func selectedLeaves(selector Selector, value any) ([]Leaf, SelectorSkip) {
	switch selected := value.(type) {
	case string:
		return []Leaf{{Selector: selector, Index: LeafIndexSingle, Value: selected}}, SelectorSkipNone
	case []string:
		// A typed string array is array-of-strings by construction.
		leaves := make([]Leaf, len(selected))
		for i, element := range selected {
			leaves[i] = Leaf{Selector: selector, Index: i, Value: element}
		}
		return leaves, SelectorSkipNone
	case []any:
		leaves := make([]Leaf, len(selected))
		for i, element := range selected {
			text, ok := element.(string)
			if !ok {
				// One non-string element refuses every element of this pointer.
				return nil, SelectorSkipNotStringArray
			}
			leaves[i] = Leaf{Selector: selector, Index: i, Value: text}
		}
		return leaves, SelectorSkipNone
	case map[string]any:
		return nil, SelectorSkipObject
	default:
		// Numbers, booleans, null, and any non-string JSON number representation.
		return nil, SelectorSkipNotString
	}
}

// Leaf is one selected string leaf: the value itself, the element position it
// held in an array-of-string target (LeafIndexSingle for a single string), and the
// selector that chose it.
//
// A leaf carries no pointer text beyond that selector and no path interpretation:
// what a value means as a filesystem locator is the caller's decision, made by
// asking the mapper about it.
type Leaf struct {
	// Selector is the compiled pointer that selected this leaf.
	Selector Selector
	// Index is the element position inside an array-of-string target, or
	// LeafIndexSingle when the pointer resolved to one string.
	Index int
	// Value is the selected string exactly as decoded. No separator, case, or
	// Unicode normalization is applied to it.
	Value string
}

// SkippedSelector records that one pointer resolved to nothing rewritable, with
// the bounded reason why.
type SkippedSelector struct {
	// Selector is the compiled pointer that was skipped.
	Selector Selector
	// Reason is the bounded, content-free reason no leaf was selected.
	Reason SelectorSkip
}

// Selection is the outcome of resolving a selector set against one decoded
// document. Skipped holds at most one entry per pointer in the set, and Leaves
// holds at most the elements of the arrays those pointers selected, so the result
// is bounded by the compiled selectors and the lengths of the arrays they name
// rather than by the document as a whole (requirement 7.9).
type Selection struct {
	// Leaves are the selected string leaves, ordered by pointer and then by array
	// position.
	Leaves []Leaf
	// Skipped are the pointers that contributed nothing, ordered as configured.
	Skipped []SkippedSelector
}

// SelectorSet is an ordered set of compiled pointers resolved against one
// document. It is the compiled form of one profile's argument or result pointer
// list, and it is a value type holding no mutable state.
type SelectorSet []Selector

// Resolve returns the leaves and skips of every pointer in the set, in
// configuration order. One skipped pointer never suppresses another: each is
// reported separately so the caller can account for it by bounded reason.
func (s SelectorSet) Resolve(document any) Selection {
	var selection Selection
	for _, selector := range s {
		leaves, skip := selector.Resolve(document)
		if skip != SelectorSkipNone {
			selection.Skipped = append(selection.Skipped, SkippedSelector{Selector: selector, Reason: skip})
			continue
		}
		selection.Leaves = append(selection.Leaves, leaves...)
	}
	return selection
}

// indexPointerSeparator returns the index of the next reference separator in s,
// or -1 when the final token is being read.
func indexPointerSeparator(s string) int {
	return strings.IndexByte(s, pointerAnchor)
}
