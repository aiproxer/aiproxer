package hooks_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// reqPartStub supports fail-open / fail-closed for panic policy tests.
type reqPartStub struct {
	id           string
	order        int
	explicitMode bool
	mode         sdk.FailureMode
	fn           func(context.Context, *aipapi.Call, sdk.PartMeta) error
}

func (s *reqPartStub) ID() string { return s.id }
func (s *reqPartStub) Order() int { return s.order }
func (s *reqPartStub) FailureMode() sdk.FailureMode {
	if s.explicitMode {
		return s.mode
	}
	return sdk.FailClosed
}

func (s *reqPartStub) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

func TestRunSubmit_failOpen_panicSecondHookStillRuns(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	first := &stubSubmit{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailOpen,
		handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			panic("boom")
		},
	}
	second := &stubSubmit{
		id: "after", order: 2,
		fn: func() { ranSecond = true },
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{first, second}})
	if err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if !ranSecond {
		t.Fatal("expected second hook after fail-open panic")
	}
}

func TestRunSubmit_failOpen_panic_emitsSingleErrorLog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := corehooks.WithDiagnosticsLogger(context.Background(), log)
	first := &stubSubmit{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailOpen,
		handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			panic("boom")
		},
	}
	second := &stubSubmit{id: "after", order: 2, fn: func() {}}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{first, second}})
	if err := bus.RunSubmit(ctx, testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	const wantSub = "hooks: isolated panic in fail-open hook"
	if !strings.Contains(s, wantSub) {
		t.Fatalf("missing fail-open panic log, got %q", s)
	}
	if strings.Count(s, wantSub) != 1 {
		t.Fatalf("want exactly one panic log, got %q", s)
	}
}

func TestRunSubmit_failClosed_panicSurfacesAsPanicError(t *testing.T) {
	t.Parallel()
	first := &stubSubmit{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailClosed,
		handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			panic("boom")
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{first}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); !ok {
		t.Fatalf("expected *safety.PanicError in chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected hook id in message: %v", err)
	}
}

func TestRunSubmit_failClosed_validationError_isNotPanicError(t *testing.T) {
	t.Parallel()
	h := &stubSubmit{
		id: "plain", order: 1, explicitMode: true, mode: sdk.FailClosed,
		handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{}, errors.New("hook rejected input")
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{h}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); ok {
		t.Fatalf("ordinary hook error must not surface as *safety.PanicError, got %v", err)
	}
	if !strings.Contains(err.Error(), "hook rejected input") {
		t.Fatalf("expected wrapped validation error text, got %v", err)
	}
}

