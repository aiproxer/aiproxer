package openaiusage_test

// R7-C1D (PR #659 adversarial repair): bounded cached/reasoning token overlap
// slice — open-source wire half.
//
// Primary provider-family containment (verified against the official OpenAI
// usage type definitions and prompt-caching guide):
//
//   - Chat: $.usage.prompt_tokens_details.cached_tokens is a subset of
//     $.usage.prompt_tokens ("Cached tokens present in the prompt"), and
//     $.usage.completion_tokens_details.reasoning_tokens is a subset of
//     $.usage.completion_tokens (the SDK documents that reasoning tokens are
//     "still counted in the total completion tokens for purposes of billing,
//     output, and context window limits").
//   - Responses: $.usage.input_tokens_details.cached_tokens is a subset of
//     $.usage.input_tokens, and
//     $.usage.output_tokens_details.reasoning_tokens is a subset of
//     $.usage.output_tokens.
//
// The adapter maps these quantities onto the host-neutral
// input/cache_read_input_token and output/reasoning_output_token components
// under metering.DefaultInclusionSchemaID (providerTokenMeasures), so the two
// containment edges are declared in that same schema identity. They are subset
// edges, not partition members: cached and reasoning tokens are not the
// complete text/audio partition, and a child-only rule over one of them must
// never prove the parent aggregate's complete coverage.
//
// The real-rater monetary assertions of this file (additive 90/0 control with
// components 22/0, 27/0, 20/0, 21/0; aggregate-only 49/0; child-only typed
// partial with 20/0 and 21/0; typed overlap classification) are preserved by
// task 3.5 in the Enterprise tree:
// enterprise/internal/preservation/openai_cached_reasoning_test.go. The
// open-source half below keeps the adapter/wire evidence assertions (real
// measure identity and presence, single recognized cache-read measure) without
// the retail rating path.

import (
	"testing"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestOpenAIUsageEvidence_CachedReasoningUnrecognizedAliasDoesNotDoubleEmit
// proves a conflicting flat compatible-provider spelling does not create a
// second cache-read measure or override the recognized typed
// prompt_tokens_details.cached_tokens value. The aggregate+subset typed-overlap
// half of the original vector is Enterprise-owned (see the package comment).
func TestOpenAIUsageEvidence_CachedReasoningUnrecognizedAliasDoesNotDoubleEmit(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"input_cache_read_tokens":99,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias-conflict"))[0]

	var count int
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheReadInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		count++
		if measure.Value == nil || measure.Value.Coefficient != "4" {
			t.Fatalf("cache_read_input_token = %+v, want recognized native 4 (alias must not override)", measure.Value)
		}
	}
	if count != 1 {
		t.Fatalf("conflicting cache alias produced %d cache_read measures, want 1", count)
	}
}

// TestOpenAIUsageEvidence_CacheWriteWireFieldNowRecognized records the R7-C1E
// reversal of the former residual. The standard Chat
// prompt_tokens_details.cache_write_tokens member is part of the adapter's
// recognized native wire path (the official SDK type contract exposes it as
// CompletionUsagePromptTokensDetails.cache_write_tokens), so it is surfaced as
// an observed native input cache-write measure and carries a frozen subset
// edge. The recognized cached_tokens member is unaffected.
func TestOpenAIUsageEvidence_CacheWriteWireFieldNowRecognized(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6},"completion_tokens_details":{"reasoning_tokens":3}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-recognized"))[0]

	var cacheRead int
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentCacheWriteInputToken {
			if measure.Value == nil || measure.Value.Coefficient != "6" || measure.Key.Direction != sdkmetering.DirectionInput {
				t.Fatalf("standard cache_write_tokens measure = %+v, want observed input 6", measure)
			}
		}
		if measure.Key.Component == sdkmetering.ComponentCacheReadInputToken {
			cacheRead++
		}
	}
	if cacheRead != 1 {
		t.Fatalf("recognized cached_tokens measure count=%d, want 1", cacheRead)
	}
}
