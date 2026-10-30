package parity_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// A producer that reports an inclusive input counter keeps that
// interpretation on every frontend that reports a prompt count.
//
// The Gemini CLI ACP agent reports usageMetadata.promptTokenCount, which
// already contains the cached content tokens, and a total of input plus
// output. The connector asserts no input relation for it, so the prompt count
// is the input the producer reported, the cached tokens stay inside that
// prompt, and the total covers prompt plus completion.
//
// The Anthropic frontend is not covered here: it reports the producer's input
// counter verbatim, so its effective Anthropic total input (input plus
// cache_read plus cache_creation) counts the cached subset twice for an
// inclusive producer. That defect predates this slice — the Anthropic encoder
// tests pin the same passthrough for the inclusive OpenAI-native producers —
// and it is corrected with the cross-frontend translation work, not here.
func TestUsage_semanticParity_inclusiveInputAcrossPromptFrontends(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		cacheRead     int
		cacheWrite    int
		uncachedInput int
	}{
		{name: "cache reads only", cacheRead: 80, uncachedInput: 40},
		{name: "cache reads and writes", cacheRead: 80, cacheWrite: 20, uncachedInput: 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hi"},
				{
					Kind:             aipapi.EventUsageDelta,
					InputTokens:      120,
					OutputTokens:     45,
					CacheReadTokens:  tc.cacheRead,
					CacheWriteTokens: tc.cacheWrite,
					TotalTokens:      165,
					UsagePresence: aipapi.UsagePresence{
						InputTokens: true, OutputTokens: true,
						CacheReadTokens: true, CacheWriteTokens: tc.cacheWrite > 0, TotalTokens: true,
					},
				},
				{Kind: aipapi.EventResponseFinished},
			}

			chat := chatUsage(t, events)
			if chat.PromptTokens != 120 || chat.CompletionTokens != 45 || chat.TotalTokens != 165 {
				t.Fatalf("chat usage = %+v", chat)
			}
			if chat.Details.CachedTokens != tc.cacheRead || chat.Details.UncachedTokens != tc.uncachedInput {
				t.Fatalf("chat prompt details = %+v", chat.Details)
			}
			if chat.Details.CacheWrite != tc.cacheWrite {
				t.Fatalf("chat cache write = %+v", chat.Details)
			}

			responses := responsesUsage(t, events)
			if responses.InputTokens != 120 || responses.OutputTokens != 45 || responses.TotalTokens != 165 {
				t.Fatalf("responses usage = %+v", responses)
			}
			if responses.Details.CachedTokens != tc.cacheRead || responses.Details.UncachedTokens != tc.uncachedInput {
				t.Fatalf("responses input details = %+v", responses.Details)
			}
			if responses.Details.CacheWrite != tc.cacheWrite {
				t.Fatalf("responses cache write = %+v", responses.Details)
			}

			gem := geminiUsage(t, events)
			if gem.PromptTokenCount != 120 || gem.CandidatesTokenCount != 45 || gem.TotalTokenCount != 165 {
				t.Fatalf("gemini usage = %+v", gem)
			}
			if gem.CachedContentTokenCount != tc.cacheRead || gem.UncachedTokens != tc.uncachedInput {
				t.Fatalf("gemini cache counters = %+v", gem)
			}
			if gem.CacheWriteTokens != tc.cacheWrite {
				t.Fatalf("gemini cache write = %+v", gem)
			}
		})
	}
}
