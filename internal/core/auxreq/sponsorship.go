package auxreq

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// WithSponsorship returns a background client that binds the operator-declared
// funding decision to every job it submits.
//
// The decision is bound at submission by the composition root and travels in
// trusted internal context only: it never enters the public auxiliary request,
// the canonical call or any wire payload, so a client can neither select nor
// spoof the funding mode of a background operation.
func WithSponsorship(inner auxiliary.BackgroundClient, decl execctx.Sponsorship) auxiliary.BackgroundClient {
	if inner == nil {
		return nil
	}
	return sponsoredBackgroundClient{inner: inner, decl: decl.Clone()}
}

// sponsoredBackgroundClient binds one operator-declared funding decision to
// the submissions of the wrapped background client.
type sponsoredBackgroundClient struct {
	inner auxiliary.BackgroundClient
	decl  execctx.Sponsorship
}

func (c sponsoredBackgroundClient) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	if c.inner == nil {
		return "", auxiliary.ErrNotConfigured
	}
	if ctx == nil {
		return "", aipapi.ErrNilContext
	}
	return c.inner.SubmitCollect(execctx.WithSponsorship(ctx, c.decl), req, opts)
}

func (c sponsoredBackgroundClient) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	if c.inner == nil {
		return aipapi.Collected{}, auxiliary.ErrNotConfigured
	}
	return c.inner.Await(ctx, id)
}

// AwaitWithReceipt preserves the optional receipt capability through the
// sponsorship wrapper. A legacy inner client still works: it returns its
// ordinary result with an explicitly unavailable receipt.
func (c sponsoredBackgroundClient) AwaitWithReceipt(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, ExecutionReceipt, error) {
	if c.inner == nil {
		return aipapi.Collected{}, ExecutionReceipt{}, auxiliary.ErrNotConfigured
	}
	if waiter, ok := c.inner.(ReceiptAwaiter); ok {
		return waiter.AwaitWithReceipt(ctx, id)
	}
	collected, err := c.inner.Await(ctx, id)
	return collected, ExecutionReceipt{}, err
}

func (c sponsoredBackgroundClient) Forget(id auxiliary.JobID) {
	if c.inner == nil {
		return
	}
	c.inner.Forget(id)
}

var (
	_ auxiliary.BackgroundClient = sponsoredBackgroundClient{}
	_ ReceiptAwaiter             = sponsoredBackgroundClient{}
)
