package extractor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func requireFields(data []byte, fields []string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: malformed object: %v", ErrInvalidResult, err)
	}
	for _, field := range fields {
		value, ok := raw[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: missing or null %s", ErrInvalidResult, field)
		}
	}
	return nil
}

func checkDepth(data []byte, max int) error {
	depth := 0
	insideString := false
	escaped := false
	for _, b := range data {
		if insideString {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				insideString = false
			}
			continue
		}
		switch b {
		case '"':
			insideString = true
		case '{', '[':
			depth++
			if depth > max {
				return fmt.Errorf("%w: JSON depth exceeds %d", ErrInvalidResult, max)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, isDelim := token.(json.Delim)
		if !isDelim {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("%w: object key", ErrInvalidResult)
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("%w: duplicate object key %q", ErrInvalidResult, name)
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%w: unexpected JSON delimiter", ErrInvalidResult)
		}
		_, err = dec.Token()
		return err
	}
	if err := walk(); err != nil {
		return fmt.Errorf("%w: malformed JSON: %v", ErrInvalidResult, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON", ErrInvalidResult)
	}
	return nil
}
