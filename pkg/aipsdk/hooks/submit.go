package hooks

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrSubmitRejected is returned when a submit hook rejects the call.
var ErrSubmitRejected = errors.New("aipsdk/hooks: submit hook rejected request")

// SubmitRejectError carries a deterministic rejection reason from a submit hook.
type SubmitRejectError struct {
	HookID string
	Reason string
}

func (e *SubmitRejectError) Error() string {
	if e.HookID != "" {
		return fmt.Sprintf("submit hook %q rejected: %s", e.HookID, e.Reason)
	}
	return "submit hook rejected: " + e.Reason
}

func (e *SubmitRejectError) Unwrap() error { return ErrSubmitRejected }

// IsSubmitReject reports whether err is or wraps a submit rejection.
func IsSubmitReject(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSubmitRejected) {
		return true
	}
	var sr *SubmitRejectError
	return errors.As(err, &sr)
}

// SubmitMeta carries core-owned metadata visible to submit hooks (no provider types).
type SubmitMeta struct {
	TraceID string
	// Annotations is a small key-value bag hooks may extend for downstream diagnostics.
	Annotations map[string]string
}

// SubmitDecision is the outcome of a single submit hook invocation.
type SubmitDecision struct {
	// Reject stops the chain; Reason is surfaced via SubmitRejectError.
	Reject bool
	Reason string
}

// SubmitHook runs before route planning on the canonical call.
type SubmitHook interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	Handle(ctx context.Context, call *aipapi.Call, meta *SubmitMeta) (SubmitDecision, error)
}
