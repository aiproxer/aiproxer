package openaiusage_test

// R7 (PR #666 CodeRabbit 4098229959, native image-token overlap): the OpenAI
// Chat/Responses wire family reports prompt_tokens_details.image_tokens as a
// directional input image TOKEN quantity that is disjoint from text_tokens and
// audio_tokens; together they account for the ordinary prompt total. The frozen
// provider-family schema previously declared image tokens as a subset of the
// text/audio partition, so a tariff that priced the true text+audio+image
// partition was misclassified as an ErrSchemaOverlapConflict with no payable
// lines. These vectors pin the corrected optional-partition semantics and pin
// that absence, null and explicit zero stay distinguishable.
//
// The real-rater monetary assertions of this file (complete 76/0 with input
// image token 2/0; complete 78/0 with all six directional amounts; absent-image
// complete 74/0; aggregate+image typed overlap) are preserved by task 3.5 in
// the Enterprise tree:
// enterprise/internal/preservation/openai_image_token_partition_test.go. The
// open-source half below keeps the adapter/wire evidence assertions (native
// image token presence and non-coercion of absence).

import (
	"testing"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// wireVisionChatWithOutputImage is a consistent Chat vision shape whose input
// and output partitions both include a native image token member:
// input 13 = text 8 + audio 3 + image 2, output 11 = text 5 + audio 4 + image 2.
const wireVisionChatWithOutputImage = `{"id":"chat-vision-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":11,"total_tokens":24,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4},"output_image_tokens":2}}`

// wireAbsentImageChatSuccess and wireAbsentImageResponsesSuccess are the clean
// audio-capable shapes with no image member at all (and no other priced detail
// member), so an omitted image token is the only evidence difference.
const wireAbsentImageChatSuccess = `{"id":"chat-no-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

const wireAbsentImageResponsesSuccess = `{"id":"resp-no-image","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3},"output_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

func assertNativeDirectionalImageTokens(t *testing.T, observation sdkmetering.Observation, direction sdkmetering.FlowDirection, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != direction {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native %s image_token unit = %q, want %q", direction, measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native %s image_token = %+v, want %s", direction, measure.Value, want)
		}
		return
	}
	t.Fatalf("native %s image_token measure missing: %+v", direction, observation.Measures)
}

func assertNoDirectionalImageTokens(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken {
			t.Fatalf("unexpected image_token measure: %+v", measure)
		}
	}
}

// TestOpenAIUsageEvidence_ChatImageTokenPartitionMixed keeps the wire half of
// the original vector: the real Chat adapter surfaces the disjoint native input
// image token alongside the text/audio children, and the recognized flat output
// image token alongside the output children — all six directional members are
// present exactly once with their provider-reported quantities. The 78/0
// complete rating is Enterprise-owned.
func TestOpenAIUsageEvidence_ChatImageTokenPartitionMixed(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatWithOutputImage, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "vision-mixed"))[0]
	assertNativeInputImageTokens(t, observation, "2")
	assertNativeDirectionalImageTokens(t, observation, sdkmetering.DirectionOutput, "2")
}

// TestOpenAIUsageEvidence_ImageTokenAbsentStillCompletes keeps the wire half of
// the original vector: an omitted native image token is not a member of the
// reported partition and is never coerced into a synthetic zero image member in
// either Chat or Responses. The 74/0 complete rating is Enterprise-owned.
func TestOpenAIUsageEvidence_ImageTokenAbsentStillCompletes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		path    string
		chat    string
		respons string
	}{
		{name: "chat", path: "chat", chat: wireAbsentImageChatSuccess, respons: wireResponsesSuccess},
		{name: "responses", path: "responses", chat: wireVisionChatSuccess, respons: wireAbsentImageResponsesSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.respons)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "absent-image-"+tc.name))[0]
			assertNoDirectionalImageTokens(t, observation)
		})
	}
}
