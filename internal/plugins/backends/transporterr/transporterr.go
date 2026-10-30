// Package transporterr classifies transport-level failures (network timeouts, DNS
// errors, refused/reset/aborted connections) shared by backend adapters when deciding
// whether an upstream failure is a transient retry/failover candidate.
package transporterr

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// IsRetryable reports whether err is a transient transport failure: a network timeout,
// a DNS failure, or a refused/reset/aborted connection (possibly wrapped).
// Caller-side context cancellation/deadline is not a transport failure.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED)
}
