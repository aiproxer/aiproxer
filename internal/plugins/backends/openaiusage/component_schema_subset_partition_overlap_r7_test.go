package openaiusage_test

// R7 (PR #659 adversarial repair, minimum double-charge blocker): open-source
// wire half of the priced-subset vector.
//
// OpenAI prompt_tokens_details reports an included cached_tokens subset while
// the native text/audio pair forms a complete partition of the ordinary prompt
// total. The frozen provider-family schema declares both. When the tariff prices
// the complete text/audio partition AND the included cached subset but leaves
// the aggregate parent unpriced, the cached tokens are already inside the
// partition, so the rater must fail closed as a typed overlap instead of posting
// the additive 8*2 + 3*5 + 4*7 = 59 double charge. The output/reasoning and
// input/cache-write sibling slices are the same shape.
//
// The real-rater monetary assertions of this file (all three table vectors →
// ErrSchemaOverlapConflict + conflict completeness, never 59/0 or 76/0, subset
// and complete siblings not payable; observed-zero child still conflicts, never
// 44/0) are preserved by task 3.5 in the Enterprise tree:
// enterprise/internal/preservation/openai_subset_overlap_test.go. The
// open-source half below keeps the adapter/wire evidence assertions: the real
// parsed cached/reasoning/cache-write measure identities and the observed-zero
// audio boundary.

import (
	"testing"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// wireSubsetPartitionChatCachedSuccess is the exact cited Chat shape:
// prompt_tokens 11 = text 8 + audio 3, with an included cached subset of 4.
const wireSubsetPartitionChatCachedSuccess = `{"id":"chat-subset-partition-cached","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cached_tokens":4},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// wireSubsetPartitionChatReasoningSuccess is the output-side analogue: the
// ordinary completion total carries the included reasoning subset while the
// native output text/audio pair is a complete partition.
const wireSubsetPartitionChatReasoningSuccess = `{"id":"chat-subset-partition-reasoning","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":3}}}`

// wireSubsetPartitionChatCacheWriteSuccess is the standard cache-write sibling:
// the ordinary prompt total carries the included standard cache_write_tokens
// subset (emitted under the native detail schema) alongside the text/audio
// partition.
const wireSubsetPartitionChatCacheWriteSuccess = `{"id":"chat-subset-partition-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cache_write_tokens":5},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

func assertSubsetPartitionMeasure(t *testing.T, observation sdkmetering.Observation, direction sdkmetering.FlowDirection, component, schemaID, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Direction != direction || measure.Key.Component != component {
			continue
		}
		if measure.Key.SchemaID != schemaID {
			t.Fatalf("%s/%s schema = %q, want %q", direction, component, measure.Key.SchemaID, schemaID)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("%s/%s value = %+v, want observed %s", direction, component, measure.Value, want)
		}
		return
	}
	t.Fatalf("%s/%s measure missing: %+v", direction, component, observation.Measures)
}

// TestOpenAIUsageEvidence_PricedSubsetConflictsWithCompletePartition keeps the
// wire half of the original intact-wire RED vector: through the real adapter,
// the cached subset parses under the default inclusion schema (4), the
// reasoning subset under the default inclusion schema (3), and the standard
// cache-write subset under the native detail schema (5), each alongside the
// text/audio partition members. The typed overlap/conflict rating over these
// shapes is Enterprise-owned.
func TestOpenAIUsageEvidence_PricedSubsetConflictsWithCompletePartition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		chatJSON   string
		direction  sdkmetering.FlowDirection
		subsetComp string
		schemaID   string
		wantValue  string
	}{
		{
			name: "input_cached", chatJSON: wireSubsetPartitionChatCachedSuccess,
			direction: sdkmetering.DirectionInput, subsetComp: sdkmetering.ComponentCacheReadInputToken,
			schemaID: sdkmetering.DefaultInclusionSchemaID, wantValue: "4",
		},
		{
			name: "output_reasoning", chatJSON: wireSubsetPartitionChatReasoningSuccess,
			direction: sdkmetering.DirectionOutput, subsetComp: sdkmetering.ComponentReasoningOutputToken,
			schemaID: sdkmetering.DefaultInclusionSchemaID, wantValue: "3",
		},
		{
			name: "input_cache_write", chatJSON: wireSubsetPartitionChatCacheWriteSuccess,
			direction: sdkmetering.DirectionInput, subsetComp: sdkmetering.ComponentCacheWriteInputToken,
			schemaID: "openai.usage.v2", wantValue: "5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chatJSON, wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, tc.name))[0]

			// The native text/audio partition members carry their provider values.
			assertSubsetPartitionMeasure(t, observation, sdkmetering.DirectionInput, sdkmetering.ComponentTextToken, "openai.usage.v2", "8")
			assertSubsetPartitionMeasure(t, observation, sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "openai.usage.v2", "3")
			assertSubsetPartitionMeasure(t, observation, sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken, "openai.usage.v2", "5")
			assertSubsetPartitionMeasure(t, observation, sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken, "openai.usage.v2", "4")
			// The included subset parses once under its declared schema identity.
			assertSubsetPartitionMeasure(t, observation, tc.direction, tc.subsetComp, tc.schemaID, tc.wantValue)
		})
	}
}

// wireZeroAudioSubsetChatSuccess is the exact zero-boundary Chat shape: the
// ordinary prompt total is 8, the native text/audio partition is 8 + 0, and the
// included cached subset is 4.
const wireZeroAudioSubsetChatSuccess = `{"id":"chat-zero-audio-subset","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":0,"cached_tokens":4},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// TestOpenAIUsageEvidence_ObservedZeroPartitionChildStillConflicts keeps the
// wire half of the original zero-boundary RED vector: the adapter surfaces the
// observed-zero input audio member as an explicit, complete partition member
// rather than an absent-measure approximation. The typed conflict rating over
// it is Enterprise-owned.
func TestOpenAIUsageEvidence_ObservedZeroPartitionChildStillConflicts(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireZeroAudioSubsetChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "zero-audio"))[0]

	// The adapter must surface an explicit observed-zero input audio measure;
	// otherwise this vector is not exercising the observed-zero boundary.
	assertObservedZeroInputAudio(t, observation)
}

func assertObservedZeroInputAudio(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Direction != sdkmetering.DirectionInput || measure.Key.Component != sdkmetering.ComponentAudioToken {
			continue
		}
		if measure.Quality != sdkmetering.QualityObserved || measure.Value == nil || measure.Value.Coefficient != "0" {
			t.Fatalf("input audio measure = %+v, want explicit observed zero", measure)
		}
		return
	}
	t.Fatalf("explicit observed-zero input audio measure missing: %+v", observation.Measures)
}
