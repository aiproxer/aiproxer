package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/anthropics/anthropic-sdk-go"
)

// TestMsgStream_handleEvent_terminalFinishReason pins the canonical terminal
// mapping for an Anthropic Messages stream: output-token-limit carries
// ResponseStatus/FinishReason and refusal is carried verbatim with no
// incomplete classification. A clean turn, a tool_use turn, and a relay
// that stamps an empty stop_reason must all leave FinishReason empty so
// the continuation recorder never mislabels them incomplete.
func TestMsgStream_handleEvent_terminalFinishReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		records        []string
		wantStatus     string
		wantFinish     string
		wantText       string
		wantToolCalls  int
		wantToolFinish int
	}{
		{
			name:       "max_tokens_truncation",
			wantStatus: "incomplete",
			wantFinish: "max_tokens",
			wantText:   "cut off he",
			records: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut off he"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":16}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			name:       "clean_end_turn",
			wantText:   "hello",
			wantFinish: "",
			records: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			name:           "tool_use_stop",
			wantFinish:     "",
			wantToolCalls:  1,
			wantToolFinish: 1,
			records: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":8}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			// Some relays stamp an empty stop_reason on every message_delta.
			// Empty means absent, never a terminal classification.
			name:       "empty_stop_reason",
			wantText:   "hello",
			wantFinish: "",
			records: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			name:       "refusal_preserved",
			wantFinish: "refusal",
			wantText:   "I cannot help with that",
			records: []string{
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I cannot help with that"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":5}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			name:       "max_tokens_inside_tool_use",
			wantStatus: "incomplete",
			wantFinish: "max_tokens",
			// Real Anthropic reports max_tokens when it truncates mid-argument.
			wantToolCalls:  1,
			wantToolFinish: 1,
			records: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":16}}`,
				`{"type":"message_stop"}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &msgStream{pending: stream.NewPendingEventQueue(0)}

			var text []byte
			toolCalls, toolFinishes := 0, 0
			var terminal aipapi.Event
			sawTerminal := false
			for _, raw := range tc.records {
				var u anthropic.MessageStreamEventUnion
				if err := json.Unmarshal([]byte(raw), &u); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				if err := s.handleEvent(u); err != nil {
					t.Fatalf("handleEvent %s: %v", raw, err)
				}
				for _, ev := range stream.DrainPending(&s.pending) {
					switch ev.Kind {
					case aipapi.EventTextDelta:
						text = append(text, ev.Delta...)
					case aipapi.EventToolCallStarted:
						toolCalls++
					case aipapi.EventToolCallFinished:
						toolFinishes++
					case aipapi.EventResponseFinished:
						terminal = ev
						sawTerminal = true
					}
				}
			}
			if !sawTerminal {
				t.Fatal("no EventResponseFinished emitted")
			}
			if string(text) != tc.wantText {
				t.Fatalf("text = %q, want %q", text, tc.wantText)
			}
			if toolCalls != tc.wantToolCalls {
				t.Fatalf("tool calls started = %d, want %d", toolCalls, tc.wantToolCalls)
			}
			if toolFinishes != tc.wantToolFinish {
				t.Fatalf("tool calls finished = %d, want %d", toolFinishes, tc.wantToolFinish)
			}
			if terminal.ResponseStatus != tc.wantStatus {
				t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
			}
			if terminal.FinishReason != tc.wantFinish {
				t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
			}
		})
	}
}
