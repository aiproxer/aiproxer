package streampump

import (
	"context"
	"io"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EventPump owns the common "pending queue before wire read" loop used by stream adapters.
// Callers keep provider-specific reading and mapping in Read/Handle.
type EventPump[T any] struct {
	Lock     *sync.Mutex
	Pending  *PendingEventQueue
	IsClosed func() bool
	Read     func() (T, bool, error)
	Handle   func(T) error
	OnEOF    func() (bool, error)
}

// Recv returns the next canonical event, draining the pending queue before reading wire input.
func (p EventPump[T]) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	for {
		p.Lock.Lock()
		if p.closed() {
			p.Lock.Unlock()
			return aipapi.Event{}, io.EOF
		}
		if ev, ok := p.Pending.PopFront(); ok {
			p.Lock.Unlock()
			return ev, nil
		}
		p.Lock.Unlock()

		item, ok, err := p.Read()
		if err != nil {
			p.Lock.Lock()
			if p.closed() {
				p.Lock.Unlock()
				return aipapi.Event{}, io.EOF
			}
			p.Lock.Unlock()
			return aipapi.Event{}, err
		}
		if !ok {
			p.Lock.Lock()
			if p.closed() {
				p.Lock.Unlock()
				return aipapi.Event{}, io.EOF
			}
			again, err := p.onEOF()
			p.Lock.Unlock()
			if err != nil {
				return aipapi.Event{}, err
			}
			if again {
				continue
			}
			return aipapi.Event{}, io.EOF
		}

		p.Lock.Lock()
		if p.closed() {
			p.Lock.Unlock()
			continue
		}
		if p.Handle != nil {
			if err := p.Handle(item); err != nil {
				p.Lock.Unlock()
				return aipapi.Event{}, err
			}
		}
		p.Lock.Unlock()
	}
}

func (p EventPump[T]) closed() bool {
	return p.IsClosed != nil && p.IsClosed()
}

func (p EventPump[T]) onEOF() (bool, error) {
	if p.OnEOF == nil {
		return false, nil
	}
	return p.OnEOF()
}
