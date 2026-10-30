package parity_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	visibleThinkerReasoning = "plan: ship it quickly"
	visibleExecutorText     = "executor-answer"
)

func visibleThinkerCanonicalStream() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: visibleThinkerReasoning},
		{Kind: aipapi.EventTextDelta, Delta: visibleExecutorText},
		{Kind: aipapi.EventResponseFinished},
	}
}

func visibleThinkerCall(extensions map[string]json.RawMessage) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:y"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("prompt")},
		}},
		Extensions: extensions,
	}
}

func modelExt(tb testing.TB, key, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{key: raw}
}

func assertVisibleThinkerWireLegality(t *testing.T, body string, expectReasoning bool) {
	t.Helper()
	if !strings.Contains(body, visibleExecutorText) {
		t.Fatalf("executor text missing from wire body: %q", body)
	}
	if expectReasoning {
		if !strings.Contains(body, visibleThinkerReasoning) {
			t.Fatalf("thinker reasoning must appear on wire; body=%q", body)
		}
	} else {
		if strings.Contains(body, visibleThinkerReasoning) {
			t.Fatalf("thinker reasoning must not appear on wire for this encoder; body=%q", body)
		}
	}
	if strings.Contains(body, interleavedthinking.MemoOpenTag) || strings.Contains(body, interleavedthinking.MemoCloseTag) {
		t.Fatalf("memo wrapper tags must not appear on wire; body=%q", body)
	}
	if strings.Contains(body, interleavedthinking.SessionSteeringGuidanceHeader) {
		t.Fatalf("tail-injected steering guidance must not appear on wire; body=%q", body)
	}
}

func TestVisibleThinkerReasoning_streamEncodesLegally(t *testing.T) {
	t.Parallel()

	type caseDef struct {
		name     string
		call     *aipapi.Call
		encode   func(context.Context, *httptest.ResponseRecorder, *aipapi.Call, aipapi.EventStream) error
		terminal string
	}

	cases := []caseDef{
		{
			name: "anthropic",
			call: visibleThinkerCall(modelExt(t, "anthropic.model", "claude-3-5-haiku-20241022")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return anthropic.WriteStreamSSE(ctx, rec, call, es, anthropic.EncodeOptions{MessageID: "msg_visible_thinker"})
			},
			terminal: "event: message_stop",
		},
		{
			name: "gemini",
			call: visibleThinkerCall(modelExt(t, "gemini.model", "gemini-2.0-flash")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return gemini.WriteStreamSSE(ctx, rec, call, es, gemini.EncodeOptions{})
			},
			terminal: `"candidates"`,
		},
		{
			name: "openailegacy",
			call: visibleThinkerCall(modelExt(t, "openailegacy.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return openailegacy.WriteStreamSSE(ctx, rec, call, es, openailegacy.EncodeOptions{
					CompletionID: "chatcmpl_visible_thinker",
					CreatedAt:    1715620000,
				})
			},
			terminal: "[DONE]",
		},
		{
			name: "openairesponses",
			call: visibleThinkerCall(modelExt(t, "openairesponses.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return openairesponses.WriteStreamSSE(ctx, rec, call, es, openairesponses.EncodeOptions{
					ResponseID: "resp_visible_thinker",
					CreatedAt:  1715620000,
				})
			},
			terminal: "response.completed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := aipapi.NewFixedEventStream(visibleThinkerCanonicalStream())
			rec := httptest.NewRecorder()
			if err := tc.encode(t.Context(), rec, tc.call, es); err != nil {
				t.Fatal(err)
			}
			body := rec.Body.String()
			assertVisibleThinkerWireLegality(t, body, true)
			if !strings.Contains(body, tc.terminal) {
				t.Fatalf("missing terminal marker %q; body=%q", tc.terminal, body)
			}
		})
	}
}

func TestVisibleThinkerReasoning_nonStreamEncodesLegally(t *testing.T) {
	t.Parallel()

	type caseDef struct {
		name            string
		call            *aipapi.Call
		encode          func(context.Context, *httptest.ResponseRecorder, *aipapi.Call, aipapi.EventStream) error
		expectReasoning bool
	}

	cases := []caseDef{
		{
			name: "anthropic",
			call: visibleThinkerCall(modelExt(t, "anthropic.model", "claude-3-5-haiku-20241022")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return anthropic.WriteNonStreamJSON(ctx, rec, call, es, anthropic.EncodeOptions{MessageID: "msg_visible_thinker_ns"})
			},
			expectReasoning: false,
		},
		{
			name: "gemini",
			call: visibleThinkerCall(modelExt(t, "gemini.model", "gemini-2.0-flash")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return gemini.WriteNonStreamJSON(ctx, rec, call, es, gemini.EncodeOptions{})
			},
			expectReasoning: false,
		},
		{
			name: "openailegacy",
			call: visibleThinkerCall(modelExt(t, "openailegacy.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return openailegacy.WriteNonStreamJSON(ctx, rec, call, es, openailegacy.EncodeOptions{
					CompletionID: "chatcmpl_visible_thinker_ns",
					CreatedAt:    1715620000,
				})
			},
			expectReasoning: true,
		},
		{
			name: "openairesponses",
			call: visibleThinkerCall(modelExt(t, "openairesponses.model", "gpt-4o-mini")),
			encode: func(ctx context.Context, rec *httptest.ResponseRecorder, call *aipapi.Call, es aipapi.EventStream) error {
				return openairesponses.WriteNonStreamJSON(ctx, rec, call, es, openairesponses.EncodeOptions{
					ResponseID: "resp_visible_thinker_ns",
					MessageID:  "msg_visible_thinker_ns",
					CreatedAt:  1715620000,
				})
			},
			expectReasoning: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := aipapi.NewFixedEventStream(visibleThinkerCanonicalStream())
			rec := httptest.NewRecorder()
			if err := tc.encode(t.Context(), rec, tc.call, es); err != nil {
				t.Fatal(err)
			}
			assertVisibleThinkerWireLegality(t, rec.Body.String(), tc.expectReasoning)
		})
	}
}

func TestVisibleThinkerSignature_streamAnthropicRoundTrip(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: visibleThinkerReasoning},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig-roundtrip"},
		{Kind: aipapi.EventTextDelta, Delta: visibleExecutorText},
		{Kind: aipapi.EventResponseFinished},
	})
	call := visibleThinkerCall(modelExt(t, "anthropic.model", "claude-3-5-haiku-20241022"))
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(t.Context(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_sig_parity"}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	assertVisibleThinkerWireLegality(t, body, true)
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("missing terminal message_stop; body=%q", body)
	}
	if !strings.Contains(body, `"signature":""`) {
		t.Fatalf("thinking content_block_start should carry an empty signature field; body=%q", body)
	}
	if !strings.Contains(body, `"type":"signature_delta"`) {
		t.Fatalf("missing signature_delta on wire; body=%q", body)
	}
	if !strings.Contains(body, `"signature":"sig-roundtrip"`) {
		t.Fatalf("signature_delta should carry the original signature; body=%q", body)
	}
}
