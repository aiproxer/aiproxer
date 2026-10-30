package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestWriteNonStreamJSON_partialToolArguments pins that a tool call whose
// argument JSON was cut off by the output-token limit still produces a usable
// Messages body. Truncated arguments must never fail the whole response: the
// visible text and the partial arguments both reach the client, and the
// terminal reports the truncation so the client knows the arguments are
// incomplete.
func TestWriteNonStreamJSON_partialToolArguments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		terminal   aipapi.Event
		args       string
		wantStop   string
		wantInput  string
		wantHasKey bool
	}{
		{
			name:       "truncated_with_incomplete_status",
			args:       `{"city":`,
			terminal:   aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: "max_tokens"},
			wantStop:   "max_tokens",
			wantInput:  `{"value":"{\"city\":"}`,
			wantHasKey: true,
		},
		{
			name:       "truncated_legacy_reason",
			args:       `{"city":`,
			terminal:   aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "max_tokens"},
			wantStop:   "max_tokens",
			wantInput:  `{"value":"{\"city\":"}`,
			wantHasKey: true,
		},
		{
			// A provider that reports a clean terminal but still emits
			// unparseable arguments must degrade the same way rather than fail.
			name:       "malformed_without_incomplete_status",
			args:       `not json at all`,
			terminal:   aipapi.Event{Kind: aipapi.EventResponseFinished},
			wantStop:   "tool_use",
			wantInput:  `{"value":"not json at all"}`,
			wantHasKey: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "here you go"},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "toolu_test", ToolName: "get_weather"},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "toolu_test", Delta: tc.args},
				{Kind: aipapi.EventToolCallFinished, ToolCallID: "toolu_test"},
				tc.terminal,
			})
			call := &aipapi.Call{
				Route:      aipapi.RouteIntent{Selector: "x:y"},
				Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")}}},
				Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
			}
			rec := httptest.NewRecorder()
			if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, anthropic.EncodeOptions{}); err != nil {
				t.Fatalf("partial tool arguments must not fail the response: %v", err)
			}
			var v struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("body is not valid JSON: %v (%s)", err, rec.Body.String())
			}
			if v.StopReason != tc.wantStop {
				t.Fatalf("stop_reason = %q, want %q; body=%s", v.StopReason, tc.wantStop, rec.Body.String())
			}
			var toolBlock *struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Input json.RawMessage `json:"input"`
			}
			sawText := false
			for i := range v.Content {
				if v.Content[i].Type == "text" && v.Content[i].Text == "here you go" {
					sawText = true
				}
				if v.Content[i].Type == "tool_use" {
					toolBlock = &v.Content[i]
				}
			}
			if !sawText {
				t.Fatalf("visible text lost: %s", rec.Body.String())
			}
			if toolBlock == nil {
				t.Fatalf("tool_use block lost: %s", rec.Body.String())
			}
			if got := string(toolBlock.Input); got != tc.wantInput {
				t.Fatalf("input = %s, want %s", got, tc.wantInput)
			}
			// The partial arguments must survive verbatim, not merely be
			// replaced by a placeholder.
			if tc.wantHasKey {
				var wrapper struct {
					Value string `json:"value"`
				}
				if err := json.Unmarshal(toolBlock.Input, &wrapper); err != nil {
					t.Fatalf("input is not an object: %v (%s)", err, toolBlock.Input)
				}
				if wrapper.Value != tc.args {
					t.Fatalf("preserved arguments = %q, want %q", wrapper.Value, tc.args)
				}
			}
		})
	}
}
