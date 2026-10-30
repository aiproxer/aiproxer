package featurehost

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/require"
)

// TestProxyMemoryCollector_LegacyClientReturnsAnUnavailableReceipt pins the
// composition fallback: a background client without the optional core receipt
// capability still answers the worker, but the adapter reports an explicitly
// unlinked receipt instead of fabricating an operation reference.
func TestProxyMemoryCollector_LegacyClientReturnsAnUnavailableReceipt(t *testing.T) {
	collector := proxyMemoryCollector{inner: &sentinelAuxClient{}}
	id, err := collector.SubmitCollect(context.Background(), auxiliary.Request{
		Role: sentinelRolePhase1,
		Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "sentinel:model"}},
	}, auxiliary.SubmitOptions{CoalesceKey: "receipt-legacy-adapter"})
	require.NoError(t, err)
	collected, receipt, err := collector.AwaitCollection(context.Background(), id)
	require.NoError(t, err)
	require.NotEmpty(t, collected.Text.String(), "the legacy result still reaches the worker")
	require.False(t, receipt.Linked(), "a legacy client must not produce a linked receipt")
	require.Equal(t, proxymemory.CollectionReceipt{}, receipt)
}
