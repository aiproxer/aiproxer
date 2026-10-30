package featurehost

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// proxyMemoryCollector adapts the process background client to the feature's
// receipt-aware collection port. It owns no registry, worker, pin or Close
// method: the scheduler remains the single lifecycle and result owner. The
// adapter only maps the core receipt onto the feature-owned value, and a
// composed client without the optional capability keeps the legacy usage-only
// path with an explicitly unavailable receipt.
type proxyMemoryCollector struct {
	inner auxiliary.BackgroundClient
}

func (c proxyMemoryCollector) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return c.inner.SubmitCollect(ctx, req, opts)
}

func (c proxyMemoryCollector) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	return c.inner.Await(ctx, id)
}

func (c proxyMemoryCollector) Forget(id auxiliary.JobID) {
	c.inner.Forget(id)
}

func (c proxyMemoryCollector) AwaitCollection(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, proxymemory.CollectionReceipt, error) {
	if waiter, ok := c.inner.(auxreq.ReceiptAwaiter); ok {
		collected, receipt, err := waiter.AwaitWithReceipt(ctx, id)
		if !receipt.Available() {
			return collected, proxymemory.CollectionReceipt{}, err
		}
		return collected, proxymemory.CollectionReceipt{
			StoreID:       receipt.Execution.StoreID,
			BillingCallID: receipt.Execution.BillingCallID,
			CompletedAt:   receipt.CompletedAt,
		}, err
	}
	collected, err := c.inner.Await(ctx, id)
	return collected, proxymemory.CollectionReceipt{}, err
}

var (
	_ auxiliary.BackgroundClient   = proxyMemoryCollector{}
	_ proxymemory.ReceiptCollector = proxyMemoryCollector{}
)
