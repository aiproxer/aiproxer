package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestApplyToolReactors_failClosed_returnsErr(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{}, errors.New("boom")
		},
	}
	b := corehooks.New(corehooks.Config{
		ToolReactors:           []sdk.ToolReactor{bad},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsFailClosed,
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Err == nil {
		t.Fatal("expected error")
	}
	if out.Emit {
		t.Fatal("expected no emit path when fail-closed")
	}
}

func TestApplyToolReactors_errorSwallowEvent(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{}, errors.New("boom")
		},
	}
	b := corehooks.New(corehooks.Config{
		ToolReactors:           []sdk.ToolReactor{bad},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsSwallowEvent,
	})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Err != nil || out.Emit {
		t.Fatalf("expected swallow without err, got %#v", out)
	}
}
