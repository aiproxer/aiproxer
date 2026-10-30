package pathvirtualization

// This file adds the one alias-recognition question the parseable-path recognizer
// cannot answer: does a block of raw bytes that is NOT a path, and is not even
// readable as JSON, spell the fixed V1 reserved namespace at all?
//
// It is needed by exactly one caller. A completed tool-call argument document the
// runtime could not parse has no structure left to walk, so there is no path to hand
// to the parseable recognizer, and there is no selector resolution to tell a
// path-bearing field from a content field. design.md "Error Handling" nevertheless
// requires the closed answer: "a recognized applicable alias must never bypass
// required expansion and reach the client." The only way to recognize an alias in
// that state is lexically, over bytes.
//
// What this recognizer deliberately does NOT decide:
//
//   - the flavor. It answers "these bytes spell the reserved namespace", never
//     "these bytes spell a POSIX alias". The flavor of a mangled spelling is exactly
//     what the parseable path must resolve, and guessing it here would put a guess in
//     front of the only place that can prove it.
//   - the drive and the tag comparison against a mapping. Whether the alias names
//     THIS workspace needs a mapping, which a document may arrive before one can be
//     derived for.
//   - whether the alias is inside a field the operator configured as path-bearing.
//     That is not knowable without structure, which is why every caller of this
//     recognizer pairs it with the closed fail-closed policy its own state requires.
//
// It is a recognizer, not a second parser. The marker must be a COMPLETE path
// segment and the tag segment is validated by the same reservedWorkspaceTag rule the
// parseable path uses, so a real directory named `my.__aip_v1__` and a substring
// mention of the marker are both outside the namespace.
//
// The marker is matched with ASCII case FOLDED, exactly like the tag, because that is
// how the parseable recognizer compares both: three of the five V1 flavors match
// case-insensitively, so a case-varied marker spelling is the same reserved root to them.
// Matching the marker byte-exactly while folding the tag gave the two halves of one
// decision different answers for the same bytes, and the answer that lost was the one
// this file exists to give. Folding the case does not loosen the segment rule: a
// case-varied spelling of an ordinary name is still an ordinary name.

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// ReservedAliasPresence is the bounded, content-free answer to whether raw bytes
// spell the fixed V1 reserved alias namespace.
//
// It is an enum with three members because the two refusals it can produce are
// genuinely different answers with different consequences, and because every value
// here may reach a metric dimension or a client-facing refusal. No member carries a
// path, alias, tag, or payload byte.
type ReservedAliasPresence uint8

const (
	// ReservedAliasAbsent marks bytes that do not spell the reserved namespace at
	// any segment boundary. An ordinary path, a substring mention of the marker,
	// and an empty payload are all this answer.
	ReservedAliasAbsent ReservedAliasPresence = iota
	// ReservedAliasMalformedTag marks bytes that spell the reserved marker at a
	// segment boundary and are followed by a tag segment that is not exactly the
	// frozen `w_` plus workspaceTagChars characters of the unpadded base32
	// alphabet. Such bytes are inside the reserved namespace and therefore must
	// never be released as an ordinary client path (requirement 4.4).
	ReservedAliasMalformedTag
	// ReservedAliasWellFormed marks bytes that spell the complete fixed V1 alias
	// root: marker at a segment boundary plus a well-formed tag segment. The alias
	// may still name a different workspace, which is a mapping decision and not
	// this recognizer's.
	ReservedAliasWellFormed
)

// String returns the fixed, low-cardinality label of an answer. It is safe for
// content-free observability dimensions: it never contains path, alias, tag, or
// payload bytes.
func (p ReservedAliasPresence) String() string {
	switch p {
	case ReservedAliasAbsent:
		return "absent"
	case ReservedAliasMalformedTag:
		return "malformed_tag"
	case ReservedAliasWellFormed:
		return "well_formed"
	default:
		return "unknown"
	}
}

