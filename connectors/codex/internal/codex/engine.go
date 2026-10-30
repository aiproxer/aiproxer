package codex

import (
	"context"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Engine struct {
	rt        *backendRuntime
	inventory modelinventory.Provider
	cfgErr    error
}

func (e *Engine) Open(ctx context.Context, call *aipapi.Call, cand routingstub.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	if e == nil {
		return nil, aipapi.ErrNilContext
	}
	if e.cfgErr != nil {
		return nil, e.cfgErr
	}
	if e.rt == nil || call == nil {
		return nil, aipapi.ErrNilContext
	}
	return e.rt.open(ctx, *call, cand)
}

func (e *Engine) Inventory() modelinventory.Provider {
	if e == nil {
		return nil
	}
	return e.inventory
}

func (e *Engine) Caps() aipapi.BackendCaps { return backendCaps }

func (e *Engine) Close() error {
	if e == nil || e.rt == nil {
		return nil
	}
	e.rt.mu.Lock()
	native := e.rt.native
	e.rt.native = nil
	e.rt.mu.Unlock()
	if native != nil {
		native.Close()
	}
	return nil
}
