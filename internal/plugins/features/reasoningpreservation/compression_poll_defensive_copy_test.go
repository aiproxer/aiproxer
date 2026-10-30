package reasoningpreservation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/require"
)

// TestPollOnce_Completed_DefensiveCopyOnAdoption proves the plugin clones the
// completed Collected at the adoption boundary. Even if an external
// auxiliary.BackgroundPoller violates the documented defensive-copy contract,
// later mutations of the poller's payload must never be visible through the
// adopted candidate because all mutable interiors (maps, slices, builders,
// nested event graphs) alias the poller-owned value.
func TestPollOnce_Completed_DefensiveCopyOnAdoption(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-defensive")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)

	// Build a Collected with every mutable interior that aliases on value copy.
	var c aipapi.Collected
	c.Text.WriteString("orig text")
	c.Reasoning.WriteString("orig reasoning")
	c.ToolArgs = map[string]*strings.Builder{"tool-1": func() *strings.Builder { b := &strings.Builder{}; b.WriteString("args1"); return b }()}
	c.ToolNames = map[string]string{"tool-1": "fn1"}
	c.ToolCallOrder = []string{"tool-1"}
	c.Warnings = []string{"warn1"}
	c.AssistantMedia = []aipapi.Part{{Kind: aipapi.PartText, Text: "media"}}
	c.ReasoningParts = []aipapi.ReasoningPart{{
		Dialect:                 aipapi.ReasoningDialectOpenAIChatTextV1,
		Text:                    "rt",
		Signature:               "sig",
		Opaque:                  []byte(`{"opaque":1}`),
		Summary:                 []byte(`{"summary":1}`),
		SummaryPresent:          true,
		Content:                 []byte(`{"content":1}`),
		ContentPresent:          true,
		EncryptedContent:        []byte(`{"enc":1}`),
		EncryptedContentPresent: true,
	}}
	c.TerminalError = &aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    "orig-code",
		ErrorMessage: "orig msg",
		Opaque:       []byte("opaque-terminal"),
		Reasoning: &aipapi.ReasoningPart{
			Dialect:                 aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:                    "term-rt",
			Signature:               "term-sig",
			Opaque:                  []byte(`{"term-opaque":1}`),
			Summary:                 []byte(`{"term-summary":1}`),
			SummaryPresent:          true,
			Content:                 []byte(`{"term-content":1}`),
			ContentPresent:          true,
			EncryptedContent:        []byte(`{"term-enc":1}`),
			EncryptedContentPresent: true,
		},
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{
				Kind:       aipapi.ContentPartText,
				Text:       "item-text",
				Annotation: &aipapi.AnnotationPart{Type: "ann", Data: []byte(`{"ann":1}`)},
			}},
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Arguments: []byte(`{"arg":1}`)},
		},
		UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 5, OutputTokens: 10}},
	}

	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindCompleted, res.Kind)
	require.NotNil(t, res.Candidate)

	// Mutate every mutable interior of the poller-owned payload after poll.
	// (Text/Reasoning builders cannot be written after a value copy — strings.Builder
	// rejects that — so they are asserted only for content below.)
	poller.result.Collected.ToolArgs["tool-1"].WriteString(" mutated")
	poller.result.Collected.ToolNames["tool-1"] = "mutated"
	poller.result.Collected.ToolCallOrder[0] = "mutated"
	poller.result.Collected.Warnings[0] = "mutated"
	poller.result.Collected.AssistantMedia[0].Text = "mutated"
	poller.result.Collected.ReasoningParts[0].Text = "mutated"
	poller.result.Collected.ReasoningParts[0].Signature = "mutated"
	poller.result.Collected.ReasoningParts[0].Opaque[0] = 'X'
	poller.result.Collected.ReasoningParts[0].Summary[0] = 'X'
	poller.result.Collected.ReasoningParts[0].Content[0] = 'X'
	poller.result.Collected.ReasoningParts[0].EncryptedContent[0] = 'X'
	poller.result.Collected.TerminalError.ErrorCode = "mutated"
	poller.result.Collected.TerminalError.Opaque[0] = 'X'
	poller.result.Collected.TerminalError.Reasoning.Text = "mutated"
	poller.result.Collected.TerminalError.Reasoning.Signature = "mutated"
	poller.result.Collected.TerminalError.Reasoning.Opaque[0] = 'X'
	poller.result.Collected.TerminalError.Reasoning.Summary[0] = 'X'
	poller.result.Collected.TerminalError.Reasoning.Content[0] = 'X'
	poller.result.Collected.TerminalError.Reasoning.EncryptedContent[0] = 'X'
	poller.result.Collected.TerminalError.Item.Content[0].Text = "mutated"
	poller.result.Collected.TerminalError.Item.Content[0].Annotation.Data[0] = 'X'
	poller.result.Collected.TerminalError.Item.ToolCall.Arguments[0] = 'X'
	poller.result.Collected.TerminalError.UsageScopes[0] = aipapi.ScopedUsageDelta{InputTokens: 99, OutputTokens: 99}

	// The adopted candidate must still see the original values.
	require.Equal(t, "orig text", res.Candidate.Collected.Text.String())
	require.Equal(t, "orig reasoning", res.Candidate.Collected.Reasoning.String())
	require.Equal(t, "args1", res.Candidate.Collected.ToolArgs["tool-1"].String())
	require.Equal(t, "fn1", res.Candidate.Collected.ToolNames["tool-1"])
	require.Equal(t, "tool-1", res.Candidate.Collected.ToolCallOrder[0])
	require.Equal(t, "warn1", res.Candidate.Collected.Warnings[0])
	require.Equal(t, "media", res.Candidate.Collected.AssistantMedia[0].Text)
	require.Equal(t, "rt", res.Candidate.Collected.ReasoningParts[0].Text)
	require.Equal(t, "sig", res.Candidate.Collected.ReasoningParts[0].Signature)
	require.Equal(t, `{"opaque":1}`, string(res.Candidate.Collected.ReasoningParts[0].Opaque))
	require.Equal(t, `{"summary":1}`, string(res.Candidate.Collected.ReasoningParts[0].Summary))
	require.Equal(t, `{"content":1}`, string(res.Candidate.Collected.ReasoningParts[0].Content))
	require.Equal(t, `{"enc":1}`, string(res.Candidate.Collected.ReasoningParts[0].EncryptedContent))
	require.Equal(t, "orig-code", res.Candidate.Collected.TerminalError.ErrorCode)
	require.Equal(t, "opaque-terminal", string(res.Candidate.Collected.TerminalError.Opaque))
	require.Equal(t, "term-rt", res.Candidate.Collected.TerminalError.Reasoning.Text)
	require.Equal(t, "term-sig", res.Candidate.Collected.TerminalError.Reasoning.Signature)
	require.Equal(t, `{"term-opaque":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Opaque))
	require.Equal(t, `{"term-summary":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Summary))
	require.Equal(t, `{"term-content":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Content))
	require.Equal(t, `{"term-enc":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.EncryptedContent))
	require.Equal(t, "item-text", res.Candidate.Collected.TerminalError.Item.Content[0].Text)
	require.Equal(t, `{"ann":1}`, string(res.Candidate.Collected.TerminalError.Item.Content[0].Annotation.Data))
	require.Equal(t, `{"arg":1}`, string(res.Candidate.Collected.TerminalError.Item.ToolCall.Arguments))
	require.Equal(t, 5, res.Candidate.Collected.TerminalError.UsageScopes[0].InputTokens)
	require.Equal(t, 10, res.Candidate.Collected.TerminalError.UsageScopes[0].OutputTokens)
}
