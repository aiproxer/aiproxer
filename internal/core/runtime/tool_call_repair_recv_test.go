package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type tcrPassFinalizer struct{}

func (tcrPassFinalizer) ID() string { return "tcr-pass" }
func (tcrPassFinalizer) Order() int { return 0 }
func (tcrPassFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

type tcrRewriteFinalizer struct {
	name string
	args []byte
}

func (f tcrRewriteFinalizer) ID() string { return "tcr-rewrite" }
func (f tcrRewriteFinalizer) Order() int { return 0 }
func (f tcrRewriteFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	name := f.name
	if name == "" {
		name = "get_weather"
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   name,
		ArgsJSON:   append([]byte(nil), f.args...),
		ReasonCode: toolcall.ReasonSyntaxRepaired,
	}, nil
}

type tcrRejectFinalizer struct{}

func (tcrRejectFinalizer) ID() string { return "tcr-reject" }
func (tcrRejectFinalizer) Order() int { return 0 }
func (tcrRejectFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionReject, ReasonCode: toolcall.ReasonUnrepairable}, nil
}

type tcrPanicFinalizer struct{}

func (tcrPanicFinalizer) ID() string { return "tcr-panic" }
func (tcrPanicFinalizer) Order() int { return 0 }
func (tcrPanicFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	panic("tcr-panic-finalizer")
}

type tcrErrFinalizer struct{}

func (tcrErrFinalizer) ID() string { return "tcr-err" }
func (tcrErrFinalizer) Order() int { return 0 }
func (tcrErrFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, errors.New("finalizer boom")
}

type tcrOrderProbeFinalizer struct {
	id    string
	ord   int
	mu    *sync.Mutex
	order *[]string
	args  []byte
}

func (f tcrOrderProbeFinalizer) ID() string { return f.id }
func (f tcrOrderProbeFinalizer) Order() int { return f.ord }
func (f tcrOrderProbeFinalizer) Finalize(_ context.Context, call toolcall.CompletedCall, _ aipapi.ToolDef, _ []aipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	f.mu.Lock()
	*f.order = append(*f.order, f.id+":"+string(call.ArgsJSON))
	f.mu.Unlock()
	if f.args != nil {
		return toolcall.Result{
			Action:     toolcall.ActionRewrite,
			ToolName:   "get_weather",
			ArgsJSON:   append([]byte(nil), f.args...),
			ReasonCode: toolcall.ReasonSyntaxRepaired,
		}, nil
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

type pushManagedStream struct {
	ch     chan aipapi.Event
	closed atomic.Bool
}

func newPushManagedStream() *pushManagedStream {
	return &pushManagedStream{ch: make(chan aipapi.Event, 64)}
}

func (s *pushManagedStream) Push(ev aipapi.Event) {
	if s.closed.Load() {
		return
	}
	s.ch <- ev
}

func (s *pushManagedStream) ClosePush() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.ch)
	}
}

func (s *pushManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case ev, ok := <-s.ch:
		if !ok {
			return aipapi.Event{}, io.EOF
		}
		return ev, nil
	}
}

func (s *pushManagedStream) Close() error {
	s.ClosePush()
	return nil
}

func (s *pushManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.ClosePush()
	return aipapi.CancelResult{}
}

type failAfterStream struct {
	events []aipapi.Event
	i      int
	fail   error
}

func (s *failAfterStream) Recv(context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}

