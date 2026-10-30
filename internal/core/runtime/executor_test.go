package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestExecutor_happyPath_collectNonStreaming(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				_ = call
				_ = ctx
				_ = cand
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens: %d", opens)
	}
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestExecutor_capabilityRejectBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"nope": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "nope:g"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsReject(err) {
		t.Fatalf("expected capability reject, got %T %v", err, err)
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend should not open, opens=%d", opens)
	}
}

func TestExecutor_preOutputRecoverableSwallowsAndLineage(t *testing.T) {
	t.Parallel()
	clock := time.Unix(1700, 0).UTC()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Now = func() time.Time { return clock }
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "sess-7.2"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].BLegID == "" || atts[1].BLegID == "" || atts[0].BLegID == atts[1].BLegID {
		t.Fatalf("expected distinct B-leg ids, got %#v and %#v", atts[0].BLegID, atts[1].BLegID)
	}
	if atts[0].Seq <= 0 || atts[1].Seq <= 0 || atts[0].Seq >= atts[1].Seq {
		t.Fatalf("expected monotonic increasing B-leg seq, got seq %d then %d", atts[0].Seq, atts[1].Seq)
	}
	if atts[0].Outcome != aipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

func TestExecutor_preOutputMultiOpenFailuresThenSuccess(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"bad2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "done"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "bad:m|bad2:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 3 {
		t.Fatalf("expected 3 backend opens, got %d", opens)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_postOutputNoSecondBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"one": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return &deltaThenErrStream{n: 0}, nil
			},
		},
		"two": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "one:m|two:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err == nil {
		t.Fatal("expected error after committed output")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("expected no failover backend open, opens=%d", opens)
	}
	_ = stream.Close()
}

type deltaThenErrStream struct{ n int }

func (d *deltaThenErrStream) Recv(context.Context) (aipapi.Event, error) {
	d.n++
	if d.n == 1 {
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}
	if d.n == 2 {
		return aipapi.Event{Kind: aipapi.EventMessageStarted}, nil
	}
	if d.n == 3 {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
	}
	return aipapi.Event{}, aipapi.RecoverablePreOutputError(errors.New("late"))
}

func (d *deltaThenErrStream) Close() error { return nil }

func (d *deltaThenErrStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_cancellationRecordsAttempt(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"slow": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return &cancelWaitStream{ctx: ctx}, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "cancel-sess"},
		Route:   aipapi.RouteIntent{Selector: "slow:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	_ = stream.Close()
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens: %d", opens)
	}
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Outcome != aipapi.AttemptCancelled {
		t.Fatalf("attempts: %#v", atts)
	}
}

type cancelWaitStream struct {
	ctx context.Context
}

func (c *cancelWaitStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-c.ctx.Done():
		return aipapi.Event{}, c.ctx.Err()
	}
}

func (c *cancelWaitStream) Close() error { return nil }

