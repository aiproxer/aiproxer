package openaiusage_test

// R7-C1E (PR #659 adversarial repair): bounded standard cache-write token
// mapping — open-source wire half.
//
// Primary provider-family containment (verified against the official OpenAI
// SDK type contract in this worktree's module cache,
// github.com/openai/openai-go/v3@v3.61.0):
//
//   - Chat: CompletionUsagePromptTokensDetails carries
//     `cache_write_tokens int64` (`json:"cache_write_tokens"`, completion.go
//     line 241) alongside cached_tokens/audio_tokens/image_tokens/text_tokens.
//   - Responses: ResponseUsageInputTokensDetails carries
//     `cache_write_tokens int64` (`json:"cache_write_tokens" api:"required"`).
//
// The standard member is a real provider family wire field, not an invented
// location. The adapter's native usage mapping (NativeUsageMeasures /
// NativeUsageEvidence) is the provider-family detail path, so the standard
// member maps onto the host-neutral input/cache_write_input_token component
// under openai.usage.v2 and retains its exact provider wire path. The
// non-standard x_aip_cache_write_tokens extension remains a compatible-provider
// fallback only and must never stand in when the standard member is present.
//
// The frozen OpenAI provider-family inclusion schema declares the input
// aggregate -> cache-write subset edge (in the matching schema identity) so a
// tariff that prices both the ordinary prompt total and its included
// cache-write tokens is a typed overlap rather than an additive double bill.
// Because it is a subset edge and not a partition member, an aggregate-only
// tariff stays complete and a child-only tariff stays a typed partial.
//
// The real-rater monetary assertions of this file (additive 91/0 and 111/0
// controls with 42/0 cache-write and 20/0 cache-read; aggregate-only 49/0;
// child-only typed partial with 42/0; typed overlap classification; malformed
// standard child-only partial) are preserved by task 3.5 in the Enterprise
// tree: enterprise/internal/preservation/openai_cache_write_test.go. The
// open-source half below keeps the adapter/wire evidence assertions.

import (
	"testing"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// wireChatCacheWriteSuccess is the true Chat shape carrying the standard
// prompt_tokens_details.cache_write_tokens member.
const wireChatCacheWriteSuccess = `{"id":"chat-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cache_write_tokens":6}}}`

// wireResponsesCacheWriteSuccess is the true Responses shape carrying the
// standard input_tokens_details.cache_write_tokens member.
const wireResponsesCacheWriteSuccess = `{"id":"resp-cache-write","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"cache_write_tokens":6}}}`

// wireChatCacheReadWriteSuccess carries both the recognized cached_tokens
// subset and the standard cache_write_tokens member so the two measures cannot
// be conflated.
const wireChatCacheReadWriteSuccess = `{"id":"chat-cache-read-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6}}}`

// wireChatCacheWriteUsage builds the Chat cache-write shape with a
// caller-supplied prompt_tokens_details object so absent/null/zero/malformed
// members can be exercised.
func wireChatCacheWriteUsage(promptDetails string) string {
	return `{"id":"chat-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":` +
		promptDetails + `}}`
}

func assertNativeInputCacheWrite(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheWriteInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native input cache_write unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Key.SchemaID != "openai.usage.v2" {
			t.Fatalf("native input cache_write schema = %q, want openai.usage.v2", measure.Key.SchemaID)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native input cache_write = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native input cache_write measure missing: %+v", observation.Measures)
}

func assertNoDefaultInputCacheWrite(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentCacheWriteInputToken &&
			measure.Key.SchemaID == sdkmetering.DefaultInclusionSchemaID {
			t.Fatalf("unexpected default-inclusion cache_write measure: %+v", measure)
		}
	}
}

func assertCacheWriteEvidence(t *testing.T, observation sdkmetering.Observation, wantPath, wantLexeme string) {
	t.Helper()
	for _, field := range observation.Evidence {
		if field.Path != wantPath {
			continue
		}
		if !field.Present || field.Lexeme != wantLexeme {
			t.Fatalf("cache_write evidence %q = %+v, want present lexeme %q", wantPath, field, wantLexeme)
		}
		return
	}
	t.Fatalf("cache_write evidence path %q missing: %+v", wantPath, observation.Evidence)
}

func assertNoInputCacheWriteMeasure(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentCacheWriteInputToken && measure.Key.Direction == sdkmetering.DirectionInput {
			t.Fatalf("unexpected input cache_write measure: %+v", measure)
		}
	}
}