// ScanReservedAlias reports whether raw spells the fixed V1 reserved alias
// namespace anywhere inside it.
//
// Recognition is separator-agnostic and purely lexical, matching
// [parseReservedAlias]: a backslash ends a segment here too, because a mangled alias
// is more dangerous than an over-refused real file name. It requires the marker to
// occupy a COMPLETE segment - preceded by a separator or by the first byte of raw,
// and followed by a separator or by the end - so a path whose directory is named
// `my.__aip_v1__` or whose name starts with `.__aip_v1__x` is an ordinary path.
//
// Both the marker and the tag are compared with ASCII case FOLDED, which is the
// comparison [parseReservedAlias] applies to both of them under a Windows flavor. A
// byte-exact marker comparison here did not make this recognizer stricter in any useful
// sense: the alias-root spellings this build itself derives are recognized by the
// parseable recognizer in every ASCII case, so refusing them lexically only ever
// released a namespace the core had already claimed.
//
// The tag segment is validated with [reservedWorkspaceTag] under the permissive
// two-case alphabet, because this recognizer does not resolve a flavor and must not
// report a shape failure for a well-formed Windows alias spelled in upper case. The
// flavor's own case rule stays where it belongs: the mapping's parseable path.
//
// A well-formed answer wins over a malformed one. Both are refusals for a caller that
// cannot expand, so the order only affects which bounded label is reported, and
// reporting the stronger signal keeps a scan of a payload with several occurrences
// from hiding a complete alias behind a partial one.
func ScanReservedAlias(raw []byte) ReservedAliasPresence {
	if presence := scanReservedAliasSpelling(raw); presence != ReservedAliasAbsent {
		return presence
	}
	// A JSON string may spell any byte of the marker as a \uXXXX escape, so the
	// literal spelling is absent while the payload still denotes the reserved
	// namespace. Re-scanning the unescaped projection recognizes the alias the
	// document actually spells. This only ever strengthens recognition: an
	// escaped marker is refused on the unparseable path exactly as a literal one
	// is, and the parseable path never reaches here.
	return scanReservedAliasSpelling(unescapeJSONProjection(raw))
}

// unescapeJSONProjection decodes every \uXXXX escape in raw, leaving every other
// byte in place so segment boundaries and tag shapes are still judged on the same
// rules. Malformed or truncated escapes are copied through unchanged, which can
// only lose recognition relative to the literal scan that already ran.
func unescapeJSONProjection(raw []byte) []byte {
	if !bytes.Contains(raw, jsonUnicodeEscape) {
		return raw
	}
	projected := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		if i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
			if decoded, ok := decodeUnicodeEscape(raw[i : i+6]); ok {
				projected = utf8.AppendRune(projected, decoded)
				i += 6
				continue
			}
		}
		projected = append(projected, raw[i])
		i++
	}
	return projected
}

// jsonUnicodeEscape is the byte prefix of a JSON \uXXXX escape.
var jsonUnicodeEscape = []byte{'\\', 'u'}

// decodeUnicodeEscape decodes one \uXXXX sequence, including a surrogate pair
// when the following six bytes spell its low surrogate.
func decodeUnicodeEscape(seq []byte) (rune, bool) {
	high, ok := decodeHexQuad(seq[2:6])
	if !ok {
		return 0, false
	}
	if !utf16.IsSurrogate(high) {
		return high, true
	}
	if len(seq) < 12 || seq[6] != '\\' || seq[7] != 'u' {
		return utf8.RuneError, false
	}
	low, ok := decodeHexQuad(seq[8:12])
	if !ok || low < 0xDC00 || low > 0xDFFF {
		return utf8.RuneError, false
	}
	return utf16.DecodeRune(high, low), true
}

// decodeHexQuad decodes four hexadecimal digits, accepting either case.
func decodeHexQuad(digits []byte) (rune, bool) {
	var value rune
	for _, digit := range digits {
		switch {
		case digit >= '0' && digit <= '9':
			value = value<<4 | rune(digit-'0')
		case digit >= 'a' && digit <= 'f':
			value = value<<4 | rune(digit-'a'+10)
		case digit >= 'A' && digit <= 'F':
			value = value<<4 | rune(digit-'A'+10)
		default:
			return 0, false
		}
	}
	return value, true
}

