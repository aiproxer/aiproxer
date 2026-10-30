package product

import (
	"context"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Engine struct {
	open      func(context.Context, *aipapi.Call) (aipapi.ManagedEventStream, error)
	closeFn   func() error
	exeCache  *acp.ExecutableCache
	exePath   string
	Inventory modelinventory.Provider
	Caps      aipapi.BackendCaps
}

func (e *Engine) ResolvedExecutable() string {
	if e == nil {
		return ""
	}
	return e.exePath
}

func (e *Engine) ExecutableCache() *acp.ExecutableCache {
	if e == nil {
		return nil
	}
	return e.exeCache
}

func (e *Engine) Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
	return e.open(ctx, call)
}

func (e *Engine) Close() error {
	if e.closeFn == nil {
		return nil
	}
	return e.closeFn()
}
