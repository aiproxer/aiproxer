package responses_test

// The numeric equivalence rule of the wire-document comparison.
//
// The contract test compares a projected request body with a golden body as
// a whole document. That comparison must not alias two different values, and
// a decoder into float64 does exactly that: 9007199254740993 and
// 9007199254740992 are the same float64, so two documents that carry
// different integers would compare EQUAL. A silent equivalence in the
// contract is exactly the kind of gap this contract is meant to close, so
// the numbers keep their exact value.
//
// # The rule
//
// Two numbers are equal when they denote the same exact decimal value.
//
//   - The literal is normalised, not rounded: the exponent is applied
//     exactly and the trailing zeros of the fraction are removed. "1",
//     "1.0" and "1e0" are ONE value and compare equal, so a golden written
//     "1.0" is not rejected by a projection that writes "1", and a golden
//     written "1" is not rejected by a projection that writes "1.0".
//   - The normalised literal is always a valid JSON number. The digits of
//     the value keep the leading zero of the literal's integer part, and a
//     positive exponent can move the decimal point past it, so the integer
//     part is stripped of its redundant leading zeros: "0.5e2" is "50" and
//     not "050", which the comparison could not render at all.
//   - A value keeps every digit it was written with, so two large integers
//     that differ are never the same value.
//
// The rule is NUMERIC rather than textual on purpose. A textual rule would
// compare "1" and "1.0" as different documents and would reject a
// legitimate golden, and a rule that silently rejects a legitimate golden is
// as bad as one that aliases two values.
//
// A literal whose exponent is beyond the bound keeps its text and is
// compared by that text: the exact expansion of such a literal would be
// unbounded, and a number that cannot be expanded is compared by what it
// was written as.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// maxNumberExponent bounds the exponent a JSON number literal may carry.
const maxNumberExponent = 4096

// decodeJSONDocument decodes a JSON document and keeps the literal text of
// every number.
//
// The decoder uses UseNumber, so a number is not silently rounded into a
// float64 on the way in.
func decodeJSONDocument(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	// The document holds exactly one value: trailing content is refused
	// rather than ignored, so a document cannot hide a second document
	// behind the first.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errStrictTrailingContent
	}
	return value, nil
}

// canonicalizeNumbers replaces every number in a decoded document with its
// canonical literal.
//
// The document keeps its shape: only the number leaves change, and a number
// that could not be normalised keeps the literal it was written with.
func canonicalizeNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = canonicalizeNumbers(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, canonicalizeNumbers(item))
		}
		return out
	case json.Number:
		canonical, ok := canonicalNumberLiteral(typed.String())
		if !ok {
			return typed
		}
		return json.Number(canonical)
	}
	return value
}

// canonicalNumberLiteral returns the exact decimal value of a JSON number
// literal.
//
// The returned literal is always a valid JSON number, so the document it
// belongs to can be rendered.
//
// The second result is false when the literal could not be normalised, in
// which case the caller compares the literal text.
func canonicalNumberLiteral(literal string) (string, bool) {
	text := literal
	negative := false
	if strings.HasPrefix(text, "-") {
		negative = true
		text = text[1:]
	}
	exponent := 0
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(text[index+1:])
		if err != nil || parsed > maxNumberExponent || parsed < -maxNumberExponent {
			return literal, false
		}
		exponent = parsed
		text = text[:index]
	}
	integerPart := text
	fractionPart := ""
	if before, after, ok := strings.Cut(text, "."); ok {
		integerPart = before
		fractionPart = after
	}
	// The value is the digits with the point after len(integerPart)+exponent
	// digits.
	digits := integerPart + fractionPart
	position := len(integerPart) + exponent
	var out string
	switch {
	case position <= 0:
		out = "0." + strings.Repeat("0", -position) + digits
	case position >= len(digits):
		out = digits + strings.Repeat("0", position-len(digits))
	default:
		out = digits[:position] + "." + digits[position:]
	}
	// The trailing zeros of the fraction carry no value.
	if index := strings.IndexByte(out, '.'); index >= 0 {
		trimmed := strings.TrimRight(out[index+1:], "0")
		if trimmed == "" {
			out = out[:index]
		} else {
			out = out[:index+1] + trimmed
		}
	}
	// The integer part must not carry redundant leading zeros: the digits
	// of the value keep the leading zero of the literal's integer part,
	// and "0.5e2" would end up as "050", which is not a valid JSON number.
	out = trimLeadingIntegerZeros(out)
	// A zero of either sign is one value.
	if negative && out != "0" {
		out = "-" + out
	}
	return out, true
}

// trimLeadingIntegerZeros removes the redundant leading zeros of the integer
// part of a decimal literal.
//
// A single zero is kept when the integer part is zero, because "0.5" is a
// decimal literal and ".5" is not. The value does not change: a leading zero
// carries no value.
func trimLeadingIntegerZeros(literal string) string {
	integerPart := literal
	fraction := ""
	if index := strings.IndexByte(literal, '.'); index >= 0 {
		integerPart = literal[:index]
		fraction = literal[index:]
	}
	trimmed := strings.TrimLeft(integerPart, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	return trimmed + fraction
}
