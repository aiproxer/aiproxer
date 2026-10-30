package geminigenerate

import (
	"context"
	"io"
	"iter"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/genai"
)

func geminiSeq(responses ...*genai.GenerateContentResponse) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, r := range responses {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func drainGeminiTerminal(t *testing.T, responses ...*genai.GenerateContentResponse) aipapi.Event {
	t.Helper()
	es := newGenaiStream(geminiSeq(responses...), "gemini-test", 0)
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

func geminiChunk(text string, finish genai.FinishReason) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: finish,
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: text}}},
		}},
	}
}

// geminiFinishOnlyChunk models the trailing frame a real Gemini stream sends:
// the finish reason arrives on a candidate whose content is already drained.
func geminiFinishOnlyChunk(finish genai.FinishReason) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{FinishReason: finish}},
	}
}

// TestGenaiStream_terminalFinishReason pins the canonical terminal mapping for a
// generateContent stream: output-token-limit carries
// ResponseStatus/FinishReason and SAFETY refusal is carried verbatim with no
// incomplete classification. Clean and tool stops stay empty so the
// continuation recorder never mislabels them incomplete.
func TestGenaiStream_terminalFinishReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		responses  []*genai.GenerateContentResponse
		wantStatus string
		wantFinish string
	}{
		{
			name:       "max_tokens_truncation",
			wantStatus: "incomplete",
			wantFinish: "MAX_TOKENS",
			responses: []*genai.GenerateContentResponse{
				geminiChunk("cut off he", ""),
				geminiFinishOnlyChunk(genai.FinishReasonMaxTokens),
			},
		},
		{
			name:       "max_tokens_on_content_chunk",
			wantStatus: "incomplete",
			wantFinish: "MAX_TOKENS",
			responses: []*genai.GenerateContentResponse{
				geminiChunk("cut off he", genai.FinishReasonMaxTokens),
			},
		},
		{
			name:      "clean_stop",
			responses: []*genai.GenerateContentResponse{geminiChunk("hello", genai.FinishReasonStop)},
		},
		{
			name: "tool_call_stop",
			responses: []*genai.GenerateContentResponse{{
				Candidates: []*genai.Candidate{{
					FinishReason: genai.FinishReasonStop,
					Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
						FunctionCall: &genai.FunctionCall{ID: "fn_1", Name: "get_weather", Args: map[string]any{"city": "Oslo"}},
					}}},
				}},
			}},
		},
		{
			name:       "safety_refusal",
			wantFinish: "SAFETY",
			responses: []*genai.GenerateContentResponse{
				geminiChunk("I cannot help with that", ""),
				geminiFinishOnlyChunk(genai.FinishReasonSafety),
			},
		},
		{
			// A chunk with no finish reason at all stays unclassified.
			name:      "absent_finish_reason",
			responses: []*genai.GenerateContentResponse{geminiChunk("hello", "")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			terminal := drainGeminiTerminal(t, tc.responses...)
			if terminal.ResponseStatus != tc.wantStatus {
				t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
			}
			if terminal.FinishReason != tc.wantFinish {
				t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
			}
		})
	}
}
