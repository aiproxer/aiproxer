package interleavedthinking

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func baseCall() aipapi.Call {
	return aipapi.Call{
		Instructions: []aipapi.Message{
			{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("Base instructions")},
			},
		},
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("Hello")},
			},
		},
		Tools: []aipapi.ToolDef{
			{
				Name:        "search",
				Description: "search tool",
			},
		},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
}

func TestShapeThinkerCall(t *testing.T) {
	t.Parallel()
	call := baseCall()
	shaped, err := ShapeThinkerCall(call, "Thinker instructions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(shaped.Instructions) != 2 {
		t.Fatalf("expected 2 instruction messages, got %d", len(shaped.Instructions))
	}
	if shaped.Instructions[0].Parts[0].Text != "Thinker instructions" {
		t.Fatalf("expected thinker instructions first, got %q", shaped.Instructions[0].Parts[0].Text)
	}
	if len(shaped.Tools) != 0 {
		t.Fatalf("expected tools to be stripped, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != "" {
		t.Fatalf("expected tool choice to be empty, got %+v", shaped.ToolChoice)
	}
}

func TestShapeThinkerCall_MissingInstructions(t *testing.T) {
	t.Parallel()
	call := baseCall()
	_, err := ShapeThinkerCall(call, "")
	if !errors.Is(err, ErrThinkerInstructionsMissing) {
		t.Fatalf("expected ErrThinkerInstructionsMissing, got %v", err)
	}
}

func TestShapeExecutorCall_Outcomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := baseCall()
	store := NewMemoStore(4096)
	scope := Scope("session-1")

	// 1. Missing ref
	_, injected, update, outcome, err := ShapeExecutorCall(ctx, call, store, scope, nil, false)
	if err != nil || injected || update != nil || outcome != MemoOutcomeSkippedMissing {
		t.Fatalf("expected skipped missing, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}

	// 2. Missing from store
	missingRef := state.MemoRef{Key: "nonexistent", Version: 1}
	_, injected, update, outcome, err = ShapeExecutorCall(ctx, call, store, scope, &missingRef, false)
	if err != nil || injected || update != nil || outcome != MemoOutcomeSkippedMissing {
		t.Fatalf("expected skipped missing, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}

	// 3. Skipped empty memo
	emptyRef, err := store.Put(ctx, scope, state.MemoState{Memo: ""})
	if err != nil {
		t.Fatalf("put empty: %v", err)
	}
	_, injected, update, outcome, err = ShapeExecutorCall(ctx, call, store, scope, &emptyRef, false)
	if err != nil || injected || update != nil || outcome != MemoOutcomeSkippedEmpty {
		t.Fatalf("expected skipped empty, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}

	// 4. Expired budget
	expiredRef, err := store.Put(ctx, scope, state.MemoState{Memo: "some memo", RegularTurnsRemaining: 0})
	if err != nil {
		t.Fatalf("put expired: %v", err)
	}
	_, injected, update, outcome, err = ShapeExecutorCall(ctx, call, store, scope, &expiredRef, false)
	if err != nil || injected || update != nil || outcome != MemoOutcomeExpired {
		t.Fatalf("expected expired, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}

	// 5. Skipped visible
	visibleRef, err := store.Put(ctx, scope, state.MemoState{Memo: "visible memo", RegularTurnsRemaining: 2, VisibleToClient: true})
	if err != nil {
		t.Fatalf("put visible: %v", err)
	}
	_, injected, update, outcome, err = ShapeExecutorCall(ctx, call, store, scope, &visibleRef, true)
	if err != nil || injected || update != nil || outcome != MemoOutcomeSkippedVisible {
		t.Fatalf("expected skipped visible, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}

	// 6. Injected successfully
	validRef, err := store.Put(ctx, scope, state.MemoState{Memo: "good plan", RegularTurnsRemaining: 2})
	if err != nil {
		t.Fatalf("put valid: %v", err)
	}
	_, injected, update, outcome, err = ShapeExecutorCall(ctx, call, store, scope, &validRef, false)
	if err != nil || !injected || update == nil || outcome != MemoOutcomeInjected {
		t.Fatalf("expected injected, got %v, injected=%v, outcome=%v", err, injected, outcome)
	}
	if update.State.RegularTurnsRemaining != 1 {
		t.Fatalf("expected budget decremented to 1, got %d", update.State.RegularTurnsRemaining)
	}
	if update.State.InjectedCount != 1 {
		t.Fatalf("expected InjectedCount 1, got %d", update.State.InjectedCount)
	}
}
