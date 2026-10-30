package acp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestPoolManagedStream_recvFlushesIncompleteToolSummariesAtEOF exercises the
// wrapper's flush-on-terminal-error path that injects pending tool summaries
// via NDJSONStreamBase.PushPendingLocked. The inner stream ends before any
// response frame (io.ErrUnexpectedEOF); the wrapper must surface the flushed
// summary first, then resurface the terminal error on the next Recv.
func TestPoolManagedStream_recvFlushesIncompleteToolSummariesAtEOF(t *testing.T) {
	t.Parallel()

	sink, ok := NewToolSummarySink(nil).(*toolSummarySink)
	if !ok {
		t.Fatal("NewToolSummarySink did not return *toolSummarySink")
	}
	if _, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
		"toolCallId": "tc1",
		"title":      "read_file",
	}); err != nil {
		t.Fatalf("HandleToolUpdate: %v", err)
	}

	// Empty body => inner Recv returns io.ErrUnexpectedEOF (no response started),
	// which drives the wrapper's flush branch. The EOF flush path never touches
	// the client transport, so a zero client is safe here.
	stream := newPromptNDJSONStream(
		context.Background(),
		io.NopCloser(strings.NewReader("")),
		&client{},
		"sess",
		1,
		"msg1",
		SessionUpdateMapperOptions{},
		nil,
		CancelProfile{},
		0,
	)
	s := &poolManagedStream{inner: stream, toolSink: sink}

	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if ev.Kind != aipapi.EventTextDelta {
		t.Fatalf("first Recv kind = %v, want TextDelta (flushed tool summary)", ev.Kind)
	}
	if !strings.Contains(ev.Delta, "read_file") {
		t.Fatalf("flushed summary missing tool name: %q", ev.Delta)
	}

	if _, err = s.Recv(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("second Recv: got %v, want io.ErrUnexpectedEOF", err)
	}
}
