package largebody_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// =============================================================================
// Task 6.3: Differential Identity Corpus & Parity against Canonical Oracle
//
// Spec Requirements:
// - Requirement 16: Deterministic Request and Economic Identity Parity.
//   Exact canonical semantic identity parity between CallIdentityWriter /
//   IdentityDigest and post-frontend-decode canonical Call diag.StableCallSum.
// - Requirement 17: Conservative Protocol Certification Matrix.
//   Conservative handling across supported protocol shapes (OpenAI Responses,
//   OpenAI Chat, OpenResponses). Non-matching shapes are recorded as documented
//   exclusions for Task 15+.
// - Requirement 18: Preserve Frontend Response, Keepalive, and Session-Carrier State.
//   Parity for deterministic downstream response IDs, message IDs, completion IDs,
//   timestamps (Unix), and trace IDs.
//
// Documented Exclusions for Fast-Path Wire Profile Eligibility (Task 15+):
// 1. Unknown / extra JSON body fields: in canonical decoders (e.g. OpenAI Responses),
//    unrecognized body fields are preserved into Extensions[openrouterwire.ExtraBodyExtPrefix+k].
//    Wire fast-path identity derivation cannot know opaque extra fields without
//    full unmarshaling; requests containing unrecognized top-level body keys are
//    canonical-only (Req 17.2).
// 2. OpenResponses continuation / storage defaults: OpenResponses create defaults
//    store=true unless explicitly set to false; requests without explicit store:false
//    or with previous_response_id are canonical-only until continuation/storage parity
//    is implemented (Req 17.4).
// 3. Normalization-sensitive legacy aliases/repairs: requests relying on frontend repair
//    of malformed message histories, legacy aliases, or tool-call fixes are canonical-only (Req 17.3, 17.5).
// 4. Custom Call-shaped callbacks: requests whose configured admission or billing
//    uses arbitrary BillingIdentity callbacks inspecting the full Call struct remain
//    canonical-only (Req 16.6, 19.1).
// =============================================================================

// DownstreamDeterministicIDs captures all downstream IDs derived from the request identity.
type DownstreamDeterministicIDs struct {
	Sum                [32]byte
	Token              string
	CallID             string
	Unix               int64
	ResponseID         string // OpenAI Responses: "resp_" + Token
	MessageID          string // OpenAI Responses: "msg_resp_" + Token
	CompletionID       string // OpenAI Chat: "chatcmpl_" + Token
	AnthropicMessageID string // Anthropic Messages: "msg_" + Token
	TraceID            string // Frontend pipe trace: CallID
}

func computeOracleDownstreamIDs(call *aipapi.Call) DownstreamDeterministicIDs {
	sum := diag.StableCallSum(call)
	token := diag.StableCallToken(call)
	callID := diag.StableCallID(call)
	unixTS := diag.StableUnix(call)
	return DownstreamDeterministicIDs{
		Sum:                sum,
		Token:              token,
		CallID:             callID,
		Unix:               unixTS,
		ResponseID:         "resp_" + token,
		MessageID:          "msg_resp_" + token,
		CompletionID:       "chatcmpl_" + token,
		AnthropicMessageID: "msg_" + token,
		TraceID:            callID,
	}
}

func computeWriterDownstreamIDs(digest largebody.IdentityDigest, explicitID string) DownstreamDeterministicIDs {
	token := digest.Token()
	callID := digest.CallID(explicitID)
	unixTS := digest.Unix()
	return DownstreamDeterministicIDs{
		Sum:                digest.Sum(),
		Token:              token,
		CallID:             callID,
		Unix:               unixTS,
		ResponseID:         "resp_" + token,
		MessageID:          "msg_resp_" + token,
		CompletionID:       "chatcmpl_" + token,
		AnthropicMessageID: "msg_" + token,
		TraceID:            callID,
	}
}

