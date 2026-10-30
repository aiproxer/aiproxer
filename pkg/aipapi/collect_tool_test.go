package aipapi_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCollect_toolStartedRecordsNameAndOrder(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_a", ToolName: "fn_a"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: `{"x":1}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_a"},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].ID != "call_a" || tcs[0].Name != "fn_a" || tcs[0].Arguments != `{"x":1}` {
		t.Fatalf("got %+v", tcs)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text %q", col.Text.String())
	}
}

func TestCollect_argsDeltaBeforeStartedStillOrders(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "1"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_b", ToolName: "late"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: "2"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].Name != "late" || tcs[0].Arguments != "12" {
		t.Fatalf("got %+v", tcs)
	}
}

func TestCollect_twoToolsPreservesOrder(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "a"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c2", ToolName: "b"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c2", Delta: "y"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: "x"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 2 || tcs[0].ID != "c1" || tcs[1].ID != "c2" {
		t.Fatalf("got %+v", tcs)
	}
	if tcs[0].Arguments != "x" || tcs[1].Arguments != "y" {
		t.Fatalf("args %+v", tcs)
	}
}

func TestCollect_toolStartedMissingIDErrors(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolName: "x"},
		{Kind: aipapi.EventResponseFinished},
	})
	_, err := aipapi.Collect(context.Background(), es)
	if err == nil {
		t.Fatal("expected error")
	}
}
