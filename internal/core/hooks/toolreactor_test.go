package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestApplyToolReactors_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	out := b.ApplyToolReactors(nil, te, sdk.ToolMeta{}) //nolint:staticcheck // deliberate nil ctx
	if out.Emit || out.Err == nil || !errors.Is(out.Err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got emit=%v err=%v", out.Emit, out.Err)
	}
}

func TestApplyToolReactors_passThroughChain(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "a"}
	r := &stubTool{id: "t1", order: 0, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
		return sdk.ToolPass, aipapi.ToolEvent{}, nil
	}}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event != te {
		t.Fatalf("expected unchanged pass-through, got %#v", out)
	}
}

func TestApplyToolReactors_rewrite(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "a"}
	rewritten := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "b"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, rewritten, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "b" {
		t.Fatalf("expected rewrite, got %#v", out)
	}
}

func TestApplyToolReactors_replace(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "n1"}
	replacement := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c2", ToolName: "n2"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolReplace, replacement, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ToolCallID != "c2" {
		t.Fatalf("expected replace, got %#v", out)
	}
}

func TestApplyToolReactors_swallow(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolSwallow, aipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Emit {
		t.Fatalf("expected swallow (no emit), got %#v", out)
	}
}

func TestApplyToolReactors_failOpenOnError(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "orig"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{}, errors.New("boom")
		},
	}
	good := &stubTool{
		id: "good", order: 2,
		fn: func(_ context.Context, cur aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			if cur.ArgsDelta != "orig" {
				t.Fatalf("expected fail-open to preserve current event, got %#v", cur)
			}
			return sdk.ToolPass, aipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{bad, good}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "orig" {
		t.Fatalf("expected pass-through after error, got %#v", out)
	}
}

func TestApplyToolReactors_chainedRewrite(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "0"}
	first := &stubTool{
		id: "a", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "1"}, nil
		},
	}
	second := &stubTool{
		id: "b", order: 2,
		fn: func(_ context.Context, cur aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: cur.ArgsDelta + "2"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{second, first}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "12" {
		t.Fatalf("expected chained rewrite, got %#v", out)
	}
}

func TestApplyToolReactors_rewrite_invalidCanonicalFailsClosed(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "orig"}
	badRewrite := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "", ArgsDelta: "oops"}
	r := &stubTool{
		id: "bad",
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, badRewrite, nil
		},
	}
	b := corehooks.New(corehooks.Config{
		ToolReactors:           []sdk.ToolReactor{r},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsFailClosed,
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Emit || out.Err == nil {
		t.Fatalf("want validation error, got %#v", out)
	}
	if _, ok := errors.AsType[*aipapi.HookMutationError](out.Err); !ok {
		t.Fatalf("want HookMutationError wrapped, got %v", out.Err)
	}
}

func TestApplyToolReactors_rewrite_invalidCanonicalFailOpenPreservesCurrentEvent(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "orig"}
	invalid := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "", ArgsDelta: "oops"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, invalid, nil
		},
	}
	good := &stubTool{
		id: "good", order: 2,
		fn: func(_ context.Context, cur aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			if cur.ArgsDelta != "orig" || cur.ToolCallID != "c1" {
				t.Fatalf("expected original event after invalid rewrite dropped, got %#v", cur)
			}
			return sdk.ToolPass, aipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{bad, good}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "orig" {
		t.Fatalf("expected unchanged emit, got %#v", out)
	}
}

type stubTool struct {
	id    string
	order int
	fn    func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error)
}

func (s *stubTool) ID() string { return s.id }
func (s *stubTool) Order() int { return s.order }
func (s *stubTool) HandleToolEvent(ctx context.Context, te aipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
	return s.fn(ctx, te, meta)
}
