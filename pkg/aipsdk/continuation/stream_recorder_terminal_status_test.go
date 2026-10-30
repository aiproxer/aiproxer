package continuation_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

type terminalStatusRecorder struct {
	calls  int
	record aipcont.ContinuationRecord
}

func (r *terminalStatusRecorder) RecordTerminal(_ context.Context, record aipcont.ContinuationRecord) error {
	r.calls++
	r.record = aipcont.CloneRecord(record)
	return nil
}

// TestStreamRecorderTerminalStatusPrecedence pins the canonical terminal-event
// to record-status mapping. ResponseStatus is authoritative when set; without
// it only the output-limit finish taxonomy marks a record incomplete. Refusal
// lexemes are complete upstream decisions and must never be recorded as
// incomplete, because the stores reject incomplete records under the default
// policy and the OpenResponses wire reports those turns as completed.
func TestStreamRecorderTerminalStatusPrecedence(t *testing.T) {
	t.Parallel()

	finished := func(status, reason string) aipapi.Event {
		return aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: status, FinishReason: reason}
	}
	tests := []struct {
		name  string
		event aipapi.Event
		want  aipcont.RecordStatus
	}{
		// Refusals carry a verbatim vendor lexeme with no explicit status:
		// a complete upstream decision, never a truncation.
		{name: "refusal_content_filter", event: finished("", "content_filter"), want: aipcont.RecordStatusCompleted},
		{name: "refusal_refusal", event: finished("", "refusal"), want: aipcont.RecordStatusCompleted},
		{name: "refusal_safety", event: finished("", "SAFETY"), want: aipcont.RecordStatusCompleted},
		{name: "refusal_guardrail_intervened", event: finished("", "guardrail_intervened"), want: aipcont.RecordStatusCompleted},

		// Explicit status is authoritative in both directions, even when the
		// vendor finish reason would infer the opposite.
		{name: "explicit_completed_over_limit_reason", event: finished("completed", "length"), want: aipcont.RecordStatusCompleted},
		{name: "explicit_completed_over_refusal_reason", event: finished("completed", "content_filter"), want: aipcont.RecordStatusCompleted},
		{name: "explicit_incomplete_over_clean_reason", event: finished("incomplete", "stop"), want: aipcont.RecordStatusIncomplete},
		{name: "explicit_incomplete_over_refusal_reason", event: finished("incomplete", "content_filter"), want: aipcont.RecordStatusIncomplete},

		// Without an explicit status, only the output-limit taxonomy is
		// incomplete; legacy providers report it without ResponseStatus.
		{name: "output_limit_length", event: finished("", "length"), want: aipcont.RecordStatusIncomplete},
		{name: "output_limit_max_tokens", event: finished("", "max_tokens"), want: aipcont.RecordStatusIncomplete},
		{name: "output_limit_max_output_tokens", event: finished("", "max_output_tokens"), want: aipcont.RecordStatusIncomplete},
		{name: "output_limit_max_tokens_upper", event: finished("", "MAX_TOKENS"), want: aipcont.RecordStatusIncomplete},

		// Clean terminals stay completed.
		{name: "clean_empty_reason", event: finished("", ""), want: aipcont.RecordStatusCompleted},
		{name: "clean_stop", event: finished("", "stop"), want: aipcont.RecordStatusCompleted},
		{name: "clean_end_turn", event: finished("", "end_turn"), want: aipcont.RecordStatusCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := &terminalStatusRecorder{}
			recorder := aipcont.NewStreamRecorder(backend, aipcont.ContinuationRecord{}, func() {})
			recorder.Observe(context.Background(), tt.event)
			if backend.calls != 1 {
				t.Fatalf("RecordTerminal calls = %d, want 1", backend.calls)
			}
			if backend.record.Status != tt.want {
				t.Fatalf("record status = %q, want %q (status=%q reason=%q)", backend.record.Status, tt.want, tt.event.ResponseStatus, tt.event.FinishReason)
			}
		})
	}
}
