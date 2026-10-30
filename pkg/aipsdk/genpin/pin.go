package genpin

import "context"

// Kind classifies a retained runtime-generation pin.
type Kind uint8

const (
	// KindSSE retains ownership for a streaming/SSE response handed beyond the
	// request lease.
	KindSSE Kind = 1
	// KindAsync retains ownership for request-spawned asynchronous work.
	KindAsync Kind = 2
	// KindProvider retains ownership for durable terminal/provider settlement.
	KindProvider Kind = 3
)

// Pin retains one runtime configuration generation reference exactly once.
// Release is idempotent and must not underflow generation ownership.
type Pin interface {
	Kind() Kind
	Release()
}

// Retainer acquires independent child pins while a request lease (or equivalent
// spawn right) is still held. Each successful Retain increments generation
// ownership; pins are independently releasable.
type Retainer interface {
	// RuntimeInstanceID returns the opaque process/manager incarnation identity
	// bound to this retainer. Empty when no generation is bound.
	RuntimeInstanceID() string
	// RuntimeGenerationID returns the bound request-plane generation identity.
	// Empty when no generation is bound.
	RuntimeGenerationID() string
	// Retain acquires a child pin of the given kind. Invalid kinds and
	// post-lease spawn attempts fail closed without consuming ownership.
	Retain(kind Kind) (Pin, bool)
}

type ctxKey struct{}

// WithRetainer attaches a generation pin retainer to ctx.
// A nil parent ctx is tolerated and substituted with [context.Background] so the
// result is always non-nil.
func WithRetainer(ctx context.Context, r Retainer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, r)
}

// FromContext returns the request-bound retainer when present.
// A nil ctx is tolerated and returns (nil, false).
func FromContext(ctx context.Context) (Retainer, bool) {
	if ctx == nil {
		return nil, false
	}
	r, ok := ctx.Value(ctxKey{}).(Retainer)
	if !ok || r == nil {
		return nil, false
	}
	return r, true
}
