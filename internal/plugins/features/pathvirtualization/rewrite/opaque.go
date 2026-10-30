package rewrite

// This file implements the conservative result handling of design.md 246-250: the
// two bounded recognizers behind the two enabled OpaqueResultMode values, applied
// only to an opaque tool-result payload whose exact tool profile explicitly asked
// for them.
//
// The rule is one sentence: a line of the payload is rewritten only when EVERY
// whitespace- or comma-delimited token on it is a path this mapping accepts, and
// then exactly those tokens are replaced. A line carrying anything else is left
// byte-for-byte alone.
//
// That rule is chosen for one reason: the only failure mode that matters here is a
// false positive. An opaque tool result is exactly the payload requirement 2.4 and
// requirement 3.3 refuse to inspect for paths, so its text is very often source, a
// diff, a shell command line, a log line, a stack frame, an embedded JSON blob, or
// prose that merely MENTIONS a workspace path. All of those put at least one token
// on the line that is not a path, so the whole line is refused instead of a path
// being pulled out of the middle of it. What survives is a line that holds nothing
// but locations, which is the only shape both enabled modes are defined over.
//
// Three further decisions follow from the same priority:
//
//   - recognition is per LINE, never per free-floating substring. A path glued to an
//     identifier, a path inside a quoted source string, and a path behind a diff
//     context marker are all mid-token or mid-line, so none of them can be a line
//     that holds nothing but locations;
//   - quotes, brackets, colons, equals signs, and every other byte are ordinary
//     token bytes, never delimiters. A delimiter set that let a key-value or quoted
//     shape be split would make `path=/ws/a.go`, `"path":"/ws/a.go"`, and
//     `grep -n hit /ws/a.go` recognizable, and those are the exact shapes of
//     strace, journalctl, git, grep, and log output this step must not touch;
//   - ambiguity fails closed everywhere. A single token that is not a path refuses
//     its whole line, an unrecognized mode recognizes nothing, and a line that
//     already mixes the alias namespace with the real root is refused, which is
//     what keeps reapplication stable instead of oscillating (requirement 2.9).
//
// The two enabled modes differ only in how many tokens a line may hold:
// path_tokens accepts one or more, matching its declared contract of "a list of
// locations, one or more per line", while path_lines accepts exactly one, matching
// its declared contract of "whole lines that hold nothing but a location". The
// line-mode recognizer is therefore a strict subset of the token-mode recognizer,
// and both share this one tokenizer and this one mapping call.
//
// Cost and purity are structural rather than checked: one pass over the payload to
// decide line by line, a second pass only over the lines that were accepted, no
// regular expression, no backtracking, no recursion, and at most one allocated
// output buffer. The step holds no state, performs no I/O, and delegates every
// prefix decision to Mapping.VirtualizePath, so path flavor semantics (POSIX
// case-sensitivity, Windows ASCII case-insensitivity, separator equivalence, and the
// segment-boundary rule) are the lexical core's answers rather than a second
// implementation of them.

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

const (
	// lineFeed is the only byte that ends a line. It is not a host newline rule: a
	// canonical result payload carries whatever the client sent, so a CR is treated
	// as an ordinary delimiter byte and a lone CR does not start a new line.
	lineFeed = '\n'
	// tokenComma is the only non-whitespace delimiter.
	tokenComma = ','
)

// rewriteOpaqueText applies the bounded recognizer of mode to one opaque result
// payload and returns the replacement text.
//
// It returns the input itself and false when nothing changed, so an opaque surface
// that needs no rewrite is republished as the very same string rather than an
// equivalent copy. Every refusal records a bounded reason on acc: the declared mode
// with nothing recognized, or the disabled default. No path, suffix, tool name, or
// payload byte reaches acc.
func (r *Rewriter) rewriteOpaqueText(text string, mode pathvirtualization.OpaqueResultMode, acc *account) (string, bool) {
	if !modeRecognizesOpaque(mode) {
		// Requirement 2.5: nothing marked this result path-oriented, so it stays
		// byte-for-byte unchanged. That is the answer for every tool no exact
		// profile claims as well (requirement 3.8).
		acc.skip(SkipReasonOpaqueResultUnchanged)
		return text, false
	}

	var published strings.Builder
	writing := false
	changed := false
	for lineStart := 0; ; {
		lineEnd := lineStart
		for lineEnd < len(text) && text[lineEnd] != lineFeed {
			lineEnd++
		}
		if r.opaqueLineAccepted(text[lineStart:lineEnd], mode, acc) {
			if r.opaqueLineRewrite(text, lineStart, lineEnd, &published, &writing, acc) {
				changed = true
			}
		} else if writing {
			// Once any earlier line has been re-spelled, a refused line still has
			// to be published, or the bytes between two rewritten lines would be
			// lost. Copying them verbatim is what keeps a payload that is mostly
			// source intact.
			published.WriteString(text[lineStart:lineEnd])
		}
		if lineEnd == len(text) {
			break
		}
		if writing {
			published.WriteByte(lineFeed)
		}
		lineStart = lineEnd + 1
	}
	if !changed {
		// The payload held no unambiguous location, so the declared mode found
		// nothing and the payload is reported unchanged rather than partially
		// rewritten (requirement 2.6).
		acc.skip(SkipReasonOpaqueResultBounded)
		return text, false
	}
	return published.String(), true
}

// modeRecognizesOpaque reports whether mode is one of the two enabled recognizers.
//
// The disabled value and any value outside the closed set are the same answer here:
// nothing is recognized and the payload is left unchanged. Failing closed on an
// unrecognized mode is deliberate, because the alternative reading of a mode this
// build does not define would be "rewrite it anyway".
func modeRecognizesOpaque(mode pathvirtualization.OpaqueResultMode) bool {
	switch mode {
	case pathvirtualization.OpaqueResultModePathTokens, pathvirtualization.OpaqueResultModePathLines:
		return true
	case pathvirtualization.OpaqueResultModeNone:
		return false
	default:
		return false
	}
}

