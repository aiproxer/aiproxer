package backendplugin

import (
	"encoding/json"
	"fmt"
)

// RawJSONState distinguishes absent, JSON null, and present JSON bytes.
type RawJSONState int

const (
	// RawJSONAbsent means the field was omitted.
	RawJSONAbsent RawJSONState = iota
	// RawJSONNull means the field was explicitly JSON null.
	RawJSONNull
	// RawJSONValue means the field contains empty or non-empty JSON bytes.
	RawJSONValue
)

const maxRawJSONDepth = 64

// RawJSON is a presence-preserving opaque JSON carrier.
type RawJSON struct {
	state RawJSONState
	data  []byte
}

// RawJSONAbsentValue returns an omitted raw JSON field.
func RawJSONAbsentValue() RawJSON { return RawJSON{state: RawJSONAbsent} }

// RawJSONNullValue returns an explicit JSON null field.
func RawJSONNullValue() RawJSON { return RawJSON{state: RawJSONNull} }

// RawJSONFromBytes returns a present JSON value (empty or non-empty).
func RawJSONFromBytes(b []byte) RawJSON {
	return RawJSON{state: RawJSONValue, data: append([]byte(nil), b...)}
}

// State returns the presence state.
func (r RawJSON) State() RawJSONState { return r.state }

// Bytes returns a copy of present JSON bytes, or nil when absent/null.
func (r RawJSON) Bytes() []byte {
	if r.state != RawJSONValue {
		return nil
	}
	return append([]byte(nil), r.data...)
}

// Validate enforces size, syntax, and depth bounds for present values.
func (r RawJSON) Validate(maxBytes uint64) error {
	if r.state != RawJSONValue {
		return nil
	}
	if err := ValidateRawJSONSize(uint64(len(r.data)), maxBytes); err != nil {
		return err
	}
	if len(r.data) == 0 {
		return nil
	}
	if !json.Valid(r.data) {
		return fmt.Errorf("%w: raw JSON must be valid", ErrInvalidInvocation)
	}
	return validateRawJSONDepth(r.data, maxRawJSONDepth)
}

func validateRawJSONDepth(data []byte, maxDepth int) error {
	var depth int
	inString := false
	escaped := false
	for i := range data {
		b := data[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf("%w: raw JSON depth exceeds %d", ErrInvalidInvocation, maxDepth)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("%w: raw JSON has mismatched brackets", ErrInvalidInvocation)
			}
		}
	}
	if inString {
		return fmt.Errorf("%w: raw JSON has unterminated string", ErrInvalidInvocation)
	}
	if depth != 0 {
		return fmt.Errorf("%w: raw JSON has mismatched brackets", ErrInvalidInvocation)
	}
	return nil
}
