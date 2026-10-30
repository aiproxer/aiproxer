package openaicompat

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// quirkCallItem and quirkOutItem build wire items for property tests.
func quirkCallItem(id string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
}

func quirkOutItem(id string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
}

func quirkUserItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser)
}

func quirkReasoningItem(id string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{ID: id}}
}

func quirkAssistantItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
}

// quirkRandomHistory generates call-before-output histories where every call is
// uniquely identified and every call is followed by its output.
func quirkRandomHistory(rng *rand.Rand, nCalls int) []responses.ResponseInputItemUnionParam {
	ids := make([]string, 0, nCalls)
	for i := range nCalls {
		ids = append(ids, string(rune('a'+i%26))+"c"+string(rune('0'+i/26)))
	}
	items := make([]responses.ResponseInputItemUnionParam, 0, 3*nCalls)
	pending := make([]string, 0, nCalls)
	remaining := ids
	for len(remaining) > 0 {
		// Emit some calls.
		for range 1 + rng.Intn(3) {
			if len(remaining) == 0 {
				break
			}
			id := remaining[0]
			remaining = remaining[1:]
			items = append(items, quirkCallItem(id))
			pending = append(pending, id)
		}
		// Emit a delayable message, a model-authored item, or nothing.
		if rng.Intn(3) == 0 {
			switch rng.Intn(4) {
			case 0:
				items = append(items, quirkUserItem("u"))
			case 1:
				items = append(items, quirkReasoningItem("rs"))
			default:
				items = append(items, quirkAssistantItem("a"))
			}
		}
		if len(pending) > 0 && rng.Intn(2) == 0 {
			idx := rng.Intn(len(pending))
			id := pending[idx]
			pending = append(pending[:idx], pending[idx+1:]...)
			items = append(items, quirkOutItem(id))
		}
	}
	for _, id := range pending {
		items = append(items, quirkOutItem(id))
	}
	return items
}

// Claim: for any generated history, the normalizer never breaks the causal
// invariants — on success it preserves non-output order, output completion
// order and length; on refusal it returns the input unchanged.
// Source: third review P2 on PR #1102 (property-based ordering regressions).
// Mutant: any reorder across classes -> FAIL.
func TestQuirks_ResponsesPropertyInvariants(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(20261015))
	for iteration := range 2000 {
		items := quirkRandomHistory(rng, 1+rng.Intn(6))
		before := quirkMarshalAll(t, items)
		nonOutputBefore := quirkNonOutputSignature(t, items)
		outputBefore := quirkOutputSignature(t, items)

		got, _, err := reorderResponsesAdjacency(items)
		if err != nil {
			if !errors.Is(err, ErrResponsesAdjacencyRefused) {
				t.Fatalf("iteration %d: unexpected error %v", iteration, err)
			}
			if quirkMarshalAll(t, got) != before {
				t.Fatalf("iteration %d: refused request was modified", iteration)
			}
			continue
		}
		if len(got) != len(items) {
			t.Fatalf("iteration %d: length changed %d -> %d", iteration, len(items), len(got))
		}
		if got := quirkNonOutputSignature(t, got); got != nonOutputBefore {
			t.Fatalf("iteration %d: non-output order changed:\n%s\n%s\ninput=%s", iteration, nonOutputBefore, got, before)
		}
		if got := quirkOutputSignature(t, got); got != outputBefore {
			t.Fatalf("iteration %d: output completion order changed: %q -> %q (input=%s)", iteration, outputBefore, got, before)
		}
		assertStrictAdjacency(t, got)
	}
}

// quirkNonOutputSignature is the ordered subsequence of non-output items.
// Delaying a user message past its tool results intentionally changes its
// absolute index; what must hold is that non-output items keep their relative
// order and their exact bytes.
func quirkNonOutputSignature(t *testing.T, items []responses.ResponseInputItemUnionParam) string {
	t.Helper()
	var parts []string
	for _, it := range items {
		if _, ok := isResponsesCall(it); ok {
			continue
		}
		if _, ok := isResponsesOutput(it); ok {
			continue
		}
		parts = append(parts, quirkMarshalAll(t, []responses.ResponseInputItemUnionParam{it}))
	}
	return joinQuirk(parts)
}

func quirkOutputSignature(t *testing.T, items []responses.ResponseInputItemUnionParam) string {
	t.Helper()
	var parts []string
	for _, it := range items {
		if id, ok := isResponsesOutput(it); ok {
			parts = append(parts, id)
		}
	}
	return joinQuirk(parts)
}

func joinQuirk(parts []string) string {
	var out strings.Builder
	for i, p := range parts {
		if i > 0 {
			out.WriteString("|")
		}
		out.WriteString(p)
	}
	return out.String()
}

// Benchmark: long tool histories must stay linear in item count. Sub-
// benchmarks at 250/500/1000/2000 pairs make an O(n^2) regression visible as
// a 4x per-doubling cost jump.
func BenchmarkReorderResponsesAdjacency(b *testing.B) {
	for _, calls := range []int{250, 500, 1000, 2000} {
		b.Run(fmt.Sprintf("calls=%d", calls), func(b *testing.B) {
			items := make([]responses.ResponseInputItemUnionParam, 0, 2*calls)
			ids := make([]string, 0, calls)
			for i := range calls {
				ids = append(ids, fmt.Sprintf("c%d", i))
			}
			for _, id := range ids {
				items = append(items, quirkCallItem(id))
			}
			for _, id := range ids {
				items = append(items, quirkOutItem(id))
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := reorderResponsesAdjacency(items); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
