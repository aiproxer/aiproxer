package app_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// Phase 7.2: renew error classification model fuzz (requirements 13.3, 13.7).

func FuzzIsAmbiguousRenewError(f *testing.F) {
	seeds := []string{
		"",
		"timeout",
		"connection reset by peer",
		"service unavailable",
		"temporary failure",
		"broken pipe",
		"timed out waiting",
		"plain validation failure",
		"not related",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		got := app.IsAmbiguousRenewError(errors.New(msg))
		_ = got
		// Sentinel paths stay definitive regardless of message fuzzing.
		for _, definitive := range []error{
			domain.ErrLeaseReleased,
			app.ErrNotFound,
			domain.ErrGenerationMismatch,
			app.ErrInvalidInput,
			domain.ErrInvalidTiming,
		} {
			if app.IsAmbiguousRenewError(definitive) {
				t.Fatalf("definitive error must not be ambiguous: %v", definitive)
			}
		}
		for _, ambiguous := range []error{
			context.DeadlineExceeded,
			context.Canceled,
			app.ErrUnavailable,
			io.ErrUnexpectedEOF,
		} {
			if !app.IsAmbiguousRenewError(ambiguous) {
				t.Fatalf("transport/timeout must be ambiguous: %v", ambiguous)
			}
		}
	})
}
