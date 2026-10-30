package credpool

import (
	"context"
	"errors"
	"time"
)

// DefaultQuotaSwitchThreshold is the proxy-wide Retry-After duration above
// which a 429 marks a credential quota-cooling: strictly greater advances to
// the next eligible key before output, at or below stays a transient
// same-key throttle. It matches upstream_credentials.quota_switch_retry_after.
const DefaultQuotaSwitchThreshold = time.Hour

// FailureClass is the provider-neutral outcome of one credential attempt. It
// carries no provider taxonomy: adapters translate HTTP/provider errors into
// these classes, and the coordinator owns ordered selection only.
type FailureClass int

const (
	// FailureNeutral is any outcome with no credential evidence (4xx other
	// than auth-invalid, fatal errors): the attempt error returns as-is.
	FailureNeutral FailureClass = iota
	// FailureRetryable is a transient upstream failure with no credential
	// evidence: pool state is untouched and the attempt error returns joined
	// with [ErrUpstreamRetryable] for caller failover.
	FailureRetryable
	// FailureAuthInvalid permanently retires the credential and advances
	// pre-output to the next eligible key.
	FailureAuthInvalid
	// FailureRateLimited is a 429-style throttle with the raw Retry-After
	// hint (possibly empty). A trustworthy hint strictly greater than the
	// threshold quota-cools the key and advances; anything else is a
	// transient same-key throttle with no state change.
	FailureRateLimited
	// FailureQuotaExhausted is explicit verified long quota-exhaustion
	// evidence without a Retry-After instant. The key quota-cools for the
	// threshold duration and the loop advances; it becomes eligible again
	// after the cooldown expires.
	FailureQuotaExhausted
)

// Coordinator runs ordered quota-aware attempts over a [Pool]. The threshold
// is fixed at construction; time comes from the injected clock, so tests use
// a fake clock and production passes nil for wall time. A Coordinator is safe
// for concurrent use: selection state lives behind the pool mutex and the
// loop spawns no goroutines.
type Coordinator struct {
	pool      *Pool
	threshold time.Duration
	now       func() time.Time
}

// NewCoordinator binds pool with a quota-switch threshold and clock. A
// non-positive threshold selects [DefaultQuotaSwitchThreshold]; a nil clock
// selects wall time. A nil pool fails closed.
func NewCoordinator(pool *Pool, threshold time.Duration, now func() time.Time) (*Coordinator, error) {
	if pool == nil {
		return nil, errors.New("credpool: nil credential pool")
	}
	if threshold <= 0 {
		threshold = DefaultQuotaSwitchThreshold
	}
	if now == nil {
		now = time.Now
	}
	return &Coordinator{pool: pool, threshold: threshold, now: now}, nil
}

// Snapshot returns the secret-free ordered status of every credential at the
// coordinator clock.
func (c *Coordinator) Snapshot() []CredentialStatus {
	return c.pool.Snapshot(c.now())
}

// Do runs fn against ordered credentials until success or a classified stop.
// The first usable key stays preferred; each credential is attempted at most
// once per call, so attempts never exceed the pool size. ctx is the first
// parameter of fn, is never stored, and a canceled context stops the loop
// without further attempts or state mutation.
func Do[T any](ctx context.Context, c *Coordinator, classify func(error) (FailureClass, string), fn func(context.Context, Credential) (T, error)) (T, error) {
	var zero T
	if c == nil || c.pool == nil {
		return zero, ErrNoUsableCredential
	}
	if classify == nil {
		classify = func(error) (FailureClass, string) { return FailureNeutral, "" }
	}
	if fn == nil {
		return zero, errors.New("credpool: nil attempt function")
	}
	tried := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		now := c.now()
		cred, acquireErr := c.pool.Acquire(now, tried)
		if acquireErr != nil {
			if len(tried) == 0 {
				return zero, acquireErr
			}
			return zero, errors.Join(ErrQuotaExhausted, acquireErr)
		}
		tried[cred.ID] = struct{}{}
		result, runErr := fn(ctx, cred)
		if runErr == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return zero, runErr
		}
		class, hint := classify(runErr)
		switch class {
		case FailureAuthInvalid:
			c.pool.MarkAuthInvalid(cred.ID)
		case FailureQuotaExhausted:
			c.pool.MarkRateLimited(cred.ID, now.Add(c.threshold))
		case FailureRateLimited:
			until, ok := CooldownFromRetryAfter(hint, now)
			if !ok || until.Sub(now) <= c.threshold {
				// Missing, malformed, past, overflowing or short hint:
				// transient throttle on the same key. No pool state
				// change, so the primary stays the priority owner and
				// no secondary is consumed, including by concurrent
				// requests sharing this coordinator.
				return zero, errors.Join(ErrTransientThrottle, runErr)
			}
			c.pool.MarkRateLimited(cred.ID, until)
		case FailureRetryable:
			return zero, errors.Join(ErrUpstreamRetryable, runErr)
		default:
			return zero, runErr
		}
	}
}
