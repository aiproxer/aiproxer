package schemainfer

import "strings"

// This file owns the two fixed key lists that decide what a declared property name
// is allowed to mean: the bounded path-key vocabulary a declared name must match
// to become a candidate at all, and the payload-concept denylist that closes a
// declared name or description against ever being one.
//
// The two lists answer different questions and have different authority. The
// vocabulary is the operator-facing half of design.md 218-219: an operator may
// widen it through typed feature configuration, and the shared selector bound
// (MaxPathKeys) caps how far. The denylist is the safety half of design.md
// 221-222 plus requirement 2.4's source-content clause: it is a fixed
// implementation contract, because a payload concept must stay unpaintable by
// configuration. Neither list can contain a name the other contains, so a declared
// name can never be both an accepted path key and a refused payload concept.

// defaultPathKeys is the conservative V1 path-key vocabulary, in the exact order
// design.md 218-219 lists it.
//
// Every entry names a declared property that means one filesystem locator: a file,
// a directory, a working directory, a project root, or a homogeneous list of them.
// `root` is included because a project-root argument is a path-bearing surface
// like any other, and it is the one entry whose real-root shortening matters most.
// The list is deliberately short: a longer list would increase the chance that a
// non-locator argument is inferred, and requirement 3.3 permits a wider
// operator-supplied vocabulary rather than a wider built-in guess.
var defaultPathKeys = []string{
	"path",
	"file_path",
	"filepath",
	"directory",
	"dir",
	"cwd",
	"workdir",
	"root",
	"target_path",
	"paths",
}

// payloadConceptKeys is the payload-concept denylist, in the exact order design.md
// 221-222 lists it, followed by the two source-content names requirement 2.4 names
// when it forbids rewriting "source-code" arguments.
//
// A declared name or declared description naming one of these is a payload, not a
// locator: the bytes hold source text, a patch, a command line, or a search
// expression, and a prefix substitution inside them would corrupt content rather
// than shorten a path. The two added names are justified by requirement 2.4
// directly, and adding a name to this list can only ever block inference, never
// create it, so the list can be extended without any new inference risk.
var payloadConceptKeys = []string{
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

// payloadConcepts is the compiled denylist: every key above in its normalized
// spelling. A declared name or description word matches the denylist only through
// this set, so the comparison can never grow with attacker-supplied bytes.
var payloadConcepts = normalizeSet(payloadConceptKeys)

// DefaultPathKeys returns the fixed V1 path-key vocabulary in the order design.md
// 218-219 lists it.
//
// The returned slice is a fresh copy, so a caller may extend its own configuration
// without being able to mutate the built-in policy. An extended vocabulary only
// ever widens what may be inferred; it can never narrow the payload denylist.
func DefaultPathKeys() []string {
	return append([]string(nil), defaultPathKeys...)
}

// PayloadConceptKeys returns the payload-concept denylist in the order design.md
// 221-222 lists it, followed by the requirement 2.4 source-content names.
//
// The returned slice is a fresh copy for the same reason as DefaultPathKeys: the
// denylist is a fixed safety contract and must not be reachable as caller-owned
// state.
func PayloadConceptKeys() []string {
	return append([]string(nil), payloadConceptKeys...)
}

// NormalizeKey reduces a declared property name to the spelling both fixed key
// lists are compared in.
//
// The reduction is deliberately narrow and byte-oriented, because it is applied to
// untrusted schema bytes and decides whether a name is a locator:
//
//   - leading and trailing ASCII space is trimmed;
//   - ASCII letters fold to lower case, so `Path`, `PATH`, and `filePath` reach the
//     same key as `path` and `filepath` without any word-splitting heuristic;
//   - every other ASCII byte becomes `_`, so `-`, `.`, and internal spaces reach the
//     same key as the underscore spelling;
//   - every byte at or above 0x80 is passed through unchanged, so no Unicode case
//     folding, whitespace trimming, or confusable mapping can make a non-ASCII name
//     match an ASCII key.
//
// The result is a comparison key only. A published pointer always addresses the
// declared name exactly as the schema spelled it, so normalization can never move a
// selector to a different JSON member.
func NormalizeKey(name string) string {
	return normalize(name, true)
}

// isPayloadConcept reports whether a declared name is one of the fixed payload
// concepts, in its normalized spelling.
func isPayloadConcept(name string) bool {
	_, listed := payloadConcepts[normalize(name, false)]
	return listed
}

// describesPayloadConcept reports whether a declared description is prose about
// one of the fixed payload concepts.
//
// Requirement 3.4 refuses a property "named or described as" a payload concept.
// A description can only ever suppress inference, so the check is a bounded
// single pass over the declared prose: it extracts words and tests them against the
// fixed denylist, which is what lets a `path` argument whose description says it
// holds file content stay unwritten. No description word can ever create a
// candidate, because vocabulary membership is decided from the declared name alone.
func describesPayloadConcept(description string) bool {
	for word := range words(description) {
		if _, listed := payloadConcepts[word]; listed {
			return true
		}
	}
	return false
}

// normalizeSet compiles a key list into a lookup set of normalized names.
func normalizeSet(keys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[normalize(key, false)] = struct{}{}
	}
	return set
}

// normalize implements the NormalizeKey rule. Trimming is offered separately
// because the fixed lists are already canonical spellings and must not be
// rewritten by their own normalization step, while a declared name may carry
// surrounding spaces.
func normalize(name string, trim bool) string {
	if trim {
		name = strings.Trim(name, " ")
	}
	if isNormalized(name) {
		// Most declared names, and every key of both fixed lists, are already in the
		// normalized spelling. Recognizing that costs one pass and saves the
		// allocation, which matters because this runs once per declared property.
		return name
	}
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == normalizedSeparator:
			b.WriteByte(c)
		case c < utf8ContinuationLimit:
			// Every remaining ASCII byte is a word separator, so `file-path`,
			// `file.path`, and `file path` all reach the underscore spelling.
			b.WriteByte(normalizedSeparator)
		default:
			// A byte at or above 0x80 is part of a multi-byte rune and is kept
			// verbatim, so no non-ASCII name can be folded onto an ASCII key.
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isNormalized reports whether name is already in the spelling normalize produces.
func isNormalized(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == normalizedSeparator {
			continue
		}
		return false
	}
	return true
}

// words yields the lower-cased ASCII alphanumeric words of s, splitting on every
// other ASCII byte. It is a range-over-func iterator so the caller can stop at the
// first denylisted word without building a slice of attacker-influenced text.
func words(s string) func(func(string) bool) {
	return func(yield func(string) bool) {
		start := -1
		flush := func(end int) bool {
			if start < 0 {
				return true
			}
			word := normalize(s[start:end], false)
			start = -1
			return yield(word)
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			// `_` stays inside a word so a denylisted key that is itself spelled with
			// an underscore is matched as one word rather than two.
			letter := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
			if letter {
				if start < 0 {
					start = i
				}
				continue
			}
			if !flush(i) {
				return
			}
		}
		flush(len(s))
	}
}

const (
	// utf8ContinuationLimit is the first byte value that cannot start an ASCII
	// character. A byte at or above it belongs to a multi-byte rune and is never a
	// separator.
	utf8ContinuationLimit = 0x80
	// normalizedSeparator is the single underscore every ASCII separator byte folds
	// to.
	normalizedSeparator = '_'
)
