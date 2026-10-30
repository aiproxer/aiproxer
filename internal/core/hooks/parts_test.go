package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestRunRequestPartHooks_zeroHooks(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	call := testCall()
	if err := b.RunRequestPartHooks(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRequestPartHooks_nilCall_isErrInvalidCall(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunRequestPartHooks(context.Background(), nil, sdk.PartMeta{})
	if !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatalf("expected ErrInvalidCall, got %v", err)
	}
}

func TestRunRequestPartHooks_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunRequestPartHooks(nil, testCall(), sdk.PartMeta{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestRunResponsePartHooks_nilEvent_isErrInvalidCall(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunResponsePartHooks(context.Background(), nil, sdk.PartMeta{})
	if !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatalf("expected ErrInvalidCall, got %v", err)
	}
}

func TestRunResponsePartHooks_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	err := b.RunResponsePartHooks(nil, ev, sdk.PartMeta{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestRunRequestPartHooks_validMutation(t *testing.T) {
	t.Parallel()
	h := &stubReqPart{
		id: "m", order: 0,
		fn: func(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
			call.Messages[0].Parts[0] = aipapi.TextPart("hello")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{h}})
	call := testCall()
	if err := b.RunRequestPartHooks(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("expected rewritten text, got %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRunRequestPartHooks_invalidMutationTypedError(t *testing.T) {
	t.Parallel()
	h := &stubReqPart{
		id: "bad", order: 0,
		fn: func(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
			call.Messages[0].Parts[0] = aipapi.Part{Kind: aipapi.PartText, Text: ""}
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{h}})
	err := b.RunRequestPartHooks(context.Background(), testCall(), sdk.PartMeta{})
	if !aipapi.IsHookMutation(err) {
		t.Fatalf("expected hook mutation error, got %v", err)
	}
}

func TestRunResponsePartHooks_zeroHooks(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	if err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunResponsePartHooks_invalidEventShape(t *testing.T) {
	t.Parallel()
	h := &stubRespPart{
		id: "bad", order: 0,
		fn: func(_ context.Context, ev *aipapi.Event, _ sdk.PartMeta) error {
			ev.Kind = aipapi.EventKind("unknown")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{h}})
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{})
	if !aipapi.IsHookMutation(err) {
		t.Fatalf("expected hook mutation error, got %v", err)
	}
}

type stubReqPart struct {
	id    string
	order int
	fn    func(context.Context, *aipapi.Call, sdk.PartMeta) error
}

func (s *stubReqPart) ID() string                   { return s.id }
func (s *stubReqPart) Order() int                   { return s.order }
func (s *stubReqPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *stubReqPart) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

type stubRespPart struct {
	id    string
	order int
	fn    func(context.Context, *aipapi.Event, sdk.PartMeta) error
}

func (s *stubRespPart) ID() string                   { return s.id }
func (s *stubRespPart) Order() int                   { return s.order }
func (s *stubRespPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *stubRespPart) HandleEvent(ctx context.Context, ev *aipapi.Event, meta sdk.PartMeta) error {
	return s.fn(ctx, ev, meta)
}
