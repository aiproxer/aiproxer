package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// executionReceipt snapshots the host operation identity of an admitted
// auxiliary execution. It is populated after the exposure-abort owner is
// installed and before the initial backend open, so the identity belongs to an
// operation that was actually admitted, and it copies only trusted values:
//
//   - the store and billing-call identities the runtime preparation stamped;
//   - the trusted principal scope of the request;
//   - the trusted detached auxiliary role, when one was selected.
//
// It performs no resolver call, request mutation, callback or nested-context
// capture. An incomplete identity stays an unavailable receipt (see
// [execctx.ExecutionReceipt.Available]).
func executionReceipt(ctx context.Context, prep *preparedRequest) execctx.ExecutionReceipt {
	if prep == nil {
		return execctx.ExecutionReceipt{}
	}
	receipt := execctx.ExecutionReceipt{
		StoreID:       prep.billingStoreID,
		BillingCallID: prep.billingCallID.String(),
	}
	if ctx != nil {
		if view, ok := scope.ScopeFromContext(ctx); ok {
			receipt.TenantID = view.TenantID.String()
			receipt.PrincipalID = view.PrincipalID.String()
		}
		if meta, ok := execctx.DetachedSessionFromContext(ctx); ok {
			receipt.Role = meta.AuxiliaryRole
		}
	}
	return receipt
}
