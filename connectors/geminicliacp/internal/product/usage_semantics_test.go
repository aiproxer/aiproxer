package product

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The Gemini CLI ACP agent reports an inclusive input counter: it fills
// inputTokens from usageMetadata.promptTokenCount, which already contains the
// cached content tokens, and its total is the input plus the output.
//
// The connector therefore asserts no input relation. A frontend then counts
// the prompt the agent reported instead of adding the cached tokens a second
// time.
func TestOpen_publishedUsageKeepsInclusiveInputRelation(t *testing.T) {
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
