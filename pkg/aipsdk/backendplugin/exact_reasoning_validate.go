package backendplugin

import (
	"encoding/json"
	"fmt"
)

// validateExactReasoningRawFields mirrors the canonical OpenResponses shape:
// summary and content are arrays when present, while encrypted_content may be
// absent, null, or any valid JSON value.
func validateExactReasoningRawFields(summary, content, encrypted RawJSON, field string) error {
	for _, item := range []struct {
		name string
		raw  RawJSON
	}{{"summary", summary}, {"content", content}} {
		switch item.raw.State() {
		case RawJSONAbsent:
			continue
		case RawJSONNull:
			return fmt.Errorf("%w: %s.%s must be a JSON array, not null", ErrInvalidInvocation, field, item.name)
		case RawJSONValue:
			if err := item.raw.Validate(DefaultMaxRawJSONBytes); err != nil {
				return err
			}
			var array []json.RawMessage
			if err := json.Unmarshal(item.raw.Bytes(), &array); err != nil {
				return fmt.Errorf("%w: %s.%s must be a JSON array", ErrInvalidInvocation, field, item.name)
			}
		}
	}
	return encrypted.Validate(DefaultMaxRawJSONBytes)
}