func assertDownstreamParity(t *testing.T, label string, got, want DownstreamDeterministicIDs) {
	t.Helper()
	if got.Sum != want.Sum {
		t.Fatalf("[%s] Sum mismatch:\ngot:  %x\nwant: %x", label, got.Sum, want.Sum)
	}
	if got.Token != want.Token {
		t.Fatalf("[%s] Token mismatch: got %q, want %q", label, got.Token, want.Token)
	}
	if got.CallID != want.CallID {
		t.Fatalf("[%s] CallID mismatch: got %q, want %q", label, got.CallID, want.CallID)
	}
	if got.Unix != want.Unix {
		t.Fatalf("[%s] Unix timestamp mismatch: got %d, want %d", label, got.Unix, want.Unix)
	}
	if got.ResponseID != want.ResponseID {
		t.Fatalf("[%s] ResponseID mismatch: got %q, want %q", label, got.ResponseID, want.ResponseID)
	}
	if got.MessageID != want.MessageID {
		t.Fatalf("[%s] MessageID mismatch: got %q, want %q", label, got.MessageID, want.MessageID)
	}
	if got.CompletionID != want.CompletionID {
		t.Fatalf("[%s] CompletionID mismatch: got %q, want %q", label, got.CompletionID, want.CompletionID)
	}
	if got.AnthropicMessageID != want.AnthropicMessageID {
		t.Fatalf("[%s] AnthropicMessageID mismatch: got %q, want %q", label, got.AnthropicMessageID, want.AnthropicMessageID)
	}
	if got.TraceID != want.TraceID {
		t.Fatalf("[%s] TraceID mismatch: got %q, want %q", label, got.TraceID, want.TraceID)
	}
}

// streamCallIdentity streams the call through CallIdentityWriter in chunks of chunkSize bytes.
func streamCallIdentity(t *testing.T, call *aipapi.Call, chunkSize int) (largebody.IdentityDigest, error) {
	t.Helper()
	cfg := largebody.CallIdentityConfig{
		ExplicitID:         call.ID,
		Session:            call.Session,
		Route:              call.Route,
		Instructions:       call.Instructions,
		PreviousResponseID: call.PreviousResponseID,
		PromptCacheKey:     call.PromptCacheKey,
		SemanticExtensions: call.SemanticExtensions,
		Tools:              call.Tools,
		ToolChoice:         call.ToolChoice,
		Options:            call.Options,
		Extensions:         call.Extensions,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		return largebody.IdentityDigest{}, err
	}

	if call.Messages != nil {
		if err := w.StartMessages(); err != nil {
			return largebody.IdentityDigest{}, err
		}
		for _, msg := range call.Messages {
			// If single text part, stream it through BeginTextPart
			if len(msg.Parts) == 1 && msg.Parts[0].Kind == aipapi.PartText {
				mw, err := w.BeginMessage(msg.Role)
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				tw, err := mw.BeginTextPart()
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				textBytes := []byte(msg.Parts[0].Text)
				for offset := 0; offset < len(textBytes); offset += chunkSize {
					end := min(offset+chunkSize, len(textBytes))
					if _, err := tw.Write(textBytes[offset:end]); err != nil {
						return largebody.IdentityDigest{}, err
					}
				}
				if err := tw.Close(); err != nil {
					return largebody.IdentityDigest{}, err
				}
				if err := mw.EndMessage(); err != nil {
					return largebody.IdentityDigest{}, err
				}
			} else {
				if err := w.AddMessage(msg); err != nil {
					return largebody.IdentityDigest{}, err
				}
			}
		}
	}

	if call.Items != nil {
		if err := w.StartItems(); err != nil {
			return largebody.IdentityDigest{}, err
		}
		for _, item := range call.Items {
			if item.Kind == aipapi.ItemKindMessage && len(item.Content) == 1 && item.Content[0].Kind == aipapi.ContentPartText &&
				item.Reference == nil && item.ToolCall == nil && item.ToolResult == nil && item.Reasoning == nil && item.Compaction == nil && item.Extension == nil {
				iw, err := w.BeginMessageItem(item.ID, item.Status, item.Role, item.Phase)
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				tw, err := iw.BeginTextContentPart()
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				textBytes := []byte(item.Content[0].Text)
				for offset := 0; offset < len(textBytes); offset += chunkSize {
					end := min(offset+chunkSize, len(textBytes))
					if _, err := tw.Write(textBytes[offset:end]); err != nil {
						return largebody.IdentityDigest{}, err
					}
				}
				if err := tw.Close(); err != nil {
					return largebody.IdentityDigest{}, err
				}
				if err := iw.EndItem(); err != nil {
					return largebody.IdentityDigest{}, err
				}
			} else {
				if err := w.AddItem(item); err != nil {
					return largebody.IdentityDigest{}, err
				}
			}
		}
	}

	return w.Digest()
}

