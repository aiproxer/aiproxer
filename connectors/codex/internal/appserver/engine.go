package appserver

import (
	"context"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Engine struct {
	open      func(context.Context, *aipapi.Call) (aipapi.ManagedEventStream, error)
	closeFn   func() error
	inventory modelinventory.Provider
	exeCache  *acp.ExecutableCache
	exePath   string
	Caps      aipapi.BackendCaps
}

func (e *Engine) Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
	if e == nil || e.open == nil {
		return nil, aipapi.ErrNilContext
	}
	return e.open(ctx, call)
}

func (e *Engine) Close() error {
	if e == nil || e.closeFn == nil {
		return nil
	}
	return e.closeFn()
}

func (e *Engine) Inventory() modelinventory.Provider {
	if e == nil {
		return nil
	}
	return e.inventory
}

func (e *Engine) ExecutableCache() *acp.ExecutableCache { return e.exeCache }
func (e *Engine) ResolvedExecutable() string            { return e.exePath }
