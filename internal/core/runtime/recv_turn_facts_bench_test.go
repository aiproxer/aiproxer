package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func recvTurnFactsBenchCall() aipapi.Call {
	parts := make([]aipapi.Part, 20)
	for i := range parts {
		parts[i] = aipapi.TextPart("history part with representative payload text")
	}
	msgs := make([]aipapi.Message, 100)
	for i := range msgs {
		msgs[i] = aipapi.Message{Role: aipapi.RoleUser, Parts: parts}
	}
	return aipapi.Call{Route: aipapi.RouteIntent{Selector: "stub:m"}, Messages: msgs}
}

// BenchmarkRequestTerminalFactsToRecvTurnFactsLargeHistory proves the conversion
// copies each immutable call once instead of once in the caller and again in
// newRecvTurnFacts.
func BenchmarkRequestTerminalFactsToRecvTurnFactsLargeHistory(b *testing.B) {
	call := recvTurnFactsBenchCall()
	facts := requestTerminalFacts{call: call, ingressCall: call, conversationFilteredBaseline: call}
	b.ReportAllocs()
	for b.Loop() {
		got := facts.toRecvTurnFacts(context.Background())
		if len(got.baseline.Messages) != 100 || len(got.ingressCall.Messages) != 100 || len(got.conversationFilteredBaseline.Messages) != 100 {
			b.Fatal("terminal facts lost the large history")
		}
	}
}
