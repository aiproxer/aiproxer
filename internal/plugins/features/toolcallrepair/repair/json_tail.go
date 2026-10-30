package repair

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"slices"
	"unicode/utf8"
)

type tailRepairKind uint8

const (
	tailRepairNone tailRepairKind = iota
	tailRepairTrailingComma
	tailRepairPendingRootValue
)

type tailAnalysis struct {
	kind            tailRepairKind
	commaOffset     int
	propertyName    string
	propertyPresent bool
	closers         []byte
}

func analyzeJSONTail(ctx context.Context, in []byte) (tailAnalysis, bool) {
	if len(in) == 0 || !utf8.Valid(in) {
		return tailAnalysis{}, false
	}
	stack := make([]tailFrame, 0, 8)
	rootDone := false
	terminalComma := -1
	for i := 0; i < len(in); {
		if err := tailContextErr(ctx); err != nil {
			return tailAnalysis{}, false
		}
		if isJSONWhitespace(in[i]) {
			i++
			continue
		}
		if len(stack) == 0 {
			if rootDone {
				return tailAnalysis{}, false
			}
			next, ok := consumeValue(ctx, in, i)
			if ok {
				if next == i+1 && (in[i] == '{' || in[i] == '[') {
					stack = append(stack, newTailFrame(in[i]))
				} else {
					rootDone = true
				}
				i = next
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		}

		switch stack[len(stack)-1].state {
		case tailObjectKeyOrEnd:
			if in[i] == '}' {
				i++
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootDone = true
				} else if !completeTailChild(stack) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			next, ok := consumeObjectKey(ctx, in, stack, i)
			if !ok {
				return tailAnalysis{}, false
			}
			i = next
			terminalComma = -1
		case tailObjectKeyAfterComma:
			next, ok := consumeObjectKey(ctx, in, stack, i)
			if !ok {
				return tailAnalysis{}, false
			}
			i = next
			terminalComma = -1
		case tailObjectColon:
			if in[i] != ':' {
				return tailAnalysis{}, false
			}
			stack[len(stack)-1].state = tailObjectValue
			i++
			terminalComma = -1
		case tailObjectValue:
			var ok bool
			stack, i, ok = consumeChildValue(ctx, in, stack, i, tailObjectCommaOrEnd)
			if !ok {
				return tailAnalysis{}, false
			}
			terminalComma = -1
		case tailObjectCommaOrEnd:
			if in[i] == ',' {
				stack[len(stack)-1].state = tailObjectKeyAfterComma
				terminalComma = i
				i++
				continue
			}
			if in[i] == '}' {
				i++
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootDone = true
				} else if !completeTailChild(stack) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		case tailArrayValueOrEnd:
			if in[i] == ']' {
				i++
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootDone = true
				} else if !completeTailChild(stack) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			var ok bool
			stack, i, ok = consumeChildValue(ctx, in, stack, i, tailArrayCommaOrEnd)
			if !ok {
				return tailAnalysis{}, false
			}
			terminalComma = -1
		case tailArrayValueAfterComma:
			var ok bool
			stack, i, ok = consumeChildValue(ctx, in, stack, i, tailArrayCommaOrEnd)
			if !ok {
				return tailAnalysis{}, false
			}
			terminalComma = -1
		case tailArrayCommaOrEnd:
			if in[i] == ',' {
				stack[len(stack)-1].state = tailArrayValueAfterComma
				terminalComma = i
				i++
				continue
			}
			if in[i] == ']' {
				i++
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootDone = true
				} else if !completeTailChild(stack) {
					return tailAnalysis{}, false
				}
				terminalComma = -1
				continue
			}
			return tailAnalysis{}, false
		default:
			return tailAnalysis{}, false
		}
	}

	if err := tailContextErr(ctx); err != nil {
		return tailAnalysis{}, false
	}
	if terminalComma >= 0 && len(stack) > 0 {
		top := stack[len(stack)-1].state
		if top == tailObjectKeyAfterComma || top == tailArrayValueAfterComma {
			return tailAnalysis{kind: tailRepairTrailingComma, commaOffset: terminalComma, closers: tailClosers(stack)}, true
		}
	}
	if !rootDone && len(stack) == 1 {
		root := stack[0]
		if root.kind == '{' && root.state == tailObjectValue && root.propertyPresent {
			return tailAnalysis{kind: tailRepairPendingRootValue, propertyName: root.key, propertyPresent: true, closers: tailClosers(stack)}, true
		}
	}
	return tailAnalysis{}, false
}

type tailState uint8

const (
	tailObjectKeyOrEnd tailState = iota
	tailObjectKeyAfterComma
	tailObjectColon
	tailObjectValue
	tailObjectCommaOrEnd
	tailArrayValueOrEnd
	tailArrayValueAfterComma
	tailArrayCommaOrEnd
)

type tailFrame struct {
	kind            byte
	state           tailState
	key             string
	propertyPresent bool
}

func newTailFrame(kind byte) tailFrame {
	if kind == '{' {
		return tailFrame{kind: kind, state: tailObjectKeyOrEnd}
	}
	return tailFrame{kind: kind, state: tailArrayValueOrEnd}
}

// completeTailChild advances the new top frame after a container closed under
// it. The caller handles the root-close case (empty stack), so this only runs
// for nested containers and takes no pointers that could force an escape.
func completeTailChild(stack []tailFrame) bool {
	top := len(stack) - 1
	switch stack[top].state {
	case tailObjectValue:
		stack[top].state = tailObjectCommaOrEnd
	case tailArrayValueOrEnd, tailArrayValueAfterComma:
		stack[top].state = tailArrayCommaOrEnd
	default:
		return false
	}
	return true
}

