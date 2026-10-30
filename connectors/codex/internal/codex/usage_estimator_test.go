package codex

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEstimateUsage_textRequest_positiveTotalsAndMetadata(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello codex")},
		}},
	}
	ev, err := est.estimateUsage(context.Background(), call, "gpt-5.3-codex", "world")
	if err != nil {
		t.Fatal(err)
	}
	if ev.InputTokens <= 0 || ev.OutputTokens <= 0 {
		t.Fatalf("tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if ev.TotalTokens != ev.InputTokens+ev.OutputTokens {
		t.Fatalf("total=%d want %d", ev.TotalTokens, ev.InputTokens+ev.OutputTokens)
	}
	if ev.Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("source=%q", ev.Accounting.Source)
	}
	if ev.Accounting.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("authority=%q", ev.Accounting.Authority)
	}
	if ev.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("plane=%q", ev.Accounting.Plane)
	}
	if ev.Accounting.Tokenizer.Type != "tiktoken" || ev.Accounting.Tokenizer.ID != "o200k_base" {
		t.Fatalf("tokenizer=%+v", ev.Accounting.Tokenizer)
	}
	if ev.Accounting.Tokenizer.Source != "github.com/tiktoken-go/tokenizer" {
		t.Fatalf("tokenizer source=%q", ev.Accounting.Tokenizer.Source)
	}
	if ev.Accounting.Tokenizer.ModelUsed != "gpt-5.3-codex" {
		t.Fatalf("model used=%q", ev.Accounting.Tokenizer.ModelUsed)
	}
}

func TestEstimateUsage_imageRefURL_usesConservativeDefault(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("describe"),
				{Kind: aipapi.PartImageRef, ImageRef: "https://example.com/image.png"},
			},
		}},
	}
	ev, err := est.estimateUsage(context.Background(), call, "gpt-5.3-codex", "done")
	if err != nil {
		t.Fatal(err)
	}
	if ev.InputTokens <= 0 || ev.OutputTokens <= 0 || ev.TotalTokens != ev.InputTokens+ev.OutputTokens {
		t.Fatalf("usage: %+v", ev)
	}
}

func TestEstimateUsage_toolsContributeToInput(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	base := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("use tools")},
		}},
	}
	withTools := base
	withTools.Tools = []aipapi.ToolDef{{
		Name:        "lookup",
		Description: "look up a value",
		Parameters:  []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}
	baseEv, err := est.estimateUsage(context.Background(), base, "gpt-5.3-codex", "ok")
	if err != nil {
		t.Fatal(err)
	}
	toolEv, err := est.estimateUsage(context.Background(), withTools, "gpt-5.3-codex", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if toolEv.InputTokens <= baseEv.InputTokens {
		t.Fatalf("tools should increase input: base=%d tools=%d", baseEv.InputTokens, toolEv.InputTokens)
	}
}

func TestUsageEstimatingStream_estimationFailureReturnsFinishedEvent(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	stream := &usageEstimatingStream{
		base: &usageEstimateTestStream{events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			{Kind: aipapi.EventResponseFinished},
		}},
		est: est,
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	ev, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("kind = %q", ev.Kind)
	}
}

func TestUsageEstimatingStream_skipsEstimationWhenOutputBufferExceedsLimit(t *testing.T) {
	t.Parallel()
	est := &usageEstimator{}
	stream := &usageEstimatingStream{
		base: &usageEstimateTestStream{events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("x", maxUsageEstimateOutputBytes+1)},
			{Kind: aipapi.EventResponseFinished},
		}},
		est: est,
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if stream.text.Len() != 0 || !stream.textFull {
		t.Fatalf("buffer len=%d full=%v", stream.text.Len(), stream.textFull)
	}
}

func TestUsageEstimatingStream_skipsWhenProviderAlreadySentUsage(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	stream := newUsageEstimatingStream(&usageEstimateTestStream{events: []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
		{Kind: aipapi.EventResponseFinished},
	}}, est, aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}}, "gpt-5.3-codex")
	var kinds []aipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
	}
	usageCount := 0
	for _, k := range kinds {
		if k == aipapi.EventUsageDelta {
			usageCount++
		}
	}
	if usageCount != 1 {
		t.Fatalf("kinds=%v usageCount=%d", kinds, usageCount)
	}
}

type usageEstimateTestStream struct {
	events []aipapi.Event
	idx    int
}

func (s *usageEstimateTestStream) Recv(context.Context) (aipapi.Event, error) {
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *usageEstimateTestStream) Close() error { return nil }

func (s *usageEstimateTestStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
}