// verifyCallIdentityParity tests that:
// 1. CanonicalCallIdentity matches diag.StableCallSum byte-for-byte.
// 2. CallIdentityWriter.WriteCall matches oracle downstream IDs.
// 3. Streaming CallIdentityWriter across multiple chunk sizes matches oracle downstream IDs.
// 4. Explicit Call.ID vs absent Call.ID preserves identity rules.
func verifyCallIdentityParity(t *testing.T, label string, call *aipapi.Call) {
	t.Helper()
	want := computeOracleDownstreamIDs(call)

	// 1. Direct CanonicalCallIdentity helper
	directDigest := largebody.CanonicalCallIdentity(call)
	gotDirect := computeWriterDownstreamIDs(directDigest, call.ID)
	assertDownstreamParity(t, label+" [CanonicalCallIdentity]", gotDirect, want)

	// 2. CallIdentityWriter via WriteCall
	w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{})
	if err != nil {
		t.Fatalf("[%s] NewCallIdentityWriter: %v", label, err)
	}
	writeDigest, err := w.WriteCall(call)
	if err != nil {
		t.Fatalf("[%s] WriteCall: %v", label, err)
	}
	gotWrite := computeWriterDownstreamIDs(writeDigest, call.ID)
	assertDownstreamParity(t, label+" [WriteCall]", gotWrite, want)

	// 3. Streaming chunked execution across various chunk boundaries
	chunkSizes := []int{1, 3, 7, 32, 512, 4096}
	for _, cs := range chunkSizes {
		streamDigest, err := streamCallIdentity(t, call, cs)
		if err != nil {
			t.Fatalf("[%s] streamCallIdentity(chunk=%d): %v", label, cs, err)
		}
		gotStream := computeWriterDownstreamIDs(streamDigest, call.ID)
		assertDownstreamParity(t, fmt.Sprintf("%s [Stream chunk=%d]", label, cs), gotStream, want)
	}

	// 4. Test explicit ID vs blank ID behavior
	callWithExplicitID := *call
	callWithExplicitID.ID = "explicit-caller-id-999"
	wantExplicit := computeOracleDownstreamIDs(&callWithExplicitID)
	if wantExplicit.Sum != want.Sum {
		t.Fatalf("[%s] Call.ID must not alter stable sum", label)
	}
	if wantExplicit.CallID != "explicit-caller-id-999" {
		t.Fatalf("[%s] Call.ID must retain explicit caller ID", label)
	}
	explicitDigest := largebody.CanonicalCallIdentity(&callWithExplicitID)
	gotExplicit := computeWriterDownstreamIDs(explicitDigest, "explicit-caller-id-999")
	assertDownstreamParity(t, label+" [ExplicitID]", gotExplicit, wantExplicit)
}

