package repair

import (
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"
)

const maxJSONScanDepth = 256

// CompleteJSONSuffix appends the minimal suffix that closes an open string and
// any unclosed object/array delimiters. It never mutates or deletes prefix bytes.
//
// Its inline string scan is deliberately lenient: it must walk unterminated
// strings to their end, and the final json.Valid/utf8.Valid gate rejects any
// input that cannot be completed into valid JSON. Callers that need strict
// string validation use skipJSONString instead.
func CompleteJSONSuffix(in []byte) ([]byte, bool) {
	if len(in) == 0 {
		return nil, false
	}
	if json.Valid(in) {
		if !utf8.Valid(in) {
			return nil, false
		}
		out := append([]byte(nil), in...)
		return out, true
	}
	stack := make([]byte, 0, 8)
	inString := false
	escape := false
	for i := range in {
		c := in[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			if len(stack) >= maxJSONScanDepth {
				return nil, false
			}
			stack = append(stack, c)
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if escape {
		return nil, false
	}
	suffix := make([]byte, 0, 1+len(stack))
	if inString {
		suffix = append(suffix, '"')
	}
	for _, opener := range slices.Backward(stack) {
		if opener == '{' {
			suffix = append(suffix, '}')
		} else {
			suffix = append(suffix, ']')
		}
	}
	out := make([]byte, len(in)+len(suffix))
	copy(out, in)
	copy(out[len(in):], suffix)
	if !json.Valid(out) || !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}

// skipJSONString validates a JSON string starting at in[i] without decoding
// its text, returning the index just past the closing quote. Value strings are
// scanned only for validity, so the tail scan avoids one string allocation per
// value; only object-key callers use the decoding consumeJSONString.
func skipJSONString(ctx context.Context, in []byte, i int) (int, bool) {
	if i >= len(in) || in[i] != '"' {
		return i, false
	}
	for j := i + 1; j < len(in); j++ {
		if j&255 == 0 {
			if err := tailContextErr(ctx); err != nil {
				return j, false
			}
		}
		c := in[j]
		if c < 0x20 {
			return j, false
		}
		if c == '\\' {
			j++
			if j >= len(in) {
				return j, false
			}
			switch in[j] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if j+4 >= len(in) {
					return j, false
				}
				for k := j + 1; k <= j+4; k++ {
					if !isHex(in[k]) {
						return k, false
					}
				}
				j += 4
			default:
				return j, false
			}
			continue
		}
		if c == '"' {
			return j + 1, true
		}
	}
	return len(in), false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func tailContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
