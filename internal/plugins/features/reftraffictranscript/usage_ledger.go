package reftraffictranscript

import (
	"context"
	"slices"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// UsageLedger records [usage.Event] observations for proof tests (attempt lineage fields preserved).
type UsageLedger struct {
	mu     sync.Mutex
	events []usage.Event
}

// NewUsageLedger returns an empty ledger.
func NewUsageLedger() *UsageLedger { return &UsageLedger{} }

var _ usage.Observer = (*UsageLedger)(nil)

// EventsSnapshot returns a defensive copy of recorded events (may be empty).
func (u *UsageLedger) EventsSnapshot() []usage.Event {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.events)
}

// OnUsage implements [usage.Observer].
func (u *UsageLedger) OnUsage(_ context.Context, ev usage.Event) error {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, ev)
	return nil
}
