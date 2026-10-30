package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// watsonx credentials: WATSONX_API_KEY[_N]. Secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
// Each slot authenticates its own IAM token exchange and refresh, isolated
// under that slot's key and never pooled as static bearer material.
const EnvAPIKeyRoot = "WATSONX_API_KEY"

// QuotaDomain is the explicit nonsecret quota scope for watsonx credential
// selection: rotation decisions never alias an unrelated provider or
// project cooldown to this domain.
const QuotaDomain = "watsonx"

// decodeAPIKeys validates host-issued ordered api_key Configure secrets
// through the shared slot decoder and returns slots in ascending order with
// their nonsecret slot identities. Sparse host numbering is preserved as
// ascending order; every rejection is fail-closed and secret-free. The
// secret-shape policy is single-sourced with the rest of the cohort; the
// identity binding lets per-slot IAM providers key off slot names.
func decodeAPIKeys(bundle backendplugin.SecretBundle) ([]openaicompat.SlotSecret, error) {
	return openaicompat.DecodeAPIKeySlotSecrets(bundle)
}

// credentialSet lifts ordered slots into pool credentials keyed by nonsecret
// slot name, so per-slot IAM providers stay bound to coordinator selection.
func credentialSet(slots []openaicompat.SlotSecret) []credpool.Credential {
	creds := make([]credpool.Credential, 0, len(slots))
	for _, slot := range slots {
		if strings.TrimSpace(slot.Secret) == "" {
			continue
		}
		creds = append(creds, credpool.Credential{ID: slot.Name, Secret: slot.Secret})
	}
	return creds
}

// classifyKeyError maps watsonx-wire failures to coordinator classes. Both
// ML API errors and IAM exchange errors classify under the authenticating
// slot: 401 (ML) and 400/401 (IAM, meaning the slot's api_key is invalid)
// are auth-invalid and advance pre-output; 429 carries the raw Retry-After
// hint for threshold-gated quota rotation; 408/5xx are transient upstream
// failures with no credential evidence; every other outcome (including
// 402/403 provider errors) is neutral and returns as-is without consuming
// another credential.
func classifyKeyError(err error) (credpool.FailureClass, string) {
	if err == nil {
		return credpool.FailureNeutral, ""
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		return classifyStatus(httpErr.StatusCode, httpErr.RetryAfter)
	}
	var iamErr *IAMError
	if errors.As(err, &iamErr) && iamErr != nil {
		// Typed IAM exchange failures classify under the authenticating
		// slot: a 400/401 means the slot's api_key is invalid. Wrapped
		// transport errors (nil-typed match) stay neutral.
		if iamErr.StatusCode == http.StatusBadRequest {
			return credpool.FailureAuthInvalid, ""
		}
		return classifyStatus(iamErr.StatusCode, iamErr.RetryAfter)
	}
	return credpool.FailureNeutral, ""
}

func classifyStatus(status int, retryAfter string) (credpool.FailureClass, string) {
	switch status {
	case http.StatusUnauthorized:
		return credpool.FailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		return credpool.FailureRateLimited, strings.TrimSpace(retryAfter)
	case http.StatusRequestTimeout:
		return credpool.FailureRetryable, ""
	default:
		if status >= 500 {
			return credpool.FailureRetryable, ""
		}
		return credpool.FailureNeutral, ""
	}
}

// runWithKeys executes fn against ordered credentials with quota-aware
// selection under the proxy-wide default quota-switch threshold. Attempts
// never exceed the pool size; the first usable key stays preferred.
func runWithKeys[T any](ctx context.Context, pool *credpool.Pool, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	return runWithKeysThreshold(ctx, pool, 0, fn)
}

// runWithKeysThreshold executes fn with an explicit quota-switch threshold from host policy.
func runWithKeysThreshold[T any](ctx context.Context, pool *credpool.Pool, threshold time.Duration, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	var zero T
	if pool == nil {
		return zero, credpool.ErrNoUsableCredential
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return zero, err
	}
	return credpool.Do(ctx, coord, classifyKeyError, fn)
}

// recoverableRunWithKeys maps pool-exhaustion outcomes to aipapi recoverable
// pre-output errors for core-owned routing; provider-specific failures
// return as-is. All errors are secret-free.
func recoverableRunWithKeys[T any](ctx context.Context, pool *credpool.Pool, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	return recoverableRunWithKeysThreshold(ctx, pool, 0, fn)
}

// recoverableRunWithKeysThreshold maps pool-exhaustion outcomes with explicit threshold.
func recoverableRunWithKeysThreshold[T any](ctx context.Context, pool *credpool.Pool, threshold time.Duration, fn func(context.Context, credpool.Credential) (T, error)) (T, error) {
	result, err := runWithKeysThreshold(ctx, pool, threshold, fn)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, credpool.ErrTransientThrottle) ||
		errors.Is(err, credpool.ErrQuotaExhausted) ||
		errors.Is(err, credpool.ErrUpstreamRetryable) ||
		errors.Is(err, credpool.ErrNoUsableCredential) {
		var zero T
		return zero, aipapi.RecoverablePreOutputError(err)
	}
	return result, err
}