// opaqueLineAccepted reports whether every token on one line is a location this
// mapping accepts, and whether the mode accepts that many tokens on one line.
//
// The whole line is the unit, so a line is refused the moment one token is not a
// path. A line with no token at all carries no location and is refused as well.
//
// Eligibility is counted here rather than while re-spelling, because the count
// describes what the mapping accepted on the line and not what the byte splice
// ended up writing. That is the same split the structured surface keeps: a selected
// leaf the mapping accepts is eligible, and it is rewritten only when its published
// value differs.
func (r *Rewriter) opaqueLineAccepted(line string, mode pathvirtualization.OpaqueResultMode, acc *account) bool {
	tokens := 0
	before, after := 0, 0
	for from := 0; from < len(line); {
		start, end, _, found := nextOpaqueToken(line, from)
		if !found {
			break
		}
		from = end
		tokens++
		if mode == pathvirtualization.OpaqueResultModePathLines && tokens > 1 {
			// A line holding more than one location is a list, not a whole line
			// that holds nothing but a location, and the line-only mode stops here
			// before it has accounted for anything.
			return false
		}
		token := line[start:end]
		virtualized, matched := r.mapping.VirtualizePath(token)
		if !matched {
			// Requirement 2.6: ambiguous content is left unchanged. One non-path
			// token refuses its line, so a path is never lifted out of a command,
			// a source line, a log record, or a sentence.
			return false
		}
		before += len(token)
		after += len(virtualized)
	}
	if tokens == 0 {
		// A line of nothing but delimiters carries no location at all.
		return false
	}
	// The line is accepted as a whole, so its accounting is committed as a whole:
	// a refused line contributes nothing, which is what keeps a caller from
	// reading savings out of content this step deliberately left alone.
	acc.eligible += tokens
	acc.bytesBefore += before
	acc.bytesAfter += after
	return true
}

// opaqueLineRewrite re-spells the tokens of one accepted line into published and
// reports whether any of them changed.
//
// Bytes that are not a recognized token are copied verbatim, including the
// indentation before the first token, the delimiter runs between tokens, and every
// earlier line, so only the mapping's own replacement bytes differ. published is
// filled on the first change from the untouched prefix, and the caller writes the
// line separators after that point.
//
// The accept pass already proved every token on this line is a location, and both
// passes tokenize the same bytes with the same helper, so this pass needs no second
// refusal check: the two cannot disagree about what the line holds.
func (r *Rewriter) opaqueLineRewrite(text string, lineStart, lineEnd int, published *strings.Builder, writing *bool, acc *account) bool {
	// The line is tokenized as its own slice, exactly as the accept pass did, so
	// neither pass can walk past the line it is looking at.
	line := text[lineStart:lineEnd]
	changed := false
	copied := lineStart
	for from := 0; from < len(line); {
		start, end, _, found := nextOpaqueToken(line, from)
		if !found {
			break
		}
		from = end
		token := line[start:end]
		virtualized, _ := r.mapping.VirtualizePath(token)
		if !*writing {
			if virtualized == token {
				continue
			}
			// Every replacement is strictly shorter than the prefix it replaces, so
			// the published payload can never outgrow the input and one capacity
			// hint for the whole payload is enough. Taking it here, on the first
			// change, is what keeps a payload nothing changes allocation-free.
			published.Grow(len(text))
			published.WriteString(text[:lineStart+start])
			// Everything up to and including this token's start is now published,
			// so the next copy starts at the token itself rather than repeating the
			// delimiters that led to it.
			copied = lineStart + start
			*writing = true
		}
		if virtualized != token {
			acc.rewritten++
		}
		// Everything since the previous token, delimiters included, is carried
		// across untouched; only the token itself is re-spelled.
		published.WriteString(text[copied : lineStart+start])
		published.WriteString(virtualized)
		copied = lineStart + end
		changed = changed || virtualized != token
	}
	if *writing && copied < lineEnd {
		// The trailing delimiter run of an accepted line belongs to the payload,
		// not to the recognizer, so it is carried across too.
		published.WriteString(text[copied:lineEnd])
	}
	return changed
}

// nextOpaqueToken returns the byte range of the next token at or after from, and the
// offset to resume from.
//
// A token is a maximal run of bytes that are neither whitespace nor a comma, so a
// run of delimiters is skipped rather than published as an empty token. The third
// result is false once the remaining bytes hold no token at all, which is how a
// trailing comma, a blank line, and an indentation-only line all end without
// producing a location.
func nextOpaqueToken(line string, from int) (start, end, next int, found bool) {
	at := from
	for at < len(line) && isTokenDelimiter(line[at]) {
		at++
	}
	if at >= len(line) {
		return 0, 0, len(line), false
	}
	start = at
	for at < len(line) && !isTokenDelimiter(line[at]) {
		at++
	}
	return start, at, at, true
}

// isTokenDelimiter reports whether a byte separates two tokens.
//
// The set is deliberately the smallest one a list of locations needs: the ASCII
// whitespace bytes and the comma. Every other byte, including a quote, a bracket, a
// colon, an equals sign, a diff marker, and both separators of the opposite path
// flavor, is an ordinary token byte, which is what makes a quoted path, a key-value
// pair, and a command argument unreachable rather than merely unlikely.
func isTokenDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\v', '\f', tokenComma:
		return true
	default:
		return false
	}
}
