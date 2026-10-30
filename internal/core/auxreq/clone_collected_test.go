package auxreq

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestBackgroundScheduler_RegistersIDBeforePublication(t *testing.T) {
	t.Parallel()
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	var observed atomic.Uint64
	started := make(chan struct{})
	s := &BackgroundScheduler{
		root:   root,
		cancel: cancel,
		cfg: SchedulerConfig{
			MaxResults:     1,
			ResultTTL:      time.Minute,
			JobTimeout:     time.Minute,
			MaxResultBytes: 1 << 20,
		},
		queue: make(chan *backgroundJob, 1),
		jobs:  make(map[auxiliary.JobID]*backgroundJob),
		byKey: make(map[string]auxiliary.JobID),
	}
	s.runner = func() ExecutorRunner {
		return observingRunner{observe: func() {
			observed.Store(s.sequence.Load())
			close(started)
		}}
	}
	s.wg.Add(1)
	go s.worker()
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	id, err := s.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "published"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if got := observed.Load(); got != 1 {
		t.Fatalf("worker observed sequence=%d, want registered first job sequence 1", got)
	}
	if _, err := s.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundScheduler_PublishRegistersIDBeforeQueueSend(t *testing.T) {
	t.Parallel()
	s := &BackgroundScheduler{
		cfg:   SchedulerConfig{MaxResults: 1, ResultTTL: time.Minute},
		queue: make(chan *backgroundJob, 1),
		jobs:  make(map[auxiliary.JobID]*backgroundJob),
		byKey: make(map[string]auxiliary.JobID),
	}
	job := &backgroundJob{key: "published", done: make(chan struct{})}
	id, admitted, err := s.publishJob(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !admitted || id == "" || job.id != id {
		t.Fatalf("published job id=%q admitted=%v, want a committed non-empty ID", id, admitted)
	}
	if s.jobs[id] != job || s.byKey[job.key] != id {
		t.Fatal("job was queued without being registered first")
	}
	if got := <-s.queue; got != job {
		t.Fatal("published queue item differs from registered job")
	}
}

func TestCloneCollected_PopulatedValueIsIndependent(t *testing.T) {
	t.Parallel()
	in := populatedCollected()
	out := aipapi.CloneCollected(in)
	if out.Text.String() != "text" || out.Reasoning.String() != "reasoning" {
		t.Fatalf("builder fields lost: text=%q reasoning=%q", out.Text.String(), out.Reasoning.String())
	}
	if out.InputTokens != 11 || out.OutputTokens != 7 || out.CacheReadTokens != 2 || out.CacheWriteTokens != 3 || out.ReasoningTokens != 4 || out.TotalTokens != 18 || out.CostNanoUnits != 99 || out.Currency != "USD" || out.CostSource != "catalog" || !out.FinishReceived || out.FinishReason != "stop" {
		t.Fatalf("scalar fields not preserved: %+v", out)
	}
	if out.ToolArgs["tool"].String() != "args" || out.ToolNames["tool"] != "lookup" || len(out.ToolCallOrder) != 1 || out.ToolCallOrder[0] != "tool" {
		t.Fatalf("tool fields not preserved: %+v", out)
	}
	if out.TerminalError == nil || out.TerminalError.ErrorMessage != "failed" || out.TerminalError.Reasoning == nil || out.TerminalError.Item == nil {
		t.Fatalf("terminal error fields not preserved: %+v", out.TerminalError)
	}

	out.ToolArgs["tool"].WriteString("-changed")
	// Text/Reasoning builders are not written after a value return (same
	// strings.Builder rule as every aipapi.Collected value), so builder content
	// is asserted read-only below.
	out.ToolNames["tool"] = "changed"
	out.ToolCallOrder[0] = "changed"
	out.Warnings[0] = "changed"
	out.TerminalError.Opaque[0] = 'X'
	out.TerminalError.Reasoning.Opaque[0] = 'X'
	out.TerminalError.Item.Content[0].Annotation.Data[0] = 'X'
	out.TerminalError.Item.ToolCall.Arguments[0] = 'X'
	out.AssistantMedia[0].Content[0] = 'X'
	out.AssistantMedia[0].Reasoning.Opaque[0] = 'X'
	out.ReasoningParts[0].Opaque[0] = 'X'

	if in.Text.String() != "text" || in.Reasoning.String() != "reasoning" || in.ToolArgs["tool"].String() != "args" {
		t.Fatal("builder clone shares mutable state")
	}
	if in.ToolNames["tool"] != "lookup" || in.ToolCallOrder[0] != "tool" || in.Warnings[0] != "warning" {
		t.Fatal("map/slice clone shares mutable state")
	}
	if in.TerminalError.Opaque[0] != 'o' || in.TerminalError.Reasoning.Opaque[0] != 'r' || in.TerminalError.Item.Content[0].Annotation.Data[0] != 'a' || in.TerminalError.Item.ToolCall.Arguments[0] != '{' {
		t.Fatal("terminal nested clone shares mutable state")
	}
	if in.AssistantMedia[0].Content[0] != 'm' || in.AssistantMedia[0].Reasoning.Opaque[0] != 'p' || in.ReasoningParts[0].Opaque[0] != 'p' {
		t.Fatal("media/reasoning clone shares mutable state")
	}

	for i := range 200 {
		copy := aipapi.CloneCollected(in)
		if copy.Text.String() != "text" || copy.ToolArgs["tool"].String() != "args" {
			t.Fatalf("repeated clone %d lost data", i)
		}
		if i%25 == 0 {
			runtime.GC()
		}
	}
}

func populatedCollected() *aipapi.Collected {
	out := &aipapi.Collected{}
	out.Text.WriteString("text")
	out.Reasoning.WriteString("reasoning")
	out.ToolArgs = map[string]*strings.Builder{"tool": {}}
	out.ToolArgs["tool"].WriteString("args")
	out.ToolNames = map[string]string{"tool": "lookup"}
	out.ToolCallOrder = []string{"tool"}
	out.Warnings = []string{"warning"}
	out.InputTokens = 11
	out.OutputTokens = 7
	out.CacheReadTokens = 2
	out.CacheWriteTokens = 3
	out.ReasoningTokens = 4
	out.TotalTokens = 18
	out.CostNanoUnits = 99
	out.Currency = "USD"
	out.CostSource = "catalog"
	out.FinishReceived = true
	out.FinishReason = "stop"
	out.TerminalError = &aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorMessage: "failed",
		Opaque:       []byte("opaque"),
		Reasoning:    &aipapi.ReasoningPart{Opaque: json.RawMessage("reasoning")},
		Item: &aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call", Name: "lookup", Arguments: json.RawMessage("{}")},
			Content:  []aipapi.ContentPart{{Kind: aipapi.ContentPartAnnotation, Annotation: &aipapi.AnnotationPart{Data: json.RawMessage("annotation")}}},
		},
	}
	out.AssistantMedia = []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage("media"), Reasoning: &aipapi.ReasoningPart{Opaque: json.RawMessage("part")}}}
	out.ReasoningParts = []aipapi.ReasoningPart{{Opaque: json.RawMessage("part-list")}}
	return out
}

type observingRunner struct{ observe func() }

func (r observingRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	r.observe()
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
}
