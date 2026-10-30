package streampeek

import (
	"context"
	"io"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// prependFirst yields one buffered event already read from the producer, then delegates
// to rest for subsequent events (rest must not re-emit that first event).
type prependFirst struct {
	first aipapi.Event
	rest  aipapi.EventStream
	sent  bool
}

type managedPrependFirst struct {
	*prependFirst
}

var (
	_ aipapi.EventStream        = (*prependFirst)(nil)
	_ aipapi.ManagedEventStream = (*managedPrependFirst)(nil)
)

// NewPrependFirst returns an EventStream whose first Recv returns first without calling rest.
// Later Recvs delegate to rest; if rest is nil, the second Recv returns io.EOF.
// Close forwards to rest when non-nil.
func NewPrependFirst(first aipapi.Event, rest aipapi.EventStream) aipapi.EventStream {
	return &prependFirst{first: first, rest: rest}
}

// NewManagedPrependFirst is the backend lifecycle-preserving form of NewPrependFirst.
func NewManagedPrependFirst(first aipapi.Event, rest aipapi.ManagedEventStream) aipapi.ManagedEventStream {
	return &managedPrependFirst{prependFirst: &prependFirst{first: first, rest: rest}}
}

func (e *prependFirst) Recv(ctx context.Context) (aipapi.Event, error) {
	if !e.sent {
		e.sent = true
		return e.first, nil
	}
	if e.rest == nil {
		return aipapi.Event{}, io.EOF
	}
	return e.rest.Recv(ctx)
}

func (e *prependFirst) Close() error {
	if e.rest == nil {
		return nil
	}
	return e.rest.Close()
}

func (e *managedPrependFirst) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if e == nil || e.rest == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	rest, ok := e.rest.(aipapi.ManagedEventStream)
	if !ok {
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	return rest.Cancel(ctx, cause)
}
