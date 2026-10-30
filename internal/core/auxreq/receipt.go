package auxreq

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// ExecutionReceipt is the scheduler-owned view of one background operation's
// host receipt. The execution identity is the runtime's neutral value; the
// completion time is the immutable scheduler bookkeeping timestamp captured
// once when the job finished. Neither is derived from the auxiliary request,
// the coalesce key or any client-supplied field.
type ExecutionReceipt struct {
	Execution   execctx.ExecutionReceipt
	CompletedAt time.Time
}

// Available reports whether the receipt names a complete host operation
// reference.
func (r ExecutionReceipt) Available() bool { return r.Execution.Available() }

// ReceiptAwaiter is the internal optional capability of a background client
// whose jobs carry a host operation receipt. It returns the collected result,
// the receipt and the job error from one locked job snapshot, so a result can
// never be paired with another job's receipt by an eviction race.
//
// The public [auxiliary.BackgroundClient] contract is unchanged; callers that
// only need the result keep using Await.
type ReceiptAwaiter interface {
	AwaitWithReceipt(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, ExecutionReceipt, error)
}

// receiptRunner is the optional executor runner capability. *runtime.Executor
// implements it through the same private canonical execution as Execute.
type receiptRunner interface {
	ExecuteWithReceipt(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, execctx.ExecutionReceipt, error)
}