func TestRunSubmit_failOpen_panicThenValidateStillRuns(t *testing.T) {
	t.Parallel()
	// Inflate is invalid only if second hook does not run / validate: first hook fail-open panics;
	// call should still be validated at end and fail if invalid.
	h := &stubSubmit{
		id: "p", order: 0, explicitMode: true, mode: sdk.FailOpen,
		handle: func(_ context.Context, call *aipapi.Call, _ *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			call.Messages[0].Parts[0].Text = strings.Repeat("x", aipapi.MaxPartTextBytes+1)
			panic("mutate then panic")
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{h}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil {
		t.Fatal("expected validation error after fail-open panic")
	}
	if !strings.Contains(err.Error(), "submit hooks") {
		t.Fatalf("expected final validate path: %v", err)
	}
}

func TestRunRequestPartHooks_failOpen_panicSecondHookRuns(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	first := &reqPartStub{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailOpen,
		fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error {
			panic("boom")
		},
	}
	second := &reqPartStub{
		id: "after", order: 2,
		fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error {
			ranSecond = true
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{first, second}})
	if err := b.RunRequestPartHooks(context.Background(), testCall(), sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if !ranSecond {
		t.Fatal("expected second request-part hook after fail-open panic")
	}
}

func TestRunRequestPartHooks_failClosed_panicSurfacesAsPanicError(t *testing.T) {
	t.Parallel()
	first := &reqPartStub{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailClosed,
		fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error {
			panic("boom")
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{first}})
	err := b.RunRequestPartHooks(context.Background(), testCall(), sdk.PartMeta{})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); !ok {
		t.Fatalf("expected *safety.PanicError, got %v", err)
	}
}

func TestRunRequestPartHooks_orderStableAfterFailOpenPanic(t *testing.T) {
	t.Parallel()
	var order []string
	first := &reqPartStub{
		id: "a", order: 1, explicitMode: true, mode: sdk.FailOpen,
		fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error {
			order = append(order, "a")
			panic("x")
		},
	}
	second := &reqPartStub{
		id: "b", order: 2,
		fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error {
			order = append(order, "b")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{first, second}})
	if err := b.RunRequestPartHooks(context.Background(), testCall(), sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("order: got %v", order)
	}
}

// respPartStub supports fail-open / fail-closed for response panic tests.
type respPartStub struct {
	id           string
	order        int
	explicitMode bool
	mode         sdk.FailureMode
	fn           func(context.Context, *aipapi.Event, sdk.PartMeta) error
}

func (s *respPartStub) ID() string { return s.id }
func (s *respPartStub) Order() int { return s.order }
func (s *respPartStub) FailureMode() sdk.FailureMode {
	if s.explicitMode {
		return s.mode
	}
	return sdk.FailClosed
}

func (s *respPartStub) HandleEvent(ctx context.Context, ev *aipapi.Event, meta sdk.PartMeta) error {
	return s.fn(ctx, ev, meta)
}

func TestRunResponsePartHooks_failOpen_panicSecondHookRuns(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	first := &respPartStub{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailOpen,
		fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error {
			panic("boom")
		},
	}
	second := &respPartStub{
		id: "after", order: 2,
		fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error {
			ranSecond = true
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{first, second}})
	if err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if !ranSecond {
		t.Fatal("expected second response-part hook after fail-open panic")
	}
}

func TestRunResponsePartHooks_failClosed_panicSurfacesAsPanicError(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	first := &respPartStub{
		id: "panic", order: 1, explicitMode: true, mode: sdk.FailClosed,
		fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error {
			panic("boom")
		},
	}
	b := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{first}})
	err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); !ok {
		t.Fatalf("expected *safety.PanicError, got %v", err)
	}
}

func TestRunResponsePartHooks_noOrderingChangeAfterFailOpenPanic(t *testing.T) {
	t.Parallel()
	var order []string
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	first := &respPartStub{
		id: "a", order: 1, explicitMode: true, mode: sdk.FailOpen,
		fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error {
			order = append(order, "a")
			panic("x")
		},
	}
	second := &respPartStub{
		id: "b", order: 2,
		fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error {
			order = append(order, "b")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{first, second}})
	if err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("order: got %v", order)
	}
}

func TestApplyToolReactors_panic_failClosed(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			panic("boom")
		},
	}
	b := corehooks.New(corehooks.Config{
		ToolReactors:           []sdk.ToolReactor{bad},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsFailClosed,
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Emit {
		t.Fatal("expected no emit")
	}
	if out.Err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*safety.PanicError](out.Err); !ok {
		t.Fatalf("expected *safety.PanicError, got %v", out.Err)
	}
}

func TestApplyToolReactors_panic_swallowEvent(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			panic("boom")
		},
	}
	b := corehooks.New(corehooks.Config{
		ToolReactors:           []sdk.ToolReactor{bad},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsSwallowEvent,
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Err != nil || out.Emit {
		t.Fatalf("expected swallow like error path, got %#v", out)
	}
}

func TestApplyToolReactors_panic_failOpenSecondReactorRuns(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "a"}
	first := &stubTool{
		id: "panic", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			panic("boom")
		},
	}
	second := &stubTool{
		id: "b", order: 2,
		fn: func(_ context.Context, cur aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: cur.ArgsDelta + "2"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{first, second}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "a2" {
		t.Fatalf("expected second reactor after fail-open panic, got %#v", out)
	}
}
