package policydecision

import (
	"context"
)

// Observer receives normalized policy decision records from the core evidence emitter
// (requirements 7.6, 7.7). Implementations must not change request execution; the
// evidence emitter isolates observer failures from runtime outcomes. Each observer in
// a chain receives its own cloned record so observer mutation cannot affect another
// observer or runtime state.
type Observer interface {
	OnPolicyDecision(ctx context.Context, record Record) error
}

// NoopObserver is the default disabled observer. It is cheap and safe to share.
type NoopObserver struct{}

// OnPolicyDecision implements Observer by returning nil without touching the record.
func (NoopObserver) OnPolicyDecision(context.Context, Record) error { return nil }

var _ Observer = NoopObserver{}

// IsNoopObserver reports whether obs is a disabled default observer: a nil
// observer, [NoopObserver], or an empty [ChainObserver]. Composition roots use
// it to skip evidence attachment and logging for deployments without policy
// decision observation, keeping the no-op path cheap (requirements 7.6, 10.5).
// A ChainObserver wrapping only NoopObserver children is not treated as no-op
// here; such wiring is not produced by the default composition root.
func IsNoopObserver(obs Observer) bool {
	switch o := obs.(type) {
	case nil:
		return true
	case NoopObserver:
		return true
	case ChainObserver:
		return len(o.observers) == 0
	}
	return false
}

// ChainObserver fans a record out to each non-nil child observer in registration order.
// Each child receives its own cloned copy of the record; child errors are ignored
// (fail-open) so a misbehaving observer cannot change request execution. A nil or
// empty chain behaves as [NoopObserver].
type ChainObserver struct {
	observers []Observer
}

var _ Observer = ChainObserver{}

// NewChainObserver returns a ChainObserver that fans records out to the supplied
// observers in order. nil observers are dropped.
func NewChainObserver(observers ...Observer) ChainObserver {
	flat := make([]Observer, 0, len(observers))
	for _, o := range observers {
		if o == nil {
			continue
		}
		flat = append(flat, o)
	}
	return ChainObserver{observers: flat}
}

// OnPolicyDecision implements Observer by delivering a fresh clone of record to each
// child. Errors are ignored so request execution is unaffected (requirement 7.6).
func (c ChainObserver) OnPolicyDecision(ctx context.Context, record Record) error {
	for _, o := range c.observers {
		if o == nil {
			continue
		}
		_ = o.OnPolicyDecision(ctx, record.Clone())
	}
	return nil
}

// Observers returns a defensive copy of the child observers in chain order.
func (c ChainObserver) Observers() []Observer {
	out := make([]Observer, len(c.observers))
	copy(out, c.observers)
	return out
}
