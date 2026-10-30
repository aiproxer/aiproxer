package openresponses

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrDecodeFailed          = errors.New("openresponses: decode failed")
	ErrUnsupportedBackground = errors.New("openresponses: background execution mode is unsupported")
	ErrUnknownDiscriminator  = errors.New("openresponses: unknown unprefixed discriminator")
	ErrDuplicateAuthority    = errors.New("openresponses: conflicting raw item and legacy message authorities")
	ErrTrailingData          = errors.New("openresponses: trailing data after JSON payload")
	ErrEncodeFailed          = errors.New("openresponses: encode failed")
	ErrBuildResourceFailed   = errors.New("openresponses: resource build failed")
	ErrSequenceViolation     = errors.New("openresponses: sequence violation")
	ErrDuplicateTerminal     = errors.New("openresponses: duplicate terminal event")
	ErrOutputAfterTerminal   = errors.New("openresponses: output emitted after terminal event")
	ErrMismatchedID          = errors.New("openresponses: mismatched item or call ID")
	ErrInvalidLifecycleState = errors.New("openresponses: invalid lifecycle transition")
)

// ErrorClassification represents internal stable error taxonomy categories for OpenResponses.
type ErrorClassification string

const (
	ClassificationInvalidRequest       ErrorClassification = "invalid_request_error"
	ClassificationAuthenticationFailed ErrorClassification = "authentication_error"
	ClassificationPermissionDenied     ErrorClassification = "permission_error"
	ClassificationNotFound             ErrorClassification = "not_found_error"
	ClassificationRateLimitExceeded    ErrorClassification = "rate_limit_error"
	ClassificationPayloadTooLarge      ErrorClassification = "payload_too_large_error"
	ClassificationUnsupportedParameter ErrorClassification = "unsupported_parameter_error"
	ClassificationInternalError        ErrorClassification = "server_error"
	ClassificationBackendUnavailable   ErrorClassification = "service_unavailable_error"
	ClassificationTimeout              ErrorClassification = "timeout_error"
)

// WireErrorDetails represents the inner error structure on the wire for OpenResponses errors.
type WireErrorDetails struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

// WireErrorEnvelope represents the top-level wire error response payload.
type WireErrorEnvelope struct {
	Error WireErrorDetails `json:"error"`
}

// SequenceError carries structured context for sequence and lifecycle errors.
type SequenceError struct {
	Code     string
	Event    string
	ID       string
	Sequence int
	Message  string
	Err      error
}

func (e *SequenceError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("openresponses sequence error [event=%s id=%s seq=%d]: %s: %v", e.Event, e.ID, e.Sequence, e.Message, e.Err)
	}
	return fmt.Sprintf("openresponses sequence error [event=%s id=%s seq=%d]: %s", e.Event, e.ID, e.Sequence, e.Message)
}

func (e *SequenceError) Unwrap() error {
	return e.Err
}

// MapErrorToWire classifies internal errors and maps them to HTTP status code, WireErrorEnvelope, and ErrorClassification.
// Wire error messages are sanitized to prevent leaking raw payloads, secrets, or internal stack traces.
func MapErrorToWire(err error) (int, WireErrorEnvelope, ErrorClassification) {
	if err == nil {
		return http.StatusOK, WireErrorEnvelope{}, ""
	}

	if limErr, ok := errors.AsType[*LimitExceededError](err); ok {
		status := http.StatusBadRequest
		class := ClassificationInvalidRequest
		wireType := string(ClassificationInvalidRequest)
		if limErr.Param == "request_size" || limErr.Param == "resource_size" {
			status = http.StatusRequestEntityTooLarge // 413
			class = ClassificationPayloadTooLarge
			wireType = string(ClassificationPayloadTooLarge)
		}
		return status, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    wireType,
				Message: err.Error(),
				Param:   limErr.Param,
				Code:    "limit_exceeded",
			},
		}, class
	}

	if seqErr, ok := errors.AsType[*SequenceError](err); ok {
		return http.StatusBadRequest, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationInvalidRequest),
				Message: seqErr.Message,
				Code:    seqErr.Code,
			},
		}, ClassificationInvalidRequest
	}

	if errors.Is(err, ErrUnknownDiscriminator) {
		return http.StatusBadRequest, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationUnsupportedParameter),
				Message: "request contains unknown or unsupported discriminator",
				Code:    "unknown_discriminator",
			},
		}, ClassificationUnsupportedParameter
	}

	if errors.Is(err, ErrDecodeFailed) ||
		errors.Is(err, ErrUnsupportedBackground) ||
		errors.Is(err, ErrDuplicateAuthority) ||
		errors.Is(err, ErrTrailingData) ||
		errors.Is(err, ErrSequenceViolation) ||
		errors.Is(err, ErrDuplicateTerminal) ||
		errors.Is(err, ErrOutputAfterTerminal) ||
		errors.Is(err, ErrMismatchedID) ||
		errors.Is(err, ErrInvalidLifecycleState) {
		return http.StatusBadRequest, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationInvalidRequest),
				Message: err.Error(),
				Code:    "invalid_request",
			},
		}, ClassificationInvalidRequest
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationTimeout),
				Message: "request processing timed out",
				Code:    "timeout",
			},
		}, ClassificationTimeout
	}

	if errors.Is(err, context.Canceled) {
		return http.StatusBadRequest, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationInvalidRequest),
				Message: "request was canceled by client",
				Code:    "client_closed_request",
			},
		}, ClassificationInvalidRequest
	}

	if errors.Is(err, ErrEncodeFailed) || errors.Is(err, ErrBuildResourceFailed) {
		return http.StatusInternalServerError, WireErrorEnvelope{
			Error: WireErrorDetails{
				Type:    string(ClassificationInternalError),
				Message: "internal server error during response processing",
				Code:    "internal_error",
			},
		}, ClassificationInternalError
	}

	// Default fallback for unspecified/internal errors
	return http.StatusInternalServerError, WireErrorEnvelope{
		Error: WireErrorDetails{
			Type:    string(ClassificationInternalError),
			Message: "an internal server error occurred",
			Code:    "internal_error",
		},
	}, ClassificationInternalError
}

// sanitizeErrorMessage applies a strict wire-message policy. Internal error
// strings are never safe by default: they may contain payloads, credentials,
// URLs, stack traces, or provider-specific details. Callers use stable category
// codes alongside this intentionally generic message.
func sanitizeErrorMessage(_ string) string {
	return "an internal system error occurred"
}