// TestDifferentialCorpus_OpenAIResponses tests real decoded OpenAI Responses request bodies
// against the canonical oracle across string inputs, array messages, instructions, tools,
// text config, options, metadata, and authoritative headers.
func TestDifferentialCorpus_OpenAIResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		body    string
		headers http.Header
	}{
		{
			name: "simple string input",
			body: `{"model":"gpt-4o","input":"Hello, how are you today?"}`,
		},
		{
			name: "message array with system instruction",
			body: `{
				"model": "gpt-4o",
				"instructions": "You are a specialized code translation assistant.",
				"input": [
					{"type": "message", "role": "user", "content": "Translate this to Go"}
				]
			}`,
		},
		{
			name: "instructions with multi-paragraph system prompt",
			body: `{
				"model": "gpt-4o",
				"instructions": "System prompt paragraph 1.\n\nSystem prompt paragraph 2.",
				"input": "User query"
			}`,
		},
		{
			name: "function call and output items",
			body: `{
				"model": "gpt-4o",
				"input": [
					{"type": "function_call", "name": "lookup_weather", "call_id": "call_abc123", "arguments": "{\"city\":\"Zurich\"}"},
					{"type": "function_call_output", "call_id": "call_abc123", "output": "Sunny, 22C"}
				]
			}`,
		},
		{
			name: "tools with complex schema and auto tool choice",
			body: `{
				"model": "gpt-4o",
				"input": "Check stock price",
				"tools": [
					{
						"type": "function",
						"name": "get_stock_quote",
						"description": "Fetch real-time stock quote",
						"parameters": {
							"type": "object",
							"properties": {
								"ticker": {"type": "string", "description": "Stock symbol"},
								"extended_hours": {"type": "boolean"}
							},
							"required": ["ticker"]
						},
						"strict": true
					}
				],
				"tool_choice": "auto"
			}`,
		},
		{
			name: "tools with specific function choice and parallel false",
			body: `{
				"model": "gpt-4o",
				"input": "Run analysis",
				"tools": [
					{
						"type": "function",
						"name": "analyze",
						"parameters": {"type": "object"}
					}
				],
				"tool_choice": {"type": "function", "function": {"name": "analyze"}},
				"parallel_tool_calls": false
			}`,
		},
		{
			name: "options and text verbosity",
			body: `{
				"model": "gpt-4o",
				"input": "Provide concise summary",
				"temperature": 0.3,
				"top_p": 0.85,
				"max_output_tokens": 1500,
				"text": {"verbosity": "low"}
			}`,
		},
		{
			name: "metadata and authoritative session headers",
			body: `{
				"model": "gpt-4o",
				"input": "Session continuation",
				"metadata": {
					"client_session_id": "client-sess-42",
					"user_id": "usr-1234"
				}
			}`,
			headers: func() http.Header {
				h := make(http.Header)
				h.Set("X-Session-ID", "auth-sess-99")
				h.Set("X-A-Leg-ID", "aleg-777")
				h.Set("X-Route-Selector", "stub:gpt-4o")
				return h
			}(),
		},
		{
			name: "multilingual unicode and html escapes in prompt",
			body: `{
				"model": "gpt-4o",
				"input": "日本語: こんにちは世界! 中文: 你好，世界! العربية: مرحبا بالعالم! <div>HTML & safe \"quotes\" \\ backslash \u2028\u2029 🚀🌍🧪"
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := openairesponses.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
				Headers:       tc.headers,
			}
			decoded, err := openairesponses.DecodeCreateRequest([]byte(tc.body), opts)
			if err != nil {
				t.Fatalf("DecodeCreateRequest failed: %v", err)
			}
			if decoded.Call == nil {
				t.Fatal("decoded.Call is nil")
			}

			verifyCallIdentityParity(t, "openairesponses:"+tc.name, decoded.Call)
		})
	}
}

// TestDifferentialCorpus_OpenAIChat tests real decoded OpenAI Legacy Chat request bodies
// (/v1/chat/completions) against the canonical oracle across roles, tool calls,
// multi-part contents, reasoning, options, and route selectors.
func TestDifferentialCorpus_OpenAIChat(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		body    string
		headers http.Header
	}{
		{
			name: "standard system user assistant conversation",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant."},
					{"role": "user", "content": "What is the capital of Switzerland?"},
					{"role": "assistant", "content": "The capital of Switzerland is Bern."}
				]
			}`,
		},
		{
			name: "assistant tool calls and tool response",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "What's the weather in Geneva?"},
					{
						"role": "assistant",
						"content": null,
						"tool_calls": [
							{
								"id": "call_12345",
								"type": "function",
								"function": {
									"name": "get_weather",
									"arguments": "{\"location\":\"Geneva\"}"
								}
							}
						]
					},
					{
						"role": "tool",
						"tool_call_id": "call_12345",
						"content": "{\"temp\": 18, \"condition\": \"cloudy\"}"
					}
				]
			}`,
		},
		{
			name: "multi-part user content with text and image",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{
						"role": "user",
						"content": [
							{"type": "text", "text": "Describe this architecture diagram:"},
							{"type": "image_url", "image_url": {"url": "https://example.com/diag.png"}}
						]
					}
				]
			}`,
		},
		{
			name: "tools tool_choice and generation parameters",
			body: `{
				"model": "gpt-4o-mini",
				"messages": [{"role": "user", "content": "Calculate metrics"}],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "compute",
							"description": "Compute math",
							"parameters": {"type": "object"}
						}
					}
				],
				"tool_choice": "required",
				"temperature": 0.7,
				"top_p": 0.95,
				"max_tokens": 800
			}`,
		},
		{
			name: "route selector and session headers",
			body: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "Query with headers"}]
			}`,
			headers: func() http.Header {
				h := make(http.Header)
				h.Set("X-Route-Selector", "custom:azure-openai")
				h.Set("X-Session-ID", "sess-chat-01")
				return h
			}(),
		},
		{
			name: "unicode control characters and escapes",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "Quotes: \"hello \\\"world\\\"\", HTML: <b>&amp;</b>, Newlines: \n\r\t, Separators: \u2028\u2029, Emoji: 👨‍👩‍👧‍👦🚀"}
				]
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := openailegacy.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
				Headers:       tc.headers,
			}
			decoded, err := openailegacy.DecodeChatRequest([]byte(tc.body), opts)
			if err != nil {
				t.Fatalf("DecodeChatRequest failed: %v", err)
			}
			if decoded.Call == nil {
				t.Fatal("decoded.Call is nil")
			}

			verifyCallIdentityParity(t, "openailegacy:"+tc.name, decoded.Call)
		})
	}
}

// TestDifferentialCorpus_OpenResponses tests decoded OpenResponses request bodies
// (item-authoritative canonical Calls) against the canonical oracle across items,
// content parts, tools, and options.
func TestDifferentialCorpus_OpenResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{
			name: "single string input mapped to message item",
			body: `{"model":"gpt-4o","input":"Hello OpenResponses world"}`,
		},
		{
			name: "tools controls and extensions",
			body: `{
				"model": "gpt-4o",
				"input": "Test tools with parameters",
				"tools": [
					{
						"type": "function",
						"name": "get_weather",
						"description": "Get current weather",
						"parameters": {"type": "object"}
					}
				],
				"tool_choice": "auto",
				"top_p": 0.95
			}`,
		},
		{
			name: "explicit item with multiple content parts",
			body: `{
				"model": "gpt-4o",
				"input": [
					{
						"type": "message",
						"role": "user",
						"content": [
							{"type": "text", "text": "First content part."},
							{"type": "text", "text": "Second content part with <div>&\"'</div> \u2028\u2029 🧪"}
						]
					}
				]
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := openresponses.DecodeCreateOptions{
				RouteSelector: "stub:gpt-4o",
			}
			decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), []byte(tc.body), opts)
			if err != nil {
				t.Fatalf("AuthenticateAndDecodeCreate failed: %v", err)
			}
			if decoded.Call == nil {
				t.Fatal("decoded.Call is nil")
			}

			verifyCallIdentityParity(t, "openresponses:"+tc.name, decoded.Call)
		})
	}
}

