package aipapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCollectWithLimits_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := aipapi.CollectWithLimits(context.Background(), nil, aipapi.CollectLimits{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, aipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollect_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := aipapi.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, aipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollectUnbounded_nil_event_stream_returns_error(t *testing.T) {
	t.Parallel()
	_, err := aipapi.CollectUnbounded(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, aipapi.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
}

func TestCollect_happyPathOrderingAndAggregation(t *testing.T) {
	t.Parallel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hel"},
		{Kind: aipapi.EventTextDelta, Delta: "lo"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2},
		{Kind: aipapi.EventWarning, WarningMessage: "x"},
		{Kind: aipapi.EventResponseFinished},
	})

	out, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out.Text.String(); got != "hello" {
		t.Fatalf("text: got %q want %q", got, "hello")
	}
	if out.InputTokens != 3 || out.OutputTokens != 2 {
		t.Fatalf("usage: got in=%d out=%d", out.InputTokens, out.OutputTokens)
	}
	if len(out.Warnings) != 1 || out.Warnings[0] != "x" {
		t.Fatalf("warnings: %#v", out.Warnings)
	}
	if !out.FinishReceived {
		t.Fatal("expected finish")
	}
}

func TestCollect_detailedUsageAggregation(t *testing.T) {
	t.Parallel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{
			Kind:             aipapi.EventUsageDelta,
			InputTokens:      10,
			OutputTokens:     4,
			CacheReadTokens:  3,
			CacheWriteTokens: 2,
			ReasoningTokens:  1,
			TotalTokens:      14,
		},
		{Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 2, CacheReadTokens: 1, TotalTokens: 17},
		{Kind: aipapi.EventResponseFinished},
	})
	out, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if out.InputTokens != 11 || out.OutputTokens != 6 {
		t.Fatalf("basic usage: got in=%d out=%d", out.InputTokens, out.OutputTokens)
	}
	if out.CacheReadTokens != 4 || out.CacheWriteTokens != 2 || out.ReasoningTokens != 1 || out.TotalTokens != 17 {
		t.Fatalf("detailed usage: cache_read=%d cache_write=%d reasoning=%d total=%d",
			out.CacheReadTokens, out.CacheWriteTokens, out.ReasoningTokens, out.TotalTokens)
	}
}

func TestCollect_errorTerminationReturnsError(t *testing.T) {
	t.Parallel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "partial"},
		{Kind: aipapi.EventError, ErrorCode: "upstream", ErrorMessage: "boom"},
	})

	out, err := aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
	var se *aipapi.StreamError
	if !errors.As(err, &se) {
		t.Fatalf("want *aipapi.StreamError, got %T: %v", err, err)
	}
	if se.Code != "upstream" || se.Message != "boom" {
		t.Fatalf("unexpected stream error: %+v", se)
	}
	if !errors.Is(err, aipapi.ErrStreamTerminal) {
		t.Fatalf("expected ErrStreamTerminal in chain, got %v", err)
	}
	if out.Text.String() != "partial" {
		t.Fatalf("expected partial text aggregation, got %q", out.Text.String())
	}
	if out.TerminalError == nil {
		t.Fatal("expected terminal error event captured")
	}
}

func TestValidateEventSequence_acceptsErrorTerminal(t *testing.T) {
	t.Parallel()

	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventError, ErrorCode: "x", ErrorMessage: "y"},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateEventSequence_rejectsDeltaBeforeMessageStarted(t *testing.T) {
	t.Parallel()

	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventTextDelta, Delta: "nope"},
		{Kind: aipapi.EventResponseFinished},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventSequence_rejectsMissingResponseStarted(t *testing.T) {
	t.Parallel()

	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventMessageStarted},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollect_duplicateResponseStarted(t *testing.T) {
	t.Parallel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseStarted},
	})

	_, err := aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollect_limitExceededOnText(t *testing.T) {
	t.Parallel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("z", 100)},
		{Kind: aipapi.EventResponseFinished},
	})
	_, err := aipapi.CollectWithLimits(context.Background(), stream, aipapi.CollectLimits{
		MaxTextBytes: 50,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, aipapi.ErrCollectLimitExceeded) {
		t.Fatalf("want ErrCollectLimitExceeded: %v", err)
	}
}

func TestCollect_assistantMediaAndFinishReason(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://example.com/x.png", AssistantMIME: "image/png"},
		{Kind: aipapi.EventAssistantFileRef, AssistantRef: "file-abc", AssistantMIME: "application/pdf", AssistantName: "doc.pdf"},
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	})
	out, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text.String() != "hi" {
		t.Fatalf("text: %q", out.Text.String())
	}
	if len(out.AssistantMedia) != 2 {
		t.Fatalf("assistant parts: %#v", out.AssistantMedia)
	}
	if out.AssistantMedia[0].Kind != aipapi.PartImageRef || out.AssistantMedia[0].ImageRef != "https://example.com/x.png" {
		t.Fatalf("image part: %#v", out.AssistantMedia[0])
	}
	if out.AssistantMedia[1].Kind != aipapi.PartFileRef || out.AssistantMedia[1].FileRef != "file-abc" {
		t.Fatalf("file part: %#v", out.AssistantMedia[1])
	}
	if out.FinishReason != "stop" {
		t.Fatalf("finish: %q", out.FinishReason)
	}
}

func TestCollect_assistantMediaLimit(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "a"},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "b"},
		{Kind: aipapi.EventResponseFinished},
	})
	_, err := aipapi.CollectWithLimits(context.Background(), stream, aipapi.CollectLimits{
		MaxAssistantMediaParts: 1,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, aipapi.ErrCollectLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_contextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
	})

	_, err := aipapi.Collect(ctx, stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}

func TestCollect_toolCallsInterleavedOrderAndDedup(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "a", ToolName: "fa"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "b", ToolName: "fb"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "b", Delta: "2"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: "1"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "b"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: "3"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "a"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c", Delta: "4"},
		{Kind: aipapi.EventResponseFinished},
	})
	out, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.ToolCallOrder, ","); got != "a,b,c" {
		t.Fatalf("tool order: %q", got)
	}
	if out.ToolNames["a"] != "fa" {
		t.Fatalf("first tool name wins: %#v", out.ToolNames)
	}
	if out.ToolArgs["a"].String() != "13" || out.ToolArgs["b"].String() != "2" || out.ToolArgs["c"].String() != "4" {
		t.Fatalf("args a=%q b=%q c=%q", out.ToolArgs["a"].String(), out.ToolArgs["b"].String(), out.ToolArgs["c"].String())
	}
}

func TestCollect_toolArgsTotalLimitUsesRunningSum(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "x", Delta: "aa"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "y", Delta: "b"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "x", Delta: "c"},
		{Kind: aipapi.EventResponseFinished},
	})
	_, err := aipapi.CollectWithLimits(context.Background(), stream, aipapi.CollectLimits{
		MaxToolArgsTotalBytes: 3,
	})
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !errors.Is(err, aipapi.ErrCollectLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}
