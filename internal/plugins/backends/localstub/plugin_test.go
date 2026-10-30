package localstub

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNew_streamCollectsTextAndUsage(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "hi", InputTokens: 2, OutputTokens: 5})
	es, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "hi" {
		t.Fatalf("text %q", col.Text.String())
	}
	if col.InputTokens != 2 || col.OutputTokens != 5 {
		t.Fatalf("usage in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
}

func TestNew_withToolName_emitsToolFrames(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "a", ToolName: "fn", InputTokens: 0, OutputTokens: 0})
	caps := execbackend.EffectiveCaps(context.Background(), be, aipapi.Call{}, routing.AttemptCandidate{})
	if _, has := caps[aipapi.CapabilityTools]; !has {
		t.Fatal("expected tools capability")
	}
	es, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].Name != "fn" || tcs[0].Arguments != `{}` {
		t.Fatalf("tools %+v", tcs)
	}
}

func TestNew_withToolArgs_emitsConfiguredDelta(t *testing.T) {
	t.Parallel()
	want := `{"location":"NYC"`
	be := New(Config{Text: "a", ToolName: "get_weather", ToolArgs: want})
	es, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].Name != "get_weather" || tcs[0].Arguments != want {
		t.Fatalf("tools %+v", tcs)
	}
}

func TestNew_withoutToolName_noToolsCapability(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "only", ToolName: ""})
	caps := execbackend.EffectiveCaps(context.Background(), be, aipapi.Call{}, routing.AttemptCandidate{})
	if _, has := caps[aipapi.CapabilityTools]; has {
		t.Fatal("did not expect tools capability")
	}
	es, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.OrderedToolCalls()) != 0 {
		t.Fatalf("expected no tool calls, got %+v", col.OrderedToolCalls())
	}
}

func TestNew_nilContextOpenError(t *testing.T) {
	t.Parallel()
	be := New(Config{})
	_, err := be.Open(nil, aipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_streamErrorAfterTextDelta_errorsAfterFirstTextDelta(t *testing.T) {
	t.Parallel()
	be := New(Config{
		Text:                      "x",
		InputTokens:               1,
		OutputTokens:              1,
		StreamErrorAfterTextDelta: true,
	})
	es, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for {
		_, rerr := es.Recv(ctx)
		if rerr != nil {
			if !errors.Is(rerr, errPostTextStream) {
				t.Fatalf("want errPostTextStream, got %v", rerr)
			}
			if aipapi.IsRecoverablePreOutput(rerr) {
				t.Fatal("post-text stub error must not be classified as recoverable pre-output")
			}
			break
		}
	}
	_ = es.Close()
}