// TestOpenAIUsageEvidence_CacheWriteStandardFieldObserved is the primary
// R7-C1E RED vector for the standard member. The real adapter must surface the
// standard Chat prompt_tokens_details.cache_write_tokens (and Responses
// input_tokens_details.cache_write_tokens) as an observed native input
// cache-write measure and retain its exact provider wire path.
func TestOpenAIUsageEvidence_CacheWriteStandardFieldObserved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		path     string
		chat     string
		respons  string
		wantPath string
	}{
		{
			name: "chat", path: "chat",
			chat: wireChatCacheWriteSuccess, respons: wireResponsesSuccess,
			wantPath: "$.usage.prompt_tokens_details.cache_write_tokens",
		},
		{
			name: "responses", path: "responses",
			chat: wireVisionChatSuccess, respons: wireResponsesCacheWriteSuccess,
			wantPath: "$.usage.input_tokens_details.cache_write_tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.respons)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "cache-write-"+tc.name))[0]

			assertNativeInputCacheWrite(t, observation, "6")
			assertCacheWriteEvidence(t, observation, tc.wantPath, "6")
			assertNoDefaultInputCacheWrite(t, observation)
		})
	}
}

// TestOpenAIUsageEvidence_CacheReadWriteRatesStayDistinct keeps the wire half
// of the original vector: the standard cache-write subset is not conflated with
// the recognized cache-read subset — exactly one measure each, carrying the
// provider-reported 6 and 4. The frozen both-priced typed-overlap and the
// schema-free additive 111/0 money assertions are Enterprise-owned.
func TestOpenAIUsageEvidence_CacheReadWriteRatesStayDistinct(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheReadWriteSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-read-write"))[0]
	assertNativeInputCacheWrite(t, observation, "6")

	var cacheRead int
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheReadInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		cacheRead++
		if measure.Value == nil || measure.Value.Coefficient != "4" {
			t.Fatalf("cache_read_input_token = %+v, want recognized native 4", measure.Value)
		}
	}
	if cacheRead != 1 {
		t.Fatalf("recognized cached_tokens measure count=%d, want 1", cacheRead)
	}
}

// TestOpenAIUsageEvidence_CacheWriteShapesAbsentNullZeroNotOverlap keeps the
// wire half of the original vector: absent and null standard members carry no
// observable measure, an explicit zero is present and observed with no payable
// contribution. The rating half (aggregate+cache-write stays complete at 49/0)
// is Enterprise-owned.
func TestOpenAIUsageEvidence_CacheWriteShapesAbsentNullZeroNotOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		promptDetails string
		hasMeasure    bool
	}{
		{name: "absent", promptDetails: `{}`},
		{name: "null", promptDetails: `{"cache_write_tokens":null}`},
		{name: "explicit_zero", promptDetails: `{"cache_write_tokens":0}`, hasMeasure: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatCacheWriteUsage(tc.promptDetails), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-shape-"+tc.name))[0]
			if tc.hasMeasure {
				assertNativeInputCacheWrite(t, observation, "0")
			} else {
				assertNoInputCacheWriteMeasure(t, observation)
			}
		})
	}
}

// TestOpenAIUsageEvidence_CacheWriteMalformedStandardBeatsExtensionAlias keeps
// the wire half of the original vector: a present-but-malformed standard
// cache_write_tokens member is authoritative — the observation holds exactly
// one explicit unavailable native input cache-write measure, the conflicting
// valid x_aip_cache_write_tokens extension alias neither stands in for it nor
// adds a second observed default-inclusion measure, and the malformed lexeme
// never reaches evidence. The child-only fail-closed rating half is
// Enterprise-owned.
func TestOpenAIUsageEvidence_CacheWriteMalformedStandardBeatsExtensionAlias(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteUsage(`{"cache_write_tokens":"oops","x_aip_cache_write_tokens":6}`), wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-malformed"))[0]

	var found int
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheWriteInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		found++
		if measure.Key.SchemaID != "openai.usage.v2" {
			t.Fatalf("malformed standard cache_write schema = %q, want the native detail schema", measure.Key.SchemaID)
		}
		if measure.Quality != sdkmetering.QualityUnavailable || measure.Value != nil {
			t.Fatalf("malformed standard cache_write measure = %+v, want unavailable with no value", measure)
		}
	}
	if found != 1 {
		t.Fatalf("malformed standard + extension produced %d input cache_write measures, want one unavailable: %+v", found, observation.Measures)
	}
	assertNoDefaultInputCacheWrite(t, observation)
	for _, field := range observation.Evidence {
		if field.Path == "$.usage.prompt_tokens_details.cache_write_tokens" || field.Lexeme == "6" {
			t.Fatalf("malformed standard + extension retained unauthorized evidence: %+v", field)
		}
	}
}
