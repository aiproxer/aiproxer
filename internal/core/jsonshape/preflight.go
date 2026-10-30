package jsonshape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Preflight validates JSON size and shape using encoding/json.Decoder.Token.
func Preflight(data []byte, limits Limits) (Result, error) {
	return PreflightWithContext(context.Background(), data, limits)
}

// PreflightWithContext is Preflight with cancellation checks at token boundaries.
func PreflightWithContext(ctx context.Context, data []byte, limits Limits) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits = NormalizeLimits(limits)
	result := Result{Bytes: len(data)}
	if err := ctx.Err(); err != nil {
		return result, canceledError(err)
	}
	if int64(len(data)) > limits.MaxBytes {
		return result, &Error{Kind: KindTooLarge, Limit: int(limits.MaxBytes), Value: len(data)}
	}
	if !utf8.Valid(data) {
		return result, &Error{Kind: KindInvalidUTF8, Msg: "invalid UTF-8"}
	}
	if whitespaceOnly(data) {
		return result, &Error{Kind: KindMalformed, Reason: MalformedEmpty, Msg: "empty JSON body"}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	frames := make([]frame, 0, 8)
	rootValues := 0

	for {
		if err := ctx.Err(); err != nil {
			return result, canceledError(err)
		}
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return result, &Error{Kind: KindMalformed, Reason: MalformedSyntax, Msg: "malformed JSON"}
		}
		result.Tokens++
		if result.Tokens > limits.MaxTokens {
			return result, &Error{Kind: KindTooManyTokens, Limit: limits.MaxTokens, Value: result.Tokens}
		}

		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				if len(frames) == 0 {
					rootValues++
					if rootValues > 1 {
						return result, &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
					}
				} else if err := countValue(&frames, limits); err != nil {
					return result, err
				}
				frames = append(frames, newObjectFrame(limits.RejectDuplicateNames))
				if err := checkDepth(len(frames), limits.MaxDepth); err != nil {
					return result, err
				}
				result.MaxDepth = max(result.MaxDepth, len(frames))
			case '[':
				if len(frames) == 0 {
					rootValues++
					if rootValues > 1 {
						return result, &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
					}
				} else if err := countValue(&frames, limits); err != nil {
					return result, err
				}
				frames = append(frames, frame{})
				if err := checkDepth(len(frames), limits.MaxDepth); err != nil {
					return result, err
				}
				result.MaxDepth = max(result.MaxDepth, len(frames))
			case '}':
				if len(frames) == 0 || !frames[len(frames)-1].object {
					return result, &Error{Kind: KindMalformed, Reason: MalformedUnexpectedClosing, Msg: "unexpected closing delimiter"}
				}
				frames = frames[:len(frames)-1]
			case ']':
				if len(frames) == 0 || frames[len(frames)-1].object {
					return result, &Error{Kind: KindMalformed, Reason: MalformedUnexpectedClosing, Msg: "unexpected closing delimiter"}
				}
				frames = frames[:len(frames)-1]
			}
			continue
		}

		if len(frames) == 0 {
			rootValues++
			if rootValues > 1 {
				return result, &Error{Kind: KindMalformed, Reason: MalformedMultipleValues, Msg: "multiple JSON values"}
			}
		}
		if err := inspectScalar(tok, &frames, limits); err != nil {
			return result, err
		}
	}

	if rootValues == 0 {
		return result, &Error{Kind: KindMalformed, Reason: MalformedEmpty, Msg: "empty JSON body"}
	}
	if len(frames) != 0 {
		return result, &Error{Kind: KindMalformed, Reason: MalformedIncomplete, Msg: "incomplete JSON body"}
	}
	if hasTrailingNonWhitespace(data[dec.InputOffset():]) {
		return result, &Error{Kind: KindMalformed, Reason: MalformedTrailingData, Msg: "trailing data after JSON value"}
	}
	return result, nil
}

type frame struct {
	object    bool
	count     int
	expectKey bool
	seen      map[string]struct{}
}

func newObjectFrame(rejectDuplicates bool) frame {
	f := frame{object: true}
	if rejectDuplicates {
		f.seen = make(map[string]struct{})
	}
	return f
}

func checkDepth(depth, limit int) error {
	if depth > limit {
		return &Error{Kind: KindTooDeep, Limit: limit, Value: depth}
	}
	return nil
}

func inspectScalar(tok json.Token, frames *[]frame, limits Limits) error {
	if s, ok := tok.(string); ok && len(*frames) > 0 {
		current := &(*frames)[len(*frames)-1]
		if current.object && !current.expectKey {
			if current.seen != nil {
				if _, dup := current.seen[s]; dup {
					return &Error{Kind: KindDuplicateName, Msg: "duplicate object member name"}
				}
				current.seen[s] = struct{}{}
			}
			current.count++
			if current.count > limits.MaxObjectKeys {
				return &Error{Kind: KindTooManyItems, Limit: limits.MaxObjectKeys, Value: current.count}
			}
			if len(s) > limits.MaxKeyBytes {
				return &Error{Kind: KindKeyTooLong, Limit: limits.MaxKeyBytes, Value: len(s)}
			}
			current.expectKey = true
			return nil
		}
	}

	if err := countValue(frames, limits); err != nil {
		return err
	}
	switch v := tok.(type) {
	case string:
		if len(v) > limits.MaxStringBytes {
			return &Error{Kind: KindStringTooLong, Limit: limits.MaxStringBytes, Value: len(v)}
		}
	case json.Number:
		if len(v) > limits.MaxNumberBytes {
			return &Error{Kind: KindNumberTooLong, Limit: limits.MaxNumberBytes, Value: len(v)}
		}
	}
	return nil
}

func countValue(frames *[]frame, limits Limits) error {
	if len(*frames) == 0 {
		return nil
	}
	current := &(*frames)[len(*frames)-1]
	if current.object {
		if !current.expectKey {
			return &Error{Kind: KindMalformed, Reason: MalformedObjectValue, Msg: "object value without key"}
		}
		current.expectKey = false
		return nil
	}
	current.count++
	if current.count > limits.MaxArrayElems {
		return &Error{Kind: KindTooManyItems, Limit: limits.MaxArrayElems, Value: current.count}
	}
	return nil
}

func whitespaceOnly(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

func hasTrailingNonWhitespace(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			return true
		}
	}
	return false
}