// TestDifferentialCorpus_HugeUnicodeAndEscapeStress tests extreme string payloads
// (1MB+ with mixed UTF-8, HTML, control chars, escapes, emojis, invalid UTF-8 bytes)
// across various streaming chunk sizes.
func TestDifferentialCorpus_HugeUnicodeAndEscapeStress(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	// 50,000 repetitions of a complex string (~2MB)
	pattern := "abc <div>&\"'\\</div>\u2028\u2029 🧪 café \\u0041 \n\t\r 🚀🌍 日本語 中文 العربية "
	for range 50000 {
		sb.WriteString(pattern)
	}
	hugeText := sb.String()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleSystem,
				Parts: []aipapi.Part{
					aipapi.TextPart("System instruction for large payload"),
				},
			},
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					aipapi.TextPart(hugeText),
				},
			},
		},
		Options: aipapi.GenerationOptions{
			ReasoningEffort: "high",
		},
	}

	verifyCallIdentityParity(t, "huge-unicode-stress-2MB", call)
}

// TestDifferentialCorpus_ItemAuthoritativeHugeStress tests extreme item-authoritative
// payloads (Items with ContentPartText) streamed in arbitrary chunk boundaries.
func TestDifferentialCorpus_ItemAuthoritativeHugeStress(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	pattern := "item payload <script>\"test\"&'safe'</script> \u2028\u2029 🌍🚀 "
	for range 40000 {
		sb.WriteString(pattern)
	}
	hugeItemText := sb.String()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "item_msg_1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: hugeItemText}},
			},
		},
	}

	verifyCallIdentityParity(t, "huge-item-stress", call)
}

