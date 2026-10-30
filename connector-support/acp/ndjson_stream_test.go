package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/streampump"
)

type fakeStrategy struct {
	label       string
	exclude     []string
	onServerReq func(ctx context.Context, probe map[string]any) error
	mapLine     func(ctx context.Context, line string, probe map[string]any) ([]aipapi.Event, error)

	mu          sync.Mutex
	cancelCount int
}

func (f *fakeStrategy) Label() string { return f.label }
func (f *fakeStrategy) IsServerRequest(probe map[string]any) bool {
	return IsInboundServerRequest(probe, f.exclude)
}

func (f *fakeStrategy) HandleServerRequest(ctx context.Context, probe map[string]any) error {
	if f.onServerReq != nil {
		return f.onServerReq(ctx, probe)
	}
	return nil
}

func (f *fakeStrategy) MapLine(ctx context.Context, line string, probe map[string]any) ([]aipapi.Event, error) {
	if f.mapLine != nil {
		return f.mapLine(ctx, line, probe)
	}
	return nil, nil
}

func (f *fakeStrategy) OnCancel() {
	f.mu.Lock()
	f.cancelCount++
	f.mu.Unlock()
}

func (f *fakeStrategy) cancelCountGet() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCount
}

func TestIsInboundServerRequest_exclusions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		probe   string
		exclude []string
		want    bool
	}{
		{"server request no exclusions", `{"jsonrpc":"2.0","id":3,"method":"foo","params":{}}`, nil, true},
		{"session/update excluded by ACP", `{"jsonrpc":"2.0","id":3,"method":"session/update","params":{}}`, []string{"session/update"}, false},
		{"session/update not excluded when no exclude list", `{"jsonrpc":"2.0","id":3,"method":"session/update","params":{}}`, nil, true},
		{"result is response not request", `{"jsonrpc":"2.0","id":10,"result":{}}`, nil, false},
		{"error is response not request", `{"jsonrpc":"2.0","id":10,"error":{}}`, nil, false},
		{"notification no id", `{"jsonrpc":"2.0","method":"x","params":{}}`, nil, false},
		{"nil method", `{"jsonrpc":"2.0","id":1,"params":{}}`, nil, false},
		{"empty method", `{"jsonrpc":"2.0","id":1,"method":"","params":{}}`, nil, false},
		{"nil probe", ``, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var probe map[string]any
			if tc.probe != "" {
				if err := json.Unmarshal([]byte(tc.probe), &probe); err != nil {
					t.Fatal(err)
				}
			}
			if got := IsInboundServerRequest(probe, tc.exclude); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNDJSONStreamBase_framingEmitsResponseAndMessageStart(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"x","params":{}}` + "\n"
	start := &fakeStrategy{
		label: "test",
		mapLine: func(_ context.Context, _ string, _ map[string]any) ([]aipapi.Event, error) {
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hi"}}, nil
		},
	}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader(line)), 0, start)

	ev, err := base.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event = %v, want ResponseStarted", ev.Kind)
	}
	ev, err = base.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventMessageStarted {
		t.Fatalf("second event = %v, want MessageStarted", ev.Kind)
	}
	ev, err = base.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "hi" {
		t.Fatalf("third event = %v, want TextDelta hi", ev)
	}
}

func TestNDJSONStreamBase_unexpectedEOFBeforeResponseStarted(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)
	_, err := base.Recv(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output unexpected EOF = terminal, want recoverable pre-output (%v)", err)
	}
}

func TestNDJSONStreamBase_cleanEOFAfterStartEmitsResponseFinished(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"x","params":{}}` + "\n"
	start := &fakeStrategy{
		label: "test",
		mapLine: func(_ context.Context, _ string, _ map[string]any) ([]aipapi.Event, error) {
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hi"}}, nil
		},
	}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader(line)), 0, start)

	for range 3 {
		if _, err := base.Recv(context.Background()); err != nil {
			t.Fatalf("drain: %v", err)
		}
	}
	ev, err := base.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %v, want ResponseFinished", ev.Kind)
	}
	_, err = base.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestNDJSONStreamBase_cancelInvokesOnCancel(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)
	res := base.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("mode = %s", res.Mode)
	}
	if start.cancelCountGet() != 1 {
		t.Fatalf("cancelCount = %d, want 1", start.cancelCountGet())
	}
}

