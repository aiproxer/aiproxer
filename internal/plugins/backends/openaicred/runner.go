package openaicred

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ExecuteWithCredentialPool executes fn within the ordered quota-aware
// credential loop before any output: the first usable key stays preferred,
// a 429 with a trustworthy Retry-After strictly greater than threshold
// quota-cools the key and advances, and anything else (short/missing/invalid
// hint, auth-invalid, retryable, fatal) follows the coordinator contract. A
// non-positive threshold selects credpool.DefaultQuotaSwitchThreshold (1h).
// Classified pool failures return a recoverable pre-output error for
// core-owned routing; unrelated provider errors return as-is. When pool is
// nil, fn is called once with an empty credential.
func ExecuteWithCredentialPool(
	ctx context.Context,
	providerID string,
	pool *credpool.Pool,
	threshold time.Duration,
	fn func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error),
) (aipapi.ManagedEventStream, error) {
	if pool == nil {
		return fn(ctx, credpool.Credential{})
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", providerID, err)
	}
	var selected credpool.Credential
	stream, runErr := credpool.Do(ctx, coord, func(err error) (credpool.FailureClass, string) {
		kind, hint := ClassifyOpenAIAPIError(err)
		switch kind {
		case FailureAuthInvalid:
			return credpool.FailureAuthInvalid, ""
		case FailureRateLimited:
			return credpool.FailureRateLimited, hint
		case FailureRetryable:
			return credpool.FailureRetryable, ""
		default:
			return credpool.FailureNeutral, ""
		}
	}, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		selected = cred
		return fn(ctx, cred)
	})
	if runErr == nil {
		// Report the credential identity only for the attempt that produced a
		// usable stream. Failed rotation attempts must not leave a stale
		// identity in the collector. The value is a non-secret equality token;
		// the host authority publishes at most a fixed label.
		if stream != nil {
			execbackend.ReportAttemptIdentity(ctx, execbackend.AttemptIdentity{Credential: credentialFingerprint(selected)})
		}
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
