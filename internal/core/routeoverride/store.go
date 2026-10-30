package routeoverride

import (
	"context"
	"time"
)

// Reader is the narrow per-turn snapshot port used by request preparation.
type Reader interface {
	Snapshot(ctx context.Context, aLegID string) (State, error)
}

// Store is the optional continuity capability for revisioned override state.
// Standard memory and Bun adapters implement this in addition to b2bua.Store
// without changing the base public continuity contract.
type Store interface {
	Reader
	Get(ctx context.Context, aLegID string) (State, error)
	Replace(ctx context.Context, aLegID, selector string, now time.Time) (State, error)
	Clear(ctx context.Context, aLegID string, now time.Time) (State, error)
}

// CommandService is the generation-bound application port for admin commands.
// Persistence stays process-owned; validation is generation-owned.
type CommandService interface {
	Get(ctx context.Context, aLegID string) (State, error)
	Replace(ctx context.Context, aLegID, selector string) (State, error)
	Clear(ctx context.Context, aLegID string) (State, error)
}

// SelectorValidator is a side-effect-free current-generation route preflight.
type SelectorValidator interface {
	ValidateSelector(ctx context.Context, raw string) error
}

// AsStore reports whether v implements the optional override store capability.
func AsStore(v any) (Store, bool) {
	s, ok := v.(Store)
	return s, ok
}

// AsReader reports whether v implements the optional override reader capability.
func AsReader(v any) (Reader, bool) {
	r, ok := v.(Reader)
	return r, ok
}
