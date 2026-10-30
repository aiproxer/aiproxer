// Package credpool re-exports the public credential pool from [pkg/credpool].
package credpool

import (
	"context"

	pkg "github.com/aiproxer/aiproxer/pkg/credpool"
)

type (
	Credential       = pkg.Credential
	State            = pkg.State
	CredentialStatus = pkg.CredentialStatus
	Pool             = pkg.Pool
)

const (
	StateUsable                 = pkg.StateUsable
	StateCooldown               = pkg.StateCooldown
	StateAuthInvalid            = pkg.StateAuthInvalid
	DefaultRateLimitFallback    = pkg.DefaultRateLimitFallback
	DefaultQuotaSwitchThreshold = pkg.DefaultQuotaSwitchThreshold
)

type (
	FailureClass = pkg.FailureClass
	Coordinator  = pkg.Coordinator
)

const (
	FailureNeutral        = pkg.FailureNeutral
	FailureRetryable      = pkg.FailureRetryable
	FailureAuthInvalid    = pkg.FailureAuthInvalid
	FailureRateLimited    = pkg.FailureRateLimited
	FailureQuotaExhausted = pkg.FailureQuotaExhausted
)

var (
	ErrNoUsableCredential            = pkg.ErrNoUsableCredential
	ErrTransientThrottle             = pkg.ErrTransientThrottle
	ErrQuotaExhausted                = pkg.ErrQuotaExhausted
	ErrUpstreamRetryable             = pkg.ErrUpstreamRetryable
	New                              = pkg.New
	NewCoordinator                   = pkg.NewCoordinator
	BackendKeySecrets                = pkg.BackendKeySecrets
	NewPoolFromBackendKeys           = pkg.NewPoolFromBackendKeys
	NewPoolFromCredentials           = pkg.NewPoolFromCredentials
	Secrets                          = pkg.Secrets
	CooldownFromRetryAfter           = pkg.CooldownFromRetryAfter
	CooldownFromRetryAfterOrFallback = pkg.CooldownFromRetryAfterOrFallback
)

// Do runs ordered quota-aware attempts over the coordinator. It is a generic
// wrapper preserving type inference across the alias boundary.
func Do[T any](ctx context.Context, c *Coordinator, classify func(error) (FailureClass, string), fn func(context.Context, Credential) (T, error)) (T, error) {
	return pkg.Do(ctx, c, classify, fn)
}
