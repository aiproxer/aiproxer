package execbackend

import "context"

// AttemptMeta is the host-owned identity of one provider attempt.
//
// ID is unique per actual attempt (the B-leg identity allocated for it) and
// must be used by a backend that needs a per-charge attempt identity. Seq is
// the attempt's order within the request. The identity is host authority: it
// is not client input and must not be derived from the request id alone, which
// repeats across retries and parallel attempts.
type AttemptMeta struct {
	ID  string
	Seq int
}

type attemptMetaKey struct{}

// WithAttemptMeta attaches the host attempt identity to an opener context.
func WithAttemptMeta(ctx context.Context, meta AttemptMeta) context.Context {
	if ctx == nil || meta.ID == "" {
		return ctx
	}
	return context.WithValue(ctx, attemptMetaKey{}, meta)
}

// AttemptMetaFromContext returns the host attempt identity, when one is set.
func AttemptMetaFromContext(ctx context.Context) (AttemptMeta, bool) {
	if ctx == nil {
		return AttemptMeta{}, false
	}
	meta, ok := ctx.Value(attemptMetaKey{}).(AttemptMeta)
	if !ok || meta.ID == "" {
		return AttemptMeta{}, false
	}
	return meta, true
}
