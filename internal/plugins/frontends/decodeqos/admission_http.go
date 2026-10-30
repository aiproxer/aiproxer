package decodeqos

import (
	"context"
	"errors"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
)

const (
	// RetryAfterSeconds is the Retry-After header value for decode admission 429 responses.
	RetryAfterSeconds = "1"
	// AdmissionRejectedWireMessage is the stable client-safe message for decode admission saturation.
	AdmissionRejectedWireMessage = "decode admission capacity exceeded"
)

// TryAcquirer is the minimal admission surface used by frontend handlers.
type TryAcquirer interface {
	TryAcquire(ctx context.Context, weight int64) (release func(), ok bool, err error)
}

// TryAdmit calls TryAcquire when a is non-nil; nil means unlimited (custom/manual mounts).
func TryAdmit(ctx context.Context, a TryAcquirer, weight int64) (release func(), ok bool, err error) {
	if a == nil {
		return func() {}, true, nil
	}
	return a.TryAcquire(ctx, weight)
}

// Guard runs fn while holding an admission release and always releases exactly once
// (including when fn panics). Use around protocol adapter Decode only.
func Guard(release func(), fn func() error) error {
	if release == nil {
		release = func() {}
	}
	defer release()
	return fn()
}

// Decision is the shared admission-reject outcome for frontend handlers.
// Status 0 means admission succeeded (no reject response).
type Decision struct {
	Status     int
	RetryAfter bool
	Message    string
}

// Decide maps TryAcquire outcome to status, Retry-After flag, and stable wire message.
func Decide(ok bool, err error) Decision {
	status, retryAfter := HTTPStatus(ok, err)
	if status == 0 {
		return Decision{}
	}
	msg := execerr.InternalWireMessage
	if status == http.StatusTooManyRequests {
		msg = AdmissionRejectedWireMessage
	}
	return Decision{Status: status, RetryAfter: retryAfter, Message: msg}
}

// HTTPStatus maps TryAcquire outcome to an HTTP status code. Zero means admission succeeded.
func HTTPStatus(ok bool, err error) (status int, retryAfter bool) {
	if ok {
		return 0, false
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return http.StatusServiceUnavailable, false
		}
		if errors.Is(err, ErrOverweight) {
			return http.StatusTooManyRequests, true
		}
		if errors.Is(err, ErrInvalidWeight) {
			return http.StatusInternalServerError, false
		}
		return http.StatusServiceUnavailable, false
	}
	return http.StatusTooManyRequests, true
}
