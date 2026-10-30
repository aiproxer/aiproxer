package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type recvMandatoryRewrite struct{ tcrRewriteFinalizer }

func (recvMandatoryRewrite) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}
}

var errRecvLaterFinalizer = errors.New("later finalizer failed")

type recvLaterFailure struct{}

func (recvLaterFailure) ID() string { return "later-failure" }
func (recvLaterFailure) Order() int { return 1 }
func (recvLaterFailure) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, errRecvLaterFinalizer
}

func TestRetryRecvStream_PreservedCallDrainsBeforeLaterError(t *testing.T) {
	t.Parallel()
	backend := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"original"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{})
	want := `{"location":"safe"}`
	ex.SetToolCallFinalizers([]toolcall.Finalizer{
		recvMandatoryRewrite{tcrRewriteFinalizer{args: []byte(want)}}, recvLaterFailure{},
	}, 64*1024)
	stream, err := ex.Execute(principalCtx("user-preserved-drain"), tcrWeatherCall())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var events []aipapi.Event
	for range 10 {
		ev, recvErr := stream.Recv(t.Context())
		if recvErr != nil {
			if !errors.Is(recvErr, errRecvLaterFinalizer) {
				t.Fatalf("wrong terminal error: %v", recvErr)
			}
			tools := tcrToolLifecycle(events)
			if len(tools) != 3 || tools[1].Delta != want || tools[2].Kind != aipapi.EventToolCallFinished {
				t.Fatalf("safe lifecycle was not delivered before error: events=%d", len(tools))
			}
			return
		}
		events = append(events, ev)
	}
	t.Fatal("later error was lost")
}

func TestRetryRecvStream_IncompleteMandatoryCallCannotFinishSuccessfully(t *testing.T) {
	for _, boundary := range []string{"response_finished", "eof"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			events := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{`},
			}
			if boundary == "response_finished" {
				events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
			}
			var opens atomic.Int32
			ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
				"openai": recordingBackend("openai", &opens, aipapi.NewFixedEventStream(events)),
			}, extensions.SnapshotOptions{})
			ex.SetToolCallFinalizers([]toolcall.Finalizer{recvMandatoryRewrite{}}, 64*1024)
			stream, err := ex.Execute(principalCtx("user-incomplete-mandatory"), tcrWeatherCall())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			for range 10 {
				ev, recvErr := stream.Recv(t.Context())
				if recvErr != nil {
					if errors.Is(recvErr, io.EOF) || !errors.Is(recvErr, runtime.ErrMandatoryBuffering) {
						t.Fatalf("incomplete mandatory call lost its refusal: %v", recvErr)
					}
					return
				}
				if ev.Kind == aipapi.EventResponseFinished || ev.Kind == aipapi.EventToolCallArgsDelta {
					t.Fatal("incomplete call released or finished")
				}
			}
			t.Fatal("missing refusal")
		})
	}
}
