package openresponses

import (
	"errors"
	"strings"
	"testing"
)

func TestLimits_DefaultLimits(t *testing.T) {
	t.Parallel()
	lim := DefaultLimits()
	if lim.MaxRequestSizeBytes <= 0 {
		t.Errorf("expected positive MaxRequestSizeBytes, got %d", lim.MaxRequestSizeBytes)
	}
	if lim.MaxResourceSizeBytes <= 0 {
		t.Errorf("expected positive MaxResourceSizeBytes, got %d", lim.MaxResourceSizeBytes)
	}
	if lim.MaxEventCount <= 0 {
		t.Errorf("expected positive MaxEventCount, got %d", lim.MaxEventCount)
	}
	if lim.MaxItemCount <= 0 {
		t.Errorf("expected positive MaxItemCount, got %d", lim.MaxItemCount)
	}
	if lim.MaxItemDepth <= 0 {
		t.Errorf("expected positive MaxItemDepth, got %d", lim.MaxItemDepth)
	}
	if lim.MaxSchemaSizeBytes <= 0 {
		t.Errorf("expected positive MaxSchemaSizeBytes, got %d", lim.MaxSchemaSizeBytes)
	}
	if lim.MaxOpaquePayloadSizeBytes <= 0 {
		t.Errorf("expected positive MaxOpaquePayloadSizeBytes, got %d", lim.MaxOpaquePayloadSizeBytes)
	}
	if lim.MaxContinuationRefSizeBytes <= 0 {
		t.Errorf("expected positive MaxContinuationRefSizeBytes, got %d", lim.MaxContinuationRefSizeBytes)
	}
	if lim.MaxContinuationRefCount <= 0 {
		t.Errorf("expected positive MaxContinuationRefCount, got %d", lim.MaxContinuationRefCount)
	}
}

func TestLimits_Validations(t *testing.T) {
	t.Parallel()
	lim := Limits{
		MaxRequestSizeBytes:         100,
		MaxResourceSizeBytes:        200,
		MaxEventCount:               10,
		MaxItemCount:                5,
		MaxItemDepth:                3,
		MaxSchemaSizeBytes:          50,
		MaxOpaquePayloadSizeBytes:   50,
		MaxContinuationRefSizeBytes: 20,
		MaxContinuationRefCount:     2,
	}

	t.Run("validate request bytes", func(t *testing.T) {
		t.Parallel()
		if err := ValidateRequestBytes(make([]byte, 50), lim); err != nil {
			t.Errorf("unexpected error for valid request bytes: %v", err)
		}
		err := ValidateRequestBytes(make([]byte, 101), lim)
		if err == nil {
			t.Fatal("expected error for oversized request bytes")
		}
		var limErr *LimitExceededError
		if !errors.As(err, &limErr) {
			t.Fatalf("expected LimitExceededError, got %T", err)
		}
		if limErr.Param != "request_size" {
			t.Errorf("expected Param=request_size, got %s", limErr.Param)
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected error to wrap ErrLimitExceeded")
		}
	})

	t.Run("validate resource bytes", func(t *testing.T) {
		t.Parallel()
		if err := ValidateResourceBytes(make([]byte, 150), lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateResourceBytes(make([]byte, 201), lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate event count", func(t *testing.T) {
		t.Parallel()
		if err := ValidateEventCount(5, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateEventCount(11, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate item count", func(t *testing.T) {
		if err := ValidateItemCount(3, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateItemCount(6, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate item depth", func(t *testing.T) {
		if err := ValidateItemDepth(2, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateItemDepth(4, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate schema size", func(t *testing.T) {
		if err := ValidateSchemaSize(30, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateSchemaSize(51, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate opaque payload size", func(t *testing.T) {
		if err := ValidateOpaquePayloadSize(30, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateOpaquePayloadSize(51, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate continuation ref", func(t *testing.T) {
		if err := ValidateContinuationRef("resp_12345", lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		longRef := strings.Repeat("a", 25)
		err := ValidateContinuationRef(longRef, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})

	t.Run("validate continuation ref count", func(t *testing.T) {
		if err := ValidateContinuationRefCount(2, lim); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		err := ValidateContinuationRefCount(3, lim)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded")
		}
	})
}

func TestLimitExceededError_Sanitization(t *testing.T) {
	t.Parallel()
	err := &LimitExceededError{
		Param:   "secret_param",
		Limit:   100,
		Actual:  200,
		Message: "limit exceeded for secret_param",
		Err:     ErrLimitExceeded,
	}

	msg := err.Error()
	if !strings.Contains(msg, "limit exceeded") {
		t.Errorf("expected error message to contain limit info, got: %s", msg)
	}
	if err.Unwrap() != ErrLimitExceeded {
		t.Errorf("expected Unwrap to return ErrLimitExceeded")
	}
}
