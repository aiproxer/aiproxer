package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// IsAmbiguousRenewError reports whether err means occupancy may still be held
// after an unconfirmed renew attempt (timeout/transport/unavailable).
func IsAmbiguousRenewError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, domain.ErrGenerationMismatch) || errors.Is(err, ErrNotFound) ||
		errors.Is(err, domain.ErrLeaseReleased) || errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, domain.ErrInvalidTiming) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"timeout", "timed out", "connection reset", "broken pipe", "unavailable", "temporary"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
