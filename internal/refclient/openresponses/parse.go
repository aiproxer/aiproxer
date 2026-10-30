package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Sentinel errors for the independent client parser.
var (
	ErrMalformed        = errors.New("refclient/openresponses: malformed payload")
	ErrRequiredPresence = errors.New("refclient/openresponses: missing required field")
	ErrExceedsLimit     = errors.New("refclient/openresponses: exceeds parse limit")
	ErrEventMismatch    = errors.New("refclient/openresponses: event/type mismatch")
	ErrSequence         = errors.New("refclient/openresponses: sequence violation")
	ErrSSEDone          = errors.New("refclient/openresponses: [DONE] terminal")
	ErrNotExtension     = errors.New("refclient/openresponses: not an extension item")
)

// ParseOptions bounds every independent parse. Zero values fall back to defaults on use.
type ParseOptions struct {
	MaxBodyBytes    int64
	MaxEventBytes   int
	MaxLineBytes    int
	MaxItems        int
	MaxContentParts int
	MaxEvents       int
}

// DefaultParseOptions returns the documented bounded-parse profile.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{
		MaxBodyBytes:    4 << 20,
		MaxEventBytes:   1 << 20,
		MaxLineBytes:    1 << 20,
		MaxItems:        256,
		MaxContentParts: 1024,
		MaxEvents:       65536,
	}
}

// normalize fills zero limits with defaults so callers may pass partial options.
func (o ParseOptions) normalize() ParseOptions {
	d := DefaultParseOptions()
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = d.MaxBodyBytes
	}
	if o.MaxEventBytes <= 0 {
		o.MaxEventBytes = d.MaxEventBytes
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = d.MaxLineBytes
	}
	if o.MaxItems <= 0 {
		o.MaxItems = d.MaxItems
	}
	if o.MaxContentParts <= 0 {
		o.MaxContentParts = d.MaxContentParts
	}
	if o.MaxEvents <= 0 {
		o.MaxEvents = d.MaxEvents
	}
	return o
}

// ParseError carries the stable category and safe message for a parse failure.
type ParseError struct {
	Category string
	Message  string
	Err      error
}

func (e *ParseError) Error() string { return e.Message }

func (e *ParseError) Unwrap() error { return e.Err }

func malformedf(format string, args ...any) error {
	return &ParseError{Category: "malformed", Message: fmt.Sprintf(format, args...), Err: ErrMalformed}
}

func presencef(field string) error {
	return &ParseError{
		Category: "required_presence",
		Message:  fmt.Sprintf("missing required field %q", field),
		Err:      ErrRequiredPresence,
	}
}

func limitf(format string, args ...any) error {
	return &ParseError{Category: "limit", Message: fmt.Sprintf(format, args...), Err: ErrExceedsLimit}
}

// boundedReader caps total bytes read from an underlying reader.
type boundedReader struct {
	r   io.Reader
	rem int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.rem <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.rem {
		p = p[:b.rem]
	}
	n, err := b.r.Read(p)
	b.rem -= int64(n)
	return n, err
}

// readBounded reads at most maxBytes bytes and fails when the source exceeds the cap.
func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultParseOptions().MaxBodyBytes
	}
	br := &boundedReader{r: r, rem: maxBytes + 1}
	data, err := io.ReadAll(br)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, limitf("body exceeds %d bytes", maxBytes)
	}
	return data, nil
}

// decodeObject decodes raw bytes into a map, rejecting non-object roots and empty bodies.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, malformedf("empty payload")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, malformedf("invalid JSON object: %v", err)
	}
	return m, nil
}

func rawString(raw json.RawMessage, required bool) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", malformedf("expected string, got %s", truncate(raw))
	}
	return s, nil
}

func rawInt64(raw json.RawMessage, required bool) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, malformedf("expected integer, got %s", truncate(raw))
	}
	return v, nil
}

func rawBool(raw json.RawMessage, required bool) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, malformedf("expected boolean, got %s", truncate(raw))
	}
	return v, nil
}

// truncate renders raw bytes safely for error messages.
func truncate(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// hasKey reports whether a raw object contains the key (including explicit null).
func hasKey(m map[string]json.RawMessage, key string) bool {
	_, ok := m[key]
	return ok
}
