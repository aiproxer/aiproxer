package credpool

import "errors"

// ErrNoUsableCredential is returned by [Pool.Acquire] when every credential is
// excluded, in cooldown, or permanently auth-invalid. Its message never
// includes API key material.
var ErrNoUsableCredential = errors.New("credpool: no usable credential")

// ErrTransientThrottle reports a 429-style throttle that must not rotate
// credentials: the Retry-After hint was at or below the quota-switch
// threshold, or it was missing, malformed, past or overflowing. The selected
// key stays the priority owner; callers return a classified pre-output
// failure instead of consuming the next credential.
var ErrTransientThrottle = errors.New("credpool: transient throttle without credential rotation")

// ErrQuotaExhausted reports a request that consumed every eligible credential
// (quota-cooled or auth-invalid) without success. It wraps the underlying
// [ErrNoUsableCredential] when the pool drains mid-request.
var ErrQuotaExhausted = errors.New("credpool: quota exhausted on all eligible credentials")

// ErrUpstreamRetryable reports a transient upstream failure (5xx, 408,
// timeout, connection reset) that carries no credential evidence. Pool state
// is untouched; the caller decides failover.
var ErrUpstreamRetryable = errors.New("credpool: retryable upstream failure")

var errEmptyCredentialList = errors.New("credpool: empty credential list")