// scannableTagSegment returns the tag candidate that follows the reserved marker at
// raw[after:], copied only as far as a valid tag could possibly reach.
//
// [isScannableWorkspaceTag] accepts a segment of exactly
// len(tagPrefix)+workspaceTagChars bytes and nothing else, so this never reads or
// copies more than that plus one byte, no matter how much payload follows the marker.
// Reading the whole remaining payload instead would copy it once per marker
// occurrence, which is quadratic on a document carrying many markers and reaches
// hundreds of megabytes on a payload still inside the bound the assembler enforces.
//
// The ANSWER is unchanged, and that needs the segment's real end rather than a fixed
// window. A window alone cannot tell a too-short segment from an exactly-valid one,
// because the bytes that follow a short segment can pad it up to the valid length,
// and it cannot tell an exactly-valid segment from a too-long one, because a long
// segment truncated to the window looks exactly valid. So this locates the segment
// end first, and reads one byte past the window only to recognise the too-long case,
// which it reports as a window one byte too wide so the exact-length check refuses it.
func scannableTagSegment(raw []byte, after int) string {
	start := after
	for start < len(raw) && isSeparator(raw[start]) {
		start++
	}
	window := len(tagPrefix) + workspaceTagChars
	limit := min(start+window, len(raw))
	end := limit
	for i := start; i < limit; i++ {
		if isSeparator(raw[i]) {
			return string(raw[start:i])
		}
	}
	if end < len(raw) && !isSeparator(raw[end]) {
		// The segment continues past the window, so it cannot be the frozen width.
		return string(raw[start : start+window+1])
	}
	return string(raw[start:end])
}

// scanReservedAliasSpelling is the literal-byte recognizer [ScanReservedAlias] runs
// first and then again over the unescaped projection.
func scanReservedAliasSpelling(raw []byte) ReservedAliasPresence {
	presence := ReservedAliasAbsent
	for offset := 0; ; {
		start, found := indexReservedMarker(raw, offset)
		if !found {
			return presence
		}
		if !isSegmentBoundaryBefore(raw, start) {
			// The marker bytes appear inside a longer name, so this occurrence is
			// not the reserved namespace. Stepping one byte past the marker's own
			// first byte is enough to skip the rest of it.
			offset = start + 1
			continue
		}
		after := start + len(reservedNamespaceV1)
		if after < len(raw) && !isSeparator(raw[after]) {
			offset = start + 1
			continue
		}
		tagSegment := scannableTagSegment(raw, after)
		if !isScannableWorkspaceTag(tagSegment) {
			// The namespace is recognized from the marker on, so a tag that is not
			// the frozen one leaves a MALFORMED reserved alias rather than an
			// ordinary path. That is the same refusal the parseable path reaches.
			presence = maxPresence(presence, ReservedAliasMalformedTag)
			offset = after
			continue
		}
		return ReservedAliasWellFormed
	}
}