func (s *failAfterStream) Close() error { return nil }
func (s *failAfterStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

type tcrTrafficCapture struct {
	mu  sync.Mutex
	btp []aipapi.Event
	ptc []aipapi.Event
}

func (c *tcrTrafficCapture) OnObservation(_ context.Context, obs traffic.Observation) error {
	var ev aipapi.Event
	if err := json.Unmarshal(obs.Body, &ev); err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch obs.Leg {
	case traffic.LegBTP:
		c.btp = append(c.btp, ev)
	case traffic.LegPTC:
		c.ptc = append(c.ptc, ev)
	}
	return nil
}

func tcrWeatherCall() *aipapi.Call {
	call := pdBaseCall("openai:gpt-4")
	call.Tools = []aipapi.ToolDef{{
		Name:       "get_weather",
		Parameters: []byte(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`),
	}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	return call
}

func tcrCollect(t *testing.T, stream aipapi.EventStream) []aipapi.Event {
	t.Helper()
	var out []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, ev)
	}
}

func tcrToolLifecycle(evs []aipapi.Event) []aipapi.Event {
	var out []aipapi.Event
	for _, ev := range evs {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			out = append(out, ev)
		}
	}
	return out
}

func TestRetryRecvStream_ToolCallFinalizationResponseFinishedClearsAssembler(t *testing.T) {
	t.Parallel()
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"NYC"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-finish-clear"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	// Observe before Close: Close also resets, so this must catch markFinished ownership.
	active, passThrough, completed, drain := runtime.ToolFinalActiveCountForTest(stream)
	if active != 0 || passThrough != 0 || completed != 0 || drain != 0 {
		t.Fatalf("response_finished must clear assembler state; active=%d passThrough=%d completed=%d drain=%d",
			active, passThrough, completed, drain)
	}
	_ = stream.Close()
	if tools := tcrToolLifecycle(got); len(tools) != 3 {
		t.Fatalf("tool lifecycle len=%d want 3: %#v", len(tools), tools)
	}
}

func TestRetryRecvStream_ToolCallFinalizationHoldsUntilFinished(t *testing.T) {
	t.Parallel()
	backend := newPushManagedStream()
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-hold"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()

	backend.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather", MessageIndex: 0})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", ToolName: "get_weather", Delta: `{"location":"NYC"}`, MessageIndex: 0})

	for range 2 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatalf("drain non-tool: %v", err)
		}
		if ev.Kind == aipapi.EventToolCallStarted || ev.Kind == aipapi.EventToolCallArgsDelta || ev.Kind == aipapi.EventToolCallFinished {
			t.Fatalf("tool lifecycle must be held until finished; saw %s", ev.Kind)
		}
	}

	blocked := make(chan aipapi.Event, 1)
	errCh := make(chan error, 1)
	go func() {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			errCh <- err
			return
		}
		blocked <- ev
	}()
	// Recv must stay blocked while the incomplete tool lifecycle is held.
	select {
	case ev := <-blocked:
		t.Fatalf("tool lifecycle must stay held; got %s", ev.Kind)
	case err := <-errCh:
		t.Fatalf("hold recv: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather", MessageIndex: 0})
	backend.Push(aipapi.Event{Kind: aipapi.EventResponseFinished})
	backend.ClosePush()

	var got []aipapi.Event
	select {
	case ev := <-blocked:
		got = append(got, ev)
	case err := <-errCh:
		t.Fatalf("after finish: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for held recv to unblock")
	}
	got = append(got, tcrCollect(t, stream)...)
	tools := tcrToolLifecycle(got)
	if len(tools) != 3 {
		t.Fatalf("tool lifecycle len=%d want 3: %#v", len(tools), tools)
	}
	if tools[0].Kind != aipapi.EventToolCallStarted || tools[0].ToolCallID != "c1" || tools[0].MessageIndex != 0 {
		t.Fatalf("started %#v", tools[0])
	}
	if tools[1].Kind != aipapi.EventToolCallArgsDelta || tools[1].Delta != `{"location":"NYC"}` || tools[1].MessageIndex != 0 {
		t.Fatalf("delta %#v", tools[1])
	}
	if tools[2].Kind != aipapi.EventToolCallFinished || tools[2].ToolCallID != "c1" {
		t.Fatalf("finished %#v", tools[2])
	}
}

func TestRetryRecvStream_ToolCallFinalizationParallelInterleave(t *testing.T) {
	t.Parallel()
	backend := newPushManagedStream()
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-parallel"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()

	backend.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "a", ToolName: "get_weather", MessageIndex: 1})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "b", ToolName: "get_weather", MessageIndex: 2})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: `{"location":"A"}`, MessageIndex: 1})
	backend.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "thinking"})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "b", Delta: `{"location":"B"}`, MessageIndex: 2})

	sawText := false
	for range 3 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		switch ev.Kind {
		case aipapi.EventTextDelta:
			if ev.Delta == "thinking" {
				sawText = true
			}
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			t.Fatalf("tool lifecycle must stay held during interleave; saw %s", ev.Kind)
		}
	}
	if !sawText {
		t.Fatal("non-tool text must pass through while tool calls are held")
	}

	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "b", ToolName: "get_weather", MessageIndex: 2})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "a", ToolName: "get_weather", MessageIndex: 1})
	backend.Push(aipapi.Event{Kind: aipapi.EventResponseFinished})
	backend.ClosePush()
	got := tcrCollect(t, stream)
	tools := tcrToolLifecycle(got)
	want := []struct {
		kind  aipapi.EventKind
		id    string
		delta string
		idx   int
	}{
		{aipapi.EventToolCallStarted, "b", "", 2},
		{aipapi.EventToolCallArgsDelta, "b", `{"location":"B"}`, 2},
		{aipapi.EventToolCallFinished, "b", "", 2},
		{aipapi.EventToolCallStarted, "a", "", 1},
		{aipapi.EventToolCallArgsDelta, "a", `{"location":"A"}`, 1},
		{aipapi.EventToolCallFinished, "a", "", 1},
	}
	if len(tools) != len(want) {
		t.Fatalf("tools=%d want %d: %#v", len(tools), len(want), tools)
	}
	for i, w := range want {
		if tools[i].Kind != w.kind || tools[i].ToolCallID != w.id || tools[i].Delta != w.delta || tools[i].MessageIndex != w.idx {
			t.Fatalf("tools[%d]=%#v want kind=%s id=%s delta=%q idx=%d", i, tools[i], w.kind, w.id, w.delta, w.idx)
		}
	}
}

func TestRetryRecvStream_ToolCallFinalizationOverflowPassThrough(t *testing.T) {
	t.Parallel()
	oversized := `{"location":"NYC-EXTRA-LONG"}`
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: oversized},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrRewriteFinalizer{args: []byte(`{}`)}}, 8)

	stream, err := ex.Execute(principalCtx("user-tcr-overflow"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	_ = stream.Close()

	var args strings.Builder
	for _, ev := range got {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if args.String() != oversized {
		t.Fatalf("overflow must exact-replay originals, got %q", args.String())
	}
}

func TestRetryRecvStream_ToolCallFinalizationRewriteLifecycle(t *testing.T) {
	t.Parallel()
	repaired := []byte(`{"location":"NYC"}`)
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather", MessageIndex: 7},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"NYC"`, MessageIndex: 7},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather", MessageIndex: 7},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrRewriteFinalizer{args: repaired}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-rewrite"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	_ = stream.Close()

	tools := tcrToolLifecycle(got)
	if len(tools) != 3 {
		t.Fatalf("rewrite lifecycle len=%d want 3: %#v", len(tools), tools)
	}
	if tools[0].Kind != aipapi.EventToolCallStarted || tools[0].ToolCallID != "c1" || tools[0].ToolName != "get_weather" || tools[0].MessageIndex != 7 {
		t.Fatalf("started %#v", tools[0])
	}
	if tools[1].Kind != aipapi.EventToolCallArgsDelta || tools[1].ToolCallID != "c1" || tools[1].ToolName != "get_weather" || tools[1].Delta != string(repaired) || tools[1].MessageIndex != 7 {
		t.Fatalf("delta %#v", tools[1])
	}
	if tools[2].Kind != aipapi.EventToolCallFinished || tools[2].ToolCallID != "c1" || tools[2].ToolName != "get_weather" || tools[2].MessageIndex != 7 {
		t.Fatalf("finished %#v", tools[2])
	}
}

func TestRetryRecvStream_ToolCallFinalizationCancelCleanup(t *testing.T) {
	t.Parallel()
	backend := newPushManagedStream()
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-cancel"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	backend.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"})
	for range 2 {
		if _, err := stream.Recv(context.Background()); err != nil {
			t.Fatalf("drain non-tool: %v", err)
		}
	}

	recvCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, rerr := stream.Recv(recvCtx)
		errCh <- rerr
	}()
	select {
	case rerr := <-errCh:
		t.Fatalf("recv returned before cancel: %v", rerr)
	case <-time.After(75 * time.Millisecond):
	}
	cancel()
	rerr := <-errCh
	if !errors.Is(rerr, context.Canceled) {
		t.Fatalf("recv err=%v want context.Canceled", rerr)
	}
	_ = stream.Close()
}

