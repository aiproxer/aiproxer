package product

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The AGY ACP wrapper reports no ACP usage at all: its prompt response
// carries no usage object and it emits no usage-bearing notification, so the
// connector has no relation to declare.
//
// Should a future agent report usage, the counters are published as reported
// and keep the inclusive interpretation until the agent's convention is
// established with evidence.
func TestOpen_publishedUsageAssertsNoInputRelation(t *testing.T) {
	t.Parallel()
	eng := &Engine{open: func(context.Context, *aipapi.Call) (aipapi.ManagedEventStream, error) {
		return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventUsageDelta, InputTokens: 120, OutputTokens: 45, CacheReadTokens: 80, TotalTokens: 165},
		})}, nil
	}}
	stream, err := eng.Open(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != aipapi.EventUsageDelta {
		t.Fatalf("event = %+v", event)
	}
	if event.Accounting.InputTokensExcludeCacheRead || event.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("accounting = %+v", event.Accounting)
	}
	if event.InputTokens != 120 || event.OutputTokens != 45 || event.CacheReadTokens != 80 || event.TotalTokens != 165 {
		t.Fatalf("counters changed: %+v", event)
	}
}
