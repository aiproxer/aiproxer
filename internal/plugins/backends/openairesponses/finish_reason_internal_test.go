package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

// TestHandleUnion_responseIncomplete_mapsTerminal pins that an upstream
// response.incomplete record terminates the canonical stream with an
// authoritative incomplete classification instead of being dropped (which
// leaves the turn with no terminal at all) or reported as clean.
func TestHandleUnion_responseIncomplete_mapsTerminal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        string
		wantReason string
	}{
		{
			name:       "max_output_tokens",
			wantReason: "length",
			raw:        `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-mini","output":[],"incomplete_details":{"reason":"max_output_tokens"}}}`,
		},
		{
			name:       "length",
			wantReason: "length",
			raw:        `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-mini","output":[],"incomplete_details":{"reason":"length"}}}`,
		},
		{
			name:       "max_tokens",
			wantReason: "max_tokens",
			raw:        `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-mini","output":[],"incomplete_details":{"reason":"max_tokens"}}}`,
		},
		{
			name:       "absent_incomplete_details",
			wantReason: "length",
			raw:        `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-mini","output":[]}}`,
		},
		{
			// A non-length truncation keeps its own reason so it is never
			// silently rewritten to the token-limit taxonomy.
			name:       "content_filter",
			wantReason: "content_filter",
			raw:        `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-4o-mini","output":[],"incomplete_details":{"reason":"content_filter"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var u responses.ResponseStreamEventUnion
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatal(err)
			}
			s := &sdkStream{}
			if err := s.handleUnion(u); err != nil {
				t.Fatalf("handleUnion: %v", err)
			}
			var terminal aipapi.Event
			sawTerminal := false
			for _, ev := range stream.DrainPending(&s.pending) {
				if ev.Kind == aipapi.EventResponseFinished {
					terminal = ev
					sawTerminal = true
				}
				if ev.Kind == aipapi.EventError {
					t.Fatalf("unexpected error event: %+v", ev)
				}
			}
			if !sawTerminal {
				t.Fatalf("no EventResponseFinished emitted for response.incomplete")
			}
			if terminal.ResponseStatus != "incomplete" {
				t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, "incomplete")
			}
			if terminal.FinishReason != tc.wantReason {
				t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantReason)
			}
		})
	}
}

// TestHandleUnion_responseCompleted_staysClean guards that the explicit
// completed record keeps an empty FinishReason so the continuation recorder
// does not mislabel a clean turn.
func TestHandleUnion_responseCompleted_staysClean(t *testing.T) {
	t.Parallel()
	raw := `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-4o-mini","output":[]}}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	if err := s.handleUnion(u); err != nil {
		t.Fatalf("handleUnion: %v", err)
	}
	var terminal aipapi.Event
	sawTerminal := false
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventResponseFinished {
			terminal = ev
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("no EventResponseFinished emitted for response.completed")
	}
	if terminal.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want empty on a clean terminal", terminal.FinishReason)
	}
}

// TestCompletionEvents_nonStreamIncomplete_mapsTerminal pins that a
// non-streaming upstream status terminates the canonical stream exactly like
// its streaming response.incomplete twin: incomplete carries the
// authoritative vendor reason, completed stays clean, and failed surfaces a
// terminal error instead of a clean finish.
func TestCompletionEvents_nonStreamIncomplete_mapsTerminal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		raw            string
		wantStatus     string
		wantReason     string
		wantError      bool
		wantErrContain string
	}{
		{
			name:       "completed_stays_clean",
			wantStatus: "",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
		},
		{
			// Compatible providers still omit status; those payloads keep
			// the pre-existing clean mapping (pinned by the provider-profile
			// safe-header/cache-key tests, whose stub has no status field).
			name:       "absent_status_stays_clean",
			wantStatus: "",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
		},
		{
			name:       "incomplete_max_output_tokens",
			wantStatus: "incomplete",
			wantReason: "length",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
		},
		{
			name:       "incomplete_length",
			wantStatus: "incomplete",
			wantReason: "length",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"length"}}`,
		},
		{
			name:       "incomplete_max_tokens",
			wantStatus: "incomplete",
			wantReason: "max_tokens",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"max_tokens"}}`,
		},
		{
			name:       "incomplete_content_filter_preserved",
			wantStatus: "incomplete",
			wantReason: "content_filter",
			raw:        `{"id":"resp_1","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"content_filter"}}`,
		},
		{
			name:      "failed_surfaces_error",
			wantError: true,
			raw:       `{"id":"resp_1","object":"response","created_at":1715620000,"status":"failed","model":"gpt-4o-mini","output":[],"error":{"code":"server_error","message":"boom"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events, err := CompletionEvents(nativeEconomicsResponse(t, tc.raw))
			if err != nil {
				t.Fatalf("CompletionEvents: %v", err)
			}
			if tc.wantError {
				for _, ev := range events {
					if ev.Kind == aipapi.EventError {
						return
					}
				}
				t.Fatalf("want terminal EventError, got %+v", events)
			}
			var terminal aipapi.Event
			sawTerminal := false
			for _, ev := range events {
				if ev.Kind == aipapi.EventResponseFinished {
					terminal = ev
					sawTerminal = true
				}
				if ev.Kind == aipapi.EventError {
					t.Fatalf("unexpected error event: %+v", ev)
				}
			}
			if !sawTerminal {
				t.Fatalf("no EventResponseFinished emitted")
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
