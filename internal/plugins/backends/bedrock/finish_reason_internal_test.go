package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestConverseStream_terminalFinishReason pins the canonical terminal mapping
// for a Bedrock Converse stream: output-token-limit carries
// ResponseStatus/FinishReason and guardrail_intervened refusal is carried
// verbatim with no incomplete classification. Clean and tool stops stay
// empty so the continuation recorder never mislabels them incomplete.
func TestConverseStream_terminalFinishReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		stopReason types.StopReason
		wantStatus string
		wantFinish string
	}{
		{
			name:       "max_tokens_truncation",
			stopReason: types.StopReasonMaxTokens,
			wantStatus: "incomplete",
			wantFinish: "max_tokens",
		},
		{
			name:       "clean_end_turn",
			stopReason: types.StopReasonEndTurn,
		},
		{
			name:       "tool_use_stop",
			stopReason: types.StopReasonToolUse,
		},
		{
			name: "stop_sequence",
			// A stop_sequence stop is a clean, deliberate termination. It must
			// not be reported as a truncation.
			stopReason: types.StopReasonStopSequence,
		},
		{
			name:       "guardrail_intervened_refusal",
			stopReason: types.StopReasonGuardrailIntervened,
			wantFinish: "guardrail_intervened",
		},
		{
			// Some relays stamp an empty stopReason. Empty means absent, never a
			// terminal classification.
			name:       "empty_stop_reason",
			stopReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &converseStream{pending: stream.NewPendingEventQueue(0)}

			text := &types.ConverseStreamOutputMemberContentBlockDelta{
				Value: types.ContentBlockDeltaEvent{
					Delta: &types.ContentBlockDeltaMemberText{Value: "hello"},
				},
			}
			if err := s.handleOutput(text); err != nil {
				t.Fatalf("handleOutput delta: %v", err)
			}
			if err := s.handleOutput(&types.ConverseStreamOutputMemberMetadata{
				Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{
					InputTokens: aws.Int32(3), OutputTokens: aws.Int32(2),
				}},
			}); err != nil {
				t.Fatalf("handleOutput metadata: %v", err)
			}
			if err := s.handleOutput(&types.ConverseStreamOutputMemberMessageStop{
				Value: types.MessageStopEvent{StopReason: tc.stopReason},
			}); err != nil {
				t.Fatalf("handleOutput message_stop: %v", err)
			}

			terminal := s.terminalEvent()
			if terminal.Kind != aipapi.EventResponseFinished {
				t.Fatalf("terminal kind = %q, want %q", terminal.Kind, aipapi.EventResponseFinished)
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
