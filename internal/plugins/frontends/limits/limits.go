package limits

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Adapter-local decode limits. These intentionally mirror canonical validation
// caps but fail before protocol adapters allocate, copy, or reparse large values.
const (
	MaxMessages       = aipapi.MaxMessages
	MaxParts          = aipapi.MaxPartsPerMessage
	MaxTools          = aipapi.MaxTools
	MaxToolSchema     = aipapi.MaxToolParametersBytes
	MaxRawJSONPayload = aipapi.MaxPartJSONBytes
	MaxMetadata       = 256
	MaxBase64Data     = aipapi.MaxRefStringBytes
)

func Count(field string, got, limit int) error {
	if got > limit {
		return fmt.Errorf("%s has %d entries; maximum is %d", field, got, limit)
	}
	return nil
}

func Bytes(field string, got, limit int) error {
	if got > limit {
		return fmt.Errorf("%s has %d bytes; maximum is %d", field, got, limit)
	}
	return nil
}

func StringBytes(field, value string, limit int) error {
	return Bytes(field, len(value), limit)
}
