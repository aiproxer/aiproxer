package gemini_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/genai"
)

// The pinned SDK must observe exactly one terminal candidate, after unchanged
// content; removing flushFinish or stamping intermediate chunks breaks this.
func TestFinishReason_SDKPreservesContentAndUsage(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "clean"
			want := genai.FinishReasonStop
			terminal := aipapi.Event{Kind: aipapi.EventResponseFinished}
			if truncated {
				name, want = "limited", genai.FinishReasonMaxTokens
				terminal.ResponseStatus, terminal.FinishReason = "incomplete", "length"
			}
			if streaming {
				name += "/stream"
			} else {
				name += "/json"
			}
			t.Run(name, func(t *testing.T) {
				es := aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "hello"},
					{Kind: aipapi.EventToolCallStarted, ToolCallID: "c", ToolName: "weather"},
					{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c", Delta: `{"city":"Oslo"}`},
					{Kind: aipapi.EventToolCallFinished, ToolCallID: "c"},
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2},
					terminal,
				})
				rec := httptest.NewRecorder()
				var err error
				if streaming {
					err = gemini.WriteStreamSSE(context.Background(), rec, &aipapi.Call{}, es, gemini.EncodeOptions{})
				} else {
					err = gemini.WriteNonStreamJSON(context.Background(), rec, &aipapi.Call{}, es, gemini.EncodeOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
				payloads := []string{rec.Body.String()}
				if streaming {
					payloads = nil
					for line := range strings.SplitSeq(rec.Body.String(), "\n") {
						if data, ok := strings.CutPrefix(line, "data: "); ok {
							payloads = append(payloads, data)
						}
					}
				}
				terminals, tools := 0, 0
				var text strings.Builder
				var usage *genai.GenerateContentResponseUsageMetadata
				for _, payload := range payloads {
					var frame genai.GenerateContentResponse
					if err := json.Unmarshal([]byte(payload), &frame); err != nil {
						t.Fatal(err)
					}
					if frame.UsageMetadata != nil {
						usage = frame.UsageMetadata
					}
					for _, candidate := range frame.Candidates {
						if terminals != 0 {
							t.Fatal("candidate emitted after terminal")
						}
						if candidate.FinishReason != "" {
							terminals++
							if candidate.FinishReason != want {
								t.Fatalf("SDK finish reason = %q, want %q", candidate.FinishReason, want)
							}
							if streaming && candidate.Content != nil && len(candidate.Content.Parts) != 0 {
								t.Fatal("terminal candidate repeated content")
							}
						}
						if candidate.Content == nil {
							continue
						}
						for _, part := range candidate.Content.Parts {
							text.WriteString(part.Text)
							if call := part.FunctionCall; call != nil {
								tools++
								if call.Name != "weather" || call.Args["city"] != "Oslo" {
									t.Fatalf("SDK tool call changed: %+v", call)
								}
							}
						}
					}
				}
				if terminals != 1 || tools != 1 || text.String() != "hello" {
					t.Fatalf("terminals=%d tools=%d text=%q", terminals, tools, text.String())
				}
				if usage == nil || usage.PromptTokenCount != 3 || usage.CandidatesTokenCount != 2 || usage.TotalTokenCount != 5 {
					t.Fatalf("SDK usage changed: %+v", usage)
				}
			})
		}
	}
}
