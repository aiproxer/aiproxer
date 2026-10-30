package auxiliary

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// GenerationExecutorRunner closes the scheduler/executor construction cycle.
type GenerationExecutorRunner struct{ exec *runtime.Executor }

func NewGenerationExecutorRunner() *GenerationExecutorRunner    { return &GenerationExecutorRunner{} }
func (r *GenerationExecutorRunner) Bind(exec *runtime.Executor) { r.exec = exec }
func (r *GenerationExecutorRunner) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if r == nil || r.exec == nil {
		return nil, errors.New("auxiliary: generation executor is not bound")
	}
	return r.exec.Execute(ctx, call)
}

// ExecuteWithReceipt preserves the optional host receipt capability across the
// generation binding: the scheduler's bound runner is this value, so without
// this forward the lexical receipt method would be lost before the scheduler
// ever sees it. The ordinary Execute contract is unchanged.
func (r *GenerationExecutorRunner) ExecuteWithReceipt(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, execctx.ExecutionReceipt, error) {
	if r == nil || r.exec == nil {
		return nil, execctx.ExecutionReceipt{}, errors.New("auxiliary: generation executor is not bound")
	}
	return r.exec.ExecuteWithReceipt(ctx, call)
}

var _ auxreq.ExecutorRunner = (*GenerationExecutorRunner)(nil)
