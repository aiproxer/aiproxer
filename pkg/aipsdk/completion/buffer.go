package completion

import (
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// BufferLimits bounds memory retained while completion gating buffers before first output (R8).
// Zero value applies [DefaultBufferLimits].
type BufferLimits struct {
	// MaxEvents is the maximum number of canonical stream events retained; exceeding fails open to live passthrough.
	MaxEvents int
}

// DefaultBufferLimits returns conservative defaults for production composition roots.
func DefaultBufferLimits() BufferLimits {
	return BufferLimits{MaxEvents: 16_384}
}

func (l BufferLimits) normalized() BufferLimits {
	if l.MaxEvents <= 0 {
		return DefaultBufferLimits()
	}
	return l
}

// OverCapacity reports whether len(events) exceeds MaxEvents after normalization.
func (l BufferLimits) OverCapacity(events int) bool {
	l = l.normalized()
	return events > l.MaxEvents
}

// Buffered is an immutable view of canonical events passed to a gate (defensive copy).
type Buffered struct {
	ev []aipapi.Event
}

// NewBuffered returns a view backed by a copy of events.
func NewBuffered(events []aipapi.Event) Buffered {
	return Buffered{ev: slices.Clone(events)}
}

// Len returns the number of events.
func (b Buffered) Len() int {
	return len(b.ev)
}

// Events returns a defensive copy for handlers that need a mutable slice.
func (b Buffered) Events() []aipapi.Event {
	return slices.Clone(b.ev)
}

// Event returns event i and ok false when out of range.
func (b Buffered) Event(i int) (aipapi.Event, bool) {
	if i < 0 || i >= len(b.ev) {
		return aipapi.Event{}, false
	}
	return b.ev[i], true
}