// indexReservedMarker returns the offset of the first spelling of the reserved marker at
// or after from, and whether one exists.
//
// Case is folded on both sides because the marker is namespace SYNTAX compared under a
// flavor's matching rules, and three of the five V1 flavors fold ASCII case. A byte-exact
// search here would disagree with [parseReservedAlias] about the same bytes: the
// parseable recognizer accepts an upper-case marker spelling as this build's own alias
// root, so a byte-exact scan reported ABSENT for a namespace the core recognizes, and the
// one caller of the scan then passed a document carrying that namespace straight through to
// the client. The tag comparison in this file already folds case for the same reason, so
// folding the marker makes the two halves of one decision agree instead of inventing a
// second, stricter namespace.
//
// The search stays anchored on the marker's own first byte, which no ASCII case fold can
// change: a byte search for that byte cannot skip a folded spelling, and it keeps the scan
// linear and allocation free over a payload inside the declared argument bound. Folding is
// ASCII-only for the same reason tag comparison is, so a multi-byte UTF-8 sequence can
// never equal a marker byte and no Unicode equivalence can invent a spelling.
func indexReservedMarker(raw []byte, from int) (int, bool) {
	markerLen := len(reservedMarkerBytes)
	// A spelling that would run past the end cannot be a complete marker, so the
	// candidate window stops one byte short of the final possible start.
	for offset := from; offset+markerLen <= len(raw); {
		gap := bytes.IndexByte(raw[offset:len(raw)-markerLen+1], reservedMarkerFirstByte)
		if gap < 0 {
			return 0, false
		}
		start := offset + gap
		if reservedMarkerAt(raw, start) {
			return start, true
		}
		offset = start + 1
	}
	return 0, false
}

// reservedMarkerFirstByte is the marker byte an ASCII case fold cannot change, used as the
// scan's anchor. It is the marker's first byte, which is the dot that opens every V1
// alias root's reserved segment.
const reservedMarkerFirstByte = '.'

// reservedMarkerAt reports whether the marker is spelled at start, ignoring ASCII case.
func reservedMarkerAt(raw []byte, start int) bool {
	if start+len(reservedMarkerBytes) > len(raw) {
		return false
	}
	for i, want := range reservedMarkerBytes {
		if toLowerASCII(raw[start+i]) != toLowerASCII(want) {
			return false
		}
	}
	return true
}

// reservedMarkerBytes is the reserved namespace marker as a byte sequence, so the scan
// compares bytes rather than strings converted from them. Its spelling is the canonical
// lower-case one the frozen derivation emits; recognition folds case on both sides, as
// [reservedMarkerAt] documents.
var reservedMarkerBytes = []byte(reservedNamespaceV1)

// isSegmentBoundaryBefore reports whether the marker at start begins a complete
// path segment.
//
// The first byte of the input counts as a boundary, so a payload that IS an alias
// root is recognized as readily as one nested inside a document. A quote does not:
// inside a JSON string literal the marker is always preceded by the byte that opens
// the alias root, which is a separator in every one of the five V1 forms.
func isSegmentBoundaryBefore(raw []byte, start int) bool {
	return start == 0 || isSeparator(raw[start-1])
}

// isScannableWorkspaceTag validates one candidate tag segment with the permissive
// two-case alphabet.
//
// [reservedWorkspaceTag] takes a flavor so a POSIX alias must carry the canonical
// lower-case tag. This recognizer resolves no flavor, so it validates the SHAPE only:
// both cases of the unpadded base32 alphabet are accepted here, and the flavor's own
// rule is applied later by the mapping on a parseable path. Every other condition -
// the exact segment length and the frozen `w_` prefix - is shared, because those are
// namespace syntax rather than matching rules.
func isScannableWorkspaceTag(segment string) bool {
	if len(segment) != len(tagPrefix)+workspaceTagChars {
		return false
	}
	if !asciiEqualFold(segment[:len(tagPrefix)], tagPrefix) {
		return false
	}
	for i := len(tagPrefix); i < len(segment); i++ {
		c := segment[i]
		if (c >= '2' && c <= '7') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// asciiEqualFold compares two ASCII segments case-insensitively and byte-exactly.
// It is deliberately not a Unicode fold, so no non-ASCII byte can ever be equal to a
// tag character.
func asciiEqualFold(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range len(want) {
		if toLowerASCII(got[i]) != want[i] {
			return false
		}
	}
	return true
}

// maxPresence returns the stronger of two answers, where well-formed outranks
// malformed outranks absent. It keeps a scan that meets several occurrences from
// reporting the weakest one it saw.
func maxPresence(a, b ReservedAliasPresence) ReservedAliasPresence {
	if a > b {
		return a
	}
	return b
}
