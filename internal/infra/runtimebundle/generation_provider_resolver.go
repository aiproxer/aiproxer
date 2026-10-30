package runtimebundle

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// generationTerminalProviders is the narrow generation-owned surface used by
// generationPresentResolver. GenerationBundle implements it.
type generationTerminalProviders interface {
	TerminalProviders() terminalworkapp.TerminalProviderView
}

// generationPresentResolver resolves terminal-work providers for rows that
// carry an exact RuntimeInstanceID + RuntimeGenerationID. It obtains the
// retained generation, uses that generation's published immutable terminal
// provider view, and never falls through to the process-global registry
// (task 3.6).
type generationPresentResolver struct {
	mu     sync.RWMutex
	lookup func(instanceID string, id int64) *runtimehost.Generation
}

func (r *generationPresentResolver) SetLookup(lookup func(instanceID string, id int64) *runtimehost.Generation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lookup = lookup
	r.mu.Unlock()
}

func (r *generationPresentResolver) Resolve(runtimeInstanceID, runtimeGenerationID, providerID string, kind sdk.WorkKind) (terminalworkapp.EffectProvider, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: no generation resolver", terminalworkapp.ErrMissingProvider)
	}
	inst := strings.TrimSpace(runtimeInstanceID)
	genID := strings.TrimSpace(runtimeGenerationID)
	if inst == "" || genID == "" {
		return nil, fmt.Errorf("%w: incomplete runtime identity", terminalworkapp.ErrMissingProvider)
	}
	id, err := strconv.ParseInt(genID, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("%w: invalid runtime generation", terminalworkapp.ErrMissingProvider)
	}
	r.mu.RLock()
	lookup := r.lookup
	r.mu.RUnlock()
	if lookup == nil {
		return nil, fmt.Errorf("%w: runtime generation %s unbound", terminalworkapp.ErrMissingProvider, genID)
	}
	g := lookup(inst, id)
	if g == nil {
		return nil, fmt.Errorf("%w: runtime generation %s/%s closed", terminalworkapp.ErrMissingProvider, inst, genID)
	}
	plane := g.RequestPlane()
	viewHost, ok := plane.(generationTerminalProviders)
	if !ok || viewHost == nil {
		return nil, fmt.Errorf("%w: generation %s missing terminal provider view", terminalworkapp.ErrMissingProvider, genID)
	}
	view := viewHost.TerminalProviders()
	if view == nil {
		return nil, fmt.Errorf("%w: generation %s empty terminal provider view", terminalworkapp.ErrMissingProvider, genID)
	}
	return view.Resolve(providerID, kind)
}

var _ terminalworkapp.GenerationBoundResolver = (*generationPresentResolver)(nil)