func TestRetryRecvStream_ToolCallFinalizationEOFCleanup(t *testing.T) {
	t.Parallel()
	backend := newPushManagedStream()
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-eof"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	backend.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	backend.Push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"})
	backend.ClosePush()
	got := tcrCollect(t, stream)
	if tools := tcrToolLifecycle(got); len(tools) != 0 {
		t.Fatalf("incomplete tool lifecycle must not reach client on EOF: %#v", tools)
	}
	_ = stream.Close()
}

func TestRetryRecvStream_ToolCallFinalizationBLegReplaceClearsState(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	leg1 := &failAfterStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "leg1", ToolName: "get_weather"},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "leg1", Delta: `{"location":"LEG1-SECRET"}`},
		},
		fail: aipapi.RecoverablePreOutputError(errors.New("pre-output failure after buffered tools")),
	}
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return leg1, nil
			},
		},
		"openai2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventToolCallStarted, ToolCallID: "c2", ToolName: "get_weather"},
					{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c2", Delta: `{"location":"X"}`},
					{Kind: aipapi.EventToolCallFinished, ToolCallID: "c2", ToolName: "get_weather"},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	call := tcrWeatherCall()
	call.Route.Selector = "openai:gpt-4|openai2:gpt-4"
	stream, err := ex.Execute(principalCtx("user-tcr-bleg"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	_ = stream.Close()
	if opens.Load() < 2 {
		t.Fatalf("expected failover opens>=2 got %d", opens.Load())
	}
	for _, ev := range got {
		if strings.Contains(ev.Delta, "LEG1-SECRET") || ev.ToolCallID == "leg1" {
			t.Fatalf("leg1 buffered tools must not reach client: %#v", ev)
		}
	}
	if tools := tcrToolLifecycle(got); len(tools) != 3 || tools[0].ToolCallID != "c2" {
		t.Fatalf("replacement leg tools=%#v", tools)
	}
}

func TestRetryRecvStream_ToolCallFinalizationNoToolsBypass(t *testing.T) {
	t.Parallel()
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrPassFinalizer{}}, 64*1024)

	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-tcr-notools"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	_ = stream.Close()
	var text strings.Builder
	for _, ev := range got {
		if ev.Kind == aipapi.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "hi" {
		t.Fatalf("no-tools path must pass text, got %q", text.String())
	}
}

func TestRetryRecvStream_ToolCallFinalizationOrderingWithPolicy(t *testing.T) {
	t.Parallel()
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"NYC"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
			ToolCallPolicies:  []toolpolicy.Policy{pdDenyToolPolicy{name: "blocked"}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{
		tcrRewriteFinalizer{name: "blocked", args: []byte(`{}`)},
	}, 64*1024)

	call := pdBaseCall("openai:gpt-4")
	call.Tools = []aipapi.ToolDef{
		{Name: "get_weather", Parameters: []byte(`{"type":"object"}`)},
		{Name: "blocked", Parameters: []byte(`{}`)},
	}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	stream, err := ex.Execute(principalCtx("user-tcr-policy"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	if sawErr == nil {
		t.Fatal("policy must deny rewritten emitted tool name")
	}
}

func TestRetryRecvStream_ToolCallFinalizationRejectTyped(t *testing.T) {
	t.Parallel()
	raw := `{"location":"secret-payload"}`
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: raw},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrRejectFinalizer{}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-reject"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	var re *toolcall.RejectError
	if !errors.As(sawErr, &re) {
		t.Fatalf("want RejectError, got %T %v", sawErr, sawErr)
	}
	if re.ReasonCode != toolcall.ReasonUnrepairable || re.ToolCallID != "c1" {
		t.Fatalf("reject %#v", re)
	}
	if strings.Contains(sawErr.Error(), "secret-payload") || strings.Contains(sawErr.Error(), raw) {
		t.Fatalf("reject error must not include raw args: %v", sawErr)
	}
}

func TestRetryRecvStream_ToolCallFinalizationFailOpenPanicAndError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		fin  toolcall.Finalizer
	}{
		{"panic", tcrPanicFinalizer{}},
		{"error", tcrErrFinalizer{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"NYC"}`},
				{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
				{Kind: aipapi.EventResponseFinished},
			})
			var opens atomic.Int32
			ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
				"openai": recordingBackend("openai", &opens, backendStream),
			}, extensions.SnapshotOptions{
				FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
					RequestTransforms: []request.Transform{pdNoopRtx{}},
				}),
			})
			ex.SetToolCallFinalizers([]toolcall.Finalizer{tc.fin}, 64*1024)
			stream, err := ex.Execute(principalCtx("user-tcr-failopen-"+tc.name), tcrWeatherCall())
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := tcrCollect(t, stream)
			_ = stream.Close()
			tools := tcrToolLifecycle(got)
			if len(tools) != 3 || tools[1].Delta != `{"location":"NYC"}` {
				t.Fatalf("fail-open exact replay got %#v", tools)
			}
		})
	}
}

func TestRetryRecvStream_ToolCallFinalizationMultiFinalizerOrder(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []string
	fins := []toolcall.Finalizer{
		tcrOrderProbeFinalizer{id: "z", ord: 10, mu: &mu, order: &order, args: []byte(`{"location":"Z"}`)},
		tcrOrderProbeFinalizer{id: "b", ord: 0, mu: &mu, order: &order, args: []byte(`{"location":"B"}`)},
		tcrOrderProbeFinalizer{id: "a", ord: 0, mu: &mu, order: &order},
	}
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"RAW"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers(fins, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-order"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := tcrCollect(t, stream)
	_ = stream.Close()

	wantOrder := []string{
		`a:{"location":"RAW"}`,
		`b:{"location":"RAW"}`,
		`z:{"location":"B"}`,
	}
	if len(order) != len(wantOrder) {
		t.Fatalf("order=%v want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order=%v want %v", order, wantOrder)
		}
	}
	tools := tcrToolLifecycle(got)
	if len(tools) != 3 || tools[1].Delta != `{"location":"Z"}` {
		t.Fatalf("final rewrite %#v", tools)
	}
}

func TestRetryRecvStream_ToolCallFinalizationBTPvsPTC(t *testing.T) {
	t.Parallel()
	capObs := &tcrTrafficCapture{}
	repaired := []byte(`{"location":"FIXED"}`)
	raw := `{"location":"RAW`
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: raw},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}, extensions.SnapshotOptions{
		TrafficObserver: capObs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	ex.SetToolCallFinalizers([]toolcall.Finalizer{tcrRewriteFinalizer{args: repaired}}, 64*1024)

	stream, err := ex.Execute(principalCtx("user-tcr-traffic"), tcrWeatherCall())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_ = tcrCollect(t, stream)
	_ = stream.Close()

	capObs.mu.Lock()
	defer capObs.mu.Unlock()
	var btpArgs, ptcArgs string
	for _, ev := range capObs.btp {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			btpArgs += ev.Delta
		}
	}
	for _, ev := range capObs.ptc {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			ptcArgs += ev.Delta
		}
	}
	if btpArgs != raw {
		t.Fatalf("BTP must keep raw args %q got %q", raw, btpArgs)
	}
	if ptcArgs != string(repaired) {
		t.Fatalf("PTC must emit repaired args %q got %q", repaired, ptcArgs)
	}
}