func TestNDJSONStreamBase_scanErrorUsesLabel(t *testing.T) {
	t.Parallel()
	errRead := errors.New("read boom")
	chunk := `{"jsonrpc":"2.0","method":"x","params":{}}` + "\n"
	r := &readOnceThenErr{data: []byte(chunk), err: errRead}
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(r), 0, start)
	_, err := base.Recv(context.Background())
	if err == nil {
		t.Fatal("expected scan error")
	}
	if !strings.Contains(err.Error(), "test: scan stream") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, errRead) {
		t.Fatalf("expected underlying read error, got %v", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output scan error = terminal, want recoverable pre-output (%v)", err)
	}
}

func TestNDJSONStreamBase_decodeErrorUsesLabel(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("{not json\n")), 0, start)
	_, err := base.Recv(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "test: decode inbound line") {
		t.Fatalf("got %v", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("expected *json.SyntaxError in chain, got %v", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output decode error = terminal, want recoverable pre-output (%v)", err)
	}
}

func TestNDJSONStreamBase_postOutputScanErrorNotRecoverable(t *testing.T) {
	t.Parallel()
	errRead := errors.New("read boom")
	line := `{"jsonrpc":"2.0","method":"x","params":{}}` + "\n"
	start := &fakeStrategy{
		label: "test",
		mapLine: func(_ context.Context, _ string, _ map[string]any) ([]aipapi.Event, error) {
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hi"}}, nil
		},
	}
	r := &readOnceThenErr{data: []byte(line), err: errRead}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(r), 0, start)
	for range 3 {
		if _, err := base.Recv(context.Background()); err != nil {
			t.Fatalf("drain pre-output events: %v", err)
		}
	}
	_, err := base.Recv(context.Background())
	if err == nil {
		t.Fatal("expected scan error after output")
	}
	if !errors.Is(err, errRead) {
		t.Fatalf("expected underlying read error, got %v", err)
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-output scan error = recoverable, want terminal (no retry after output)")
	}
}

func TestNDJSONStreamBase_postOutputDecodeErrorNotRecoverable(t *testing.T) {
	t.Parallel()
	data := `{"jsonrpc":"2.0","method":"x","params":{}}` + "\n" + "{not json\n"
	start := &fakeStrategy{
		label: "test",
		mapLine: func(_ context.Context, _ string, _ map[string]any) ([]aipapi.Event, error) {
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "hi"}}, nil
		},
	}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader(data)), 0, start)
	for range 3 {
		if _, err := base.Recv(context.Background()); err != nil {
			t.Fatalf("drain pre-output events: %v", err)
		}
	}
	_, err := base.Recv(context.Background())
	if err == nil {
		t.Fatal("expected decode error after output")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-output decode error = recoverable, want terminal (no retry after output)")
	}
}

func TestNDJSONStreamBase_recvNilContext(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)
	//nolint:staticcheck // SA1012: this test verifies the stream's nil-context rejection (see TestNDJSONStreamBase_recvNilContext).
	_, err := base.Recv(nil)
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v, want ErrNilContext", err)
	}
}

func TestNDJSONStreamBase_recvCancelledContextInvokesOnCancelAndCloses(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := base.Recv(cctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if start.cancelCountGet() != 1 {
		t.Fatalf("cancelCount = %d, want 1", start.cancelCountGet())
	}
}

func TestNDJSONStreamBase_closeIsIdempotent(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNDJSONStreamBase_pushPendingLockedReturnsEventsViaRecv(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 0, start)

	injected := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "summary-1"},
		{Kind: aipapi.EventTextDelta, Delta: "summary-2"},
	}
	for _, ev := range injected {
		if err := base.PushPendingLocked(ev); err != nil {
			t.Fatalf("PushPendingLocked: %v", err)
		}
	}
	for _, want := range injected {
		ev, err := base.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind != want.Kind || ev.Delta != want.Delta {
			t.Fatalf("got %+v, want %+v", ev, want)
		}
	}
}

func TestNDJSONStreamBase_pushPendingLockedRespectsQueueCap(t *testing.T) {
	t.Parallel()
	start := &fakeStrategy{label: "test"}
	base := NewNDJSONStreamBase(context.Background(), io.NopCloser(strings.NewReader("")), 1, start)
	if err := base.PushPendingLocked(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a"}); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := base.PushPendingLocked(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b"}); !errors.Is(err, streampump.ErrPendingQueueFull) {
		t.Fatalf("second push: got %v, want ErrPendingQueueFull", err)
	}
}
