package openresponses

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

// validateJSONStrictWithLimits translates the protocol limits into the shared
// structural profile. Protocol-specific item/schema limits remain validated by
// their owning decoder after this generic pass.
func validateJSONStrictWithLimits(data []byte, configured Limits) error {
	defaults := DefaultLimits()
	if configured == (Limits{}) {
		configured = defaults
	}
	if configured.MaxRequestSizeBytes <= 0 {
		configured.MaxRequestSizeBytes = defaults.MaxRequestSizeBytes
	}
	if configured.MaxItemDepth <= 0 {
		configured.MaxItemDepth = defaults.MaxItemDepth
	}

	shape := jsonshape.RequestEnvelopeLimits()
	shape.MaxBytes = int64(configured.MaxRequestSizeBytes)
	shape.MaxDepth = configured.MaxItemDepth
	shape.RejectDuplicateNames = true
	_, err := jsonshape.Preflight(data, shape)
	if err == nil {
		return nil
	}
	return mapStrictJSONError(err)
}

func mapStrictJSONError(err error) error {
	if err == nil {
		return nil
	}
	var shapeErr *jsonshape.Error
	if !errors.As(err, &shapeErr) {
		return fmt.Errorf("%w: invalid JSON", ErrDecodeFailed)
	}
	switch shapeErr.Kind {
	case jsonshape.KindTooLarge:
		return &LimitExceededError{
			Param:   "request_size",
			Limit:   shapeErr.Limit,
			Actual:  shapeErr.Value,
			Message: "request payload size exceeds limit",
			Err:     ErrLimitExceeded,
		}
	case jsonshape.KindTooDeep:
		return &LimitExceededError{
			Param:   "item_depth",
			Limit:   shapeErr.Limit,
			Actual:  shapeErr.Value,
			Message: fmt.Sprintf("JSON depth %d exceeds limit %d", shapeErr.Value, shapeErr.Limit),
			Err:     ErrLimitExceeded,
		}
	case jsonshape.KindTooManyTokens, jsonshape.KindTooManyItems,
		jsonshape.KindStringTooLong, jsonshape.KindKeyTooLong, jsonshape.KindNumberTooLong:
		return &LimitExceededError{
			Param:   "request_shape",
			Limit:   shapeErr.Limit,
			Actual:  shapeErr.Value,
			Message: "request JSON shape exceeds limit",
			Err:     ErrLimitExceeded,
		}
	case jsonshape.KindCanceled:
		return fmt.Errorf("%w: request canceled", ErrDecodeFailed)
	case jsonshape.KindDuplicateName:
		return fmt.Errorf("%w: duplicate key", ErrDecodeFailed)
	case jsonshape.KindInvalidUTF8:
		return fmt.Errorf("%w: invalid UTF-8 encoding", ErrDecodeFailed)
	case jsonshape.KindMalformed:
		switch shapeErr.Reason {
		case jsonshape.MalformedTrailingData, jsonshape.MalformedMultipleValues:
			return ErrTrailingData
		case jsonshape.MalformedIncomplete:
			return fmt.Errorf("%w: unclosed JSON structure", ErrDecodeFailed)
		default:
			return fmt.Errorf("%w: malformed JSON", ErrDecodeFailed)
		}
	default:
		return fmt.Errorf("%w: malformed JSON", ErrDecodeFailed)
	}
}
