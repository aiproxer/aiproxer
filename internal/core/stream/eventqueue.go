package stream

import "github.com/aiproxer/aiproxer/pkg/streampump"

// ErrPendingQueueFull is returned when [PendingEventQueue.Push] would exceed a configured max length.
var ErrPendingQueueFull = streampump.ErrPendingQueueFull

// PendingEventQueue buffers canonical events for adapters that translate one wire
// chunk into zero or more aipapi.Event values.
type PendingEventQueue = streampump.PendingEventQueue

// EventPump owns the common "pending queue before wire read" loop used by stream adapters.
type EventPump[T any] = streampump.EventPump[T]

// NewPendingEventQueue returns a queue with the given max pending events (0 = unlimited).
var NewPendingEventQueue = streampump.NewPendingEventQueue

// DrainPending pops every queued event in order and returns them.
var DrainPending = streampump.DrainPending
