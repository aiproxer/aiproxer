package openresponses

import (
	"errors"
	"fmt"
)

// ErrLimitExceeded is the sentinel root error for limit exceedances.
var ErrLimitExceeded = errors.New("openresponses: limit exceeded")

// Limits holds independent bounded configuration thresholds for OpenResponses payload and state processing.
type Limits struct {
	MaxRequestSizeBytes         int
	MaxResourceSizeBytes        int
	MaxEventCount               int
	MaxItemCount                int
	MaxItemDepth                int
	MaxSchemaSizeBytes          int
	MaxOpaquePayloadSizeBytes   int
	MaxContinuationRefSizeBytes int
	MaxContinuationRefCount     int
}

// DefaultLimits returns the standard bounded limits for production OpenResponses operations.
func DefaultLimits() Limits {
	return Limits{
		MaxRequestSizeBytes:         8 * 1024 * 1024,  // 8 MiB max request payload
		MaxResourceSizeBytes:        16 * 1024 * 1024, // 16 MiB max resource payload
		MaxEventCount:               10000,            // 10,000 max stream events
		MaxItemCount:                1000,             // 1,000 max items per request/resource
		MaxItemDepth:                16,               // 16 max item nesting depth
		MaxSchemaSizeBytes:          1024 * 1024,      // 1 MiB max tool schema
		MaxOpaquePayloadSizeBytes:   512 * 1024,       // 512 KiB max extension payload
		MaxContinuationRefSizeBytes: 4096,             // 4,096 bytes max continuation reference
		MaxContinuationRefCount:     100,              // 100 max continuation references
	}
}

// LimitExceededError carries structured context when an operational limit is exceeded.
type LimitExceededError struct {
	Param   string
	Limit   int
	Actual  int
	Message string
	Err     error
}

func (e *LimitExceededError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("openresponses limit error [%s]: %s (actual %d > limit %d)", e.Param, e.Message, e.Actual, e.Limit)
	}
	return fmt.Sprintf("openresponses limit error [%s]: limit %d exceeded by actual %d", e.Param, e.Limit, e.Actual)
}

func (e *LimitExceededError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrLimitExceeded
}

// ValidateRequestBytes checks payload size before JSON unmarshal to prevent allocation amplification.
func ValidateRequestBytes(data []byte, limits Limits) error {
	if limits.MaxRequestSizeBytes > 0 && len(data) > limits.MaxRequestSizeBytes {
		return &LimitExceededError{
			Param:   "request_size",
			Limit:   limits.MaxRequestSizeBytes,
			Actual:  len(data),
			Message: "request payload size exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateResourceBytes checks resource size against independent limits.
func ValidateResourceBytes(data []byte, limits Limits) error {
	if limits.MaxResourceSizeBytes > 0 && len(data) > limits.MaxResourceSizeBytes {
		return &LimitExceededError{
			Param:   "resource_size",
			Limit:   limits.MaxResourceSizeBytes,
			Actual:  len(data),
			Message: "resource payload size exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateEventCount checks accumulated stream event count against limits.
func ValidateEventCount(count int, limits Limits) error {
	if limits.MaxEventCount > 0 && count > limits.MaxEventCount {
		return &LimitExceededError{
			Param:   "event_count",
			Limit:   limits.MaxEventCount,
			Actual:  count,
			Message: "accumulated event count exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateItemCount checks item array length against limits.
func ValidateItemCount(count int, limits Limits) error {
	if limits.MaxItemCount > 0 && count > limits.MaxItemCount {
		return &LimitExceededError{
			Param:   "item_count",
			Limit:   limits.MaxItemCount,
			Actual:  count,
			Message: "item count exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateItemDepth checks item nesting depth against limits.
func ValidateItemDepth(depth int, limits Limits) error {
	if limits.MaxItemDepth > 0 && depth > limits.MaxItemDepth {
		return &LimitExceededError{
			Param:   "item_depth",
			Limit:   limits.MaxItemDepth,
			Actual:  depth,
			Message: "item nesting depth exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateSchemaSize checks tool/output schema byte size against limits.
func ValidateSchemaSize(size int, limits Limits) error {
	if limits.MaxSchemaSizeBytes > 0 && size > limits.MaxSchemaSizeBytes {
		return &LimitExceededError{
			Param:   "schema_size",
			Limit:   limits.MaxSchemaSizeBytes,
			Actual:  size,
			Message: "schema size exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateOpaquePayloadSize checks vendor extension payload size against limits.
func ValidateOpaquePayloadSize(size int, limits Limits) error {
	if limits.MaxOpaquePayloadSizeBytes > 0 && size > limits.MaxOpaquePayloadSizeBytes {
		return &LimitExceededError{
			Param:   "opaque_payload_size",
			Limit:   limits.MaxOpaquePayloadSizeBytes,
			Actual:  size,
			Message: "opaque extension payload size exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateContinuationRef checks continuation reference string length against limits.
func ValidateContinuationRef(ref string, limits Limits) error {
	if limits.MaxContinuationRefSizeBytes > 0 && len(ref) > limits.MaxContinuationRefSizeBytes {
		return &LimitExceededError{
			Param:   "continuation_ref_size",
			Limit:   limits.MaxContinuationRefSizeBytes,
			Actual:  len(ref),
			Message: "continuation reference length exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}

// ValidateContinuationRefCount checks number of referenced continuation items against limits.
func ValidateContinuationRefCount(count int, limits Limits) error {
	if limits.MaxContinuationRefCount > 0 && count > limits.MaxContinuationRefCount {
		return &LimitExceededError{
			Param:   "continuation_ref_count",
			Limit:   limits.MaxContinuationRefCount,
			Actual:  count,
			Message: "continuation reference count exceeds limit",
			Err:     ErrLimitExceeded,
		}
	}
	return nil
}