func tailClosers(stack []tailFrame) []byte {
	out := make([]byte, 0, len(stack))
	for _, frame := range slices.Backward(stack) {
		if frame.kind == '{' {
			out = append(out, '}')
		} else {
			out = append(out, ']')
		}
	}
	return out
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// consumeObjectKey reads a quoted property key onto the top object frame and
// moves it to the colon state. It returns the next scan index. The slice is
// passed by value; element mutations share the caller's backing array.
func consumeObjectKey(ctx context.Context, in []byte, stack []tailFrame, i int) (int, bool) {
	if in[i] != '"' {
		return i, false
	}
	next, key, ok := consumeJSONString(ctx, in, i)
	if !ok {
		return i, false
	}
	top := len(stack) - 1
	stack[top].key = key
	stack[top].propertyPresent = true
	stack[top].state = tailObjectColon
	return next, true
}

// consumeChildValue consumes the value expected by the top frame. An open
// container marker pushes a new frame; a completed scalar moves the frame to
// the completed state. It returns the (possibly appended) stack and the next
// scan index; the caller adopts the returned slice so appends can never alias
// stale elements.
func consumeChildValue(ctx context.Context, in []byte, stack []tailFrame, i int, completed tailState) ([]tailFrame, int, bool) {
	next, ok := consumeValue(ctx, in, i)
	if !ok {
		return stack, i, false
	}
	if next == i+1 && (in[i] == '{' || in[i] == '[') {
		if len(stack) >= maxJSONScanDepth {
			return stack, i, false
		}
		return append(stack, newTailFrame(in[i])), next, true
	}
	stack[len(stack)-1].state = completed
	return stack, next, true
}

func consumeValue(ctx context.Context, in []byte, i int) (int, bool) {
	if i >= len(in) {
		return i, false
	}
	switch in[i] {
	case '{', '[':
		return i + 1, true
	case '"':
		return skipJSONString(ctx, in, i)
	default:
		return consumePrimitive(ctx, in, i)
	}
}

// consumeJSONString validates and decodes a JSON string starting at in[i],
// returning the index just past the closing quote and the decoded text. Only
// callers that need the decoded value (object keys) use this; value scanning
// uses skipJSONString.
func consumeJSONString(ctx context.Context, in []byte, i int) (int, string, bool) {
	end, ok := skipJSONString(ctx, in, i)
	if !ok {
		return i, "", false
	}
	var value string
	if err := json.Unmarshal(in[i:end], &value); err != nil {
		return i, "", false
	}
	return end, value, true
}

func consumePrimitive(ctx context.Context, in []byte, i int) (int, bool) {
	start := i
	for i < len(in) && !isPrimitiveDelimiter(in[i]) {
		if i&255 == 0 {
			if err := tailContextErr(ctx); err != nil {
				return i, false
			}
		}
		i++
	}
	if start == i {
		return i, false
	}
	token := in[start:i]
	// Literals take the allocation-free fast path; every other accepted scalar
	// must be a complete JSON number. Object/array shapes cannot appear here
	// because consumeValue dispatches on delimiters first, so any map/slice
	// result decoded below is rejected.
	if bytes.Equal(token, []byte("true")) || bytes.Equal(token, []byte("false")) || bytes.Equal(token, []byte("null")) {
		return i, true
	}
	if !json.Valid(token) {
		return i, false
	}
	var v any
	if err := json.Unmarshal(token, &v); err != nil {
		return i, false
	}
	switch v.(type) {
	case map[string]any, []any:
		return i, false
	default:
		return i, true
	}
}

func isPrimitiveDelimiter(c byte) bool {
	return isJSONWhitespace(c) || c == ',' || c == ']' || c == '}'
}

// assembleCandidate builds and validates an append-only candidate from the
// three parts. The total size is computed in uint64 so the sum cannot overflow,
// then bounded by maxBytes and int before the allocation; the result must be
// valid JSON and UTF-8. The fixed signature avoids a variadic packing
// allocation on the repair path.
func assembleCandidate(maxBytes int, prefix, value, closers []byte) ([]byte, bool) {
	total := uint64(len(prefix)) + uint64(len(value)) + uint64(len(closers))
	if maxBytes > 0 && total > uint64(maxBytes) {
		return nil, false
	}
	if total > uint64(math.MaxInt) {
		return nil, false
	}
	out := make([]byte, 0, int(total))
	out = append(out, prefix...)
	out = append(out, value...)
	out = append(out, closers...)
	if !json.Valid(out) || !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}

func buildTrailingCommaCandidate(in []byte, a tailAnalysis, maxBytes int) ([]byte, bool) {
	if a.kind != tailRepairTrailingComma || a.commaOffset < 0 || a.commaOffset >= len(in) || in[a.commaOffset] != ',' {
		return nil, false
	}
	return assembleCandidate(maxBytes, in[:a.commaOffset], in[a.commaOffset+1:], a.closers)
}

func buildPendingValueCandidate(in []byte, a tailAnalysis, value []byte, maxBytes int) ([]byte, bool) {
	if a.kind != tailRepairPendingRootValue || !a.propertyPresent {
		return nil, false
	}
	return assembleCandidate(maxBytes, in, value, a.closers)
}
