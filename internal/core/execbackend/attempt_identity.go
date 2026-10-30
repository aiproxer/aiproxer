package execbackend

import (
	"context"
	"sync"
)

// AttemptIdentity is the content-free, non-secret identity of the downstream
// target that served one attempt. Its fields are opaque tokens for equality
// comparison only; they are never logged, persisted, emitted on a usage event,
// or published to the prompt-cache collapse diagnostic. A core-owned authority
// compares them in-process and publishes at most a fixed label.
type AttemptIdentity struct {
	// Cache identifies the effective downstream cache residency target: the
	// provider endpoint/region/service tier, or a provider-issued
	// cache-affinity identifier. Empty when the backend exposes none.
	Cache string
	// Credential identifies the credential or account behind the backend.
	// Empty when the backend used no credential or exposes no identity.
	Credential string
}

// AttemptIdentityCollector accumulates the identity a backend reports during
// one attempt open. It is created per attempt and never shared across
// requests; an adapter that reports nothing leaves both fields empty.
type AttemptIdentityCollector struct {
	mu       sync.Mutex
	identity AttemptIdentity
}

// Identity returns the merged observation. Empty reports never erase an
// earlier non-empty value: a backend that reports in stages may leave a field
// unknown on a later report without discarding what it already established.
func (c *AttemptIdentityCollector) Identity() AttemptIdentity {
	if c == nil {
		return AttemptIdentity{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity
}

type attemptIdentityContextKey struct{}

// WithAttemptIdentityCollector returns a context carrying a fresh collector.
// The collector belongs to one attempt; callers must not reuse it across
// attempts.
func WithAttemptIdentityCollector(ctx context.Context) (context.Context, *AttemptIdentityCollector) {
	if ctx == nil {
		ctx = context.Background()
	}
	collector := &AttemptIdentityCollector{}
	return context.WithValue(ctx, attemptIdentityContextKey{}, collector), collector
}

// ReportAttemptIdentity merges identity into the collector attached to ctx.
// Non-empty fields replace the previously observed field; empty fields leave
// it unchanged. A context without a collector is a no-op, so adapters can
// report unconditionally without knowing whether the host observes identities.
func ReportAttemptIdentity(ctx context.Context, identity AttemptIdentity) {
	if ctx == nil {
		return
	}
	collector, _ := ctx.Value(attemptIdentityContextKey{}).(*AttemptIdentityCollector)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if identity.Cache != "" {
		collector.identity.Cache = identity.Cache
	}
	if identity.Credential != "" {
		collector.identity.Credential = identity.Credential
	}
}