// TestDifferentialCorpus_ItemAuthoritativeEmptyTextPart tests an item with an empty-text
// single-part content part streamed through BeginMessageItem / BeginTextContentPart
// against the canonical oracle across both populated and minimal probe shapes.
func TestDifferentialCorpus_ItemAuthoritativeEmptyTextPart(t *testing.T) {
	t.Parallel()

	// 1. Populated item with ID, status, route, and empty text
	callWithMeta := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "item_empty_text_1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: ""}},
			},
		},
	}
	verifyCallIdentityParity(t, "empty-text-item-with-meta", callWithMeta)

	// 2. Minimal reviewer probe shape (no explicit ID or status)
	callProbe := &aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: ""}},
			},
		},
	}
	verifyCallIdentityParity(t, "empty-text-item-minimal-probe", callProbe)
}

// TestDifferentialCorpus_RandomizedChunkStreaming tests that streaming arbitrary text
// chunks with pseudo-random sizes (from 1 byte to 128 bytes) produces identical identity.
func TestDifferentialCorpus_RandomizedChunkStreaming(t *testing.T) {
	t.Parallel()

	text := "Lorem ipsum dolor sit amet, <div>&\"'</div> \u2028\u2029 🧪 café \\u0041 \n\t\r 🚀🌍 " +
		strings.Repeat("repeated pattern for randomized chunk testing ", 100)

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o"},
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(text)},
			},
		},
	}

	want := computeOracleDownstreamIDs(call)

	// Run 10 randomized chunk iterations
	rng := rand.New(rand.NewPCG(42, 100))
	textBytes := []byte(text)

	for iter := range 10 {
		w, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
			Route: call.Route,
		})
		if err != nil {
			t.Fatalf("iter %d NewCallIdentityWriter: %v", iter, err)
		}

		mw, err := w.BeginMessage(aipapi.RoleUser)
		if err != nil {
			t.Fatalf("iter %d BeginMessage: %v", iter, err)
		}
		tw, err := mw.BeginTextPart()
		if err != nil {
			t.Fatalf("iter %d BeginTextPart: %v", iter, err)
		}

		offset := 0
		for offset < len(textBytes) {
			chunkSize := rng.IntN(127) + 1 // 1 to 127 bytes
			end := min(offset+chunkSize, len(textBytes))
			if _, err := tw.Write(textBytes[offset:end]); err != nil {
				t.Fatalf("iter %d Write [%d:%d]: %v", iter, offset, end, err)
			}
			offset = end
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("iter %d tw.Close: %v", iter, err)
		}
		if err := mw.EndMessage(); err != nil {
			t.Fatalf("iter %d mw.EndMessage: %v", iter, err)
		}

		digest, err := w.Digest()
		if err != nil {
			t.Fatalf("iter %d w.Digest: %v", iter, err)
		}

		got := computeWriterDownstreamIDs(digest, "")
		assertDownstreamParity(t, fmt.Sprintf("random-chunk-iter-%d", iter), got, want)
	}
}
