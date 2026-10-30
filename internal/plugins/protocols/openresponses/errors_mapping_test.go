package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorMapping_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		inputErr               error
		expectedStatus         int
		expectedType           string
		expectedClassification ErrorClassification
	}{
		{
			name:                   "ErrDecodeFailed",
			inputErr:               ErrDecodeFailed,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "Wrapped ErrDecodeFailed",
			inputErr:               fmt.Errorf("json parse error: %w", ErrDecodeFailed),
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ErrUnsupportedBackground",
			inputErr:               ErrUnsupportedBackground,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ErrUnknownDiscriminator",
			inputErr:               ErrUnknownDiscriminator,
			expectedStatus:         400,
			expectedType:           "unsupported_parameter_error",
			expectedClassification: ClassificationUnsupportedParameter,
		},
		{
			name:                   "ErrDuplicateAuthority",
			inputErr:               ErrDuplicateAuthority,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ErrTrailingData",
			inputErr:               ErrTrailingData,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ErrEncodeFailed",
			inputErr:               ErrEncodeFailed,
			expectedStatus:         500,
			expectedType:           "server_error",
			expectedClassification: ClassificationInternalError,
		},
		{
			name:                   "ErrBuildResourceFailed",
			inputErr:               ErrBuildResourceFailed,
			expectedStatus:         500,
			expectedType:           "server_error",
			expectedClassification: ClassificationInternalError,
		},
		{
			name:                   "ErrSequenceViolation",
			inputErr:               ErrSequenceViolation,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ErrDuplicateTerminal",
			inputErr:               ErrDuplicateTerminal,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "LimitExceededError_RequestSize",
			inputErr:               &LimitExceededError{Param: "request_size", Limit: 100, Actual: 200, Message: "request size limit exceeded", Err: ErrLimitExceeded},
			expectedStatus:         413,
			expectedType:           "payload_too_large_error",
			expectedClassification: ClassificationPayloadTooLarge,
		},
		{
			name:                   "LimitExceededError_ItemDepth",
			inputErr:               &LimitExceededError{Param: "item_depth", Limit: 16, Actual: 17, Message: "item nesting depth exceeds limit", Err: ErrLimitExceeded},
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "LimitExceededError_RequestShape",
			inputErr:               &LimitExceededError{Param: "request_shape", Limit: 100000, Actual: 100001, Message: "request JSON shape exceeds limit", Err: ErrLimitExceeded},
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "LimitExceededError_ItemCount",
			inputErr:               &LimitExceededError{Param: "item_count", Limit: 10, Actual: 20, Message: "item count limit exceeded", Err: ErrLimitExceeded},
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "ContextDeadlineExceeded",
			inputErr:               context.DeadlineExceeded,
			expectedStatus:         504,
			expectedType:           "timeout_error",
			expectedClassification: ClassificationTimeout,
		},
		{
			name:                   "ContextCanceled",
			inputErr:               context.Canceled,
			expectedStatus:         400,
			expectedType:           "invalid_request_error",
			expectedClassification: ClassificationInvalidRequest,
		},
		{
			name:                   "UnknownInternalError",
			inputErr:               errors.New("something went wrong inside system"),
			expectedStatus:         500,
			expectedType:           "server_error",
			expectedClassification: ClassificationInternalError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, wireEnv, class := MapErrorToWire(tc.inputErr)
			if status != tc.expectedStatus {
				t.Errorf("status: expected %d, got %d", tc.expectedStatus, status)
			}
			if wireEnv.Error.Type != tc.expectedType {
				t.Errorf("error type: expected %q, got %q", tc.expectedType, wireEnv.Error.Type)
			}
			if class != tc.expectedClassification {
				t.Errorf("classification: expected %q, got %q", tc.expectedClassification, class)
			}
			if wireEnv.Error.Message == "" {
				t.Error("error message should not be empty")
			}

			// Ensure wire envelope serializes to valid JSON
			data, err := json.Marshal(wireEnv)
			if err != nil {
				t.Fatalf("failed to marshal wire envelope: %v", err)
			}
			var roundTrip WireErrorEnvelope
			if err := json.Unmarshal(data, &roundTrip); err != nil {
				t.Fatalf("failed to unmarshal wire envelope: %v", err)
			}
			if roundTrip.Error.Type != wireEnv.Error.Type {
				t.Errorf("roundtrip type mismatch: %q vs %q", roundTrip.Error.Type, wireEnv.Error.Type)
			}
		})
	}
}

func TestErrorMapping_Sanitization(t *testing.T) {
	t.Parallel()
	sensitiveErr := fmt.Errorf("database connection failed at postgres://user:secret123@db.internal:5432/mydb: %w", ErrBuildResourceFailed)
	_, wireEnv, _ := MapErrorToWire(sensitiveErr)

	if strings.Contains(wireEnv.Error.Message, "secret123") {
		t.Errorf("sanitized error message leaked secret: %s", wireEnv.Error.Message)
	}
	if strings.Contains(wireEnv.Error.Message, "postgres://") {
		t.Errorf("sanitized error message leaked internal connection string: %s", wireEnv.Error.Message)
	}
}
