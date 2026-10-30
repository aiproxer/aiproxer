package aipapi_test

import (
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchValidateHistoryCall mirrors the runtime large-history fixture
// (internal/core/runtime BenchmarkExecutorExecuteAndDrain32DeltasLargeHistory):
// a content-realistic agent history whose canonical envelope validation must not
// allocate per part.
func benchValidateHistoryCall(messages, partsPerMessage int) aipapi.Call {
	msgs := make([]aipapi.Message, 0, messages)
	for range messages {
		parts := make([]aipapi.Part, 0, partsPerMessage)
		for range partsPerMessage {
			parts = append(parts, aipapi.TextPart("history part with representative payload text"))
		}
		msgs = append(msgs, aipapi.Message{Role: aipapi.RoleUser, Parts: parts})
	}
	return aipapi.Call{Route: aipapi.RouteIntent{Selector: "stub:m"}, Messages: msgs}
}

// BenchmarkCallValidate_history measures canonical envelope validation across
// request-history sizes. Diagnostic field labels are rendered only for
// violations, so a valid call must stay flat in part count.
func BenchmarkCallValidate_history(b *testing.B) {
	for _, tc := range []struct {
		messages int
		parts    int
	}{
		{20, 4},
		{100, 20},
	} {
		call := benchValidateHistoryCall(tc.messages, tc.parts)
		b.Run(fmt.Sprintf("messages=%d/parts=%d", tc.messages, tc.parts), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := call.Validate(); err != nil {
					b.Fatalf("valid call rejected: %v", err)
				}
			}
		})
	}
}
