package openaiusage_test

// R7-C1C (PR #659 adversarial repair): bounded output-image overlap slice —
// open-source wire half.
//
// The OpenAI native Chat/Responses output detail has no image token member, so
// the only supported way the adapter emits a native directional OUTPUT image
// token is the recognized compatible-provider flat alias family
// (output_image_tokens / image_output_tokens / completion_image_tokens). That
// flat measure is a token quantity included in the ordinary completion/output
// total, exactly like the nested Chat input image tokens are included in the
// prompt total.
//
// The frozen provider-family schema declares the OUTPUT subset edge: an
// aggregate output_token plus its included native output image token is a typed
// overlap rather than an additive double bill, while an aggregate-only or
// child-only tariff keeps its prior behavior and no output image token is
// fabricated from a nested completion detail.
//
// The real-rater monetary assertions of this file (schema-free control 72/0;
// frozen overlap/conflict suppressing output lines while the unrelated input
// stays payable 20/0; aggregate-only complete 47/0; child-only partial with
// 25/0; every alias typed overlap/conflict) are preserved by task 3.5 in the
// Enterprise tree: enterprise/internal/preservation/openai_output_image_test.go.
// The open-source half below keeps the adapter/wire evidence assertions.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// wireFlatOutputImageChatUsage is the recognized compatible-provider flat Chat
// shape: the ordinary completion_tokens aggregate plus one flat output image
// token member. It is the output-side analogue of the established flat
// multimodal fixture in
// enterprise/internal/infra/billingstore/phase19_1_lifecycle_certification_test.go.
func wireFlatOutputImageChatUsage(key, value string) string {
	return `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19,"` + key + `":` + value + `}}`
}

// wireFlatOutputImageChatNoEvidence is the same shape with no output image token
// member at all (the absent shape).
func wireFlatOutputImageChatNoEvidence() string {
	return `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19}}`
}

// wireFlatChatEvidence drives one real Chat completion response through the
// production adapter and provider evidence buffer, returning the bound call ID
// and the single intact observation.
func wireFlatChatEvidence(t *testing.T, chatJSON, label string) (usageevidence.BillingCallID, sdkmetering.Observation) {
	t.Helper()
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	return callID, openWireObservations(t, server, "chat", wireIdentityForCall(callID, label))[0]
}

func assertNativeOutputImageTokens(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionOutput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native output image_token unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native output image_token = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native output image_token measure missing: %+v", observation.Measures)
}

func assertNoOutputImageToken(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			t.Fatalf("unexpected output image_token measure: %+v", measure)
		}
	}
}

// TestOpenAIUsageEvidence_OutputImageAliasesShareCanonicalOverlap keeps the wire
// half of the original vector: every recognized flat compatible-provider output
// image token alias resolves to the same canonical native child, yielding
// exactly one native output image token measure with the provider-reported
// value. The typed aggregate+image overlap is Enterprise-owned.
func TestOpenAIUsageEvidence_OutputImageAliasesShareCanonicalOverlap(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"output_image_tokens", "image_output_tokens", "completion_image_tokens"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, observation := wireFlatChatEvidence(t, wireFlatOutputImageChatUsage(key, "5"), "alias-"+key)
			assertNativeOutputImageTokens(t, observation, "5")

			var count int
			for _, measure := range observation.Measures {
				if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%s alias produced %d native output image measures, want 1", key, count)
			}
		})
	}
}

// TestOpenAIUsageEvidence_OutputImageConflictingAliasFirstWinsAndOverlaps keeps
// the wire half of the original vector: a response carrying two conflicting
// aliases keeps exactly one native output image token measure (the first
// declared alias wins, matching the adapter's existing alias policy). The typed
// overlap classification is Enterprise-owned.
func TestOpenAIUsageEvidence_OutputImageConflictingAliasFirstWinsAndOverlaps(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19,"output_image_tokens":5,"image_output_tokens":9}}`
	_, observation := wireFlatChatEvidence(t, chatJSON, "alias-conflict")
	assertNativeOutputImageTokens(t, observation, "5")

	var count int
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("conflicting output image aliases produced %d native measures, want 1: %+v", count, observation.Measures)
	}
}

// TestOpenAIUsageEvidence_OutputImageAbsentNullZeroNotOverlap keeps the wire
// half of the original vector: absent and null are not an observable quantity,
// while an explicit zero is present as an observed zero quantity. The
// aggregate-only 47/0 completeness is Enterprise-owned.
func TestOpenAIUsageEvidence_OutputImageAbsentNullZeroNotOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		chatJSON string
		hasImage bool
	}{
		{name: "absent", chatJSON: wireFlatOutputImageChatNoEvidence()},
		{name: "null", chatJSON: wireFlatOutputImageChatUsage("output_image_tokens", "null")},
		{name: "explicit_zero", chatJSON: wireFlatOutputImageChatUsage("output_image_tokens", "0"), hasImage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, observation := wireFlatChatEvidence(t, tc.chatJSON, "shape-"+tc.name)
			if tc.hasImage {
				assertNativeOutputImageTokens(t, observation, "0")
			} else {
				assertNoOutputImageToken(t, observation)
			}
		})
	}
}

// TestNativeUsageMeasuresDoesNotFabricateOutputImageTokens proves the optional
// output partition edge is not fed by an invented Chat completion image field: a
// hypothetical nested completion_tokens_details.image_tokens member (or
// Responses output detail image_tokens) must not create a native output image
// token measure, so the output optional partition edge can only ever bind to the
// recognized flat provider vocabulary. The nested Chat
// prompt_tokens_details.image_tokens input measure remains intact.
func TestNativeUsageMeasuresDoesNotFabricateOutputImageTokens(t *testing.T) {
	t.Parallel()
	raw := `{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"image_tokens":3}}`
	measures := openaiusage.NativeUsageMeasures(raw)
	for _, measure := range measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			t.Fatalf("completion_tokens_details.image_tokens must not fabricate an output image token: %+v", measure)
		}
	}
	var inputImage bool
	for _, measure := range measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionInput {
			inputImage = true
		}
	}
	if !inputImage {
		t.Fatalf("nested prompt_tokens_details.image_tokens input measure missing: %+v", measures)
	}
}
