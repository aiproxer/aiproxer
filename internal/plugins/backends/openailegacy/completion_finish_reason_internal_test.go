package openailegacy

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
)

// TestCompletionEvents_terminalFinishReason pins the canonical terminal mapping
// for a non-streaming ChatCompletion. Output-token-limit is classified
// incomplete/length and content-filter refusal is carried verbatim;
// forwarding any other raw vendor lexeme (e.g. tool_calls) would mark a clean
// turn incomplete, because the continuation recorder treats every FinishReason
// other than "" / "stop" / "end_turn" as a truncation.
func TestCompletionEvents_terminalFinishReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		finishReason  string
		wantStatus    string
		wantReason    string
		wantToolCalls int
	}{
		{
			name:          "length_truncation",
			finishReason:  "length",
			wantStatus:    "incomplete",
			wantReason:    "length",
			wantToolCalls: 0,
		},
		{
			name: "clean_stop",
		},
		{
			// Regression guard: the raw "tool_calls" lexeme must not leak onto
			// the terminal, or the continuation recorder records this clean
			// turn as incomplete.
			name:          "tool_calls_stop",
			finishReason:  "tool_calls",
			wantToolCalls: 1,
		},
		{
			name:         "content_filter",
			finishReason: "content_filter",
			wantReason:   "content_filter",
		},
		{
			name:         "empty_finish_reason",
			finishReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			toolCalls := 0
			if tc.wantToolCalls > 0 {
				toolCalls = 1
			}
			var raw string
			if toolCalls == 1 {
				raw = `{"id":"cc","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
			} else {
				raw = `{"id":"cc","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"` + tc.finishReason + `"}]}`
			}
			var comp openai.ChatCompletion
			if err := json.Unmarshal([]byte(raw), &comp); err != nil {
				t.Fatalf("decode: %v", err)
			}

			events := CompletionEvents(comp)
			var terminal aipapi.Event
			sawTerminal := false
			for _, ev := range events {
				if ev.Kind == aipapi.EventResponseFinished {
					terminal = ev
					sawTerminal = true
				}
			}
			if !sawTerminal {
				t.Fatal("no EventResponseFinished emitted")
			}
			if terminal.ResponseStatus != tc.wantStatus {
				t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
			}
			if terminal.FinishReason != tc.wantReason {
				t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantReason)
			}
		})
	}
}
