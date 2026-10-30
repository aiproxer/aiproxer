package openresponsescompat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// buildCredentialPool assembles the ordered per-instance credential pool from
// the structured credentials first and the legacy ordered key fields
// otherwise (requirement 3.1). An empty key set is the compatible no-auth
// mode: it returns no pool and the single attempt omits Authorization.
// A nil pool is the compatible no-auth mode: the single attempt proceeds
// without an Authorization header.
func buildCredentialPool(spec BackendSpec) (*credpool.Pool, error) {
	if len(spec.Credentials) > 0 {
		return credpool.New(spec.Credentials)
	}
	pool, err := credpool.NewPoolFromBackendKeys(spec.APIKey, spec.APIKeys)
	if err != nil {
		return nil, nil
	}
	return pool, nil
}

// classifyCredentialError translates one attempt failure into the shared
// quota-aware selection contract (requirements 3.1–3.3): 401 retires the key,
// a 429 carries its Retry-After hint for the threshold decision, and every
// other pre-output transport/protocol/parse failure stays a retryable
// failover candidate without touching pool state, preserving the existing
// pre-output classification. Terminal 4xx failures stay terminal.
func classifyCredentialError(err error) (credpool.FailureClass, string) {
	if hf, ok := errors.AsType[*httpFailureError](err); ok {
		switch hf.Kind {
		case httpFailureAuthInvalid:
			return credpool.FailureAuthInvalid, ""
		case httpFailureRateLimited:
			return credpool.FailureRateLimited, hf.RetryAfter
		case httpFailureRetryable:
			return credpool.FailureRetryable, ""
		default:
			return credpool.FailureNeutral, ""
		}
	}
	if transporterr.IsRetryable(err) {
		return credpool.FailureRetryable, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return credpool.FailureNeutral, ""
	}
	return credpool.FailureRetryable, ""
}

// executeWithPool runs fn against the ordered credentials before any output:
// the first usable key stays preferred, auth-invalid and long-hint 429s
// advance pre-output, and short/missing/malformed hints never consume the
// secondary key. Classified pool exhaustion returns a recoverable pre-output
// error for core-owned routing; unrelated terminal failures return as-is.
// A nil pool (compatible no-auth) runs fn once with an empty credential.
func executeWithPool(ctx context.Context, id string, pool *credpool.Pool, threshold time.Duration, fn func(context.Context, credpool.Credential) (aipapi.ManagedEventStream, error)) (aipapi.ManagedEventStream, error) {
	if pool == nil {
		return fn(ctx, credpool.Credential{})
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	stream, runErr := credpool.Do(ctx, coord, classifyCredentialError, fn)
	if runErr == nil {
		return stream, nil
	}
	if errors.Is(runErr, credpool.ErrTransientThrottle) ||
		errors.Is(runErr, credpool.ErrQuotaExhausted) ||
		errors.Is(runErr, credpool.ErrUpstreamRetryable) ||
		errors.Is(runErr, credpool.ErrNoUsableCredential) {
		return nil, aipapi.RecoverablePreOutputError(runErr)
	}
	return nil, runErr
}
