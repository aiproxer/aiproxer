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

// EnvAPITokenRoot is the canonical nonsecret host environment namespace for
// Replicate credentials: REPLICATE_API_TOKEN[_N]. Secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
const EnvAPITokenRoot = "REPLICATE_API_TOKEN"

// QuotaDomain is the explicit nonsecret quota scope for Replicate
// credential selection: rotation decisions never alias an unrelated
// provider or project cooldown to this domain.
const QuotaDomain = "replicate"

// decodeAPITokens validates host-issued ordered api_token Configure secrets
// through the shared SDK slot decoder. The forbidden-field policy and slot
// shape stay single-sourced with the rest of the cohort; only error
// classification is provider-specific (Replicate wire taxonomy).
func decodeAPITokens(bundle backendplugin.SecretBundle) ([]string, error) {
	return openaicompat.DecodeAPITokenSlots(bundle)
}

// classifyKeyError maps Replicate-wire HTTP failures to coordinator classes:
// 401 is auth-invalid and advances pre-output; 429 carries the raw
// Retry-After hint for threshold-gated quota rotation; 408/5xx are transient
// upstream failures with no credential evidence; every other outcome
// (including 402/403 provider errors) is neutral and returns as-is without
// consuming another credential.
func classifyKeyError(err error) (credpool.FailureClass, string) {
	if err == nil {
		return credpool.FailureNeutral, ""
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil {
		return credpool.FailureNeutral, ""
	}
	switch httpErr.StatusCode {
	case http.StatusUnauthorized:
		return credpool.FailureAuthInvalid, ""
	case http.StatusTooManyRequests:
		return credpool.FailureRateLimited, strings.TrimSpace(httpErr.RetryAfter)
	case http.StatusRequestTimeout:
		return credpool.FailureRetryable, ""
	default:
		if httpErr.StatusCode >= 500 {
			return credpool.FailureRetryable, ""
		}
		return credpool.FailureNeutral, ""
	}
}

// runWithKeys executes fn against ordered credentials with quota-aware
// selection under the proxy-wide default quota-switch threshold. Attempts
// never exceed the pool size; the first usable token stays preferred.
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
