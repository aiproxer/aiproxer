package aipapi_test

import (
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchValidateItemHistoryCall mirrors an agent history expressed through the
// ordered item authority: message items carrying content parts, tool calls, and
// their tool results.
func benchValidateItemHistoryCall(items int) aipapi.Call {
	out := make([]aipapi.Item, 0, items)
	for i := range items {
		id := fmt.Sprintf("item_%d", i)
		switch i % 3 {
		case 0:
			out = append(out, aipapi.Item{
				ID:   id,
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "history part with representative payload text"},
					{Kind: aipapi.ContentPartText, Text: "second history part with representative payload text"},
				},
			})
		case 1:
			out = append(out, aipapi.Item{
				ID:   id,
				Kind: aipapi.ItemKindToolCall,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    fmt.Sprintf("call_%d", i),
					Name:      "search",
					Arguments: []byte("{\"q\":\"needle\"}"),
				},
			})
		case 2:
			out = append(out, aipapi.Item{
				ID:   id,
				Kind: aipapi.ItemKindToolResult,
				ToolResult: &aipapi.ToolResultItem{
					CallID: fmt.Sprintf("call_%d", i-1),
					Name:   "search",
					Output: "tool result payload with representative text",
				},
			})
		}
	}
	return aipapi.Call{Route: aipapi.RouteIntent{Selector: "stub:m"}, Items: out}
}

func BenchmarkCallValidate_items(b *testing.B) {
	for _, items := range []int{30, 300} {
		call := benchValidateItemHistoryCall(items)
		b.Run(fmt.Sprintf("items=%d", items), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := call.Validate(); err != nil {
					b.Fatalf("valid call rejected: %v", err)
				}
			}
		})
	}
}
