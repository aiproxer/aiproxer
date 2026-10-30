package openailegacy

import (
	"context"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// scriptedChatDecoderLegacy replays a fixed list of chat-completion SSE payloads
// and then reports end of stream.
type scriptedChatDecoderLegacy struct {
	payloads []string
	pos      int
}

func (d *scriptedChatDecoderLegacy) Event() ssestream.Event {
	if d.pos >= len(d.payloads) {
		return ssestream.Event{}
	}
	ev := ssestream.Event{Data: []byte(d.payloads[d.pos])}
	d.pos++
	return ev
}

func (d *scriptedChatDecoderLegacy) Next() bool   { return d.pos < len(d.payloads) }
func (d *scriptedChatDecoderLegacy) Close() error { return nil }
func (d *scriptedChatDecoderLegacy) Err() error   { return nil }

func drainLegacyTerminal(t *testing.T, payloads []string) aipapi.Event {
	t.Helper()
	sdk := ssestream.NewStream[openai.ChatCompletionChunk](&scriptedChatDecoderLegacy{payloads: payloads}, nil)
	es := NewChatStream(sdk, 0)
	defer func() { _ = es.Close() }()

	var terminal aipapi.Event
	sawTerminal := false
	for {
		ev, err := es.Recv(context.Background())
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("recv: %v", err)
		}
		if ev.Kind == aipapi.EventResponseFinished {
			terminal = ev
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("no EventResponseFinished emitted")
	}
	return terminal
}

// TestChatStream_terminalFinishReason pins the canonical terminal mapping for an
// OpenAI legacy chat stream: output-token-limit carries
// ResponseStatus/FinishReason, content-filter refusal is carried verbatim
// with no incomplete classification. Clean and tool stops stay empty so the
// continuation recorder never mislabels them incomplete.
func TestChatStream_terminalFinishReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		payloads   []string
		wantStatus string
		wantFinish string
	}{
		{
			name:       "length_truncation",
			wantStatus: "incomplete",
			wantFinish: "length",
			payloads: []string{
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"cut off he"},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
			},
		},
		{
			name: "clean_stop",
			payloads: []string{
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
		},
		{
			name: "tool_calls_stop",
			payloads: []string{
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
		},
		{
			name:       "content_filter_refusal",
			wantFinish: "content_filter",
			payloads: []string{
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"I cannot help with that"},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
			},
		},
		{
			// Some relays stamp an empty finish_reason on every chunk. Empty
			// means absent, never a terminal classification.
			name: "empty_finish_reason",
			payloads: []string{
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":""}]}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			terminal := drainLegacyTerminal(t, tc.payloads)
			if terminal.ResponseStatus != tc.wantStatus {
				t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
			}
			if terminal.FinishReason != tc.wantFinish {
				t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
			}
		})
	}
}
