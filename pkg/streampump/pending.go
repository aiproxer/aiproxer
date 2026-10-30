// Package streampump provides a bounded pending-event queue and generic Recv loop
// shared by core stream adapters and connector backends.
package streampump

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrPendingQueueFull is returned when [PendingEventQueue.Push] would exceed a configured max length.
var ErrPendingQueueFull = errors.New("streampump: pending event queue capacity exceeded")

// PendingEventQueue buffers canonical events for adapters that translate one wire
// chunk into zero or more aipapi.Event values. It avoids slice-prefix dequeue
// (pending = pending[1:]) which retains a large backing array over long streams.
//
// When constructed with a positive max length (see [NewPendingEventQueue]), Push returns
// [ErrPendingQueueFull] once the queue would exceed that cap. When max length is zero
// (default), the queue is unbounded until other request limits apply.
type PendingEventQueue struct {
	buf    []aipapi.Event
	head   int
	maxLen int
}

// NewPendingEventQueue returns a queue with the given max pending events (0 = unlimited).
func NewPendingEventQueue(maxLen int) PendingEventQueue {
	return PendingEventQueue{maxLen: maxLen}
}

// Len returns the number of queued events.
func (q *PendingEventQueue) Len() int {
	return len(q.buf) - q.head
}

// Push appends an event to the tail.
func (q *PendingEventQueue) Push(ev aipapi.Event) error {
	if q.maxLen > 0 && q.Len() >= q.maxLen {
		return ErrPendingQueueFull
	}
	q.compactIfNeeded()
	q.buf = append(q.buf, ev)
	return nil
}

// PopFront removes and returns the oldest event. The second result is false when empty.
func (q *PendingEventQueue) PopFront() (aipapi.Event, bool) {
	if len(q.buf) <= q.head {
		q.buf = q.buf[:0]
		q.head = 0
		return aipapi.Event{}, false
	}
	ev := q.buf[q.head]
	q.head++
	q.compactIfNeeded()
	return ev, true
}

// DrainPending pops every queued event in order and returns them. The queue is empty afterward.
func DrainPending(q *PendingEventQueue) []aipapi.Event {
	out := make([]aipapi.Event, 0, q.Len())
	for {
		ev, ok := q.PopFront()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func (q *PendingEventQueue) compactIfNeeded() {
	alive := len(q.buf) - q.head
	if q.head == 0 {
		if alive == 0 && cap(q.buf) > 0 {
			q.buf = q.buf[:0]
		}
		return
	}
	if alive == 0 {
		q.buf = q.buf[:0]
		q.head = 0
		return
	}
	if q.head < 64 && q.head <= alive {
		return
	}
	if cap(q.buf) > 1024 && alive < cap(q.buf)/4 {
		next := make([]aipapi.Event, alive, alive*2)
		copy(next, q.buf[q.head:])
		q.buf = next
		q.head = 0
		return
	}
	oldLen := len(q.buf)
	copy(q.buf[:alive], q.buf[q.head:])
	clear(q.buf[alive:oldLen])
	q.buf = q.buf[:alive]
	q.head = 0
}