func (c *cancelWaitStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecutor_applyNegotiatedDowngradesReasoning(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var seenReasoning string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				seenReasoning = call.Options.ReasoningEffort
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if seenReasoning != "" {
		t.Fatalf("expected downgrade clearing reasoning, got %q", seenReasoning)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_backendOpen_contextCarriesTraceAndALeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openTrace, openALeg string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openTrace = diag.TraceID(ctx)
				openALeg = diag.ALegID(ctx)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "x"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if openTrace != diag.StableCallID(call) {
		t.Fatalf("trace = %q, want %q", openTrace, diag.StableCallID(call))
	}
	if openALeg == "" {
		t.Fatal("expected non-empty a_leg_id in backend Open context")
	}
	_, _ = aipapi.Collect(context.Background(), stream)
}

func TestExecutor_traceUsesCallIDWhenPresent(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openTrace string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openTrace = diag.TraceID(ctx)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		ID:    "client-req-42",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if openTrace != "client-req-42" {
		t.Fatalf("trace = %q, want client-req-42", openTrace)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
}

func TestExecutor_decisionLog_backendOpened(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Log = log
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	dec := buf.Bytes()
	if !bytes.Contains(dec, []byte(`"msg":"backend_attempt_opened"`)) {
		t.Fatalf("log missing backend_attempt_opened: %s", string(dec))
	}
	var found bool
	for line := range bytes.SplitSeq(dec, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["msg"] == "backend_attempt_opened" && m["trace_id"] != nil && m["a_leg_id"] != nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no structured backend_attempt_opened with trace: %s", string(dec))
	}
}

func TestExecutor_routeQueryMergesIntoGenerationOptions(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var captured aipapi.GenerationOptions
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				captured = call.Options
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4?temperature=0.2&reasoning_effort=high"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.2 {
		t.Fatalf("temperature from route: %#v", captured.Temperature)
	}
	if captured.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort from route: %q", captured.ReasoningEffort)
	}
}

func TestExecutor_routeQueryOverridesExplicitCallOptions(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var captured aipapi.GenerationOptions
	temp := 0.11
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				captured = call.Options
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4?temperature=0.99"},
		Options: aipapi.GenerationOptions{
			Temperature: &temp,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.99 {
		t.Fatalf("route temperature must override explicit call option, got %#v", captured.Temperature)
	}
}

func TestExecutor_callID_matchesAssignedTrace(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	wantTrace := diag.StableCallID(call)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if call.ID != wantTrace {
		t.Fatalf("call.ID = %q, want %q", call.ID, wantTrace)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
}

func TestExecutor_requestPartHook_metaIncludesBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got sdk.PartMeta
	reqHook := &executorTestReqPart{
		id: "req-meta", order: 0,
		fn: func(_ context.Context, _ *aipapi.Call, meta sdk.PartMeta) error {
			got = meta
			return nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{reqHook},
	})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	if _, err := ex.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if got.TraceID == "" || got.ALegID == "" {
		t.Fatalf("expected non-empty TraceID and ALegID, got %+v", got)
	}
	if got.BLegID == "" || got.AttemptSeq <= 0 {
		t.Fatalf("request hook after NextBLeg: want BLegID and positive AttemptSeq, got %+v", got)
	}
	if got.BackendID != "openai" {
		t.Fatalf("request hook BackendID = %q, want openai", got.BackendID)
	}
}

func TestExecutor_responsePartHook_and_toolReactor_metaOnRecv(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var respMeta sdk.PartMeta
	var toolMeta sdk.ToolMeta
	respHook := &executorTestRespPart{
		id: "resp-meta", order: 0,
		fn: func(_ context.Context, _ *aipapi.Event, meta sdk.PartMeta) error {
			respMeta = meta
			return nil
		},
	}
	toolHook := &executorTestToolReactor{
		id: "tool-meta", order: 0,
		fn: func(_ context.Context, _ aipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			toolMeta = meta
			return sdk.ToolPass, aipapi.ToolEvent{}, nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{
		ResponsePartHooks: []sdk.ResponsePartHook{respHook},
		ToolReactors:      []sdk.ToolReactor{toolHook},
	})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "fn"},
					{Kind: aipapi.EventTextDelta, Delta: "x"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for {
		_, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
	}
	if respMeta.TraceID == "" || respMeta.ALegID == "" {
		t.Fatalf("response hook: want non-empty TraceID and ALegID, got %+v", respMeta)
	}
	if respMeta.BLegID == "" || respMeta.AttemptSeq <= 0 {
		t.Fatalf("response hook: want non-empty BLegID and positive AttemptSeq on recv path, got %+v", respMeta)
	}
	if respMeta.BackendID != "openai" {
		t.Fatalf("response hook: BackendID = %q, want openai", respMeta.BackendID)
	}
	if toolMeta.TraceID == "" || toolMeta.ALegID == "" {
		t.Fatalf("tool reactor: want non-empty TraceID and ALegID, got %+v", toolMeta)
	}
	if toolMeta.BLegID == "" || toolMeta.AttemptSeq <= 0 {
		t.Fatalf("tool reactor: want non-empty BLegID and positive AttemptSeq, got %+v", toolMeta)
	}
	_ = stream.Close()
}

func TestExecutor_downgradeNotStickyAcrossRetries(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var captured string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				captured = call.Options.ReasoningEffort
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "sess-downgrade"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if captured != "high" {
		t.Fatalf("second backend must see baseline reasoning effort, got %q", captured)
	}
}

func TestExecutor_maxAttemptsBlocksFurtherBLegs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.MaxAttempts = 2
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"c": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "sess-max"},
		Route:   aipapi.RouteIntent{Selector: "a:m|b:m|c:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected max attempts error")
	}
	if !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
		t.Fatalf("expected ErrMaxRouteAttempts, got %v", err)
	}
}

func TestExecutor_modelOnlySelectorUsesDefaultBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.DefaultBackend = "openai"
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = cand.Primary.Backend + ":" + cand.Primary.Model
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "openai:gpt-4o-mini" {
		t.Fatalf("opened candidate: got %q", opened)
	}
	_, _ = aipapi.Collect(context.Background(), s)
}

func TestExecutor_execute_nilContext(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"x": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(nil), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(nil, call) //nolint:staticcheck // deliberate nil ctx; expect aipapi.ErrNilContext
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestExecutor_Execute_nilHookBus(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = nil
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"x": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(nil), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "x:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil hook bus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type executorTestReqPart struct {
	id    string
	order int
	fn    func(context.Context, *aipapi.Call, sdk.PartMeta) error
}

func (s *executorTestReqPart) ID() string                   { return s.id }
func (s *executorTestReqPart) Order() int                   { return s.order }
func (s *executorTestReqPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *executorTestReqPart) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

type executorTestRespPart struct {
	id    string
	order int
	fn    func(context.Context, *aipapi.Event, sdk.PartMeta) error
}

func (s *executorTestRespPart) ID() string                   { return s.id }
func (s *executorTestRespPart) Order() int                   { return s.order }
func (s *executorTestRespPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *executorTestRespPart) HandleEvent(ctx context.Context, ev *aipapi.Event, meta sdk.PartMeta) error {
	return s.fn(ctx, ev, meta)
}

type executorTestToolReactor struct {
	id    string
	order int
	fn    func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error)
}

func (s *executorTestToolReactor) ID() string                   { return s.id }
func (s *executorTestToolReactor) Order() int                   { return s.order }
func (s *executorTestToolReactor) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (s *executorTestToolReactor) HandleToolEvent(ctx context.Context, te aipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
	return s.fn(ctx, te, meta)
}
